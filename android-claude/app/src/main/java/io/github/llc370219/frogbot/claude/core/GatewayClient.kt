package io.github.llc370219.frogbot.claude.core

import androidx.compose.runtime.getValue
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
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONArray
import org.json.JSONObject

data class V2ApprovalCard(
    val id: String,
    val botId: String,
    val action: String,
    val triggerSource: String,
    val tainted: Boolean,
    val targetDomain: String,
    val summary: String,
    val reason: String,
    val canWhitelist: Boolean,
    val whitelistDomain: String,
)

data class V2VerifySession(
    val id: String,
    val botId: String,
    val url: String,
    val reason: String,
    val status: String,
    val viewportWidth: Int,
    val viewportHeight: Int,
)

data class V2CLITask(
    val id: String,
    val botId: String,
    val cli: String,
    val mode: String,
    val status: String,
    val promptBrief: String,
)

data class V2MeshPeer(
    val nodeId: String,
    val name: String,
    val networkTag: String,
    val online: Boolean,
    val bots: List<String>,
)

/**
 * Frog Bot V2 单点总控多路复用 WebSocket 客户端（OkHttp WebSocket）。
 * 只需连接任意一台公网 Home Node，即可全网漫游操控所有 bot@node，接收结构化审批卡、
 * 就绪重放 CDP 过验证推流画面与实时 Coding CLI 日志流，并在退后台时上报在线状态以触发离线唤醒推送。
 */
class GatewayClient(private val scope: CoroutineScope) {
    var connected by mutableStateOf(false)
    var nodeId by mutableStateOf("")
    val pendingApprovals = mutableStateListOf<V2ApprovalCard>()
    val verifySessions = mutableStateListOf<V2VerifySession>()
    val cliTasks = mutableStateListOf<V2CLITask>()
    val cliLogs = mutableStateMapOf<String, String>()
    val meshPeers = mutableStateListOf<V2MeshPeer>()
    var activeVerifyId by mutableStateOf<String?>(null)
    var latestFrameBase64 by mutableStateOf<String?>(null)

    private var server: ServerEntry? = null
    private var ws: WebSocket? = null
    private var reconnectJob: Job? = null

    val attentionCount: Int
        get() = pendingApprovals.size + verifySessions.count { it.status == "WAITING_HUMAN" || it.status == "VERIFYING" }

    fun attach(entry: ServerEntry) {
        server = entry
        connect()
    }

    fun disconnect() {
        reconnectJob?.cancel()
        reconnectJob = null
        ws?.close(1000, "closed")
        ws = null
        connected = false
    }

    fun connect() {
        val srv = server ?: return
        if (srv.config.isTunneled) {
            connected = false
            return
        }
        val base = srv.config.url.trimEnd('/')
        val wsUrl = base.replaceFirst("^https://".toRegex(), "wss://")
            .replaceFirst("^http://".toRegex(), "ws://") + "/api/v2/ws"
        val rb = Request.Builder().url(wsUrl)
        srv.client.cookie?.takeIf { it.isNotEmpty() }?.let {
            rb.header("Cookie", "${ServerClient.COOKIE}=$it")
        }
        ws?.cancel()
        ws = ServerClient.stream.newWebSocket(rb.build(), object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                scope.launch {
                    connected = true
                    refreshStatus()
                }
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                scope.launch { handleFrame(text) }
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                scheduleReconnect()
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                scheduleReconnect()
            }
        })
    }

    private fun scheduleReconnect() {
        scope.launch {
            connected = false
            reconnectJob?.cancel()
            reconnectJob = launch {
                delay(3_000)
                if (isActive && server != null) connect()
            }
        }
    }

    fun refreshStatus() {
        val srv = server ?: return
        scope.launch(Dispatchers.IO) {
            runCatching { srv.client.getObject("/api/v2/status") }.onSuccess { obj ->
                scope.launch {
                    nodeId = obj.optString("node_id", nodeId)
                    parseApprovals(obj.optJSONArray("pending_approvals"))
                    parseVerifications(obj.optJSONArray("pending_verifications"))
                    parseCLITasks(obj.optJSONArray("cli_tasks"))
                    parseMeshPeers(obj.optJSONArray("peers"))
                }
            }
        }
    }

    private fun handleFrame(raw: String) {
        val root = runCatching { JSONObject(raw) }.getOrNull() ?: return
        val payload = root.optJSONObject("payload")
        when (root.optString("type")) {
            "hello" -> {
                if (payload != null) {
                    nodeId = payload.optString("node_id", nodeId)
                    parseApprovals(payload.optJSONArray("pending_approvals"))
                    parseVerifications(payload.optJSONArray("pending_verifications"))
                }
            }
            "approval.pending" -> payload?.let {
                val card = toApprovalCard(it)
                if (pendingApprovals.none { a -> a.id == card.id }) pendingApprovals.add(0, card)
            }
            "verify.waiting" -> payload?.let {
                val sess = toVerifySession(it)
                verifySessions.removeAll { v -> v.id == sess.id }
                verifySessions.add(0, sess)
            }
            "verify.frame" -> payload?.let {
                val b64 = it.optString("data_base64")
                if (b64.isNotEmpty()) latestFrameBase64 = b64
            }
            "cli.log.snapshot" -> payload?.let {
                val tid = it.optString("task_id")
                if (tid.isNotEmpty()) cliLogs[tid] = it.optString("content")
            }
            "cli.log.chunk" -> payload?.let {
                val tid = it.optString("task_id")
                if (tid.isNotEmpty()) cliLogs[tid] = (cliLogs[tid] ?: "") + it.optString("chunk")
            }
        }
    }

    private fun sendFrame(type: String, payload: JSONObject, target: String? = null) {
        val f = JSONObject().put("type", type).put("payload", payload)
        if (!target.isNullOrEmpty()) f.put("target", target)
        ws?.send(f.toString())
    }

    fun setForeground(foreground: Boolean) = sendFrame("presence", JSONObject().put("foreground", foreground))

    fun registerPushEndpoint(unifiedPushUrl: String = "", fcmToken: String = "", barkUrl: String = "") =
        sendFrame(
            "push.register",
            JSONObject()
                .put("unified_push_url", unifiedPushUrl)
                .put("fcm_device_token", fcmToken)
                .put("bark_url", barkUrl),
        )

    fun sendRoamChat(target: String, text: String) =
        sendFrame("chat.send", JSONObject().put("text", text), target)

    fun decideApproval(cardId: String, choice: String, whitelistDomain: String = "") {
        sendFrame(
            "approval.decide",
            JSONObject().put("card_id", cardId).put("choice", choice).put("whitelist_domain", whitelistDomain),
        )
        pendingApprovals.removeAll { it.id == cardId }
    }

    fun subscribeCLILog(taskId: String) = sendFrame("cli.log.subscribe", JSONObject().put("task_id", taskId))

    fun cancelCLITask(taskId: String) = sendFrame("cli.cancel", JSONObject().put("task_id", taskId))

    fun startVerify(sessionId: String) {
        activeVerifyId = sessionId
        latestFrameBase64 = null
        sendFrame("verify.ready", JSONObject().put("session_id", sessionId))
    }

    fun sendVerifyClick(sessionId: String, normX: Double, normY: Double) = sendFrame(
        "verify.input",
        JSONObject().put("session_id", sessionId).put(
            "event",
            JSONObject().put("type", "click").put("x", normX).put("y", normY).put("normalized", true),
        ),
    )

    fun sendVerifyDrag(sessionId: String, x0: Double, y0: Double, x1: Double, y1: Double) = sendFrame(
        "verify.input",
        JSONObject().put("session_id", sessionId).put(
            "event",
            JSONObject().put("type", "drag")
                .put("x", x0).put("y", y0).put("end_x", x1).put("end_y", y1)
                .put("normalized", true).put("duration_ms", 260),
        ),
    )

    fun sendVerifyText(sessionId: String, text: String) = sendFrame(
        "verify.input",
        JSONObject().put("session_id", sessionId).put(
            "event",
            JSONObject().put("type", "text").put("text", text),
        ),
    )

    fun completeVerify(sessionId: String) {
        sendFrame("verify.complete", JSONObject().put("session_id", sessionId))
        verifySessions.removeAll { it.id == sessionId }
        if (activeVerifyId == sessionId) {
            activeVerifyId = null
            latestFrameBase64 = null
        }
    }

    fun cancelVerify(sessionId: String) {
        sendFrame("verify.cancel", JSONObject().put("session_id", sessionId))
        if (activeVerifyId == sessionId) {
            activeVerifyId = null
            latestFrameBase64 = null
        }
    }

    private fun parseApprovals(arr: JSONArray?) {
        if (arr == null) return
        pendingApprovals.clear()
        for (i in 0 until arr.length()) arr.optJSONObject(i)?.let { pendingApprovals.add(toApprovalCard(it)) }
    }

    private fun parseVerifications(arr: JSONArray?) {
        if (arr == null) return
        verifySessions.clear()
        for (i in 0 until arr.length()) arr.optJSONObject(i)?.let { verifySessions.add(toVerifySession(it)) }
    }

    private fun parseCLITasks(arr: JSONArray?) {
        if (arr == null) return
        cliTasks.clear()
        for (i in 0 until arr.length()) arr.optJSONObject(i)?.let { j ->
            cliTasks.add(
                V2CLITask(
                    id = j.optString("id"),
                    botId = j.optString("bot_id"),
                    cli = j.optString("cli"),
                    mode = j.optString("mode"),
                    status = j.optString("status"),
                    promptBrief = j.optString("prompt_brief"),
                ),
            )
        }
    }

    private fun parseMeshPeers(arr: JSONArray?) {
        if (arr == null) return
        meshPeers.clear()
        for (i in 0 until arr.length()) arr.optJSONObject(i)?.let { j ->
            val botsArr = j.optJSONArray("bots")
            val bots = mutableListOf<String>()
            if (botsArr != null) for (k in 0 until botsArr.length()) bots.add(botsArr.optString(k))
            meshPeers.add(
                V2MeshPeer(
                    nodeId = j.optString("node_id"),
                    name = j.optString("name"),
                    networkTag = j.optString("network_tag"),
                    online = j.optBoolean("online"),
                    bots = bots,
                ),
            )
        }
    }

    private fun toApprovalCard(j: JSONObject) = V2ApprovalCard(
        id = j.optString("id"),
        botId = j.optString("bot_id"),
        action = j.optString("action"),
        triggerSource = j.optString("trigger_source"),
        tainted = j.optBoolean("tainted"),
        targetDomain = j.optString("target_domain"),
        summary = j.optString("summary"),
        reason = j.optString("reason"),
        canWhitelist = j.optBoolean("can_whitelist"),
        whitelistDomain = j.optString("whitelist_domain"),
    )

    private fun toVerifySession(j: JSONObject) = V2VerifySession(
        id = j.optString("id"),
        botId = j.optString("bot_id"),
        url = j.optString("url"),
        reason = j.optString("reason"),
        status = j.optString("status"),
        viewportWidth = j.optInt("viewport_width", 1280),
        viewportHeight = j.optInt("viewport_height", 800),
    )
}
