package io.github.llc370219.frogbot.claude.ui

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Visibility
import androidx.compose.material.icons.filled.VisibilityOff
import androidx.compose.material.icons.outlined.AccountCircle
import androidx.compose.material.icons.outlined.Cable
import androidx.compose.material.icons.outlined.Delete
import androidx.compose.material.icons.outlined.Dns
import androidx.compose.material.icons.outlined.Info
import androidx.compose.material.icons.outlined.Notifications
import androidx.compose.material.icons.outlined.NotificationsActive
import androidx.compose.material.icons.outlined.OpenInBrowser
import androidx.compose.material.icons.outlined.Palette
import androidx.compose.material.icons.outlined.SmartToy
import androidx.compose.material.icons.outlined.SystemUpdate
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.llc370219.frogbot.claude.KeepAliveService
import io.github.llc370219.frogbot.claude.core.AgentConnections
import io.github.llc370219.frogbot.claude.core.ApiChannel
import io.github.llc370219.frogbot.claude.core.ApiSettings
import io.github.llc370219.frogbot.claude.core.AppState
import io.github.llc370219.frogbot.claude.core.BotRef
import io.github.llc370219.frogbot.claude.core.BotSession
import io.github.llc370219.frogbot.claude.core.ServerClient
import io.github.llc370219.frogbot.claude.core.ServerEntry
import io.github.llc370219.frogbot.claude.core.ServerStatus
import io.github.llc370219.frogbot.claude.core.str
import kotlinx.coroutines.launch

private fun statusText(s: ServerEntry): String = when (val st = s.status) {
    ServerStatus.Online -> {
        val n = s.devices.sumOf { it.bots.size }
        "在线 · $n 个 Bot" + if (s.config.insecure) " · HTTP 未加密" else ""
    }
    ServerStatus.Connecting -> "正在连接"
    ServerStatus.NeedsLogin -> "登录已过期"
    is ServerStatus.Offline -> "离线：${st.error}"
}

// ---------- 设置首页 ----------

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(state: AppState, nav: Nav) {
    val context = LocalContext.current
    Scaffold(topBar = { TopAppBar(title = { Text("设置") }) }, containerColor = MaterialTheme.colorScheme.surface) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState())) {
            SectionLabel("服务器")
            state.servers.forEach { s ->
                ListRow(s.name, statusText(s), Icons.Outlined.Dns, chevron = true, onClick = { nav.push(Screen.Server(s.id)) })
            }
            ListRow("添加服务器", icon = Icons.Filled.Add, titleColor = MaterialTheme.colorScheme.primary, onClick = { nav.push(Screen.AddServer()) })
            SectionLabel("通用")
            SwitchRow(
                "后台保持连接", "用一条常驻通知保持连接，审批和任务结束能马上提醒你", Icons.Outlined.NotificationsActive, state.keepAlive,
            ) { on ->
                state.setKeepAlive(on)
                if (on) runCatching { KeepAliveService.start(context) } else KeepAliveService.stop(context)
            }
            ListRow("通知", "等你确认、任务结束、新回复", Icons.Outlined.Notifications, chevron = true, onClick = { nav.push(Screen.Notifications) })
            ListRow("外观", when (state.appearance) { "light" -> "浅色"; "dark" -> "深色"; else -> "跟随系统" }, Icons.Outlined.Palette, chevron = true,
                onClick = { nav.push(Screen.Appearance) })
            Note("Frog Bot Claude 0.1.0")
        }
    }
}

@Composable
fun AppearanceScreen(state: AppState, nav: Nav) {
    SubPage("外观", nav) {
        listOf("system" to "跟随系统", "light" to "浅色", "dark" to "深色").forEach { (k, v) ->
            ListRow(v, onClick = { state.setAppearance(k) }, leading = { RadioButton(selected = state.appearance == k, onClick = null) })
        }
    }
}

@Composable
fun NotificationsScreen(state: AppState, nav: Nav) {
    var a by remember { mutableStateOf(state.prefs.notifyApproval) }
    var t by remember { mutableStateOf(state.prefs.notifyTask) }
    var r by remember { mutableStateOf(state.prefs.notifyReply) }
    SubPage("通知", nav) {
        SectionLabel("提醒我")
        SwitchRow("有操作需要我确认", checked = a) { a = it; state.prefs.notifyApproval = it }
        SwitchRow("任务完成或失败", checked = t) { t = it; state.prefs.notifyTask = it }
        SwitchRow("新回复", checked = r) { r = it; state.prefs.notifyReply = it }
        Note("App 在前台时不弹通知。想在后台及时收到，打开「设置 › 后台保持连接」。通知样式在系统设置里调整。")
    }
}

// ---------- 添加服务器 / 重新登录 ----------

@Composable
fun AddServerScreen(state: AppState, existing: ServerEntry?, nav: Nav) {
    val scope = rememberCoroutineScope()
    var name by remember { mutableStateOf(existing?.config?.name ?: "") }
    var address by remember { mutableStateOf(existing?.config?.url ?: "") }
    var user by remember { mutableStateOf(existing?.config?.username ?: "admin") }
    var pass by remember { mutableStateOf("") }
    var show by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val insecure = ServerClientHelpers.insecure(address)

    SubPage(if (existing == null) "添加服务器" else "重新登录", nav) {
        Column(Modifier.padding(horizontal = 16.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {
            Text(
                if (existing == null) "填写安装 Frog Bot 时打印的访问地址、用户名和密码。" else "会话过期或密码在别处改过了，输入密码重新连接。",
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            if (existing == null) {
                OutlinedTextField(name, { name = it }, label = { Text("名称（可选）") }, placeholder = { Text("东京 VPS") }, singleLine = true, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(
                    address, { address = it }, label = { Text("地址") }, placeholder = { Text("bot.example.com") }, singleLine = true,
                    supportingText = { Text("没写协议时，域名按 HTTPS、IP 按 HTTP") },
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri), modifier = Modifier.fillMaxWidth(),
                )
            }
            OutlinedTextField(user, { user = it }, label = { Text("用户名") }, singleLine = true, modifier = Modifier.fillMaxWidth())
            OutlinedTextField(
                pass, { pass = it }, label = { Text("密码") }, singleLine = true, modifier = Modifier.fillMaxWidth(),
                visualTransformation = if (show) VisualTransformation.None else PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                trailingIcon = { IconButton(onClick = { show = !show }) { Icon(if (show) Icons.Filled.VisibilityOff else Icons.Filled.Visibility, "显示密码") } },
            )
            if (insecure) Text("这是明文 HTTP 连接，密码和聊天内容在网络上不加密。建议给服务器配域名并开启 HTTPS。", color = Frog.extra.warn, fontSize = 13.sp)
            error?.let { Text(it, color = MaterialTheme.colorScheme.error, fontSize = 13.sp) }
            Button(
                onClick = {
                    busy = true; error = null
                    scope.launch {
                        runCatching { state.connect(existing, name, address, user, pass) }
                            .onSuccess { nav.pop() }
                            .onFailure { error = ServerClient.describe(it) }
                        busy = false
                    }
                },
                enabled = !busy && (existing != null || address.isNotBlank()),
                modifier = Modifier.fillMaxWidth(),
            ) { Text(if (busy) "正在连接…" else if (existing == null) "连接" else "登录") }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Icon(Icons.Outlined.Info, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
                Text(
                    "手机不保存密码，只保存登录会话（7 天有效）。服务器要能从手机访问到：公网地址、同一个 Wi-Fi，或者 Tailscale 这类组网工具。",
                    fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}

object ServerClientHelpers {
    fun insecure(address: String): Boolean {
        val u = io.github.llc370219.frogbot.claude.core.ServerConfig.normalize(address) ?: return false
        return u.startsWith("http://") && !io.github.llc370219.frogbot.claude.core.ServerConfig.isLoopback(
            u.removePrefix("http://").substringBefore('/').substringBefore(':'),
        )
    }
}

// ---------- 一台服务器 ----------

@Composable
fun ServerScreen(state: AppState, s: ServerEntry, nav: Nav) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val refs = s.devices.filter { it.online }.flatMap { g -> g.bots.map { BotRef(s.id, g.id, it.id) } }
    var selected by remember { mutableStateOf(refs.firstOrNull()) }
    val ref = selected?.takeIf { it in refs } ?: refs.firstOrNull()
    val session = ref?.let { state.session(it) }
    var settings by remember { mutableStateOf<ApiSettings?>(null) }
    var conn by remember { mutableStateOf<AgentConnections?>(null) }
    var picker by remember { mutableStateOf(false) }
    var confirmRemove by remember { mutableStateOf(false) }
    var update by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(ref) {
        settings = null; conn = null
        if (session != null) {
            settings = runCatching { session.loadSettings() }.getOrNull()
            conn = runCatching { session.loadConnections() }.getOrNull()
        }
    }

    SubPage(s.name, nav, actions = {
        IconButton(onClick = { runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(s.config.url))) } }) {
            Icon(Icons.Outlined.OpenInBrowser, "在浏览器中打开")
        }
    }) {
        SectionLabel("连接")
        ListRow("地址", s.config.url)
        ListRow("状态", statusText(s) + (session?.info?.version?.let { " · Frog Bot $it" } ?: ""))
        if (s.config.insecure) Note("明文 HTTP：密码和聊天内容在网络上不加密，建议给服务器配域名开启 HTTPS。", Frog.extra.warn)
        if (s.status == ServerStatus.NeedsLogin) {
            ListRow("重新登录", "登录已过期", Icons.Outlined.AccountCircle, chevron = true, onClick = { nav.push(Screen.AddServer(s.id)) })
        } else if (s.authEnabled) {
            ListRow("账号", "${s.config.username} · 登录 7 天有效", Icons.Outlined.AccountCircle, chevron = true, onClick = { nav.push(Screen.Account(s.id)) })
        }

        SectionLabel("Bot 的模型和 Agent")
        if (session == null) {
            Note(if (s.status == ServerStatus.NeedsLogin) "重新登录后才能读取设置。" else "这台服务器上还没有可设置的 Bot。")
        } else {
            Box {
                ListRow("设置哪个 Bot", "${session.profile.name} · ${session.deviceName}", Icons.Outlined.SmartToy, onClick = { picker = true })
                DropdownMenu(picker, { picker = false }) {
                    refs.forEach { r ->
                        val ss = state.session(r) ?: return@forEach
                        DropdownMenuItem(text = { Text("${ss.profile.name} · ${ss.deviceName}") }, onClick = { selected = r; picker = false })
                    }
                }
            }
            val st = settings
            val chat = st?.let { x -> if (x.envOverride) "由环境变量指定" else listOfNotNull(x.channel(x.chatChannel)?.name, x.chatModel.ifEmpty { null }).joinToString(" · ").ifEmpty { "未选择" } } ?: "—"
            val agent = if (!conn?.cli.isNullOrEmpty()) "由 ${conn!!.clis.firstOrNull { it.id == conn!!.cli }?.name ?: conn!!.cli} 执行"
            else st?.let { x -> listOfNotNull(x.channel(x.agentChannel)?.name, x.agentModel.ifEmpty { null }).joinToString(" · ").ifEmpty { "未选择" } } ?: "—"
            ListRow("聊天模型", chat, chevron = session.isLocalDevice, onClick = if (session.isLocalDevice) ({ nav.push(Screen.BotEditor(s.id, session.ref.device, session.ref)) }) else null)
            ListRow("任务模型", agent, chevron = session.isLocalDevice, onClick = if (session.isLocalDevice) ({ nav.push(Screen.BotEditor(s.id, session.ref.device, session.ref)) }) else null)
            ListRow("API 渠道", st?.let { "${it.channels.size} 个渠道" } ?: "—", Icons.Outlined.Cable, chevron = true, onClick = { nav.push(Screen.Channels(session.ref)) })
            ListRow(
                "Agent", if (conn?.cli.isNullOrEmpty()) "执行任务使用 Frog（内置）" else "执行任务使用 ${conn!!.clis.firstOrNull { it.id == conn!!.cli }?.name ?: conn!!.cli}",
                Icons.Outlined.SmartToy, chevron = true, onClick = { nav.push(Screen.Agents(session.ref)) },
            )
            Note("API 渠道整台设备共用；聊天模型、任务模型和 Agent 每个 Bot 各自选，互不影响。")
        }

        HorizontalDivider(Modifier.padding(vertical = 8.dp))
        ListRow("检查更新", update, Icons.Outlined.SystemUpdate, onClick = {
            update = "正在检查…"
            scope.launch {
                update = runCatching { state.checkUpdate(s) }.fold({ j ->
                    when (j.str("state")) {
                        "available" -> "有新版本 ${j.str("latest")}（当前 ${j.str("current")}）"
                        "current" -> "已是最新（${j.str("current")}）"
                        else -> "当前 ${j.str("current").ifEmpty { "版本未知" }}"
                    }
                }, { "检查失败：${ServerClient.describe(it)}" })
            }
        })
        ListRow("从这台手机移除", icon = Icons.Outlined.Delete, titleColor = MaterialTheme.colorScheme.error, onClick = { confirmRemove = true })
    }

    if (confirmRemove) {
        AlertDialog(
            onDismissRequest = { confirmRemove = false },
            title = { Text("移除「${s.name}」？") },
            text = { Text("只是从这台手机移除并退出登录，服务器上的 Bot、任务和数据都不受影响。") },
            confirmButton = { TextButton(onClick = { confirmRemove = false; state.remove(s); nav.pop() }) { Text("移除", color = MaterialTheme.colorScheme.error) } },
            dismissButton = { TextButton(onClick = { confirmRemove = false }) { Text("取消") } },
        )
    }
}

// ---------- 账号 ----------

@Composable
fun AccountScreen(state: AppState, s: ServerEntry, nav: Nav) {
    val scope = rememberCoroutineScope()
    var old by remember { mutableStateOf("") }
    var new by remember { mutableStateOf("") }
    var again by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var msg by remember { mutableStateOf<Pair<String, Boolean>?>(null) }
    SubPage("账号与登录", nav, subtitle = s.name) {
        ListRow("用户名", s.config.username)
        SectionLabel("修改密码")
        Column(Modifier.padding(horizontal = 16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            listOf(Triple("当前密码", old) { v: String -> old = v }, Triple("新密码（至少 8 位）", new) { v: String -> new = v }, Triple("再输一次", again) { v: String -> again = v })
                .forEach { (label, value, set) ->
                    OutlinedTextField(value, set, label = { Text(label) }, singleLine = true, visualTransformation = PasswordVisualTransformation(),
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password), modifier = Modifier.fillMaxWidth())
                }
            Text("修改后，浏览器和其他设备上的登录都会失效。这台手机会自动换成新的会话。", fontSize = 13.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
            msg?.let { Text(it.first, color = if (it.second) MaterialTheme.colorScheme.error else Frog.extra.ok, fontSize = 13.sp) }
            Button(enabled = !busy && old.isNotEmpty() && new.length >= 8 && new == again, modifier = Modifier.fillMaxWidth(), onClick = {
                busy = true; msg = null
                scope.launch {
                    runCatching { state.changePassword(s, old, new) }
                        .onSuccess { old = ""; new = ""; again = ""; msg = "密码已修改，已用新密码重新登录。" to false }
                        .onFailure { msg = ServerClient.describe(it) to true }
                    busy = false
                }
            }) { Text("修改密码") }
            OutlinedButton(modifier = Modifier.fillMaxWidth(), onClick = { scope.launch { state.logout(s); nav.pop() } }) {
                Text("退出登录", color = MaterialTheme.colorScheme.error)
            }
        }
    }
}

// ---------- API 渠道（属于某个 Bot） ----------

@Composable
fun ChannelsScreen(session: BotSession, nav: Nav) {
    val scope = rememberCoroutineScope()
    var saved by remember { mutableStateOf<ApiSettings?>(null) }
    var draft by remember { mutableStateOf<ApiSettings?>(null) }
    var editing by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    var msg by remember { mutableStateOf<Pair<String, Boolean>?>(null) }

    LaunchedEffect(session.ref) {
        runCatching { session.loadSettings() }.onSuccess { saved = it; draft = it }.onFailure { msg = ServerClient.describe(it) to true }
    }
    val d = draft
    val editId = editing
    if (d != null && editId != null && d.channel(editId) != null) {
        ChannelEditor(session, d.channel(editId)!!, onDone = { ch ->
            draft = d.copy(channels = d.channels.map { if (it.id == ch.id) ch else it })
            editing = null
        }, onRemove = {
            val rest = d.channels.filter { it.id != editId }
            val next = rest.firstOrNull { it.enabled }
            draft = d.copy(
                channels = rest,
                chatChannel = if (d.chatChannel == editId) next?.id ?: "" else d.chatChannel,
                chatModel = if (d.chatChannel == editId) next?.visibleModels?.firstOrNull() ?: "" else d.chatModel,
                agentChannel = if (d.agentChannel == editId) next?.id ?: "" else d.agentChannel,
                agentModel = if (d.agentChannel == editId) next?.visibleModels?.firstOrNull() ?: "" else d.agentModel,
            )
            editing = null
        }, onCancel = { editing = null })
        return
    }

    SubPage("API 渠道", nav, subtitle = "${session.profile.name} · ${session.deviceName}", actions = {
        IconButton(enabled = d != null, onClick = {
            val cur = draft ?: return@IconButton
            var n = cur.channels.size + 1
            while (cur.channel("channel-$n") != null) n++
            val ch = ApiChannel("channel-$n", "新渠道", "", enabled = true, authRequired = true)
            draft = cur.copy(
                channels = cur.channels + ch,
                chatChannel = cur.chatChannel.ifEmpty { ch.id }, agentChannel = cur.agentChannel.ifEmpty { ch.id },
            )
            editing = ch.id
        }) { Icon(Icons.Filled.Add, "添加渠道") }
    }) {
        Note("「${session.deviceName}」上所有 Bot 共用这些渠道，密钥加密保存在那台设备上。下面的聊天模型和任务模型只对「${session.profile.name}」生效。保存后下一轮对话和任务生效。")
        if (d == null) {
            if (msg == null) Busy() else Note(msg!!.first, MaterialTheme.colorScheme.error)
            return@SubPage
        }
        if (d.envOverride) Note("当前模型由启动时的环境变量指定，优先于这里的设置。", Frog.extra.warn)
        if (d.channels.isEmpty()) Note("还没有 API 渠道。点右上角 + 添加服务商或本地模型接口。")
        d.channels.forEach { ch ->
            ListRow(
                ch.name + if (ch.id == d.chatChannel) "（默认）" else "",
                (ch.baseUrl.ifEmpty { "还没填接口地址" }) + "\n${ch.visibleModels.size} 个可选模型 · " + (if (ch.authRequired) "API Key" else "无需密钥"),
                Icons.Outlined.Cable, onClick = { editing = ch.id },
            ) {
                Switch(checked = ch.enabled, onCheckedChange = { on -> draft = d.copy(channels = d.channels.map { if (it.id == ch.id) it.copy(enabled = on) else it }) })
            }
        }
        if (d.channels.isNotEmpty()) {
            SectionLabel("${session.profile.name} 用的模型")
            RoutePicker("聊天模型", d, d.chatChannel, d.chatModel) { c, m -> draft = d.copy(chatChannel = c, chatModel = m) }
            RoutePicker("任务模型（Agent API）", d, d.agentChannel, d.agentModel) { c, m -> draft = d.copy(agentChannel = c, agentModel = m) }
            Note("聊天模型负责回话和派活；任务模型给后台 Agent 用。在「Agent」里选了 CLI 时，任务改用 CLI 自己的账户和模型。")
        }
        msg?.let { Note(it.first, if (it.second) MaterialTheme.colorScheme.error else Frog.extra.ok) }
        val valid = d.channels.isEmpty() || (
            d.channels.all { it.name.isNotBlank() && it.baseUrl.isNotBlank() } &&
                d.channel(d.chatChannel)?.let { it.enabled && d.chatModel in it.visibleModels } == true &&
                d.channel(d.agentChannel)?.let { it.enabled && d.agentModel in it.visibleModels } == true
            )
        Row(Modifier.padding(16.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            if (d != saved) OutlinedButton(onClick = { draft = saved; msg = null }) { Text("还原") }
            Button(enabled = !busy && d != saved && valid, onClick = {
                busy = true; msg = null
                scope.launch {
                    runCatching { session.saveSettings(d) }
                        .onSuccess { saved = it; draft = it; msg = "已加密保存。" to false }
                        .onFailure { msg = ServerClient.describe(it) to true }
                    busy = false
                }
            }) { Text(if (busy) "正在保存…" else "保存设置") }
        }
        if (d != saved && !valid) Note("请先给聊天模型和任务模型各选一个已启用渠道里的模型。", Frog.extra.warn)
    }
}

@Composable
private fun RoutePicker(title: String, s: ApiSettings, channel: String, model: String, onPick: (String, String) -> Unit) {
    var open by remember { mutableStateOf(false) }
    Box {
        val label = listOfNotNull(s.channel(channel)?.name, model.ifEmpty { null }).joinToString(" · ").ifEmpty { "未选择" }
        ListRow(title, label, onClick = { open = true })
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

@Composable
private fun ChannelEditor(session: BotSession, start: ApiChannel, onDone: (ApiChannel) -> Unit, onRemove: () -> Unit, onCancel: () -> Unit) {
    val scope = rememberCoroutineScope()
    var ch by remember { mutableStateOf(start) }
    var key by remember { mutableStateOf(start.apiKey ?: "") }
    var newModel by remember { mutableStateOf("") }
    var note by remember { mutableStateOf<Pair<String, Boolean>?>(null) }
    var busy by remember { mutableStateOf(false) }
    var confirm by remember { mutableStateOf(false) }
    fun current() = ch.copy(apiKey = if (ch.authRequired) key.ifEmpty { null } else null)

    androidx.activity.compose.BackHandler { onCancel() }
    Scaffold(containerColor = MaterialTheme.colorScheme.surface) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text(start.name, style = MaterialTheme.typography.headlineSmall)
            Text("OpenAI 兼容的接口（/v1）。API Key 加密保存在服务器上，这里不会显示。", color = MaterialTheme.colorScheme.onSurfaceVariant, fontSize = 13.sp)
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("启用", Modifier.weight(1f)); Switch(checked = ch.enabled, onCheckedChange = { ch = ch.copy(enabled = it) })
            }
            OutlinedTextField(ch.name, { ch = ch.copy(name = it) }, label = { Text("名称") }, singleLine = true, modifier = Modifier.fillMaxWidth())
            OutlinedTextField(ch.baseUrl, { ch = ch.copy(baseUrl = it) }, label = { Text("接口地址") }, placeholder = { Text("https://api.example.com/v1") },
                singleLine = true, keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri), modifier = Modifier.fillMaxWidth())
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("需要 API Key", Modifier.weight(1f)); Switch(checked = ch.authRequired, onCheckedChange = { ch = ch.copy(authRequired = it) })
            }
            if (ch.authRequired) {
                OutlinedTextField(key, { key = it }, label = { Text("API Key") }, placeholder = { Text(if (ch.hasApiKey) "已加密保存，留空保留" else "粘贴 API Key") },
                    singleLine = true, visualTransformation = PasswordVisualTransformation(), modifier = Modifier.fillMaxWidth())
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("可选模型 · 共 ${ch.models.size} 个，显示 ${ch.visibleModels.size} 个", Modifier.weight(1f), style = MaterialTheme.typography.labelLarge)
                TextButton(enabled = !busy && ch.baseUrl.isNotBlank(), onClick = {
                    busy = true; note = null
                    scope.launch {
                        runCatching { session.fetchModels(current()) }.onSuccess { list ->
                            val added = list.filter { it !in ch.models }
                            ch = ch.copy(models = ch.models + added, hiddenModels = if (ch.models.isEmpty()) ch.hiddenModels else ch.hiddenModels + added)
                            note = "拉到 ${list.size} 个模型" to false
                        }.onFailure { note = ServerClient.describe(it) to true }
                        busy = false
                    }
                }) { Text("拉取模型") }
            }
            ch.models.forEach { m ->
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(checked = m !in ch.hiddenModels, onCheckedChange = { on ->
                        ch = ch.copy(hiddenModels = if (on) ch.hiddenModels - m else ch.hiddenModels + m)
                    })
                    Text(m, fontFamily = FontFamily.Monospace, fontSize = 13.sp, modifier = Modifier.weight(1f))
                }
            }
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(newModel, { newModel = it }, label = { Text("手动添加模型 ID") }, singleLine = true, modifier = Modifier.weight(1f))
                TextButton(enabled = newModel.isNotBlank(), onClick = {
                    val m = newModel.trim()
                    if (m !in ch.models) ch = ch.copy(models = ch.models + m)
                    newModel = ""
                }) { Text("添加") }
            }
            val testModel = ch.visibleModels.firstOrNull()
            OutlinedButton(enabled = !busy && testModel != null && ch.baseUrl.isNotBlank(), onClick = {
                busy = true; note = null
                scope.launch {
                    runCatching { session.testChannel(current(), testModel!!) }
                        .onSuccess { note = "正常 · $it ms（$testModel）" to false }
                        .onFailure { note = ServerClient.describe(it) to true }
                    busy = false
                }
            }) { Text("测试连接") }
            note?.let { Text(it.first, color = if (it.second) MaterialTheme.colorScheme.error else Frog.extra.ok, fontSize = 13.sp) }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                TextButton(onClick = { confirm = true }) { Text("移除渠道", color = MaterialTheme.colorScheme.error) }
                Box(Modifier.weight(1f))
                OutlinedButton(onClick = onCancel) { Text("取消") }
                Button(onClick = { onDone(current()) }) { Text("完成") }
            }
            Text("点「完成」后回到渠道列表，再点「保存设置」才会生效。", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
    if (confirm) {
        AlertDialog(
            onDismissRequest = { confirm = false },
            title = { Text("移除渠道「${ch.name}」？") },
            text = { Text("保存设置后生效，密钥会一起删除。") },
            confirmButton = { TextButton(onClick = { confirm = false; onRemove() }) { Text("移除", color = MaterialTheme.colorScheme.error) } },
            dismissButton = { TextButton(onClick = { confirm = false }) { Text("取消") } },
        )
    }
}

// ---------- Agent ----------

@Composable
fun AgentsScreen(session: BotSession, nav: Nav) {
    val scope = rememberCoroutineScope()
    var conn by remember { mutableStateOf<AgentConnections?>(null) }
    var msg by remember { mutableStateOf<Pair<String, Boolean>?>(null) }
    var busy by remember { mutableStateOf(false) }
    suspend fun load() {
        runCatching { session.loadConnections() }.onSuccess { conn = it }.onFailure { msg = ServerClient.describe(it) to true }
    }
    LaunchedEffect(session.ref) { load() }
    fun use(id: String) {
        busy = true; msg = null
        scope.launch {
            runCatching { session.saveConnection(id) }.onSuccess {
                conn = it
                val name = if (id.isEmpty()) "Frog（内置）" else it.clis.firstOrNull { c -> c.id == id }?.name ?: id
                msg = "新任务将使用 $name，正在进行的任务不受影响。" to false
            }.onFailure { msg = ServerClient.describe(it) to true }
            busy = false
        }
    }
    SubPage("Agent", nav, subtitle = "${session.profile.name} · ${session.deviceName}", actions = {
        TextButton(enabled = !busy, onClick = { scope.launch { busy = true; load(); busy = false } }) { Text("重新检测") }
    }) {
        Note("这个 Bot 的后台任务用哪个 Agent 执行，只影响这个 Bot。聊天回复仍用 API 渠道里的聊天模型。")
        val c = conn
        if (c == null) {
            if (msg == null) Busy() else Note(msg!!.first, MaterialTheme.colorScheme.error)
            return@SubPage
        }
        ListRow("Frog", "内置 · 用 API 渠道里的任务模型", leading = { FrogAgentAvatar(40.dp) }, onClick = { if (!busy) use("") }) {
            RadioButton(selected = c.cli.isEmpty(), onClick = { if (!busy) use("") })
        }
        c.clis.forEach { cli ->
            ListRow(
                cli.name, "${cli.id} · " + if (cli.installed) "已安装 · 用 CLI 自己的账户" else "没有检测到",
                leading = { AgentIcon(cli.id, 40.dp) },
                titleColor = if (cli.installed) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.onSurfaceVariant,
                onClick = if (cli.installed && !busy) ({ use(cli.id) }) else null,
            ) {
                RadioButton(selected = c.cli == cli.id, enabled = cli.installed, onClick = { if (!busy) use(cli.id) })
            }
        }
        msg?.let { Note(it.first, if (it.second) MaterialTheme.colorScheme.error else Frog.extra.ok) }
        Note("检测的是「${session.deviceName}」上的安装情况。CLI 要用运行 Frog Bot 的账户在那台机器上登录好；每次启动外部 CLI 前都要在任务卡片里批准。")
    }
}
