package io.github.llc370219.frogbot.claude.ui

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.Bolt
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Code
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.CreateNewFolder
import androidx.compose.material.icons.filled.DeleteOutline
import androidx.compose.material.icons.filled.Description
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.FolderOpen
import androidx.compose.material.icons.filled.Key
import androidx.compose.material.icons.filled.Psychology
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material.icons.filled.Stop
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material.icons.outlined.ErrorOutline
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TextField
import androidx.compose.material3.TextFieldDefaults
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.llc370219.frogbot.claude.core.AgyChatSession
import io.github.llc370219.frogbot.claude.core.AgyFileEntry
import io.github.llc370219.frogbot.claude.core.AgyMessage
import io.github.llc370219.frogbot.claude.core.AgyPermission
import io.github.llc370219.frogbot.claude.core.AgyProject
import io.github.llc370219.frogbot.claude.core.AgyStore
import io.github.llc370219.frogbot.claude.core.AgyToolCall
import io.github.llc370219.frogbot.claude.core.AppState
import io.github.llc370219.frogbot.claude.core.Fmt
import io.github.llc370219.frogbot.claude.core.ServerClient
import io.github.llc370219.frogbot.claude.core.ServerEntry
import io.github.llc370219.frogbot.claude.core.objects
import kotlinx.coroutines.launch

// MARK: - Coding 模式首页工作区（主机/CLI/项目/会话）

@Composable
fun CodingWorkspaceFeed(
    state: AppState,
    server: ServerEntry,
    query: String,
    nav: Nav,
    contentPadding: PaddingValues,
) {
    val store = remember(server.id) { state.agyStore(server) }
    val scope = rememberCoroutineScope()
    var showNewProject by remember { mutableStateOf(false) }
    var showFilesProject by remember { mutableStateOf<AgyProject?>(null) }
    var hostMenu by remember { mutableStateOf(false) }

    LaunchedEffect(server.id, server.online) {
        if (server.online) store.loadAll()
    }

    val filteredSessions = remember(store.sessions, query) {
        val q = query.trim().lowercase()
        if (q.isEmpty()) store.sessions
        else store.sessions.filter { it.title.lowercase().contains(q) || it.agentName.lowercase().contains(q) }
    }

    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = contentPadding,
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        // 多主机切换栏 + 官方 CLI 登录快捷入口
        item(key = "coding-host-bar") {
            Row(
                Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 2.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                Box {
                    Surface(
                        shape = RoundedCornerShape(999.dp),
                        color = MaterialTheme.colorScheme.surfaceContainerLow,
                        border = BorderStroke(0.7.dp, MaterialTheme.colorScheme.outlineVariant),
                        onClick = { if (state.servers.size > 1) hostMenu = true },
                    ) {
                        Row(
                            Modifier.padding(horizontal = 12.dp, vertical = 7.dp),
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(6.dp),
                        ) {
                            StatusDot(if (server.online) Frog.extra.ok else Frog.extra.warn, size = 7)
                            Text(server.name, style = MaterialTheme.typography.labelLarge, maxLines = 1)
                            server.config.tunnelBadge?.let { badge ->
                                Text(
                                    badge,
                                    fontSize = 10.sp,
                                    color = MaterialTheme.colorScheme.primary,
                                    fontWeight = FontWeight.SemiBold,
                                )
                            }
                            if (state.servers.size > 1) {
                                Icon(Icons.Filled.ExpandMore, null, Modifier.size(16.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                        }
                    }
                    DropdownMenu(hostMenu, { hostMenu = false }) {
                        state.servers.forEach { s ->
                            DropdownMenuItem(
                                text = { Text(s.name + (s.config.tunnelBadge?.let { " · $it" } ?: "")) },
                                onClick = {
                                    hostMenu = false
                                    state.activeCodingServerId = s.id
                                },
                                trailingIcon = if (s.id == server.id) ({ Icon(Icons.Filled.Check, null, Modifier.size(16.dp)) }) else null,
                            )
                        }
                    }
                }
                Spacer(Modifier.weight(1f))
                Surface(
                    shape = RoundedCornerShape(999.dp),
                    color = MaterialTheme.colorScheme.surfaceContainerLow,
                    border = BorderStroke(0.7.dp, MaterialTheme.colorScheme.outlineVariant),
                    onClick = { nav.push(Screen.CliAuth(server.id)) },
                ) {
                    Row(
                        Modifier.padding(horizontal = 11.dp, vertical = 7.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(5.dp),
                    ) {
                        Icon(Icons.Filled.Key, null, Modifier.size(14.dp), tint = MaterialTheme.colorScheme.primary)
                        Text("CLI 登录", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurface)
                    }
                }
                IconButton(onClick = { scope.launch { store.loadAll() } }, modifier = Modifier.size(34.dp)) {
                    Icon(Icons.Filled.Refresh, "刷新", Modifier.size(18.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        }

        // 已安装的 Coding CLI 横向切换条
        if (store.installedAgents.isNotEmpty()) {
            item(key = "coding-clis") {
                Row(
                    Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 16.dp),
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    store.installedAgents.forEach { cli ->
                        val active = cli.id == store.activeAgentId
                        FilterChip(
                            selected = active,
                            onClick = { scope.launch { store.selectAgent(cli.id) } },
                            label = { Text(cli.name, fontSize = 12.5.sp) },
                            leadingIcon = { Icon(Icons.Filled.Terminal, null, Modifier.size(14.dp)) },
                        )
                    }
                }
            }
        }

        // 项目目录横向胶囊 + 新建/文件按钮
        item(key = "coding-projects") {
            Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text("项目目录", style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Spacer(Modifier.weight(1f))
                    store.activeProject?.let { proj ->
                        TextButton(
                            onClick = { showFilesProject = proj },
                            contentPadding = PaddingValues(horizontal = 8.dp, vertical = 0.dp),
                        ) {
                            Icon(Icons.Filled.FolderOpen, null, Modifier.size(15.dp))
                            Spacer(Modifier.width(4.dp))
                            Text("文件", fontSize = 12.5.sp)
                        }
                    }
                    TextButton(
                        onClick = { showNewProject = true },
                        contentPadding = PaddingValues(horizontal = 8.dp, vertical = 0.dp),
                    ) {
                        Icon(Icons.Filled.CreateNewFolder, null, Modifier.size(15.dp))
                        Spacer(Modifier.width(4.dp))
                        Text("添加项目", fontSize = 12.5.sp)
                    }
                }
                if (store.projects.isEmpty()) {
                    Surface(
                        shape = Shapes12,
                        color = MaterialTheme.colorScheme.surfaceContainerLow,
                        border = BorderStroke(0.7.dp, MaterialTheme.colorScheme.outlineVariant),
                        onClick = { showNewProject = true },
                    ) {
                        Row(
                            Modifier.fillMaxWidth().padding(14.dp),
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(10.dp),
                        ) {
                            Icon(Icons.Filled.CreateNewFolder, null, tint = MaterialTheme.colorScheme.primary)
                            Column {
                                Text("新建或关联项目目录", style = MaterialTheme.typography.bodyMedium, fontWeight = FontWeight.Medium)
                                Text("为官方 Coding CLI 指定工作区文件夹", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                        }
                    }
                } else {
                    Row(
                        Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        store.projects.forEach { proj ->
                            val selected = store.activeProject?.id == proj.id
                            FilterChip(
                                selected = selected,
                                onClick = { scope.launch { store.selectProject(proj.id) } },
                                label = { Text(proj.name, maxLines = 1) },
                                leadingIcon = { Icon(Icons.Filled.Folder, null, Modifier.size(15.dp)) },
                            )
                        }
                    }
                }
            }
        }

        if (store.error != null) {
            item(key = "coding-err") {
                Text(
                    store.error ?: "",
                    color = MaterialTheme.colorScheme.error,
                    style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.padding(horizontal = 16.dp),
                )
            }
        }

        // 新建会话快捷按钮
        if (store.activeProject != null) {
            item(key = "coding-new-sess") {
                Surface(
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                    shape = Shapes12,
                    color = MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.55f),
                    onClick = {
                        scope.launch {
                            runCatching { store.createSession() }
                                .onSuccess { s ->
                                    nav.push(Screen.CodingChat(server.id, s.id, s.projectId, s.title))
                                }
                                .onFailure { store.error = ServerClient.describe(it) }
                        }
                    },
                ) {
                    Row(
                        Modifier.padding(horizontal = 14.dp, vertical = 12.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        Icon(Icons.Filled.Add, null, Modifier.size(18.dp), tint = MaterialTheme.colorScheme.primary)
                        Text(
                            "在「${store.activeProject?.name}」新建无气泡 Coding 会话",
                            style = MaterialTheme.typography.bodyMedium,
                            fontWeight = FontWeight.SemiBold,
                            color = MaterialTheme.colorScheme.primary,
                        )
                    }
                }
            }
        }

        if (store.loading && store.sessions.isEmpty()) {
            item(key = "coding-loading") { Busy() }
        } else if (filteredSessions.isEmpty() && store.activeProject != null) {
            item(key = "coding-empty") {
                Text(
                    "当前项目暂无 Coding 会话，点击上方按钮立即开始。",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 20.dp),
                )
            }
        } else {
            itemsIndexed(filteredSessions, key = { _, s -> "cs-${s.id}" }) { _, s ->
                Surface(
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp),
                    shape = Shapes12,
                    color = MaterialTheme.colorScheme.surfaceContainerLow,
                    border = BorderStroke(0.6.dp, MaterialTheme.colorScheme.outlineVariant),
                    onClick = { nav.push(Screen.CodingChat(server.id, s.id, s.projectId, s.title)) },
                ) {
                    Row(
                        Modifier.padding(horizontal = 14.dp, vertical = 12.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(12.dp),
                    ) {
                        val dotColor = when {
                            s.pendingPermissions > 0 -> Frog.extra.warn
                            s.busy -> MaterialTheme.colorScheme.primary
                            else -> MaterialTheme.colorScheme.outline
                        }
                        StatusDot(dotColor, size = 8)
                        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                            Text(
                                s.title.ifEmpty { "新对话" },
                                style = MaterialTheme.typography.bodyLarge,
                                fontWeight = FontWeight.Medium,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                            )
                            Text(
                                listOfNotNull(
                                    s.agentName.ifEmpty { s.agent.ifEmpty { null } },
                                    s.mode.ifEmpty { null },
                                    s.model.ifEmpty { null },
                                ).joinToString(" · "),
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                            )
                        }
                        IconButton(
                            onClick = { scope.launch { runCatching { store.deleteSession(s.id) } } },
                            modifier = Modifier.size(32.dp),
                        ) {
                            Icon(Icons.Filled.DeleteOutline, "删除", Modifier.size(18.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                }
            }
        }
    }

    if (showNewProject) {
        NewProjectDialog(store = store, onDismiss = { showNewProject = false })
    }
    showFilesProject?.let { proj ->
        ProjectFilesDialog(server = server, project = proj, onDismiss = { showFilesProject = null })
    }
}

// MARK: - 无气泡 Coding 实时工作台流（左侧绿条指令块 + 全宽平坦 Markdown + 垂直工具时间线）

@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
fun CodingChatScreen(state: AppState, server: ServerEntry, chat: AgyChatSession, nav: Nav) {
    val scope = rememberCoroutineScope()
    val listState = rememberLazyListState()
    var sending by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var configMenu by remember { mutableStateOf(false) }
    var cmdMenu by remember { mutableStateOf(false) }

    DisposableEffect(chat.sessionId) {
        chat.start()
        onDispose { }
    }

    LaunchedEffect(chat.messages.size, chat.streaming.length, chat.liveTools.size, chat.pendingPermissions.size) {
        val total = chat.messages.size + chat.pendingPermissions.size + (if (chat.busy) 1 else 0)
        if (total > 0) {
            runCatching { listState.animateScrollToItem(total) }
        }
    }

    fun sendNow(overrideText: String? = null, behavior: String? = null) {
        val text = (overrideText ?: chat.draft).trim()
        if (text.isEmpty() || sending) return
        val keep = chat.draft
        if (overrideText == null) chat.draft = ""
        sending = true
        error = null
        scope.launch {
            runCatching { chat.send(text, behavior) }
                .onFailure {
                    if (overrideText == null) chat.draft = keep
                    error = ServerClient.describe(it)
                }
            sending = false
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = { nav.pop() }) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, "返回")
                    }
                },
                title = {
                    Box {
                        Column(
                            Modifier.clickable { configMenu = true },
                            verticalArrangement = Arrangement.spacedBy(1.dp),
                        ) {
                            Text(
                                chat.title.ifEmpty { "新对话" },
                                style = MaterialTheme.typography.titleMedium,
                                fontWeight = FontWeight.SemiBold,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                            )
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                                Text(
                                    listOfNotNull(
                                        chat.mode.ifEmpty { null },
                                        chat.model.ifEmpty { null },
                                        if (chat.usedTokens > 0) "${maxOf(1, chat.usedTokens / 1000)}k" else null,
                                    ).joinToString(" · "),
                                    style = MaterialTheme.typography.labelSmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                )
                                Icon(Icons.Filled.ExpandMore, null, Modifier.size(13.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                        }
                        DropdownMenu(configMenu, { configMenu = false }) {
                            if (chat.modes.isNotEmpty()) {
                                DropdownMenuItem(text = { Text("执行权限模式", fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.primary) }, onClick = {}, enabled = false)
                                chat.modes.forEach { m ->
                                    DropdownMenuItem(
                                        text = { Text(m.name) },
                                        trailingIcon = if (chat.mode == m.id) ({ Icon(Icons.Filled.Check, null, Modifier.size(16.dp)) }) else null,
                                        onClick = {
                                            configMenu = false
                                            scope.launch { runCatching { chat.setConfig(mode = m.id) } }
                                        },
                                    )
                                }
                            }
                            if (chat.models.isNotEmpty()) {
                                HorizontalDivider()
                                DropdownMenuItem(text = { Text("切换模型", fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.primary) }, onClick = {}, enabled = false)
                                chat.models.forEach { ml ->
                                    DropdownMenuItem(
                                        text = { Text(ml.name) },
                                        trailingIcon = if (chat.model == ml.id) ({ Icon(Icons.Filled.Check, null, Modifier.size(16.dp)) }) else null,
                                        onClick = {
                                            configMenu = false
                                            scope.launch { runCatching { chat.setConfig(model = ml.id) } }
                                        },
                                    )
                                }
                            }
                        }
                    }
                },
                actions = {
                    IconButton(onClick = {
                        scope.launch {
                            runCatching { state.agyStore(server).createSession(chat.mode, chat.model, chat.agent) }
                                .onSuccess { s ->
                                    nav.replaceTop(Screen.CodingChat(server.id, s.id, s.projectId, s.title))
                                }
                                .onFailure { error = ServerClient.describe(it) }
                        }
                    }) {
                        Icon(Icons.Filled.Add, "新建会话")
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.surface),
            )
        },
        containerColor = MaterialTheme.colorScheme.surface,
    ) { pad ->
        Column(Modifier.padding(pad).fillMaxSize().imePadding().navigationBarsPadding()) {
            HorizontalDivider(thickness = 0.5.dp, color = MaterialTheme.colorScheme.outlineVariant)

            // 主文档流：彻底去气泡！
            LazyColumn(
                state = listState,
                modifier = Modifier.weight(1f).fillMaxWidth(),
                contentPadding = PaddingValues(horizontal = 16.dp, vertical = 16.dp),
                verticalArrangement = Arrangement.spacedBy(16.dp),
            ) {
                if (!chat.ready) {
                    item(key = "loading") { Busy() }
                } else if (chat.messages.isEmpty() && !chat.busy) {
                    item(key = "empty") {
                        Column(
                            Modifier.fillMaxWidth().padding(top = 48.dp),
                            horizontalAlignment = Alignment.CenterHorizontally,
                            verticalArrangement = Arrangement.spacedBy(12.dp),
                        ) {
                            Box(
                                Modifier.size(52.dp).background(MaterialTheme.colorScheme.surfaceContainer, Shapes16),
                                contentAlignment = Alignment.Center,
                            ) {
                                Icon(Icons.Filled.Code, null, Modifier.size(26.dp), tint = MaterialTheme.colorScheme.primary)
                            }
                            if (chat.cwd.isNotEmpty()) {
                                Text(chat.cwd.substringAfterLast('/'), style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
                                Text(chat.cwd, fontFamily = FontFamily.Monospace, fontSize = 11.5.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                            FlowRow(
                                horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.CenterHorizontally),
                                verticalArrangement = Arrangement.spacedBy(8.dp),
                            ) {
                                listOf("梳理当前项目结构", "运行测试并排查问题", "规划新功能改动方案").forEach { q ->
                                    OutlinedButton(onClick = { sendNow(overrideText = q) }, contentPadding = PaddingValues(horizontal = 12.dp, vertical = 6.dp)) {
                                        Text(q, fontSize = 12.5.sp)
                                    }
                                }
                            }
                        }
                    }
                }

                itemsIndexed(chat.messages, key = { _, m -> m.id }) { idx, msg ->
                    CodingMessageBlock(msg = msg, showDivider = idx > 0 && msg.role == "user")
                }

                itemsIndexed(chat.pendingPermissions, key = { _, p -> "perm-${p.id}" }) { _, perm ->
                    CodingPermCallout(perm = perm) { optId ->
                        scope.launch {
                            runCatching { chat.decide(perm.id, optId) }
                                .onFailure { error = ServerClient.describe(it) }
                        }
                    }
                }

                if (chat.busy) {
                    item(key = "live-turn") {
                        Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                            if (chat.thinking.isNotEmpty()) {
                                CodingThinkingDisclosure(chat.thinking, live = chat.thinkEnd == 0.0)
                            }
                            if (chat.liveTools.isNotEmpty()) {
                                CodingToolTimeline(chat.liveTools)
                            }
                            if (chat.streaming.isNotEmpty()) {
                                FlatMarkdownStream(text = chat.streaming, streaming = true)
                            } else if (chat.thinking.isEmpty() || chat.thinkEnd > 0.0) {
                                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                    CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp)
                                    Text("Agent 正在执行…", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                                }
                            }
                        }
                    }
                }
            }

            // 底部工作台输入栏
            HorizontalDivider(thickness = 0.5.dp, color = MaterialTheme.colorScheme.outlineVariant)
            Column(Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surface).padding(horizontal = 12.dp, vertical = 8.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                error?.let { err ->
                    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                        Text(err, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall, modifier = Modifier.weight(1f), maxLines = 2)
                        TextButton(onClick = { error = null }) { Text("关闭", fontSize = 12.sp) }
                    }
                }

                if (chat.queue.isNotEmpty()) {
                    Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        chat.queue.forEach { q ->
                            Surface(shape = RoundedCornerShape(999.dp), color = MaterialTheme.colorScheme.surfaceContainer) {
                                Row(Modifier.padding(horizontal = 9.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                                    Icon(
                                        if (q.behavior == "steer") Icons.Filled.Bolt else Icons.Filled.Schedule,
                                        null, Modifier.size(13.dp),
                                        tint = if (q.behavior == "steer") Frog.extra.warn else MaterialTheme.colorScheme.primary,
                                    )
                                    Text(q.text, fontSize = 12.sp, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.width(140.dp))
                                    Icon(
                                        Icons.Filled.Close, "撤回",
                                        Modifier.size(14.dp).clickable { scope.launch { runCatching { chat.cancelQueued(q.id) } } },
                                        tint = MaterialTheme.colorScheme.onSurfaceVariant,
                                    )
                                }
                            }
                        }
                    }
                }

                Row(
                    Modifier.fillMaxWidth()
                        .background(MaterialTheme.colorScheme.surfaceContainerLow, RoundedCornerShape(22.dp))
                        .padding(horizontal = 6.dp, vertical = 4.dp),
                    verticalAlignment = Alignment.Bottom,
                    horizontalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    if (chat.commands.isNotEmpty()) {
                        Box {
                            IconButton(onClick = { cmdMenu = true }, modifier = Modifier.size(36.dp)) {
                                Text("/", fontFamily = FontFamily.Monospace, fontWeight = FontWeight.Bold, fontSize = 18.sp, color = MaterialTheme.colorScheme.primary)
                            }
                            DropdownMenu(cmdMenu, { cmdMenu = false }) {
                                chat.commands.forEach { c ->
                                    DropdownMenuItem(
                                        text = {
                                            Column {
                                                Text("/${c.name}", fontFamily = FontFamily.Monospace, fontWeight = FontWeight.SemiBold, color = MaterialTheme.colorScheme.primary)
                                                if (c.description.isNotEmpty()) Text(c.description, fontSize = 11.5.sp, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1)
                                            }
                                        },
                                        onClick = {
                                            chat.draft = "/${c.name}" + if (c.inputHint.isNullOrEmpty()) "" else " "
                                            cmdMenu = false
                                        },
                                    )
                                }
                            }
                        }
                    }

                    TextField(
                        value = chat.draft,
                        onValueChange = { chat.draft = it },
                        modifier = Modifier.weight(1f),
                        placeholder = { Text(if (chat.busy) "执行中，发送将自动排队…" else "输入 Coding 指令…", fontSize = 14.5.sp) },
                        maxLines = 5,
                        colors = TextFieldDefaults.colors(
                            focusedContainerColor = Color.Transparent,
                            unfocusedContainerColor = Color.Transparent,
                            focusedIndicatorColor = Color.Transparent,
                            unfocusedIndicatorColor = Color.Transparent,
                        ),
                    )

                    val trimmed = chat.draft.trim()
                    if (chat.busy && trimmed.isEmpty()) {
                        IconButton(
                            onClick = { scope.launch { runCatching { chat.stopPrompt() } } },
                            modifier = Modifier.size(36.dp).background(MaterialTheme.colorScheme.onSurface, CircleShape),
                        ) {
                            Icon(Icons.Filled.Stop, "停止", Modifier.size(18.dp), tint = MaterialTheme.colorScheme.surface)
                        }
                    } else {
                        if (chat.busy && trimmed.isNotEmpty()) {
                            IconButton(
                                onClick = { sendNow(behavior = "steer") },
                                enabled = !sending,
                                modifier = Modifier.size(36.dp).background(Frog.extra.warn.copy(alpha = 0.16f), CircleShape),
                            ) {
                                Icon(Icons.Filled.Bolt, "插话", Modifier.size(18.dp), tint = Frog.extra.warn)
                            }
                        }
                        IconButton(
                            onClick = { sendNow(behavior = if (chat.busy) "follow_up" else null) },
                            enabled = trimmed.isNotEmpty() && !sending,
                            modifier = Modifier.size(36.dp).background(
                                if (trimmed.isNotEmpty() && !sending) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outlineVariant,
                                CircleShape,
                            ),
                        ) {
                            Icon(
                                if (chat.busy) Icons.Filled.Schedule else Icons.Filled.ArrowUpward,
                                if (chat.busy) "排队" else "发送",
                                Modifier.size(18.dp),
                                tint = MaterialTheme.colorScheme.onPrimary,
                            )
                        }
                    }
                }
            }
        }
    }
}

// MARK: - 无气泡消息渲染组件

@Composable
private fun CodingMessageBlock(msg: AgyMessage, showDivider: Boolean) {
    val clipboard = LocalClipboardManager.current
    if (msg.role == "user") {
        Column(Modifier.fillMaxWidth()) {
            if (showDivider) {
                HorizontalDivider(
                    Modifier.padding(bottom = 14.dp),
                    thickness = 0.5.dp,
                    color = MaterialTheme.colorScheme.outlineVariant,
                )
            }
            Row(
                Modifier.fillMaxWidth()
                    .height(IntrinsicSize.Min)
                    .clip(RoundedCornerShape(10.dp))
                    .background(MaterialTheme.colorScheme.surfaceContainerLow)
                    .padding(horizontal = 12.dp, vertical = 10.dp),
                horizontalArrangement = Arrangement.spacedBy(10.dp),
                verticalAlignment = Alignment.Top,
            ) {
                Box(
                    Modifier.width(3.dp).fillMaxHeight().background(MaterialTheme.colorScheme.primary, RoundedCornerShape(2.dp)),
                )
                Text(">", fontFamily = FontFamily.Monospace, fontWeight = FontWeight.Bold, fontSize = 13.5.sp, color = MaterialTheme.colorScheme.primary)
                Text(
                    msg.text,
                    modifier = Modifier.weight(1f),
                    fontSize = 15.sp,
                    lineHeight = 22.sp,
                    fontWeight = FontWeight.Medium,
                    color = MaterialTheme.colorScheme.onSurface,
                )
                Icon(
                    Icons.Filled.ContentCopy, "复制指令",
                    Modifier.size(16.dp).clickable { clipboard.setText(AnnotatedString(msg.text)) },
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    } else {
        Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            if (!msg.thinking.isNullOrEmpty()) {
                CodingThinkingDisclosure(msg.thinking, live = false)
            }
            if (msg.tools.isNotEmpty()) {
                CodingToolTimeline(msg.tools)
            }
            if (msg.text.isNotEmpty()) {
                FlatMarkdownStream(text = msg.text, streaming = false)
            }
            if (!msg.error.isNullOrEmpty()) {
                Row(
                    Modifier.fillMaxWidth()
                        .height(IntrinsicSize.Min)
                        .clip(RoundedCornerShape(8.dp))
                        .background(MaterialTheme.colorScheme.error.copy(alpha = 0.1f))
                        .padding(10.dp),
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    Box(Modifier.width(3.dp).fillMaxHeight().background(MaterialTheme.colorScheme.error, RoundedCornerShape(2.dp)))
                    Text(msg.error, color = MaterialTheme.colorScheme.error, fontSize = 12.5.sp)
                }
            }
        }
    }
}

@Composable
private fun FlatMarkdownStream(text: String, streaming: Boolean) {
    val blocks = remember(text) { Markdown.parse(text) }
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        blocks.forEachIndexed { i, b ->
            MdBlockView(b, caret = streaming && i == blocks.lastIndex)
        }
        if (blocks.isEmpty() && streaming) {
            Text("▍", color = MaterialTheme.colorScheme.primary, fontSize = 15.sp)
        }
    }
}

@Composable
private fun CodingThinkingDisclosure(text: String, live: Boolean) {
    var open by remember(live) { mutableStateOf(live) }
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Row(
            Modifier.clickable { open = !open }.padding(vertical = 2.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(6.dp),
        ) {
            Icon(Icons.Filled.Psychology, null, Modifier.size(15.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
            Text(
                if (live) "正在思考…" else "查看思考过程",
                style = MaterialTheme.typography.labelMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Icon(if (open) Icons.Filled.ExpandLess else Icons.Filled.ExpandMore, null, Modifier.size(15.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        AnimatedVisibility(open) {
            Row(Modifier.fillMaxWidth().height(IntrinsicSize.Min)) {
                Box(Modifier.width(2.dp).fillMaxHeight().background(MaterialTheme.colorScheme.outlineVariant))
                Spacer(Modifier.width(10.dp))
                Text(
                    text,
                    fontSize = 12.5.sp,
                    lineHeight = 18.sp,
                    fontStyle = FontStyle.Italic,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}

@Composable
private fun CodingToolTimeline(tools: List<AgyToolCall>) {
    var expanded by remember { mutableStateOf<Set<String>>(emptySet()) }
    Row(Modifier.fillMaxWidth().height(IntrinsicSize.Min)) {
        Spacer(Modifier.width(4.dp))
        Box(Modifier.width(1.5.dp).fillMaxHeight().background(MaterialTheme.colorScheme.outlineVariant))
        Spacer(Modifier.width(10.dp))
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(4.dp)) {
            tools.forEach { tc ->
                val running = tc.status == "in_progress" || tc.status == "pending"
                val failed = tc.status == "failed" || tc.status == "error"
                val hasExtra = tc.detail.isNotEmpty() || tc.output.isNotEmpty()
                val isOpen = tc.id in expanded

                Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    Row(
                        Modifier.fillMaxWidth()
                            .let { if (hasExtra) it.clickable { expanded = if (isOpen) expanded - tc.id else expanded + tc.id } else it }
                            .padding(vertical = 3.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(7.dp),
                    ) {
                        if (running) {
                            CircularProgressIndicator(Modifier.size(12.dp), strokeWidth = 1.6.dp)
                        } else {
                            Icon(
                                if (failed) Icons.Outlined.ErrorOutline else Icons.Filled.CheckCircle,
                                null, Modifier.size(13.dp),
                                tint = if (failed) MaterialTheme.colorScheme.error else Frog.extra.ok,
                            )
                        }
                        Text(
                            tc.title.ifEmpty { tc.name },
                            fontFamily = FontFamily.Monospace,
                            fontSize = 12.sp,
                            color = if (failed) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurface,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.weight(1f),
                        )
                        if (hasExtra) {
                            Icon(
                                if (isOpen) Icons.Filled.ExpandLess else Icons.Filled.ExpandMore,
                                null, Modifier.size(15.dp),
                                tint = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                    }

                    AnimatedVisibility(isOpen && hasExtra) {
                        Column(
                            Modifier.fillMaxWidth()
                                .background(MaterialTheme.colorScheme.surfaceContainer, RoundedCornerShape(8.dp))
                                .padding(8.dp),
                            verticalArrangement = Arrangement.spacedBy(4.dp),
                        ) {
                            if (tc.detail.isNotEmpty()) {
                                Text(tc.detail, fontFamily = FontFamily.Monospace, fontSize = 11.5.sp, fontWeight = FontWeight.Medium)
                            }
                            if (tc.output.isNotEmpty()) {
                                Text(
                                    tc.output.take(1800),
                                    fontFamily = FontFamily.Monospace,
                                    fontSize = 11.sp,
                                    lineHeight = 16.sp,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                )
                            }
                        }
                    }
                }
            }
        }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun CodingPermCallout(perm: AgyPermission, onDecide: (String) -> Unit) {
    Row(
        Modifier.fillMaxWidth()
            .height(IntrinsicSize.Min)
            .clip(RoundedCornerShape(10.dp))
            .background(Frog.extra.warn.copy(alpha = 0.08f))
            .padding(12.dp),
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Box(Modifier.width(3.dp).fillMaxHeight().background(Frog.extra.warn, RoundedCornerShape(2.dp)))
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                Icon(Icons.Filled.Shield, null, Modifier.size(16.dp), tint = Frog.extra.warn)
                Text("请求操作确认：${perm.title}", fontWeight = FontWeight.SemiBold, fontSize = 13.5.sp, color = Frog.extra.warn)
            }
            if (perm.detail.isNotEmpty()) {
                Text(
                    perm.detail,
                    fontFamily = FontFamily.Monospace,
                    fontSize = 11.5.sp,
                    modifier = Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainer, RoundedCornerShape(8.dp)).padding(8.dp),
                )
            }
            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                perm.options.forEach { opt ->
                    val reject = opt.optionId.contains("reject") || opt.kind.contains("reject")
                    if (reject) {
                        OutlinedButton(
                            onClick = { onDecide(opt.optionId) },
                            colors = ButtonDefaults.outlinedButtonColors(contentColor = MaterialTheme.colorScheme.error),
                            contentPadding = PaddingValues(horizontal = 14.dp, vertical = 4.dp),
                        ) { Text(opt.name, fontSize = 12.5.sp) }
                    } else {
                        Button(
                            onClick = { onDecide(opt.optionId) },
                            contentPadding = PaddingValues(horizontal = 14.dp, vertical = 4.dp),
                        ) { Text(opt.name, fontSize = 12.5.sp) }
                    }
                }
            }
        }
    }
}

// MARK: - 新建项目 & 项目文件弹窗

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun NewProjectDialog(store: AgyStore, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    var linkExisting by remember { mutableStateOf(false) }
    var name by remember { mutableStateOf("") }
    var path by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("添加项目目录") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
                    SegmentedButton(selected = !linkExisting, onClick = { linkExisting = false }, shape = SegmentedButtonDefaults.itemShape(0, 2)) { Text("新建空目录") }
                    SegmentedButton(selected = linkExisting, onClick = { linkExisting = true }, shape = SegmentedButtonDefaults.itemShape(1, 2)) { Text("关联已有目录") }
                }
                if (!linkExisting) {
                    OutlinedTextField(value = name, onValueChange = { name = it }, label = { Text("文件夹名称") }, placeholder = { Text("my-webapp") }, singleLine = true)
                    val root = store.status?.projectsRoot.orEmpty()
                    if (root.isNotEmpty()) Text("位置：$root/${name.ifEmpty { "<名称>" }}", fontSize = 11.5.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                } else {
                    OutlinedTextField(value = path, onValueChange = { path = it }, label = { Text("主机绝对路径") }, placeholder = { Text("/home/user/app") }, singleLine = true)
                    OutlinedTextField(value = name, onValueChange = { name = it }, label = { Text("显示名称（可选）") }, singleLine = true)
                }
                error?.let { Text(it, color = MaterialTheme.colorScheme.error, fontSize = 12.sp) }
            }
        },
        confirmButton = {
            Button(
                enabled = !busy && (if (linkExisting) path.isNotBlank() else name.isNotBlank()),
                onClick = {
                    busy = true
                    error = null
                    scope.launch {
                        runCatching { store.createProject(name.trim(), if (linkExisting) path.trim() else "") }
                            .onSuccess { onDismiss() }
                            .onFailure { error = ServerClient.describe(it) }
                        busy = false
                    }
                },
            ) { Text(if (busy) "处理中…" else "确认") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("取消") } },
    )
}

@Composable
private fun ProjectFilesDialog(server: ServerEntry, project: AgyProject, onDismiss: () -> Unit) {
    var subDir by remember { mutableStateOf("") }
    var entries by remember { mutableStateOf<List<AgyFileEntry>>(emptyList()) }
    var error by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(project.id, subDir) {
        error = null
        val q = if (subDir.isEmpty()) emptyMap() else mapOf("dir" to subDir)
        runCatching { server.client.getArray("/api/agy/projects/${project.id}/files", q).objects().map(AgyFileEntry::from) }
            .onSuccess { entries = it }
            .onFailure { error = ServerClient.describe(it) }
    }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(if (subDir.isEmpty()) project.name else "${project.name}/$subDir", maxLines = 1, overflow = TextOverflow.Ellipsis) },
        text = {
            Column(Modifier.fillMaxWidth().height(320.dp).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                if (subDir.isNotEmpty()) {
                    TextButton(onClick = { subDir = subDir.substringBeforeLast('/', "") }) { Text("← 返回上一级") }
                }
                error?.let { Text(it, color = MaterialTheme.colorScheme.error, fontSize = 12.sp) }
                if (entries.isEmpty() && error == null) {
                    Text("目录为空", color = MaterialTheme.colorScheme.onSurfaceVariant, fontSize = 13.sp)
                }
                entries.forEach { e ->
                    val rel = if (subDir.isEmpty()) e.name else "$subDir/${e.name}"
                    Row(
                        Modifier.fillMaxWidth()
                            .let { if (e.isDir) it.clickable { subDir = rel } else it }
                            .padding(vertical = 7.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        Icon(
                            if (e.isDir) Icons.Filled.Folder else Icons.Filled.Description,
                            null, Modifier.size(18.dp),
                            tint = if (e.isDir) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                        Text(if (e.isDir) "${e.name}/" else e.name, modifier = Modifier.weight(1f), fontSize = 13.5.sp, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        e.size?.let { Text(Fmt.size(it), fontSize = 11.5.sp, color = MaterialTheme.colorScheme.onSurfaceVariant) }
                    }
                }
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text("完成") } },
    )
}
