package io.github.llc370219.frogbot.claude.core

import android.content.SharedPreferences
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

sealed interface RelayState {
    data object Off : RelayState
    data object Connecting : RelayState
    /** 打开了，但还没有可以联系的对象（至少要两台服务器各有一个 Bot 参与） */
    data object Waiting : RelayState
    data object Active : RelayState
    /** 服务器版本太旧 */
    data object Unsupported : RelayState
    data class Failed(val error: String) : RelayState
}

/**
 * 客户端互联：这个 App 里登录的几台服务器，它们的聊天 Bot 可以互相发文字（问问题、托付任务）。
 *
 * - 只在 App 开着、总开关打开时工作；服务器之间不直接连接，消息由 App 转交，不排队。
 * - 每台服务器、每个 Bot 都能单独关掉，关掉的不会出现在别人的通讯录里，也收不到消息。
 * - 只转交文字：对方收到后做不做、怎么做，都在对方自己的机器上决定。
 *
 * 每台参与的服务器一个长轮询循环：POST /api/relay/sync 上报通讯录并取走要转发的消息，
 * 转给目标服务器的 POST /api/relay/inbox，下一轮带上回执。
 */
class RelayManager(private val sp: SharedPreferences, private val scope: CoroutineScope) {
    var enabled by mutableStateOf(sp.getBoolean("relay.enabled", false))
        private set
    var offServers by mutableStateOf(sp.getStringSet("relay.offServers", emptySet())!!.toSet())
        private set
    var offBots by mutableStateOf(sp.getStringSet("relay.offBots", emptySet())!!.toSet())
        private set
    val states = mutableStateMapOf<String, RelayState>()

    private val clientId: String = sp.getString("relay.clientID", null) ?: ("android-" + UUID.randomUUID().toString().replace("-", "")).also {
        sp.edit().putString("relay.clientID", it).apply()
    }
    private var servers: () -> List<ServerEntry> = { emptyList() }
    private val loops = HashMap<String, Job>()
    private val pushed = HashMap<String, String>()
    /** 曾经同步过的服务器（关掉时要通知它们） */
    private val joined = HashMap<String, ServerClient>()

    fun attach(list: () -> List<ServerEntry>) {
        servers = list
        apply()
    }

    // ---- 开关

    fun changeEnabled(v: Boolean) {
        enabled = v
        sp.edit().putBoolean("relay.enabled", v).apply()
        apply()
    }

    fun isOn(s: ServerEntry) = s.id !in offServers
    fun isOn(s: ServerEntry, bot: String) = "${s.id}/$bot" !in offBots

    fun setServer(s: ServerEntry, on: Boolean) {
        offServers = if (on) offServers - s.id else offServers + s.id
        sp.edit().putStringSet("relay.offServers", offServers).apply()
        apply()
    }

    fun setBot(s: ServerEntry, bot: String, on: Boolean) {
        val k = "${s.id}/$bot"
        offBots = if (on) offBots - k else offBots + k
        sp.edit().putStringSet("relay.offBots", offBots).apply()
        apply()
    }

    fun localBots(s: ServerEntry): List<BotProfile> = s.devices.firstOrNull { it.id == "local" }?.bots ?: emptyList()

    /** 这台服务器本机上参与互联的 Bot（设备互联过来的远程 Bot 不算） */
    private fun participants(s: ServerEntry): List<BotProfile> =
        if (!enabled || !isOn(s) || !s.online) emptyList() else localBots(s).filter { isOn(s, it.id) }

    // ---- 同步

    /** 开关、服务器列表或 Bot 列表变了以后调用：启停循环，并立刻把新通讯录告诉每台服务器 */
    fun apply() {
        val list = servers()
        val wanted = HashMap<String, JSONObject>()
        for (s in list) {
            val body = syncBody(s, list)
            if (body == null) {
                states[s.id] = if (enabled && isOn(s) && s.online) RelayState.Waiting else RelayState.Off
            } else {
                wanted[s.id] = body
            }
        }
        for (id in joined.keys.toList()) {
            if (wanted.containsKey(id)) continue
            loops.remove(id)?.cancel()
            pushed.remove(id)
            joined.remove(id)?.let { sendDisable(it) }
        }
        for (s in list) {
            val body = wanted[s.id] ?: continue
            joined[s.id] = s.client
            val sig = body.toString()
            if (loops[s.id]?.isActive != true) {
                pushed[s.id] = sig
                states[s.id] = RelayState.Connecting
                loops[s.id] = scope.launch { loop(s) }
            } else if (pushed[s.id] != sig) {
                // 循环正在长轮询，旧通讯录要等它返回才更新；单独发一次不等待的同步，立刻生效
                pushed[s.id] = sig
                scope.launch {
                    runCatching {
                        val acks = forward(sync(s, JSONArray(), false) ?: return@runCatching, s)
                        if (acks.length() > 0) sync(s, acks, false)
                    }
                }
            }
        }
    }

    /** 服务器从 App 里删掉之前调用 */
    fun forget(s: ServerEntry) {
        states.remove(s.id)
        loops.remove(s.id)?.cancel()
        pushed.remove(s.id)
        joined.remove(s.id)?.let { sendDisable(it) }
    }

    private fun sendDisable(client: ServerClient) {
        val body = JSONObject().put("client_id", clientId).put("enabled", false)
        scope.launch { withContext(NonCancellable) { runCatching { client.post("/api/relay/sync", body, client = ServerClient.quick) } } }
    }

    private fun syncBody(s: ServerEntry, list: List<ServerEntry>): JSONObject? {
        val mine = participants(s)
        if (mine.isEmpty()) return null
        val peers = JSONArray()
        for (other in list) {
            if (other.id == s.id) continue
            for (b in participants(other)) {
                peers.put(JSONObject().put("address", "relay:${key(other)}/${b.id}").put("name", b.name).put("server", other.name))
            }
        }
        if (peers.length() == 0) return null
        return JSONObject().put("client_id", clientId).put("enabled", true)
            .put("allowed", JSONArray(mine.map { it.id })).put("peers", peers)
    }

    /** 没有可参与的对象时返回 null */
    private suspend fun sync(s: ServerEntry, acks: JSONArray, wait: Boolean): List<JSONObject>? {
        val body = syncBody(s, servers()) ?: return null
        body.put("acks", acks).put("wait", wait)
        val out = s.client.post("/api/relay/sync", body, client = if (wait) ServerClient.longPoll else ServerClient.quick)
        return out.arr("items").objects()
    }

    private suspend fun CoroutineScope.loop(s: ServerEntry) {
        var acks = JSONArray()
        var failures = 0
        while (isActive) {
            try {
                val items = sync(s, acks, true)
                if (items == null) {
                    delay(2_000) // 刚好没有可参与的对象了：apply() 会停掉这个循环
                    continue
                }
                acks = forward(items, s)
                failures = 0
                if (isActive) states[s.id] = RelayState.Active
            } catch (e: ApiException) {
                if (!isActive) return
                acks = JSONArray()
                if (e.status == 404) {
                    states[s.id] = RelayState.Unsupported
                    delay(60_000)
                } else {
                    failures++
                    states[s.id] = RelayState.Failed(e.message ?: "同步失败")
                    delay(minOf(30_000L, 1_000L shl minOf(failures, 5)))
                }
            }
        }
    }

    /** 把服务器 s 上的 Bot 发出的消息送到目标服务器，返回回执 */
    private suspend fun forward(items: List<JSONObject>, from: ServerEntry): JSONArray {
        val acks = JSONArray()
        for (it in items) {
            val id = it.str("id")
            if (id.isEmpty()) continue
            val parts = it.str("to").removePrefix("relay:").split("/", limit = 2)
            val ack = JSONObject().put("id", id)
            val target = if (parts.size == 2) servers().firstOrNull { s -> key(s) == parts[0] } else null
            if (target != null && participants(target).isNotEmpty()) {
                val body = JSONObject().put("client_id", clientId).put("id", id)
                    .put("from", "relay:${key(from)}/${it.str("from")}").put("to", parts[1])
                    .put("text", it.str("text")).put("request_id", it.str("request_id"))
                    .put("reply_to", it.str("reply_to")).put("wants_reply", it.bool("wants_reply"))
                try {
                    target.client.post("/api/relay/inbox", body, client = ServerClient.quick)
                } catch (e: ApiException) {
                    ack.put("error", "转交失败：${e.message}")
                }
            } else {
                ack.put("error", "对方不在线或没有打开互联")
            }
            acks.put(ack)
        }
        return acks
    }

    companion object {
        /** 服务器在互联地址里的名字：relay:<key>/<BotID> */
        fun key(s: ServerEntry): String = s.id.lowercase().filter { it in 'a'..'z' || it in '0'..'9' }.take(12).ifEmpty { "server" }
    }
}
