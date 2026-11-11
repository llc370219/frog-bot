package io.github.llc370219.frogbot.claude.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.llc370219.frogbot.claude.core.ApiSettings
import io.github.llc370219.frogbot.claude.core.AppState
import io.github.llc370219.frogbot.claude.core.BotRef
import io.github.llc370219.frogbot.claude.core.ServerClient
import io.github.llc370219.frogbot.claude.core.ServerEntry
import kotlinx.coroutines.launch

/** 新建 / 编辑 Bot：名字、职责，以及这个 Bot 自己的聊天模型和任务模型 */
@Composable
fun BotEditorScreen(state: AppState, server: ServerEntry, device: String, ref: BotRef?, nav: Nav) {
    val scope = rememberCoroutineScope()
    val profile = ref?.let { server.profile(it) }
    val session = ref?.let { state.session(it) }
    var name by remember { mutableStateOf(profile?.name ?: "") }
    var instructions by remember { mutableStateOf(profile?.instructions ?: "") }
    var settings by remember { mutableStateOf<ApiSettings?>(null) }
    var original by remember { mutableStateOf<ApiSettings?>(null) }
    var cli by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var confirmDelete by remember { mutableStateOf(false) }
    var purge by remember { mutableStateOf(false) }

    LaunchedEffect(ref) {
        if (session != null) {
            runCatching { session.loadSettings() }.onSuccess { settings = it; original = it }
            cli = runCatching { session.loadConnections().cli }.getOrDefault("")
        }
    }

    SubPage(if (profile == null) "新建 Bot" else "编辑 Bot", nav, subtitle = server.deviceName(device)) {
        Column(Modifier.padding(horizontal = 16.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {
            if (profile != null) {
                Row(verticalAlignment = Alignment.CenterVertically) { BotAvatar(profile, 56.dp) }
            }
            OutlinedTextField(name, { name = it }, label = { Text("名称") }, placeholder = { Text("例如：代码审查") }, singleLine = true, modifier = Modifier.fillMaxWidth())
            OutlinedTextField(
                instructions, { instructions = it }, label = { Text("职责（可选）") },
                placeholder = { Text("这个 Bot 负责什么、说话风格、注意事项…") },
                modifier = Modifier.fillMaxWidth().heightIn(min = 120.dp),
            )
        }
        val s = settings
        if (profile != null) {
            SectionLabel("模型")
            if (s == null) {
                Busy()
            } else if (s.channels.none { it.enabled }) {
                Note("这个 Bot 还没有可用的 API 渠道。到「设置 › 服务器 › API 渠道」里添加。")
            } else {
                ModelRow("聊天模型", s, s.chatChannel, s.chatModel) { c, m -> settings = s.copy(chatChannel = c, chatModel = m) }
                if (cli.isEmpty()) {
                    ModelRow("任务模型", s, s.agentChannel, s.agentModel) { c, m -> settings = s.copy(agentChannel = c, agentModel = m) }
                } else {
                    ListRow("任务模型", "由 ${if (cli == "codex") "Codex CLI" else if (cli == "claude") "Claude Code" else cli} 执行（在 Agent 里修改）")
                }
                Note("聊天模型负责回话和派活，可以选快一点的；任务模型给后台 Agent 干活用。每个 Bot 各选各的模型；API 渠道是整台设备共用的。")
            }
        } else {
            Note("创建后，可以在聊天页顶部给它单独选模型。")
        }
        error?.let { Note(it, MaterialTheme.colorScheme.error) }
        Row(Modifier.fillMaxWidth().padding(16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
            if (profile != null && profile.id != "main" && device == "local") {
                TextButton(onClick = { confirmDelete = true }) { Text("删除 Bot", color = MaterialTheme.colorScheme.error) }
            }
            Box(Modifier.weight(1f))
            Button(enabled = !busy && name.isNotBlank(), onClick = {
                busy = true; error = null
                scope.launch {
                    runCatching {
                        val changed = profile == null || name.trim() != profile.name || instructions != profile.instructions
                        if (changed) {
                            val id = state.saveBot(server, device, profile, name.trim(), instructions)
                            if (profile == null && id != null) {
                                nav.pop()
                                nav.push(Screen.Chat(BotRef(server.id, device, id)))
                                return@runCatching
                            }
                        }
                        val cur = settings
                        if (session != null && cur != null && cur != original) session.saveRoutes(cur.chatChannel, cur.chatModel, cur.agentChannel, cur.agentModel)
                        nav.pop()
                    }.onFailure { error = ServerClient.describe(it) }
                    busy = false
                }
            }) { Text(if (busy) "正在保存…" else if (profile == null) "创建" else "保存") }
        }
    }

    if (confirmDelete && ref != null) {
        AlertDialog(
            onDismissRequest = { confirmDelete = false },
            title = { Text("删除「${profile?.name}」？") },
            text = {
                Column {
                    Text("会停止它的任务和定时计划。默认只从列表里移除，聊天记录、记忆、密钥和工作区文件仍留在服务器上。")
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Checkbox(purge, { purge = it })
                        Text("彻底删除（连聊天记录、记忆、密钥和工作区文件一起删掉，无法恢复）", fontSize = 13.sp)
                    }
                }
            },
            confirmButton = {
                TextButton(onClick = {
                    confirmDelete = false
                    scope.launch {
                        runCatching { state.deleteBot(ref, purge) }
                            .onSuccess { nav.tab(Screen.Home) }
                            .onFailure { error = ServerClient.describe(it) }
                    }
                }) { Text("删除", color = MaterialTheme.colorScheme.error) }
            },
            dismissButton = { TextButton(onClick = { confirmDelete = false }) { Text("取消") } },
        )
    }
}

@Composable
private fun ModelRow(title: String, s: ApiSettings, channel: String, model: String, onPick: (String, String) -> Unit) {
    var open by remember { mutableStateOf(false) }
    Box {
        ListRow(title, listOfNotNull(s.channel(channel)?.name, model.ifEmpty { null }).joinToString(" · ").ifEmpty { "未选择" }, onClick = { open = true })
        DropdownMenu(open, { open = false }) {
            s.channels.filter { it.enabled }.forEach { ch ->
                DropdownMenuItem(text = { Text(ch.name, color = MaterialTheme.colorScheme.primary, fontSize = 13.sp) }, onClick = {}, enabled = false)
                ch.visibleModels.forEach { m ->
                    DropdownMenuItem(
                        text = { Text(m, fontFamily = FontFamily.Monospace, fontSize = 14.sp) },
                        leadingIcon = { RadioButton(selected = ch.id == channel && m == model, onClick = null) },
                        onClick = { onPick(ch.id, m); open = false },
                    )
                }
            }
        }
    }
}
