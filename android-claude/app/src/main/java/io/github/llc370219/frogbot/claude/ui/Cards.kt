package io.github.llc370219.frogbot.claude.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.outlined.Block
import androidx.compose.material.icons.outlined.Description
import androidx.compose.material.icons.outlined.Edit
import androidx.compose.material.icons.outlined.Folder
import androidx.compose.material.icons.outlined.GppMaybe
import androidx.compose.material.icons.outlined.HourglassEmpty
import androidx.compose.material.icons.outlined.InsertDriveFile
import androidx.compose.material.icons.outlined.Key
import androidx.compose.material.icons.outlined.Pause
import androidx.compose.material.icons.outlined.Search
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.AssistChip
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableDoubleStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.llc370219.frogbot.claude.core.Approval
import io.github.llc370219.frogbot.claude.core.Attachment
import io.github.llc370219.frogbot.claude.core.BotSession
import io.github.llc370219.frogbot.claude.core.BotTask
import io.github.llc370219.frogbot.claude.core.Card
import io.github.llc370219.frogbot.claude.core.Fmt
import io.github.llc370219.frogbot.claude.core.Msg
import io.github.llc370219.frogbot.claude.core.SecretRequest
import io.github.llc370219.frogbot.claude.core.ServerClient
import io.github.llc370219.frogbot.claude.core.Stamp
import io.github.llc370219.frogbot.claude.core.Step
import io.github.llc370219.frogbot.claude.core.TaskStatus
import io.github.llc370219.frogbot.claude.core.str
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/** 每秒走一次的“现在”，运行中的任务计时用 */
@Composable
fun rememberNow(live: Boolean): Double {
    var now by remember { mutableDoubleStateOf(Stamp.now()) }
    LaunchedEffect(live) {
        while (live) { now = Stamp.now(); delay(1000) }
    }
    return now
}

// ---------- 消息 ----------

@Composable
fun UserBubble(msg: Msg, session: BotSession) {
    Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(4.dp)) {
        if (msg.files.isNotEmpty()) Files(msg.files)
        if (msg.text.isNotEmpty()) {
            Box(
                Modifier.widthIn(max = 300.dp)
                    .background(MaterialTheme.colorScheme.primary, RoundedCornerShape(20.dp, 20.dp, 4.dp, 20.dp))
                    .padding(horizontal = 14.dp, vertical = 10.dp),
            ) {
                Text(msg.text, color = MaterialTheme.colorScheme.onPrimary, fontSize = 15.sp, lineHeight = 22.sp)
            }
        }
        Reactions(msg)
    }
}

/** 助理的回复：一句一个气泡，头像贴着最后一个气泡 */
@Composable
fun AssistantRow(session: BotSession, text: String, streaming: Boolean = false, extras: @Composable () -> Unit = {}) {
    val pieces = remember(text) { bubblePieces(text) }
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Row(verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            BotAvatar(session.profile, 32.dp)
            Column(verticalArrangement = Arrangement.spacedBy(2.dp), modifier = Modifier.weight(1f)) {
                if (pieces.isEmpty() && streaming) Bubble(true) { TypingDots() }
                pieces.forEachIndexed { i, p ->
                    val last = i == pieces.lastIndex
                    if (p is MdBlock.Code) CodeBox(p.code)
                    else Bubble(last) { MdBlockView(p, caret = streaming && last) }
                }
            }
        }
        Column(Modifier.padding(start = 40.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) { extras() }
    }
}

private fun bubblePieces(text: String): List<MdBlock> = Markdown.parse(text).flatMap { b ->
    if (b is MdBlock.Para) Fmt.sentences(b.text).ifEmpty { listOf(b.text) }.map { MdBlock.Para(it) } else listOf(b)
}

@Composable
private fun Bubble(last: Boolean, content: @Composable () -> Unit) {
    Box(
        Modifier.widthIn(max = 290.dp)
            .background(MaterialTheme.colorScheme.surfaceContainerHigh, RoundedCornerShape(20.dp, 20.dp, 20.dp, if (last) 4.dp else 20.dp))
            .padding(horizontal = 14.dp, vertical = 10.dp),
    ) { content() }
}

@Composable
fun TypingDots() {
    val now = rememberNow(true)
    val phase = ((now / 400).toLong() % 3).toInt()
    Row(horizontalArrangement = Arrangement.spacedBy(5.dp), modifier = Modifier.padding(vertical = 6.dp)) {
        repeat(3) { i ->
            Box(Modifier.size(7.dp).background(MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = if (i == phase) 0.9f else 0.35f), RoundedCornerShape(50)))
        }
    }
}

@Composable
fun AssistantMessage(session: BotSession, msg: Msg) {
    AssistantRow(session, msg.text.ifEmpty { if (msg.card == null && msg.secret == null && msg.files.isEmpty()) "…" else "" }) {
        if (!msg.thinking.isNullOrEmpty()) Thinking(msg.thinking, msg.thinkingMs)
        if (msg.files.isNotEmpty()) Files(msg.files)
        msg.card?.let { OptionCard(session, it) }
        msg.secret?.let { SecretCard(session, it) }
        Reactions(msg)
    }
}

@Composable
private fun Thinking(text: String, ms: Double?) {
    var open by remember { mutableStateOf(false) }
    Column(Modifier.animateContentSize()) {
        Text(
            "已思考 ${maxOf(1, ((ms ?: 0.0) / 1000).toInt())} 秒 " + if (open) "▴" else "▾",
            style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.clickable { open = !open },
        )
        if (open) Text(text, fontStyle = FontStyle.Italic, fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

@Composable
private fun Reactions(msg: Msg) {
    if (msg.reactions.isEmpty()) return
    Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
        msg.reactions.forEach { r ->
            Text(
                r.emoji, fontSize = 14.sp,
                modifier = Modifier.background(MaterialTheme.colorScheme.surfaceContainerHigh, RoundedCornerShape(50)).padding(horizontal = 8.dp, vertical = 2.dp),
            )
        }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun Files(files: List<Attachment>) {
    FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        files.forEach { f ->
            AssistChip(
                onClick = {}, label = { Text(f.name, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                leadingIcon = { Icon(if (f.isImage) Icons.Outlined.Description else Icons.Outlined.InsertDriveFile, null, Modifier.size(18.dp)) },
            )
        }
    }
}

// ---------- 任务卡片 ----------

@Composable
fun TaskCard(session: BotSession, task: BotTask, pos: Int?, onApprove: () -> Unit) {
    var userOpen by remember { mutableStateOf<Boolean?>(null) }
    val open = userOpen ?: (task.status == TaskStatus.running)
    val now = rememberNow(task.isLive)
    val ex = Frog.extra
    Column(
        Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerHigh, Shapes16)
            .let { if (task.awaiting > 0) it.border(1.dp, ex.att.copy(alpha = 0.5f), Shapes16) else it }
            .animateContentSize(),
    ) {
        Row(
            Modifier.fillMaxWidth().clickable { userOpen = !open }.padding(horizontal = 16.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            StatusIcon(task)
            Column(Modifier.weight(1f)) {
                Text(task.title, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(
                    Fmt.statusLine(task, now, pos), style = MaterialTheme.typography.bodySmall, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    color = if (task.awaiting > 0) ex.att else MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Icon(if (open) Icons.Filled.ExpandLess else Icons.Filled.ExpandMore, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        if (open) {
            Column(Modifier.padding(bottom = 8.dp)) {
                if (task.steps.isEmpty()) {
                    Text(
                        if (task.status == TaskStatus.queued) "等前面的任务做完…" else "正在看要做什么…",
                        style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                    )
                }
                task.steps.forEach { StepRow(it, now) }
                if (task.status == TaskStatus.running && task.text.isNotEmpty()) {
                    Text(task.text.takeLast(300) + " ▍", fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp))
                }
                if (!task.isLive && !task.error.isNullOrEmpty()) {
                    Text(
                        task.error, fontSize = 13.sp, color = MaterialTheme.colorScheme.error,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                    )
                }
                if (task.status == TaskStatus.completed) {
                    val result = (task.summary ?: task.output ?: "").trim()
                    if (result.isNotEmpty()) {
                        Text(result.replace(Regex("[*`#]"), ""), fontSize = 13.sp, maxLines = 4, overflow = TextOverflow.Ellipsis,
                            color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp))
                    }
                }
                TaskActions(session, task, pos, Modifier.padding(horizontal = 8.dp))
            }
        }
    }
}

@Composable
private fun StatusIcon(task: BotTask) {
    val ex = Frog.extra
    Box(Modifier.size(22.dp), contentAlignment = Alignment.Center) {
        when {
            task.awaiting > 0 -> Icon(Icons.Outlined.GppMaybe, null, tint = ex.att)
            task.status == TaskStatus.running -> CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.5.dp)
            task.status == TaskStatus.completed -> Icon(Icons.Filled.Check, null, tint = ex.ok)
            task.status == TaskStatus.failed -> Icon(Icons.Filled.Close, null, tint = MaterialTheme.colorScheme.error)
            task.status == TaskStatus.paused -> Icon(Icons.Outlined.Pause, null, tint = ex.warn)
            task.status == TaskStatus.queued -> Icon(Icons.Outlined.HourglassEmpty, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
            else -> Icon(Icons.Outlined.Block, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

@Composable
fun TaskActions(session: BotSession, task: BotTask, pos: Int?, modifier: Modifier = Modifier) {
    val scope = rememberCoroutineScope()
    fun act(a: String) { scope.launch { runCatching { session.task(task.id, a) } } }
    Row(modifier, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
        when (task.status) {
            TaskStatus.running -> {
                if (!task.pausing) TextButton(onClick = { act("pause") }) { Text("暂停") }
                TextButton(onClick = { act("cancel") }) { Text("停止", color = MaterialTheme.colorScheme.error) }
            }
            TaskStatus.paused -> {
                TextButton(onClick = { act("resume") }) { Text(if (task.stopReason == "held") "开始" else "继续") }
                TextButton(onClick = { act("cancel") }) { Text("停止", color = MaterialTheme.colorScheme.error) }
            }
            TaskStatus.queued -> {
                if ((pos ?: 0) > 1) TextButton(onClick = { act("top") }) { Text("下一个做") }
                TextButton(onClick = { act("pause") }) { Text("挂起") }
                TextButton(onClick = { act("cancel") }) { Text("停止", color = MaterialTheme.colorScheme.error) }
            }
            TaskStatus.failed, TaskStatus.cancelled -> TextButton(onClick = { act("retry") }) { Text("重试") }
            TaskStatus.completed -> {}
        }
    }
}

@Composable
private fun StepRow(step: Step, now: Double) {
    var open by remember { mutableStateOf(false) }
    val (verb, target) = Fmt.describe(step.tool, step.args)
    val icon = when (step.tool) {
        "read_file" -> Icons.Outlined.Description
        "write_file", "edit_file" -> Icons.Outlined.Edit
        "list_dir" -> Icons.Outlined.Folder
        "grep" -> Icons.Outlined.Search
        else -> Icons.Outlined.Terminal
    }
    Column(Modifier.fillMaxWidth().clickable(enabled = step.output != null) { open = !open }.padding(horizontal = 16.dp, vertical = 6.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            Icon(icon, null, Modifier.size(18.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
            Text(verb, fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Text(target, fontSize = 12.5.sp, fontFamily = FontFamily.Monospace, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            when {
                !step.done -> {
                    if (now - step.at > 3000) Text("${((now - step.at) / 1000).toInt()}s", fontSize = 11.sp, color = MaterialTheme.colorScheme.primary)
                    CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp)
                }
                step.denied -> Text("未批准", fontSize = 11.sp, color = MaterialTheme.colorScheme.error)
                step.error -> Text("出错", fontSize = 11.sp, color = Frog.extra.warn)
                else -> Icon(Icons.Filled.Check, null, Modifier.size(16.dp), tint = Frog.extra.ok)
            }
        }
        if (open && step.output != null) {
            Spacer(Modifier.size(6.dp))
            CodeBox(step.output.take(4000))
        }
    }
}

// ---------- 审批卡片 ----------

val toolLabels = mapOf(
    "run_as_root" to "以 root 执行命令", "shell" to "执行命令", "run_agent_cli" to "启动外部 Agent", "write_file" to "写入文件", "edit_file" to "修改文件",
    "read_file" to "读取文件", "list_dir" to "查看目录", "grep" to "搜索", "delete_secret" to "删除密钥",
)
val riskLabels = mapOf("danger" to "高风险", "exec" to "执行命令", "write" to "写入文件", "read" to "只读")

fun approvalQuestion(a: Approval) = when (a.toolName) {
    "run_as_root" -> "允许以 root 身份执行这条命令吗？"
    "shell" -> "允许执行这条命令吗？"
    "run_agent_cli" -> "允许启动外部 Agent 吗？"
    "write_file" -> "允许写入这个文件吗？"
    "edit_file" -> "允许修改这个文件吗？"
    "delete_secret" -> "允许删除这个密钥吗？"
    else -> "允许这个操作吗？"
}

fun approvalTarget(a: Approval): String {
    val g = a.args
    return when (a.toolName) {
        "run_as_root" -> "# " + g.str("command")
        "shell" -> "$ " + g.str("command")
        "run_agent_cli" -> g.str("cli") + "\n" + g.str("task")
        "write_file", "edit_file" -> g.str("path")
        "delete_secret" -> g.str("name")
        else -> g.toString(2)
    }
}

@Composable
fun ApprovalCard(session: BotSession, a: Approval, onDetails: () -> Unit) {
    val ex = Frog.extra
    if (a.status != "pending") {
        val (text, color) = when (a.status) {
            "allow" -> (if (a.scope == "session") "已允许，本任务内不再询问" else "已允许一次") to ex.ok
            "deny" -> "已拒绝" to MaterialTheme.colorScheme.error
            else -> "已失效" to MaterialTheme.colorScheme.onSurfaceVariant
        }
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Icon(if (a.status == "allow") Icons.Filled.Check else Icons.Filled.Close, null, Modifier.size(16.dp), tint = color)
            Text(text, fontSize = 13.sp, color = color)
            Text(approvalTarget(a).lines().first(), fontSize = 12.sp, fontFamily = FontFamily.Monospace, maxLines = 1, overflow = TextOverflow.Ellipsis,
                color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        return
    }
    val scope = rememberCoroutineScope()
    var busy by remember { mutableStateOf(false) }
    Column(
        Modifier.fillMaxWidth().background(ex.attContainer, Shapes16).padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Icon(Icons.Outlined.GppMaybe, null, tint = ex.onAttContainer)
            Column {
                Text(approvalQuestion(a), color = ex.onAttContainer, style = MaterialTheme.typography.titleSmall)
                val sub = listOfNotNull(session.tasks[a.taskId]?.title?.let { "「$it」" }, riskLabels[a.risk]).joinToString(" · ")
                if (sub.isNotEmpty()) Text(sub, color = ex.onAttContainer.copy(alpha = 0.8f), fontSize = 13.sp)
            }
        }
        Text(
            approvalTarget(a), fontFamily = FontFamily.Monospace, fontSize = 13.sp, maxLines = 6, overflow = TextOverflow.Ellipsis,
            color = MaterialTheme.colorScheme.onSurface,
            modifier = Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surface, Shapes12).padding(12.dp),
        )
        if (session.isLocalDevice) {
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End)) {
                TextButton(onClick = onDetails) { Text("查看详情", color = ex.onAttContainer) }
                if (a.toolName == "run_as_root") {
                    Button(onClick = onDetails) { Text("输入密码授权") } // root 命令要在详情里输入登录密码
                } else Button(onClick = {
                    busy = true
                    scope.launch { runCatching { session.decide(a.id, "once") }; busy = false }
                }, enabled = !busy) { Text("允许一次") }
            }
        } else {
            Text("这个操作在「${session.deviceName}」上，请在那台设备的 frog 界面审批。", fontSize = 13.sp, color = ex.onAttContainer)
        }
    }
}

// ---------- 选项卡片、密钥卡片 ----------

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun OptionCard(session: BotSession, card: Card) {
    val scope = rememberCoroutineScope()
    val pending = card.status == "pending"
    Column(
        Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerHigh, Shapes16).padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Text(card.question, style = MaterialTheme.typography.titleSmall)
        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            card.options.forEach { o ->
                FilterChip(
                    selected = card.answer == o, enabled = pending,
                    onClick = { scope.launch { runCatching { session.answer(card.id, o) } } },
                    label = { Text(o) },
                )
            }
        }
        if (pending) TextButton(onClick = { scope.launch { runCatching { session.dismiss(card.id) } } }) { Text("都不选") }
        if (card.status == "dismissed") Text("已关闭，未选择", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

@Composable
fun SecretCard(session: BotSession, req: SecretRequest) {
    val scope = rememberCoroutineScope()
    var value by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    when (req.status) {
        "saved" -> Text("🔒 已安全保存 ${req.name}（值已加密，不会显示）", fontSize = 13.sp, color = Frog.extra.ok)
        "cancelled" -> Text("已取消，未保存 ${req.name}", fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
        else -> Column(
            Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainerHigh, Shapes16).padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            Row(horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.CenterVertically) {
                Icon(Icons.Outlined.Key, null, tint = MaterialTheme.colorScheme.primary)
                Text((if (req.exists) "更新密钥 " else "保存密钥 ") + req.name, style = MaterialTheme.typography.titleSmall)
            }
            if (!req.description.isNullOrEmpty()) Text(req.description, fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            if (!session.isLocalDevice) {
                Text("请在「${session.deviceName}」的 frog 界面填写。", fontSize = 13.sp)
            } else {
                OutlinedTextField(
                    value, { value = it }, label = { Text("粘贴密钥") }, singleLine = true,
                    visualTransformation = PasswordVisualTransformation(),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                    modifier = Modifier.fillMaxWidth(),
                )
                Text("直接加密保存在服务器上，不进聊天记录，助理也看不到。", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                error?.let { Text(it, fontSize = 12.sp, color = MaterialTheme.colorScheme.error) }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Button(enabled = value.isNotEmpty() && !busy, onClick = {
                        busy = true; error = null
                        scope.launch {
                            runCatching { session.saveSecret(req.id, value) }.onSuccess { value = "" }.onFailure { error = ServerClient.describe(it) }
                            busy = false
                        }
                    }) { Text("保存") }
                    OutlinedButton(enabled = !busy, onClick = { scope.launch { runCatching { session.cancelSecret(req.id) } } }) { Text("取消") }
                }
            }
        }
    }
}

@Suppress("unused")
private val unusedColor = Color.Unspecified
