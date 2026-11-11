package io.github.llc370219.frogbot.claude.core

import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant
import java.time.OffsetDateTime

// 与 Go 后端、网页版 web/src/api.ts 的数据一一对应。JSON 用 Android 自带的 org.json 宽松读取：取不到就给空值。

fun JSONObject?.str(key: String): String = if (this == null || !has(key) || isNull(key)) "" else optString(key, "")
fun JSONObject?.strOrNull(key: String): String? = if (this == null || isNull(key) || !has(key)) null else optString(key)
fun JSONObject?.bool(key: String): Boolean = this?.optBoolean(key, false) ?: false
fun JSONObject?.num(key: String): Double? = if (this == null || !has(key) || isNull(key)) null else optDouble(key).takeUnless { it.isNaN() }
fun JSONObject?.obj(key: String): JSONObject? = this?.optJSONObject(key)
fun JSONObject?.arr(key: String): JSONArray = this?.optJSONArray(key) ?: JSONArray()

fun JSONArray.objects(): List<JSONObject> = (0 until length()).mapNotNull { optJSONObject(it) }
fun JSONArray.strings(): List<String> = (0 until length()).mapNotNull { if (isNull(it)) null else optString(it) }

object Stamp {
    fun now(): Double = System.currentTimeMillis().toDouble()

    /** ISO 时间 → 毫秒；解析不了就当作“现在”（和网页版 ts() 一样） */
    fun ms(s: String?): Double {
        if (s.isNullOrEmpty()) return now()
        return try {
            OffsetDateTime.parse(s).toInstant().toEpochMilli().toDouble()
        } catch (_: Exception) {
            try { Instant.parse(s).toEpochMilli().toDouble() } catch (_: Exception) { now() }
        }
    }
}

data class BotProfile(val id: String, val name: String, val instructions: String = "", val avatarSlot: Int? = null) {
    companion object {
        fun from(j: JSONObject) = BotProfile(
            id = j.str("id"), name = j.str("name"), instructions = j.str("instructions"),
            avatarSlot = if (j.has("avatarSlot") && !j.isNull("avatarSlot")) j.optInt("avatarSlot") else null,
        )
    }
}

data class DeviceGroup(val id: String, val name: String, val online: Boolean, val bots: List<BotProfile>) {
    companion object {
        fun from(j: JSONObject) = DeviceGroup(j.str("id"), j.str("name"), j.bool("online"), j.arr("bots").objects().map(BotProfile::from))
    }
}

/** 一台设备上的一个 Bot：服务器 + 设备（本机为 "local"）+ Bot ID */
data class BotRef(val server: String, val device: String, val bot: String) {
    val key: String get() = "$server|$device|$bot"

    companion object {
        fun parse(key: String): BotRef? {
            val p = key.split("|")
            return if (p.size == 3) BotRef(p[0], p[1], p[2]) else null
        }
    }
}

data class Attachment(val path: String, val name: String, val size: Long, val mime: String?) {
    val isImage: Boolean
        get() {
            if (!mime.isNullOrEmpty()) return mime.startsWith("image/") && !mime.contains("svg")
            val ext = name.substringAfterLast('.', "").lowercase()
            return ext in setOf("png", "jpg", "jpeg", "gif", "webp", "heic")
        }

    fun json(): JSONObject = JSONObject().put("path", path).put("name", name).put("size", size).apply { if (mime != null) put("mime", mime) }

    companion object {
        fun from(j: JSONObject) = Attachment(
            path = j.str("path"),
            name = j.strOrNull("name") ?: j.str("path").substringAfterLast('/'),
            size = j.optLong("size", 0), mime = j.strOrNull("mime"),
        )
    }
}

data class Card(val id: String, val question: String, val options: List<String>, val status: String, val answer: String?) {
    companion object {
        fun from(j: JSONObject) = Card(j.str("id"), j.str("question"), j.arr("options").strings(), j.strOrNull("status") ?: "pending", j.strOrNull("answer"))
    }
}

data class SecretRequest(val id: String, val name: String, val description: String?, val status: String, val exists: Boolean) {
    companion object {
        fun from(j: JSONObject) = SecretRequest(j.str("id"), j.str("name"), j.strOrNull("description"), j.strOrNull("status") ?: "pending", j.bool("exists"))
    }
}

data class Reaction(val by: String, val emoji: String)

data class Msg(
    val id: String,
    val role: String,
    val text: String,
    val card: Card?,
    val secret: SecretRequest?,
    val tools: List<String>,
    val created: Double,
    val thinking: String?,
    val thinkingMs: Double?,
    val files: List<Attachment>,
    val reactions: List<Reaction>,
) {
    companion object {
        fun from(j: JSONObject) = Msg(
            id = j.str("id"), role = j.str("role"), text = j.str("text"),
            card = j.obj("card")?.let(Card::from), secret = j.obj("secret")?.let(SecretRequest::from),
            tools = j.arr("tools").strings(), created = Stamp.ms(j.strOrNull("created_at")),
            thinking = j.strOrNull("thinking"), thinkingMs = j.num("thinking_ms"),
            files = j.arr("files").objects().map(Attachment::from),
            reactions = j.arr("reactions").objects().map { Reaction(it.str("by"), it.str("emoji")) },
        )
    }
}

data class Step(
    val id: String,
    val tool: String,
    val args: JSONObject,
    val thought: String,
    val output: String? = null,
    val error: Boolean = false,
    val denied: Boolean = false,
    val done: Boolean = false,
    val at: Double,
    val doneAt: Double? = null,
)

enum class TaskStatus {
    queued, running, paused, completed, failed, cancelled;

    val terminal: Boolean get() = this == completed || this == failed || this == cancelled

    companion object {
        fun of(s: String?): TaskStatus? = entries.firstOrNull { it.name == s }
    }
}

data class BotTask(
    val id: String,
    val title: String,
    val input: String,
    val status: TaskStatus,
    val steps: List<Step> = emptyList(),
    val text: String = "",
    val thinking: String = "",
    val output: String? = null,
    val error: String? = null,
    val stopReason: String? = null,
    val created: Double,
    val started: Double? = null,
    val ended: Double? = null,
    val awaiting: Int = 0,
    val prio: Double,
    val pausing: Boolean = false,
    val files: List<String> = emptyList(),
    val summary: String? = null,
    val duration: Double? = null,
    val lastEvent: Double,
    val lastBeat: Double,
    val activeMs: Double = 0.0,
) {
    val isOpen: Boolean get() = !status.terminal
    val isLive: Boolean get() = status == TaskStatus.queued || status == TaskStatus.running
    fun elapsed(now: Double): Double = duration ?: (activeMs + (started?.let { now - it } ?: 0.0))
}

data class Approval(
    val id: String,
    val taskId: String,
    val toolName: String,
    val args: JSONObject,
    val risk: String,
    val reason: String,
    val status: String, // pending | allow | deny | expired
    val scope: String?,
    val created: Double,
) {
    val fromChat: Boolean get() = taskId.startsWith("chat_")
}

enum class ItemKind { Msg, Task, Approval }

data class TimelineItem(val kind: ItemKind, val id: String)

// ---------- 设置：每个 Bot 独立的 API 渠道、聊天模型、任务模型、Agent ----------

data class ApiChannel(
    val id: String,
    val name: String,
    val baseUrl: String,
    val enabled: Boolean,
    val authRequired: Boolean,
    val apiKey: String? = null, // 只写：null 表示保留服务器上原来的密钥
    val hasApiKey: Boolean = false,
    val models: List<String> = emptyList(),
    val hiddenModels: List<String> = emptyList(),
) {
    val visibleModels: List<String> get() = models.filter { it !in hiddenModels }

    fun json(): JSONObject = JSONObject()
        .put("id", id).put("name", name).put("base_url", baseUrl).put("enabled", enabled)
        .put("auth_required", authRequired)
        .put("models", JSONArray(models)).put("hidden_models", JSONArray(hiddenModels))
        .apply { if (apiKey != null) put("api_key", apiKey) }

    companion object {
        fun from(j: JSONObject) = ApiChannel(
            id = j.str("id"), name = j.str("name"), baseUrl = j.str("base_url"), enabled = j.bool("enabled"),
            authRequired = j.bool("auth_required"), hasApiKey = j.bool("has_api_key"),
            models = j.arr("models").strings(), hiddenModels = j.arr("hidden_models").strings(),
        )
    }
}

data class ApiSettings(
    val channels: List<ApiChannel>,
    val chatChannel: String,
    val chatModel: String,
    val agentChannel: String,
    val agentModel: String,
    val envOverride: Boolean,
) {
    fun channel(id: String) = channels.firstOrNull { it.id == id }

    fun draft(): JSONObject = JSONObject()
        .put("providers", JSONArray(channels.map { it.json() }))
        .put("default_provider_id", chatChannel)
        .put("model", chatModel)
        .put("agent_provider_id", agentChannel)
        .put("agent_model", agentModel)
        .put("base_url", channel(chatChannel)?.baseUrl ?: "")

    companion object {
        fun from(j: JSONObject): ApiSettings {
            val channels = j.arr("providers").objects().map(ApiChannel::from)
            val chat = j.strOrNull("default_provider_id")?.takeIf { it.isNotEmpty() } ?: channels.firstOrNull()?.id ?: ""
            val model = j.str("model")
            return ApiSettings(
                channels = channels, chatChannel = chat, chatModel = model,
                agentChannel = j.strOrNull("agent_provider_id")?.takeIf { it.isNotEmpty() } ?: chat,
                agentModel = j.strOrNull("agent_model")?.takeIf { it.isNotEmpty() } ?: model,
                envOverride = j.bool("env_override"),
            )
        }
    }
}

data class CliStatus(val id: String, val name: String, val installed: Boolean)
data class AgentConnections(val cli: String, val clis: List<CliStatus>) {
    companion object {
        fun from(j: JSONObject) = AgentConnections(j.str("cli"), j.arr("clis").objects().map { CliStatus(it.str("id"), it.str("name"), it.bool("installed")) })
    }
}

data class Schedule(
    val id: String, val title: String, val enabled: Boolean, val kind: String,
    val hour: Int?, val minute: Int?, val weekdays: List<Int>,
    val nextRun: String?, val lastStatus: String?, val atLocal: String?,
) {
    companion object {
        fun from(j: JSONObject) = Schedule(
            id = j.str("id"), title = j.str("title"), enabled = j.bool("enabled"), kind = j.str("kind"),
            hour = j.num("hour")?.toInt(), minute = j.num("minute")?.toInt(),
            weekdays = (0 until j.arr("weekdays").length()).map { j.arr("weekdays").optInt(it) },
            nextRun = j.strOrNull("next_run_local"), lastStatus = j.strOrNull("last_status"), atLocal = j.strOrNull("at_local"),
        )
    }
}

data class BotInfo(val name: String, val chatModel: String, val agentModel: String, val version: String?, val stallSec: Int?) {
    companion object {
        fun from(j: JSONObject) = BotInfo(j.str("name"), j.str("chat_model"), j.str("agent_model"), j.strOrNull("version"), j.num("stall_sec")?.toInt())
    }
}
