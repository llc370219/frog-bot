package io.github.llc370219.frogbot.claude.core

import android.content.Context
import android.content.SharedPreferences
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

sealed interface ServerStatus {
    data object Connecting : ServerStatus
    data object Online : ServerStatus
    data object NeedsLogin : ServerStatus
    data class Offline(val error: String) : ServerStatus
}

/** App 里的一台服务器：连接设置、在线状态、上面的设备和 Bot 列表 */
class ServerEntry(config: ServerConfig, cookie: String?) {
    val id: String = config.id
    var config by mutableStateOf(config)
    var client = ServerClient(config, cookie)
    var status by mutableStateOf<ServerStatus>(ServerStatus.Connecting)
    var devices by mutableStateOf<List<DeviceGroup>>(emptyList())
    var authEnabled by mutableStateOf(true)
    var onNeedsLogin: ((ServerEntry) -> Unit)? = null
    var poll: Job? = null

    val name: String get() = config.name
    val online: Boolean get() = status == ServerStatus.Online

    fun markNeedsLogin() {
        if (status == ServerStatus.NeedsLogin) return
        status = ServerStatus.NeedsLogin
        onNeedsLogin?.invoke(this)
    }

    fun deviceName(device: String): String =
        if (device == "local") name else devices.firstOrNull { it.id == device }?.name ?: device

    fun profile(ref: BotRef): BotProfile? = devices.firstOrNull { it.id == ref.device }?.bots?.firstOrNull { it.id == ref.bot }
}

/** 偏好设置 */
class Prefs(private val sp: SharedPreferences) {
    var keepAlive: Boolean
        get() = sp.getBoolean("keepAlive", false)
        set(v) = sp.edit().putBoolean("keepAlive", v).apply()
    var appearance: String
        get() = sp.getString("appearance", "system") ?: "system"
        set(v) = sp.edit().putString("appearance", v).apply()
    var notifyApproval: Boolean
        get() = sp.getBoolean("notifyApproval", true)
        set(v) = sp.edit().putBoolean("notifyApproval", v).apply()
    var notifyTask: Boolean
        get() = sp.getBoolean("notifyTask", true)
        set(v) = sp.edit().putBoolean("notifyTask", v).apply()
    var notifyReply: Boolean
        get() = sp.getBoolean("notifyReply", true)
        set(v) = sp.edit().putBoolean("notifyReply", v).apply()
}

/** 整个 App 的状态：服务器、会话、未读、正在看的 Bot */
class AppState(private val context: Context) {
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val sp: SharedPreferences = context.getSharedPreferences("frogbot", Context.MODE_PRIVATE)
    val prefs = Prefs(sp)

    val servers = mutableStateListOf<ServerEntry>()
    val sessions = mutableStateMapOf<String, BotSession>()
    val unread = mutableStateMapOf<String, Int>()

    /** 正在看的 Bot（在聊天页里）；不在聊天页时为 null */
    var viewing by mutableStateOf<BotRef?>(null)
    var foreground = false
    var appearance by mutableStateOf(prefs.appearance)
    var keepAlive by mutableStateOf(prefs.keepAlive)

    var onNotify: ((BotSession, String, NewsKind) -> Unit)? = null

    /** 客户端互联（设置 › Bot 互联） */
    val relay = RelayManager(sp, scope)

    init {
        val list = runCatching { JSONArray(sp.getString("servers", "[]")) }.getOrElse { JSONArray() }
        val cookies = runCatching { JSONObject(sp.getString("sessions", "{}") ?: "{}") }.getOrElse { JSONObject() }
        for (j in list.objects()) {
            val c = ServerConfig.from(j)
            servers.add(ServerEntry(c, cookies.strOrNull(c.id)))
        }
        servers.forEach(::wire)
        relay.attach { servers.toList() }
    }

    val totalUnread: Int get() = unread.values.sum()

    fun server(id: String) = servers.firstOrNull { it.id == id }
    fun session(ref: BotRef) = sessions[ref.key]

    val allRefs: List<BotRef>
        get() = servers.flatMap { s -> s.devices.flatMap { g -> g.bots.map { BotRef(s.id, g.id, it.id) } } }

    private fun persist() {
        sp.edit().putString("servers", JSONArray(servers.map { it.config.json() }).toString()).apply()
    }

    private fun saveCookie(s: ServerEntry) {
        val cookies = runCatching { JSONObject(sp.getString("sessions", "{}") ?: "{}") }.getOrElse { JSONObject() }
        if (s.client.cookie == null) cookies.remove(s.id) else cookies.put(s.id, s.client.cookie)
        sp.edit().putString("sessions", cookies.toString()).apply()
    }

    private fun wire(s: ServerEntry) {
        s.onNeedsLogin = { entry -> stopSessions(entry) }
        startPolling(s)
    }

    /** 每 10 秒刷新设备和 Bot 列表；每台服务器单独轮询 */
    private fun startPolling(s: ServerEntry) {
        s.poll?.cancel()
        s.poll = scope.launch {
            while (isActive) {
                refresh(s)
                delay(10_000)
            }
        }
    }

    suspend fun refresh(s: ServerEntry) {
        if (s.status == ServerStatus.NeedsLogin) return
        try {
            s.devices = loadDevices(s.client, s.name)
            s.status = ServerStatus.Online
            syncSessions(s)
        } catch (e: ApiException) {
            if (e.unauthorized) s.markNeedsLogin() else s.status = ServerStatus.Offline(e.message ?: "离线")
        } catch (e: Exception) {
            s.status = ServerStatus.Offline(ServerClient.describe(e))
        } finally {
            relay.apply()
        }
    }

    private suspend fun loadDevices(client: ServerClient, fallbackName: String): List<DeviceGroup> = try {
        client.getArray("/api/devices").objects().map(DeviceGroup::from)
    } catch (e: ApiException) {
        if (e.status != 404) throw e
        listOf(DeviceGroup("local", fallbackName, true, client.getArray("/api/bots").objects().map(BotProfile::from)))
    }

    private fun syncSessions(s: ServerEntry) {
        val alive = HashSet<String>()
        for (g in s.devices) {
            if (!g.online) continue
            for (b in g.bots) {
                val ref = BotRef(s.id, g.id, b.id)
                alive.add(ref.key)
                val deviceName = if (g.id == "local") s.name else g.name
                val existing = sessions[ref.key]
                if (existing != null) {
                    if (existing.profile != b) existing.profile = b
                    existing.deviceName = deviceName
                    existing.start()
                } else if (sessions.size < 48 || ref == viewing) {
                    val session = BotSession(ref, b, deviceName, s, scope)
                    session.onNews = { sess, text, kind -> handleNews(sess, text, kind) }
                    sessions[ref.key] = session
                    session.start()
                }
            }
        }
        val removed = sessions.keys.filter { key ->
            val ref = BotRef.parse(key) ?: return@filter false
            ref.server == s.id && key !in alive && s.devices.none { it.id == ref.device && !it.online }
        }
        for (key in removed) {
            sessions[key]?.stop()
            sessions.remove(key)
            unread.remove(key)
        }
    }

    fun stopSessions(s: ServerEntry) {
        sessions.values.filter { it.ref.server == s.id }.forEach { it.stop() }
    }

    /** 添加服务器，或给已有服务器重新登录 */
    suspend fun connect(existing: ServerEntry?, name: String, address: String, username: String, password: String) {
        val url = ServerConfig.normalize(address) ?: throw ApiException("地址格式不对，例如 https://bot.example.com 或 http://1.2.3.4:8080")
        val user = username.trim().ifEmpty { "admin" }
        var config = ServerConfig(existing?.id ?: UUID.randomUUID().toString(), name.trim(), url, user)
        val client = ServerClient(config, null)
        val me = client.me()
        val enabled = me.bool("enabled")
        if (enabled && !me.bool("authenticated")) {
            if (password.isEmpty()) throw ApiException("请输入密码")
            client.login(user, password)
        }
        val groups = loadDevices(client, config.baseUrl?.host ?: "frog")
        if (config.name.isEmpty()) config = config.copy(name = groups.firstOrNull()?.name ?: config.baseUrl?.host ?: "frog")
        client.config = config

        val s = existing ?: ServerEntry(config, client.cookie).also { servers.add(it) }
        if (existing != null) {
            stopSessions(existing)
            sessions.keys.filter { it.startsWith(existing.id + "|") }.forEach { sessions.remove(it) }
        }
        s.config = config
        s.client = client
        s.authEnabled = enabled
        s.devices = groups
        s.status = ServerStatus.Online
        persist()
        saveCookie(s)
        if (existing == null) wire(s) else startPolling(s)
        syncSessions(s)
        relay.apply()
    }

    fun remove(s: ServerEntry) {
        relay.forget(s)
        s.poll?.cancel()
        stopSessions(s)
        sessions.keys.filter { it.startsWith(s.id + "|") }.forEach { sessions.remove(it); unread.remove(it) }
        val client = s.client
        scope.launch { runCatching { client.post("/api/auth/logout") } }
        client.cookie = null
        saveCookie(s)
        servers.remove(s)
        persist()
        relay.apply()
    }

    suspend fun logout(s: ServerEntry) {
        runCatching { s.client.post("/api/auth/logout") }
        s.client.cookie = null
        saveCookie(s)
        stopSessions(s)
        s.markNeedsLogin()
        relay.apply()
    }

    suspend fun changePassword(s: ServerEntry, old: String, new: String) {
        s.client.post("/api/auth/password", JSONObject().put("old_password", old).put("new_password", new))
        s.client.login(s.config.username, new)
        saveCookie(s)
        sessions.values.filter { it.ref.server == s.id }.forEach { it.stop(); it.start() }
        refresh(s)
    }

    suspend fun checkUpdate(s: ServerEntry): JSONObject = s.client.getObject("/api/update")

    suspend fun saveBot(s: ServerEntry, device: String, existing: BotProfile?, name: String, instructions: String): String? {
        val body = JSONObject().put("name", name).put("instructions", instructions)
        val id = if (existing != null) {
            s.client.post("/api/bots/${existing.id}", body, method = "PATCH")
            existing.id
        } else {
            val path = if (device == "local") "/api/bots" else "/api/devices/$device/bots"
            s.client.post(path, body).str("id").ifEmpty { null }
        }
        refresh(s)
        return id
    }

    suspend fun deleteBot(ref: BotRef, purge: Boolean) {
        val s = server(ref.server) ?: return
        s.client.post("/api/bots/${ref.bot}", method = "DELETE", query = if (purge) mapOf("purge" to "1") else emptyMap())
        sessions[ref.key]?.stop()
        sessions.remove(ref.key)
        refresh(s)
    }

    fun changeAppearance(v: String) { prefs.appearance = v; appearance = v }
    fun changeKeepAlive(v: Boolean) { prefs.keepAlive = v; keepAlive = v }

    fun markRead(ref: BotRef) { unread.remove(ref.key) }

    private fun handleNews(session: BotSession, text: String, kind: NewsKind) {
        if (foreground && viewing == session.ref) return
        unread[session.ref.key] = (unread[session.ref.key] ?: 0) + 1
        val wanted = when (kind) {
            NewsKind.Reply -> prefs.notifyReply
            NewsKind.Task -> prefs.notifyTask
            NewsKind.Approval -> prefs.notifyApproval
        }
        if (wanted && !foreground) onNotify?.invoke(session, text, kind)
    }
}
