package io.github.llc370219.frogbot.claude.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.outlined.Schedule
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.AssistChip
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.llc370219.frogbot.claude.core.AppState
import io.github.llc370219.frogbot.claude.core.BotRef
import io.github.llc370219.frogbot.claude.core.Schedule
import io.github.llc370219.frogbot.claude.core.ServerClient
import kotlinx.coroutines.launch

/** 自动调度器：选一个 Bot，看它的定时计划；可以暂停 / 恢复 / 立即执行 / 删除 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SchedulerScreen(state: AppState, nav: Nav) {
    val scope = rememberCoroutineScope()
    val refs = state.allRefs.filter { state.session(it) != null }
    var selected by remember { mutableStateOf(state.viewing ?: refs.firstOrNull()) }
    val ref = selected?.takeIf { it in refs } ?: refs.firstOrNull()
    val session = ref?.let { state.session(it) }
    var plans by remember { mutableStateOf<List<Schedule>?>(null) }
    var message by remember { mutableStateOf<Pair<String, Boolean>?>(null) }
    var picker by remember { mutableStateOf(false) }
    var reload by remember { mutableIntStateOf(0) }
    var confirm by remember { mutableStateOf<Schedule?>(null) }

    LaunchedEffect(ref, session?.scheduleRevision, reload) {
        plans = null
        if (session != null) {
            runCatching { session.schedules() }.onSuccess { plans = it; message = null }
                .onFailure { plans = emptyList(); message = "读不到定时计划：${ServerClient.describe(it)}" to true }
        }
    }

    fun act(p: Schedule, action: String) {
        val s = session ?: return
        val done = mapOf("pause" to "已暂停", "resume" to "已恢复", "run" to "已提交到任务队列", "delete" to "已删除")[action] ?: "完成"
        scope.launch {
            runCatching { s.schedule(p.id, action) }
                .onSuccess { message = "「${p.title}」$done" to false }
                .onFailure { message = "操作失败：${ServerClient.describe(it)}" to true }
            reload++
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("自动调度器") },
                actions = { IconButton(onClick = { reload++ }) { Icon(Icons.Filled.Refresh, "刷新") } },
            )
        },
        containerColor = MaterialTheme.colorScheme.surface,
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize()) {
            Box(Modifier.padding(horizontal = 16.dp)) {
                AssistChip(
                    onClick = { picker = true },
                    label = { Text(session?.let { "${it.profile.name} · ${it.deviceName}" } ?: "没有可用的 Bot") },
                    trailingIcon = { Icon(Icons.Filled.ArrowDropDown, null) },
                )
                DropdownMenu(picker, { picker = false }) {
                    refs.forEach { r ->
                        val s = state.session(r) ?: return@forEach
                        DropdownMenuItem(text = { Text("${s.profile.name} · ${s.deviceName}") }, onClick = { selected = r; picker = false })
                    }
                }
            }
            message?.let { Note(it.first, if (it.second) MaterialTheme.colorScheme.error else Frog.extra.ok) }
            when {
                session == null -> Note("还没有连接任何 Bot。")
                plans == null -> Busy()
                plans!!.isEmpty() -> Note("还没有定时计划。")
                else -> LazyColumn(Modifier.weight(1f)) {
                    items(plans!!, key = { it.id }) { p -> PlanRow(p, onToggle = { act(p, if (it) "resume" else "pause") }, onRun = { act(p, "run") }, onDelete = { confirm = p }) }
                }
            }
            Note("在对话里告诉 Bot 要做什么、什么时候做，就会出现在这里。「更多」里可以立即执行一次或删除。")
        }
    }

    confirm?.let { p ->
        AlertDialog(
            onDismissRequest = { confirm = null },
            title = { Text("删除定时计划「${p.title}」？") },
            confirmButton = { TextButton(onClick = { confirm = null; act(p, "delete") }) { Text("删除", color = MaterialTheme.colorScheme.error) } },
            dismissButton = { TextButton(onClick = { confirm = null }) { Text("取消") } },
        )
    }
}

@Composable
private fun PlanRow(p: Schedule, onToggle: (Boolean) -> Unit, onRun: () -> Unit, onDelete: () -> Unit) {
    var menu by remember { mutableStateOf(false) }
    ListRow(p.title, planDetail(p), Icons.Outlined.Schedule, titleColor = if (p.enabled) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.onSurfaceVariant) {
        Switch(checked = p.enabled, onCheckedChange = onToggle)
        Box {
            IconButton(onClick = { menu = true }) { Icon(Icons.Filled.MoreVert, "更多") }
            DropdownMenu(menu, { menu = false }) {
                DropdownMenuItem(text = { Text("立即执行一次") }, onClick = { menu = false; onRun() })
                DropdownMenuItem(text = { Text("删除", color = MaterialTheme.colorScheme.error) }, onClick = { menu = false; onDelete() })
            }
        }
    }
}

private fun planDetail(p: Schedule): String {
    val time = p.hour?.let { String.format("%02d:%02d", it, p.minute ?: 0) } ?: ""
    val parts = ArrayList<String>()
    when (p.kind) {
        "once" -> parts.add("单次" + (p.atLocal?.let { " · $it" } ?: ""))
        "daily" -> parts.add("每天 $time")
        "weekly" -> {
            val names = listOf("日", "一", "二", "三", "四", "五", "六")
            val days = p.weekdays.filter { it in 0..6 }.joinToString("、") { "周" + names[it] }
            parts.add((days.ifEmpty { "每周" }) + " " + time)
        }
        "" -> {}
        else -> parts.add(p.kind)
    }
    if (!p.enabled) parts.add("已暂停") else p.nextRun?.takeIf { it.isNotEmpty() }?.let { parts.add("下次：$it") }
    return parts.joinToString(" · ")
}

@Suppress("unused")
private fun keepRef(r: BotRef) = r
