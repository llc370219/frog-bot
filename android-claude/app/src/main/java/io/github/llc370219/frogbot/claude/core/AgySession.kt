package io.github.llc370219.frogbot.claude.core

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
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
import java.net.URLEncoder

// MARK: - Coding 工作台（直连官方 Coding CLI）数据模型

data class AgyModeOption(val id: String, val name: String, val description: String = "") {
    companion object {
        fun from(j: JSONObject) = AgyModeOption(j.str("id"), j.str("name").ifEmpty { j.str("id") }, j.str("description"))
    }
}

data class AgyModelOption(val id: String, val name: String) {
    companion object {
        fun from(j: JSONObject) = AgyModelOption(j.str("id"), j.str("name").ifEmpty { j.str("id") })
    }
}

data class AgyCommandOption(val name: String, val description: String = "", val inputHint: String? = null) {
    companion object {
        fun from(j: JSONObject) = AgyCommandOption(j.str("name"), j.str("description"), j.strOrNull("input_hint"))
    }
}

data class AgyQueuedPrompt(val id: String, val text: String, val behavior: String) {
    companion object {
        fun from(j: JSONObject) = AgyQueuedPrompt(
            id = j.str("id"),
            text = j.str("text"),
            behavior = j.str("behavior").ifEmpty { "follow_up" },
        )
    }
}

data class AgyAgentCLI(
    val id: String,
    val name: String,
    val installed: Boolean,
    val path: String = "",
    val version: String = "",
) {
    companion object {
        fun from(j: JSONObject) = AgyAgentCLI(
            id = j.str("id"),
            name = j.str("name").ifEmpty { j.str("id") },
            installed = j.bool("installed"),
            path = j.str("path"),
            version = j.str("version"),
        )
    }
}

data class AgyStatus(
    val installed: Boolean,
    val defaultAgent: String,
    val agents: List<AgyAgentCLI>,
    val projectsRoot: String,
    val defaultMode: String,
    val defaultModel: String,
    val error: String = "",
) {
    companion object {
        fun from(j: JSONObject) = AgyStatus(
            installed = j.bool("installed"),
            defaultAgent = j.str("default_agent"),
            agents = j.arr("agents").objects().map(AgyAgentCLI::from),
            projectsRoot = j.str("projects_root"),
            defaultMode = j.str("default_mode").ifEmpty { "auto" },
            defaultModel = j.str("default_model"),
            error = j.str("error"),
        )
    }
}

data class AgyProject(
    val id: String,
    val name: String,
    val path: String,
    val managed: Boolean,
) {
    companion object {
        fun from(j: JSONObject) = AgyProject(
            id = j.str("id"),
            name = j.str("name"),
            path = j.str("path"),
            managed = j.bool("managed"),
        )
    }
}

data class AgyFileEntry(val name: String, val isDir: Boolean, val size: Long?) {
    companion object {
        fun from(j: JSONObject) = AgyFileEntry(
            name = j.str("name"),
            isDir = j.bool("is_dir"),
            size = if (j.has("size") && !j.isNull("size")) j.optLong("size") else null,
        )
    }
}

data class AgySessionSummary(
    val id: String,
    val projectId: String,
    val cwd: String,
    val agent: String,
    val agentName: String,
    val title: String,
    val mode: String,
    val model: String,
    val busy: Boolean,
    val pendingPermissions: Int,
    val updatedAt: String,
) {
    companion object {
        fun from(j: JSONObject) = AgySessionSummary(
            id = j.str("id"),
            projectId = j.str("project_id"),
            cwd = j.str("cwd"),
            agent = j.str("agent"),
            agentName = j.str("agent_name"),
            title = j.str("title").ifEmpty { "新对话" },
            mode = j.str("mode").ifEmpty { "auto" },
            model = j.str("model"),
            busy = j.bool("busy"),
            pendingPermissions = j.optInt("pending_permissions", 0),
            updatedAt = j.str("updated_at"),
        )
    }
}

data class AgyToolCall(
    val id: String,
    val name: String,
    val title: String,
    val kind: String,
    val status: String,
    val detail: String,
    val output: String,
) {
    companion object {
        fun from(j: JSONObject) = AgyToolCall(
            id = j.str("id"),
            name = j.str("name"),
            title = j.str("title"),
            kind = j.str("kind"),
            status = j.str("status"),
            detail = j.str("detail"),
            output = j.str("output"),
        )
    }
}

data class AgyMessage(
    val id: String,
    val role: String,
    val text: String,
    val thinking: String?,
    val thinkingMs: Double?,
    val tools: List<AgyToolCall>,
    val error: String?,
    val created: Double,
) {
    companion object {
        fun from(j: JSONObject) = AgyMessage(
            id = j.str("id"),
            role = j.str("role"),
            text = j.str("text"),
            thinking = j.strOrNull("thinking"),
            thinkingMs = j.num("thinking_ms"),
            tools = j.arr("tools").objects().map(AgyToolCall::from),
            error = j.strOrNull("error"),
            created = Stamp.ms(j.strOrNull("created_at")),
        )
    }
}

data class AgyPermOption(val optionId: String, val name: String, val kind: String) {
    companion object {
        fun from(j: JSONObject) = AgyPermOption(j.str("optionId"), j.str("name"), j.str("kind"))
    }
}

data class AgyPermission(
    val id: String,
    val sessionId: String,
    val toolName: String,
    val title: String,
    val detail: String,
    val options: List<AgyPermOption>,
    val status: String,
    val chosenId: String?,
) {
    companion object {
        fun from(j: JSONObject) = AgyPermission(
            id = j.str("id"),
            sessionId = j.str("session_id"),
            toolName = j.str("tool_name"),
            title = j.str("title"),
            detail = j.str("detail"),
            options = j.arr("options").objects().map(AgyPermOption::from),
            status = j.str("status"),
            chosenId = j.strOrNull("chosen_id"),
        )
    }
}

// MARK: - 单台服务器的 Coding 项目与会话仓库

class AgyStore(val server: ServerEntry) {
    var status by mutableStateOf<AgyStatus?>(null)
    var projects by mutableStateOf<List<AgyProject>>(emptyList())
    var selectedProjectId by mutableStateOf("")
    var sessions by mutableStateOf<List<AgySessionSummary>>(emptyList())
    var loading by mutableStateOf(false)
    var error by mutableStateOf<String?>(null)

    val client: ServerClient get() = server.client

    val activeProject: AgyProject?
        get() = projects.firstOrNull { it.id == selectedProjectId } ?: projects.firstOrNull()

    val installedAgents: List<AgyAgentCLI>
        get() = status?.agents?.filter { it.installed } ?: emptyList()

    val activeAgentId: String
        get() = status?.defaultAgent?.takeIf { it.isNotEmpty() } ?: installedAgents.firstOrNull()?.id ?: "agy"

    suspend fun loadAll() {
        loading = true
        error = null
        try {
            val stObj = client.getObject("/api/agy/status")
            val prArr = client.getArray("/api/agy/projects")
            status = AgyStatus.from(stObj)
            val projs = prArr.objects().map(AgyProject::from)
            projects = projs
            if (projs.isNotEmpty() && projs.none { it.id == selectedProjectId }) {
                selectedProjectId = projs.first().id
            }
            val pid = activeProject?.id
            sessions = if (!pid.isNullOrEmpty()) {
                runCatching {
                    client.getArray("/api/agy/sessions", mapOf("project_id" to pid)).objects().map(AgySessionSummary::from)
                }.getOrDefault(emptyList())
            } else emptyList()
        } catch (e: Exception) {
            error = ServerClient.describe(e)
        } finally {
            loading = false
        }
    }

    suspend fun selectAgent(agentId: String) {
        try {
            val st = client.post("/api/agy/agent", JSONObject().put("agent", agentId))
            status = AgyStatus.from(st)
            error = null
        } catch (e: Exception) {
            error = ServerClient.describe(e)
        }
    }

    suspend fun selectProject(id: String) {
        selectedProjectId = id
        refreshSessions()
    }

    suspend fun refreshSessions() {
        val pid = activeProject?.id ?: run {
            sessions = emptyList()
            return
        }
        try {
            sessions = client.getArray("/api/agy/sessions", mapOf("project_id" to pid)).objects().map(AgySessionSummary::from)
            error = null
        } catch (e: Exception) {
            error = ServerClient.describe(e)
        }
    }

    suspend fun createProject(name: String, path: String): AgyProject {
        val obj = client.post("/api/agy/projects", JSONObject().put("name", name).put("path", path))
        val p = AgyProject.from(obj)
        if (projects.none { it.id == p.id }) projects = projects + p
        selectedProjectId = p.id
        refreshSessions()
        return p
    }

    suspend fun deleteProject(project: AgyProject, deleteFiles: Boolean = false) {
        val q = if (deleteFiles) mapOf("delete_files" to "1") else emptyMap()
        client.post("/api/agy/projects/${project.id}", method = "DELETE", query = q)
        projects = projects.filterNot { it.id == project.id }
        if (selectedProjectId == project.id) selectedProjectId = projects.firstOrNull()?.id ?: ""
        refreshSessions()
    }

    suspend fun createSession(mode: String = "", model: String = "", agent: String = ""): AgySessionSummary {
        val pid = activeProject?.id ?: throw ApiException("请先创建或关联一个项目目录")
        val targetAgent = agent.ifEmpty { activeAgentId }
        val res = client.post(
            "/api/agy/sessions",
            JSONObject()
                .put("project_id", pid)
                .put("agent", targetAgent)
                .put("mode", mode)
                .put("model", model),
        )
        val summary = AgySessionSummary.from(res.obj("session") ?: res)
        sessions = listOf(summary) + sessions.filterNot { it.id == summary.id }
        return summary
    }

    suspend fun deleteSession(id: String) {
        client.post("/api/agy/sessions/$id", method = "DELETE")
        sessions = sessions.filterNot { it.id == id }
    }
}

// MARK: - 单个 Coding 对话的实时 SSE 会话（同时兼容直连与 froge2ee://）

class AgyChatSession(
    val server: ServerEntry,
    val sessionId: String,
    val projectId: String,
    initialTitle: String,
    private val scope: CoroutineScope,
) {
    var title by mutableStateOf(initialTitle)
    var cwd by mutableStateOf("")
    var agent by mutableStateOf("")
    var agentName by mutableStateOf("")
    var mode by mutableStateOf("auto")
    var model by mutableStateOf("")
    var busy by mutableStateOf(false)
    var ready by mutableStateOf(false)
    var connected by mutableStateOf(false)

    var messages by mutableStateOf<List<AgyMessage>>(emptyList())
    var permissions by mutableStateOf<List<AgyPermission>>(emptyList())
    var queue by mutableStateOf<List<AgyQueuedPrompt>>(emptyList())
    var streaming by mutableStateOf("")
    var thinking by mutableStateOf("")
    var thinkStart by mutableStateOf(0.0)
    var thinkEnd by mutableStateOf(0.0)
    var liveTools by mutableStateOf<List<AgyToolCall>>(emptyList())
    var usedTokens by mutableIntStateOf(0)
    var maxTokens by mutableIntStateOf(0)
    var modes by mutableStateOf<List<AgyModeOption>>(emptyList())
    var models by mutableStateOf<List<AgyModelOption>>(emptyList())
    var commands by mutableStateOf<List<AgyCommandOption>>(emptyList())
    var draft by mutableStateOf("")

    private var job: Job? = null

    val client: ServerClient get() = server.client
    val pendingPermissions: List<AgyPermission> get() = permissions.filter { it.status == "pending" }

    private fun enc(s: String): String = URLEncoder.encode(s, "UTF-8").replace("+", "%20")

    fun start() {
        if (job?.isActive == true) return
        job = scope.launch {
            while (isActive) {
                val unauth = streamOnce()
                connected = false
                if (unauth) {
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

    private suspend fun streamOnce(): Boolean = withContext(Dispatchers.IO) {
        val path = "/api/agy/sessions/${enc(sessionId)}/stream"
        val query = mapOf("project_id" to projectId)
        val e2ee = client.e2eeTunnel
        if (e2ee != null) {
            var unauth = false
            try {
                e2ee.streamSSE(
                    path = client.relativePath(path, query),
                    onStatus = { status ->
                        if (status == 401) unauth = true
                        else if (status == 200) withContext(Dispatchers.Main) { connected = true }
                    },
                    onLine = { line ->
                        if (!line.startsWith("data:")) return@streamSSE
                        val payload = line.removePrefix("data:").trimStart()
                        val ev = runCatching { JSONObject(payload) }.getOrNull() ?: return@streamSSE
                        withContext(Dispatchers.Main) { apply(ev) }
                    },
                )
            } catch (_: Exception) {
                // 外层自动重连
            }
            return@withContext unauth
        }

        val req: Request = try {
            client.request(path, query).header("Accept", "text/event-stream").get().build()
        } catch (_: Exception) {
            return@withContext false
        }
        try {
            ServerClient.stream.newCall(req).execute().use { resp ->
                if (resp.code == 401) return@withContext true
                if (resp.code != 200) return@withContext false
                val src = resp.body?.source() ?: return@withContext false
                withContext(Dispatchers.Main) { connected = true }
                while (isActive) {
                    val line = src.readUtf8Line() ?: break
                    if (!line.startsWith("data:")) continue
                    val payload = line.removePrefix("data:").trimStart()
                    val ev = runCatching { JSONObject(payload) }.getOrNull() ?: continue
                    withContext(Dispatchers.Main) { apply(ev) }
                }
            }
        } catch (_: Exception) {
            // 自动重连
        }
        false
    }

    private fun apply(ev: JSONObject) {
        val kind = ev.str("kind")
        when (kind) {
            "snapshot" -> {
                val d = ev.obj("data") ?: return
                ready = true
                connected = true
                val s = d.obj("session")
                if (s != null) {
                    s.str("title").takeIf { it.isNotEmpty() }?.let { title = it }
                    cwd = s.str("cwd")
                    s.str("agent").takeIf { it.isNotEmpty() }?.let { agent = it }
                    s.str("agent_name").takeIf { it.isNotEmpty() }?.let { agentName = it }
                    s.str("mode").takeIf { it.isNotEmpty() }?.let { mode = it }
                    model = s.str("model")
                    busy = s.bool("busy")
                }
                messages = d.arr("messages").objects().map(AgyMessage::from)
                permissions = d.arr("permissions").objects().map(AgyPermission::from)
                queue = d.arr("queue").objects().map(AgyQueuedPrompt::from)
                streaming = d.str("streaming")
                thinking = d.str("thinking")
                thinkStart = d.num("think_start") ?: 0.0
                thinkEnd = d.num("think_end") ?: 0.0
                liveTools = d.arr("live_tools").objects().map(AgyToolCall::from)
                usedTokens = d.optInt("used_tokens", 0)
                maxTokens = d.optInt("max_tokens", 0)
                val mds = d.arr("modes").objects().map(AgyModeOption::from)
                if (mds.isNotEmpty()) modes = mds
                val mls = d.arr("models").objects().map(AgyModelOption::from)
                if (mls.isNotEmpty()) models = mls
                val cmds = d.arr("commands").objects().map(AgyCommandOption::from)
                if (cmds.isNotEmpty()) commands = cmds
            }

            "queue.update" -> {
                queue = ev.arr("data").objects().map(AgyQueuedPrompt::from)
            }

            "commands" -> {
                commands = ev.arr("data").objects().map(AgyCommandOption::from)
            }

            "message" -> {
                val d = ev.obj("data") ?: return
                val m = AgyMessage.from(d)
                if (messages.none { it.id == m.id }) {
                    messages = messages + m
                }
                if (m.role == "assistant") {
                    streaming = ""
                    thinking = ""
                    thinkStart = 0.0
                    thinkEnd = 0.0
                    liveTools = emptyList()
                }
            }

            "thinking.delta" -> {
                val d = ev.obj("data") ?: return
                if (thinkStart == 0.0) thinkStart = Stamp.now()
                thinkEnd = 0.0
                thinking += d.str("text")
            }

            "text.delta" -> {
                val d = ev.obj("data") ?: return
                if (thinkStart > 0.0 && thinkEnd == 0.0) thinkEnd = Stamp.now()
                streaming += d.str("text")
            }

            "tool.upsert" -> {
                val d = ev.obj("data") ?: return
                if (thinkStart > 0.0 && thinkEnd == 0.0) thinkEnd = Stamp.now()
                val tc = AgyToolCall.from(d)
                val idx = liveTools.indexOfFirst { it.id == tc.id }
                liveTools = if (idx >= 0) {
                    liveTools.toMutableList().also { it[idx] = tc }
                } else {
                    liveTools + tc
                }
            }

            "permission.upsert" -> {
                val d = ev.obj("data") ?: return
                val pr = AgyPermission.from(d)
                val idx = permissions.indexOfFirst { it.id == pr.id }
                permissions = if (idx >= 0) {
                    permissions.toMutableList().also { it[idx] = pr }
                } else {
                    permissions + pr
                }
            }

            "status" -> {
                val d = ev.obj("data") ?: return
                busy = d.bool("busy")
                d.str("title").takeIf { it.isNotEmpty() }?.let { title = it }
            }

            "session.meta" -> {
                val d = ev.obj("data") ?: return
                val s = d.obj("session")
                if (s != null) {
                    s.str("title").takeIf { it.isNotEmpty() }?.let { title = it }
                    s.str("mode").takeIf { it.isNotEmpty() }?.let { mode = it }
                    s.str("model").takeIf { it.isNotEmpty() }?.let { model = it }
                }
                val u = d.optInt("used_tokens", 0)
                if (u > 0) usedTokens = u
                val mx = d.optInt("max_tokens", 0)
                if (mx > 0) maxTokens = mx
            }

            "turn.done" -> {
                val d = ev.obj("data") ?: return
                busy = d.bool("busy")
                streaming = ""
                thinking = ""
                thinkStart = 0.0
                thinkEnd = 0.0
                liveTools = emptyList()
            }
        }
    }

    suspend fun send(text: String, behavior: String? = null) {
        val body = JSONObject().put("project_id", projectId).put("text", text)
        if (!behavior.isNullOrEmpty()) body.put("behavior", behavior)
        client.post("/api/agy/sessions/${enc(sessionId)}/messages", body)
    }

    suspend fun cancelQueued(queueId: String) {
        client.post("/api/agy/sessions/${enc(sessionId)}/queue/${enc(queueId)}", method = "DELETE")
    }

    suspend fun stopPrompt() {
        client.post("/api/agy/sessions/${enc(sessionId)}/stop")
    }

    suspend fun decide(permId: String, optionId: String) {
        client.post("/api/agy/sessions/${enc(sessionId)}/permissions/${enc(permId)}", JSONObject().put("option_id", optionId))
    }

    suspend fun setConfig(mode: String? = null, model: String? = null) {
        val body = JSONObject()
        if (mode != null) body.put("mode", mode)
        if (model != null) body.put("model", model)
        val out = client.post("/api/agy/sessions/${enc(sessionId)}/config", body)
        out.str("mode").takeIf { it.isNotEmpty() }?.let { this.mode = it }
        out.str("model").takeIf { it.isNotEmpty() }?.let { this.model = it }
    }
}
