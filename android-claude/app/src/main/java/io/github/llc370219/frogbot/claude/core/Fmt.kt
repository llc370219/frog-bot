package io.github.llc370219.frogbot.claude.core

import java.text.SimpleDateFormat
import java.util.Calendar
import java.util.Date
import java.util.Locale
import kotlin.math.roundToInt

/** 文字格式化：时长、相对时间、把工具调用翻译成人话（和网页版 lib/format.ts 一样） */
object Fmt {
    fun dur(ms: Double): String {
        val s = maxOf(0, (ms / 1000).roundToInt())
        if (s < 60) return "$s 秒"
        val m = s / 60
        if (m < 60) return "$m 分 ${s % 60} 秒"
        return "${m / 60} 小时 ${m % 60} 分"
    }

    fun clock(ms: Double): String {
        val s = maxOf(0, (ms / 1000).toInt())
        return "${s / 60}:" + String.format(Locale.US, "%02d", s % 60)
    }

    fun ago(ms: Double): String =
        if (ms < 60_000) "${maxOf(1, (ms / 1000).roundToInt())} 秒前" else "${(ms / 60_000).roundToInt()} 分钟前"

    fun size(n: Long): String = when {
        n < 1024 -> "$n B"
        n < 1 shl 20 -> "${n / 1024} KB"
        else -> String.format(Locale.US, "%.1f MB", n / 1048576.0)
    }

    fun base(p: String) = p.substringAfterLast('/')

    fun time(ms: Double): String {
        val d = Date(ms.toLong())
        val now = Calendar.getInstance()
        val c = Calendar.getInstance().apply { time = d }
        val today = now.get(Calendar.YEAR) == c.get(Calendar.YEAR) && now.get(Calendar.DAY_OF_YEAR) == c.get(Calendar.DAY_OF_YEAR)
        return SimpleDateFormat(if (today) "HH:mm" else "M月d日", Locale.CHINA).format(d)
    }

    private val shellKinds = listOf(
        Regex("""\b(go test|npm (run )?test|pytest|cargo test|jest|vitest)\b""") to "运行测试",
        Regex("""\b(go build|npm run build|make|cargo build|tsc)\b""") to "构建项目",
        Regex("""\b(go run|node|python3?|\./)\S*""") to "运行程序",
        Regex("""\b(npm|pnpm|yarn|pip3?|go get|apt(-get)?) (i|install|add)\b""") to "安装依赖",
        Regex("""^git\b""") to "Git 操作",
        Regex("""\b(curl|wget)\b""") to "访问网络",
        Regex("""^(ls|tree|find)\b""") to "查看文件",
        Regex("""^(cat|head|tail|less|wc)\b""") to "查看内容",
        Regex("""^(rm|mv|cp|mkdir)\b""") to "整理文件",
    )

    /** 一次工具调用 → (动作, 对象)，例如 ("读取", "README.md") */
    fun describe(tool: String, args: org.json.JSONObject): Pair<String, String> = when (tool) {
        "run_agent_cli" -> "运行" to (args.strOrNull("cli") ?: "外部 Agent")
        "read_file" -> "读取" to base(args.str("path"))
        "write_file" -> "写入" to base(args.str("path"))
        "edit_file" -> "修改" to base(args.str("path"))
        "list_dir" -> "查看目录" to args.str("path").let { if (it.isEmpty() || it == ".") "工作区" else it }
        "grep" -> "搜索" to "「${args.str("pattern")}」"
        "shell" -> {
            val c = args.str("command").trim()
            val verb = shellKinds.firstOrNull { it.first.containsMatchIn(c) }?.second ?: "执行命令"
            verb to (if (c.length > 60) c.take(57) + "…" else c)
        }
        else -> tool to ""
    }

    fun nowDoing(t: BotTask): String {
        if (t.awaiting > 0) return "等你确认一个操作"
        if (t.pausing) return "正在暂停（做完这一步就停）"
        t.steps.lastOrNull { !it.done }?.let { cur ->
            val (verb, target) = describe(cur.tool, cur.args)
            return "正在$verb" + if (target.isNotEmpty() && cur.tool != "shell") " $target" else ""
        }
        if (t.text.isNotEmpty()) return "正在整理结果"
        if (t.thinking.isNotEmpty()) return "正在思考下一步"
        return if (t.steps.isEmpty()) "正在看要做什么" else "正在想下一步"
    }

    fun statusLine(t: BotTask, now: Double, pos: Int?): String {
        val el = dur(t.elapsed(now))
        return when (t.status) {
            TaskStatus.completed -> "已完成 · 用时 $el · ${t.steps.size} 步" + if (t.files.isEmpty()) "" else " · 改动 ${t.files.size} 个文件"
            TaskStatus.cancelled -> (if (t.stopReason == "stall") "长时间没有进展，已自动停止" else "已停止") + " · ${t.steps.size} 步"
            TaskStatus.failed -> "失败 · ${t.steps.size} 步 · $el"
            TaskStatus.paused -> when (t.stopReason) {
                "held" -> "待办 · 还没开始，等你说开始"
                "restart" -> "服务重启后暂停 · 进度已保存"
                "chat" -> "先暂停，等聊完再继续"
                else -> "已暂停 · 进度已保存"
            }
            TaskStatus.queued -> pos?.let { "排队中 · 第 $it 位" } ?: "马上开始…"
            TaskStatus.running -> "${nowDoing(t)} · ${t.steps.size} 步 · $el"
        }
    }

    fun liveToolLabel(label: String): String {
        for (p in listOf("查看了", "读取了", "搜索了", "请求", "派出了", "写入了", "停止了", "暂停了", "恢复了", "排上了")) {
            if (label.startsWith(p)) return "正在${p.replace("了", "")}${label.removePrefix(p)}…"
        }
        return "$label…"
    }

    /** 把一段话拆成一句一句（和网页版 splitSentences 一样） */
    fun sentences(text: String): List<String> {
        val out = ArrayList<String>()
        val cur = StringBuilder()
        var inCode = false
        fun push() {
            val t = cur.toString().trim()
            if (t.isNotEmpty()) out.add(t)
            cur.setLength(0)
        }
        var i = 0
        while (i < text.length) {
            val ch = text[i]
            cur.append(ch)
            if (ch == '`') inCode = !inCode
            if (!inCode) {
                if (ch == '\n') {
                    push()
                } else if (ch == '。' || ch == '！' || ch == '？') {
                    if (i + 1 < text.length && text[i + 1] in "”\"’'") { i++; cur.append(text[i]) }
                    push()
                } else if (ch == '!' || ch == '?') {
                    if (i + 1 == text.length || text[i + 1].isWhitespace()) push()
                }
            }
            i++
        }
        push()
        return out
    }
}
