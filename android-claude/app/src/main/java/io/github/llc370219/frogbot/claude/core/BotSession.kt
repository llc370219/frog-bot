package io.github.llc370219.frogbot.claude.core

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.Request
import org.json.JSONObject
import java.util.UUID

enum class NewsKind { Reply, Task, Approval }

data class PendingUpload(
    val id: String = UUID.randomUUID().toString(),
    val name: String,
    val size: Long,
    val attachment: Attachment? = null,
    val error: String? = null,
) {
    val uploading: Boolean get() = attachment == null && error == null
}

/**
 * 一个 Bot 的实时状态：连着它的 SSE 事件流，把事件合并成时间线（消息、任务卡、审批）。
 * 断线后带 ?after= 续传。合并规则和网页版 web/src/api.ts 的 apply 一致。
 */
class BotSession(
    val ref: BotRef,
    profile: BotProfile,
    deviceName: String,
    val server: ServerEntry,
    private val scope: CoroutineScope,
) {
    var profile by mutableStateOf(profile)
    var deviceName by mutableStateOf(deviceName)

    val items = mutableStateListOf<TimelineItem>()
    val msgs = mutableStateMapOf<String, Msg>()
    val tasks = mutableStateMapOf<String, BotTask>()
    val approvals = mutableStateMapOf<String, Approval>()
    var scheduleRevision by mutableIntStateOf(0)

    var streaming by mutableStateOf("")
    var toolLabel by mutableStateOf("")
    var busy by mutableStateOf(false)
    var thinking by mutableStateOf("")

    var ready by mutableStateOf(false)
    var connected by mutableStateOf(false)
    var info by mutableStateOf<BotInfo?>(null)

    var draft by mutableStateOf("")
    val uploads = mutableStateListOf<PendingUpload>()

    var onNews: ((BotSession, String, NewsKind) -> Unit)? = null

    private var lastSeq = 0L
    private var job: Job? = null

    val client: ServerClient get() = server.client
    val isLocalDevice: Boolean get() = ref.device == "local"

    val basePath: String
        get() = if (isLocalDevice) "/api/bots/${ref.bot}" else "/api/devices/${ref.device}/bots/${ref.bot}"

    // 派生状态
    val running: List<BotTask> get() = tasks.values.filter { it.status == TaskStatus.running }
    val queued: List<BotTask> get() = tasks.values.filter { it.status == TaskStatus.queued }.sortedBy { it.prio }
    val paused: List<BotTask> get() = tasks.values.filter { it.status == TaskStatus.paused }
    val working: Boolean get() = tasks.values.any { it.isLive }
    val pendingApprovals: Int get() = approvals.values.count { it.status == "pending" }
    val openTasks: Int get() = tasks.values.count { it.isOpen }
    val queuePositions: Map<String, Int> get() = queued.mapIndexed { i, t -> t.id to i + 1 }.toMap()
    val stallMs: Double get() = minOf((info?.stallSec ?: 720) * 1000.0 / 2, 180_000.0)

    val lastMessage: Msg?
        get() = items.asReversed().firstNotNullOfOrNull { if (it.kind == ItemKind.Msg) msgs[it.id] else null }

    val regenerateId: String?
        get() {
            if (busy) return null
            val visible = items.filter { it.kind == ItemKind.Msg }.mapNotNull { msgs[it.id] }
            val lastUser = visible.lastOrNull { it.role == "user" } ?: return null
            val lastReply = visible.lastOrNull { it.role == "assistant" } ?: return null
            return if (lastReply.created >= lastUser.created) lastReply.id else null
        }

    // ---------- 事件流 ----------

    fun start() {
        if (job?.isActive == true) return
        job = scope.launch {
            if (info == null) launch { loadInfo() }
            while (isActive) {
                val end = streamOnce()
                connected = false
                if (end == End.Unauthorized) {
                    server.markNeedsLogin()
                    return@launch
                }
                delay(2000)
            }
        }
    }

    fun stop() {
        job?.cancel()
        job = null
        connected = false
    }

    private enum class End { Dropped, Unauthorized }

    private suspend fun streamOnce(): End = withContext(Dispatchers.IO) {
        val query = if (lastSeq > 0) mapOf("after" to lastSeq.toString()) else emptyMap()
        val req: Request = try {
            client.request("$basePath/stream", query).header("Accept", "text/event-stream").get().build()
        } catch (_: Exception) {
            return@withContext End.Dropped
        }
        val call = ServerClient.stream.newCall(req)
        try {
            call.execute().use { resp ->
                if (resp.code == 401) return@withContext End.Unauthorized
                if (resp.code != 200) return@withContext End.Dropped
                val source = resp.body?.source() ?: return@withContext End.Dropped
                withContext(Dispatchers.Main) { connected = true; busy = false; resetTurn() }
                var replaying = true
                val batch = ArrayList<JSONObject>()
                while (isActive) {
                    val line = source.readUtf8Line() ?: break
                    if (!line.startsWith("data:")) continue
                    val payload = line.removePrefix("data:").trimStart()
                    val ev = runCatching { JSONObject(payload) }.getOrNull() ?: continue
                    if (replaying) {
                        batch.add(ev)
                        if (ev.str("kind") == "replay.done") {
                            replaying = false
                            val copy = ArrayList(batch)
                            batch.clear()
                            withContext(Dispatchers.Main) { copy.forEach(::handle) }
                        }
                    } else {
                        withContext(Dispatchers.Main) { handle(ev) }
                    }
                }
                if (batch.isNotEmpty()) withContext(Dispatchers.Main) { batch.forEach(::handle) }
            }
        } catch (_: Exception) {
            // 断线：外层循环会重连
        } finally {
            call.cancel()
        }
        End.Dropped
    }

    private fun handle(ev: JSONObject) {
        val seq = ev.optLong("seq", 0)
        if (seq > 0) {
            if (seq <= lastSeq) return
            lastSeq = seq
        }
        apply(ev)
    }

    private fun resetTurn() {
        streaming = ""; toolLabel = ""; thinking = ""
    }

    private fun news(text: String, kind: NewsKind) {
        if (!ready) return // 重放历史不提醒
        onNews?.invoke(this, text, kind)
    }

    private fun apply(ev: JSONObject) {
        val d = ev.obj("data")
        when (ev.str("kind")) {
            "replay.done" -> {
                ready = true
                busy = d.bool("busy")
                scheduleRevision++
            }
            "message" -> {
                val role = d.str("role")
                if (role == "notice" || d == null) return
                val id = d.str("id")
                if (d.bool("hidden")) {
                    items.removeAll { it.id == id }
                    msgs.remove(id)
                    return
                }
                val m = Msg.from(d)
                if (!msgs.containsKey(id)) {
                    // 聊天层的回应排在它这一轮刚派出的工作卡片前面
                    var at = items.size
                    if (role == "assistant") {
                        while (at > 0) {
                            val prev = items[at - 1]
                            val t = if (prev.kind == ItemKind.Task) tasks[prev.id] else null
                            if (t != null && t.steps.isEmpty() && t.created > m.created - 60_000) at-- else break
                        }
                    }
                    items.add(at, TimelineItem(ItemKind.Msg, id))
                    if (role == "assistant" && m.text.isNotEmpty()) news(m.text, NewsKind.Reply)
                }
                msgs[id] = m
                if (role == "assistant" && (m.text.isNotEmpty() || !m.thinking.isNullOrEmpty())) resetTurn()
            }
            "message.hidden" -> {
                val ids = ev.arr("data").strings().toSet()
                items.removeAll { it.kind == ItemKind.Msg && it.id in ids }
                ids.forEach { msgs.remove(it) }
            }
            "message.reaction" -> {
                val id = d.str("id")
                val m = msgs[id] ?: return
                val by = d.str("by")
                val keep = m.reactions.filter { it.by != by }.toMutableList()
                if (d.bool("on")) keep.add(Reaction(by, d.str("emoji")))
                msgs[id] = m.copy(reactions = keep)
            }
            "card" -> {
                val card = Card.from(d ?: return)
                msgs.entries.filter { it.value.card?.id == card.id }.forEach { msgs[it.key] = it.value.copy(card = card) }
            }
            "secret.card" -> {
                val req = SecretRequest.from(d ?: return)
                msgs.entries.filter { it.value.secret?.id == req.id }.forEach { msgs[it.key] = it.value.copy(secret = req) }
            }
            "chat.thinking" -> thinking += d.str("text")
            "chat.delta" -> streaming += d.str("text")
            "chat.status" -> {
                busy = d.bool("busy")
                if (!busy) resetTurn()
            }
            "chat.tool" -> toolLabel = d.str("label")
            "task.created" -> {
                val id = d.str("id")
                if (!tasks.containsKey(id)) items.add(TimelineItem(ItemKind.Task, id))
                val c = Stamp.ms(d.strOrNull("created_at"))
                val prio = d.num("prio")?.takeIf { it != 0.0 } ?: (c * 1e6)
                tasks[id] = BotTask(
                    id = id, title = d.str("title"), input = d.str("input"),
                    status = TaskStatus.of(d.str("status")) ?: TaskStatus.queued,
                    stopReason = d.strOrNull("stop_reason"), created = c, prio = prio, lastEvent = c, lastBeat = c,
                )
            }
            "task.updated" -> {
                val id = d.str("id")
                var t = tasks[id] ?: return
                TaskStatus.of(d.str("status"))?.let { st ->
                    t = t.copy(status = st)
                    if (st.terminal) t = t.copy(ended = Stamp.ms(ev.strOrNull("time")))
                    if (st == TaskStatus.queued) t = t.copy(stopReason = null, pausing = false)
                }
                d.num("prio")?.takeIf { it != 0.0 }?.let { t = t.copy(prio = it) }
                d.strOrNull("error")?.takeIf { it.isNotEmpty() }?.let { t = t.copy(error = it) }
                d.strOrNull("stop_reason")?.takeIf { it.isNotEmpty() }?.let { t = t.copy(stopReason = it) }
                tasks[id] = t
            }
            "task.pausing" -> {
                val id = d.str("id")
                tasks[id]?.let { tasks[id] = it.copy(pausing = true, stopReason = d.strOrNull("stop_reason")) }
            }
            "task.prio" -> {
                val id = d.str("id")
                val p = d.num("prio") ?: return
                tasks[id]?.let { tasks[id] = it.copy(prio = p) }
            }
            "task.summary" -> {
                val id = d.str("id")
                val t = tasks[id] ?: return
                val files = d.arr("files").strings()
                tasks[id] = t.copy(
                    summary = d.strOrNull("summary"),
                    files = files.ifEmpty { t.files },
                    duration = d.num("duration_sec")?.let { it * 1000 } ?: t.duration,
                )
            }
            "agent" -> applyAgent(d ?: return)
            "schedule.upserted", "schedule.deleted" -> scheduleRevision++
            "schedule.run" -> {
                scheduleRevision++
                val st = d.obj("claim").str("status")
                if (st == "needs_verify" || st == "failed" || st == "expired") {
                    news("定时计划「${d.obj("plan").str("title")}」：$st", NewsKind.Task)
                }
            }
        }
    }

    private fun applyAgent(e: JSONObject) {
        val taskId = e.str("task_id")
        val type = e.str("type")
        val d = e.obj("data") ?: JSONObject()
        val at = Stamp.ms(e.strOrNull("time"))

        if (type == "approval.requested") {
            val id = d.str("id")
            if (!approvals.containsKey(id)) {
                items.add(TimelineItem(ItemKind.Approval, id))
                news("有一个操作等你确认", NewsKind.Approval)
            }
            approvals[id] = Approval(id, taskId, d.str("tool_name"), d.obj("args") ?: JSONObject(), d.str("risk"), d.str("reason"), "pending", null, at)
            tasks[taskId]?.let { tasks[taskId] = it.copy(awaiting = it.awaiting + 1, lastEvent = at) }
            return
        }
        if (type == "approval.decided") {
            val aid = d.str("approval_id")
            val a = approvals[aid]
            if (a != null && a.status == "pending") {
                approvals[aid] = a.copy(status = d.str("decision"), scope = d.strOrNull("scope"))
                tasks[taskId]?.let { tasks[taskId] = it.copy(awaiting = maxOf(0, it.awaiting - 1), lastEvent = at) }
            }
            return
        }

        var t = tasks[taskId] ?: return
        if (type == "heartbeat") {
            tasks[taskId] = t.copy(lastBeat = at)
            return
        }
        t = t.copy(lastEvent = maxOf(t.lastEvent, at))
        when (type) {
            "task.started" -> t = t.copy(status = TaskStatus.running, stopReason = null, pausing = false, started = at, ended = null)
            "thinking.delta" -> t = t.copy(thinking = (t.thinking + d.str("text")).takeLast(600))
            "text.delta" -> t = t.copy(text = (t.text + d.str("text")).takeLast(4000), thinking = "")
            "tool.call" -> {
                val step = Step(d.str("call_id"), d.str("tool"), d.obj("args") ?: JSONObject(), t.text.trim(), at = at)
                t = t.copy(thinking = "", text = "", steps = t.steps + step)
            }
            "tool.result" -> {
                val callId = d.str("call_id")
                val steps = t.steps.toMutableList()
                val idx = steps.indexOfLast { it.id == callId && !it.done }.takeIf { it >= 0 } ?: (steps.size - 1)
                if (idx >= 0) {
                    val st = steps[idx].copy(
                        output = d.strOrNull("output"), error = d.bool("error"), denied = d.bool("denied"), done = true, doneAt = at,
                    )
                    steps[idx] = st
                    val path = st.args.str("path")
                    var files = t.files
                    if ((st.tool == "write_file" || st.tool == "edit_file") && path.isNotEmpty() && !st.error && !st.denied && path !in files) {
                        files = files + path
                    }
                    t = t.copy(steps = steps, files = files)
                }
            }
            "task.paused" -> {
                t = t.copy(
                    activeMs = t.activeMs + (t.started?.let { at - it } ?: 0.0), status = TaskStatus.paused, pausing = false,
                    text = "", thinking = "", started = null, steps = t.steps.map { it.copy(done = true) },
                )
            }
            "task.completed", "task.failed", "task.cancelled" -> {
                val st = TaskStatus.of(d.str("status")) ?: TaskStatus.of(type.removePrefix("task.")) ?: TaskStatus.completed
                t = t.copy(
                    activeMs = t.activeMs + (t.started?.let { at - it } ?: 0.0), status = st, output = d.strOrNull("output"),
                    ended = at, started = null, pausing = false, text = "", thinking = "",
                    error = d.strOrNull("error")?.takeIf { it.isNotEmpty() } ?: t.error,
                    steps = t.steps.map { it.copy(done = true) },
                )
                tasks[taskId] = t
                val verb = when (st) { TaskStatus.completed -> "完成了"; TaskStatus.failed -> "失败了"; else -> "已停止" }
                news("任务「${t.title}」$verb", NewsKind.Task)
                return
            }
        }
        tasks[taskId] = t
    }

    // ---------- 操作 ----------

    suspend fun loadInfo() {
        info = runCatching { BotInfo.from(client.getObject("$basePath/info")) }.getOrNull()
    }

    suspend fun send(text: String, files: List<Attachment>) {
        client.post("$basePath/messages", JSONObject().put("text", text).put("files", org.json.JSONArray(files.map { it.json() })))
    }

    suspend fun stopAll() { client.post("$basePath/stop") }
    suspend fun stopReply() { client.post("$basePath/stop", query = mapOf("scope" to "reply")) }
    suspend fun regenerate() { client.post("$basePath/regenerate") }
    suspend fun react(id: String, emoji: String) { client.post("$basePath/messages/$id/react", JSONObject().put("emoji", emoji)) }
    suspend fun task(id: String, action: String) { client.post("$basePath/tasks/$id/$action") }
    /** password 只有 root 命令（run_as_root）需要；grantMinutes > 0 表示之后这么多分钟内免输密码（每条仍要批准）。 */
    suspend fun decide(id: String, decision: String, password: String? = null, grantMinutes: Int = 0) {
        val body = JSONObject().put("decision", decision)
        if (password != null) body.put("password", password).put("grant_minutes", grantMinutes)
        client.post("$basePath/approvals/$id", body)
    }
    suspend fun answer(cardId: String, answer: String) { client.post("$basePath/cards/$cardId", JSONObject().put("answer", answer)) }
    suspend fun dismiss(cardId: String) { client.post("$basePath/cards/$cardId", JSONObject().put("dismiss", true)) }
    suspend fun saveSecret(requestId: String, value: String) { client.post("$basePath/secrets", JSONObject().put("request_id", requestId).put("value", value)) }
    suspend fun cancelSecret(requestId: String) { client.post("$basePath/secrets/requests/$requestId/cancel") }

    suspend fun loadSettings(): ApiSettings = ApiSettings.from(client.getObject("$basePath/settings"))
    suspend fun saveSettings(s: ApiSettings): ApiSettings {
        val out = ApiSettings.from(client.post("$basePath/settings", s.draft()))
        loadInfo()
        return out
    }
    /** 只保存这个 Bot 用哪个渠道的哪个模型（渠道本身整台设备共用） */
    suspend fun saveRoutes(chatChannel: String, chatModel: String, agentChannel: String, agentModel: String): ApiSettings {
        val body = JSONObject().put("default_provider_id", chatChannel).put("model", chatModel)
            .put("agent_provider_id", agentChannel).put("agent_model", agentModel)
        val out = ApiSettings.from(client.post("$basePath/settings/routes", body))
        loadInfo()
        return out
    }
    suspend fun setChatModel(channel: String, model: String) {
        val s = loadSettings()
        saveRoutes(channel, model, s.agentChannel, s.agentModel)
    }
    suspend fun loadConnections(): AgentConnections = AgentConnections.from(client.getObject("$basePath/settings/connections"))
    /** 选择 Agent。Pi 要同时给出 API 渠道和模型。 */
    suspend fun saveConnection(cli: String, channel: String? = null, model: String? = null): AgentConnections {
        val body = JSONObject().put("cli", cli)
        if (channel != null) body.put("cli_channel", channel)
        if (model != null) body.put("cli_model", model)
        return AgentConnections.from(client.post("$basePath/settings/connections", body))
    }
    /** 提权状态和开关（整台服务器共用；设备互联不转发，只能管这台服务器本机）。 */
    suspend fun loadPrivilege(): PrivilegeStatus = PrivilegeStatus.from(client.getObject("/api/settings/privilege"))
    suspend fun privilegeAction(action: String, password: String? = null): PrivilegeStatus {
        val body = JSONObject().put("action", action)
        if (password != null) body.put("password", password)
        return PrivilegeStatus.from(client.post("/api/settings/privilege", body))
    }
    /** 一键安装 / 更新 CLI。只能装在这台服务器本机：设备互联不转发这个接口。 */
    suspend fun installCli(id: String): AgentConnections =
        AgentConnections.from(client.post("/api/settings/clis/install", JSONObject().put("id", id)))
    suspend fun fetchModels(channel: ApiChannel): List<String> {
        val body = JSONObject().put("provider_id", channel.id).put("base_url", channel.baseUrl)
        if (!channel.authRequired) body.put("api_key", "") else channel.apiKey?.let { body.put("api_key", it) }
        return client.post("$basePath/settings/models", body).arr("models").strings()
    }
    suspend fun testChannel(channel: ApiChannel, model: String): Int {
        val body = JSONObject().put("provider_id", channel.id).put("base_url", channel.baseUrl).put("model", model)
        if (!channel.authRequired) body.put("api_key", "") else channel.apiKey?.let { body.put("api_key", it) }
        return client.post("$basePath/settings/test", body).optInt("latency_ms", 0)
    }
    suspend fun schedules(): List<Schedule> = client.getArray("$basePath/schedules").objects().map(Schedule::from)
    suspend fun schedule(id: String, action: String) { client.post("$basePath/schedules/$id/$action") }
    suspend fun fileBytes(path: String): ByteArray = client.bytes("$basePath/files", mapOf("path" to path, "download" to "1"))

    fun attach(name: String, mime: String, data: ByteArray) {
        val p = PendingUpload(name = name, size = data.size.toLong())
        uploads.add(p)
        scope.launch {
            val result = runCatching { client.upload("$basePath/uploads", name, mime, data) }
            val i = uploads.indexOfFirst { it.id == p.id }
            if (i >= 0) {
                uploads[i] = result.fold({ uploads[i].copy(attachment = it) }, { uploads[i].copy(error = ServerClient.describe(it)) })
            }
        }
    }
}
