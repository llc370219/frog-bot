package io.github.llc370219.frogbot.claude.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Code
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.outlined.GppMaybe
import androidx.compose.material.icons.outlined.LockClock
import androidx.compose.material3.Badge
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.llc370219.frogbot.claude.core.AppState
import io.github.llc370219.frogbot.claude.core.BotProfile
import io.github.llc370219.frogbot.claude.core.BotRef
import io.github.llc370219.frogbot.claude.core.Fmt
import io.github.llc370219.frogbot.claude.core.ServerEntry
import io.github.llc370219.frogbot.claude.core.ServerStatus
import kotlinx.coroutines.launch

/**
 * 极简双核主页（与 iOS / macOS 统一设计语言）：
 * - 顶部悬浮栏：左侧设置按钮（带在线状态点）+ 中间模式标题胶囊 + 右侧调度器/新建按钮
 * - 中间画布：Bot 模式（气泡对话列表）↔ Coding 模式（直连官方 CLI 无气泡工作台）
 * - 底部悬浮栏：左侧 `</>` 模式切换圆钮 + 右侧极简搜索胶囊
 */
@Composable
fun HomeScreen(state: AppState, nav: Nav) {
    val scope = rememberCoroutineScope()
    var query by remember { mutableStateOf("") }
    var addMenu by remember { mutableStateOf(false) }

    val hasOnline = state.servers.any { it.online }
    val hasNeedsLogin = state.servers.any { it.status == ServerStatus.NeedsLogin }
    val statusDotColor = when {
        hasOnline -> Frog.extra.ok
        hasNeedsLogin -> Frog.extra.warn
        else -> MaterialTheme.colorScheme.outline
    }

    Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.surface)) {
        // 主内容区
        val contentPad = PaddingValues(top = 74.dp, bottom = 92.dp)
        if (state.codingMode) {
            val activeSrv = state.activeCodingServer()
            if (activeSrv != null) {
                CodingWorkspaceFeed(
                    state = state,
                    server = activeSrv,
                    query = query,
                    nav = nav,
                    contentPadding = contentPad,
                )
            } else {
                Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    Text("请先添加一台 Frog Bot 主机", color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        } else {
            LazyColumn(Modifier.fillMaxSize(), contentPadding = contentPad) {
                for (s in state.servers) {
                    item(key = "h-${s.id}") { ServerHeader(s) }
                    when (val st = s.status) {
                        ServerStatus.NeedsLogin -> item(key = "l-${s.id}") {
                            ListRow(
                                "登录已过期", "这台服务器的会话失效了，重新登录后继续",
                                leading = { Icon(Icons.Outlined.LockClock, null, tint = Frog.extra.warn) },
                                onClick = { nav.push(Screen.AddServer(s.id)) },
                            ) { TextButton(onClick = { nav.push(Screen.AddServer(s.id)) }) { Text("重新登录") } }
                        }
                        is ServerStatus.Offline -> if (s.devices.isEmpty()) item(key = "o-${s.id}") {
                            ListRow("连不上", st.error)
                        }
                        ServerStatus.Connecting -> if (s.devices.isEmpty()) item(key = "c-${s.id}") {
                            Row(
                                Modifier.padding(16.dp),
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(12.dp),
                            ) {
                                CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                                Text("正在连接…", color = MaterialTheme.colorScheme.onSurfaceVariant)
                            }
                        }
                        else -> {}
                    }
                    for (g in s.devices) {
                        val bots = g.bots.filter { query.isBlank() || it.name.contains(query.trim(), ignoreCase = true) }
                        if (g.id != "local" && (bots.isNotEmpty() || query.isBlank())) {
                            item(key = "g-${s.id}-${g.id}") {
                                Text(
                                    g.name + if (g.online) " · 互联" else " · 离线",
                                    style = MaterialTheme.typography.labelMedium,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    modifier = Modifier.padding(start = 16.dp, top = 8.dp, bottom = 2.dp),
                                )
                            }
                        }
                        items(bots, key = { "b-${s.id}-${g.id}-${it.id}" }) { b ->
                            BotRow(state, s, BotRef(s.id, g.id, b.id), b) {
                                nav.push(Screen.Chat(BotRef(s.id, g.id, b.id)))
                            }
                        }
                    }
                }
            }
        }

        // 顶部悬浮栏
        Row(
            Modifier.align(Alignment.TopCenter)
                .fillMaxWidth()
                .background(MaterialTheme.colorScheme.surface.copy(alpha = 0.94f))
                .statusBarsPadding()
                .padding(horizontal = 16.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceBetween,
        ) {
            // 左：设置圆钮（带状态点）
            Surface(
                shape = CircleShape,
                color = MaterialTheme.colorScheme.surfaceContainerLow,
                border = BorderStroke(0.7.dp, MaterialTheme.colorScheme.outlineVariant),
                onClick = { nav.push(Screen.Settings) },
            ) {
                Box(Modifier.size(40.dp), contentAlignment = Alignment.Center) {
                    Icon(Icons.Filled.Settings, "设置", Modifier.size(20.dp), tint = MaterialTheme.colorScheme.onSurface)
                    Box(Modifier.align(Alignment.TopEnd).padding(8.dp)) {
                        StatusDot(statusDotColor, size = 7)
                    }
                }
            }

            // 中：Coding 标识胶囊 或 品牌标题
            if (state.codingMode) {
                Surface(
                    shape = RoundedCornerShape(999.dp),
                    color = MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.65f),
                    border = BorderStroke(0.7.dp, MaterialTheme.colorScheme.primary.copy(alpha = 0.35f)),
                ) {
                    Row(
                        Modifier.padding(horizontal = 12.dp, vertical = 6.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(5.dp),
                    ) {
                        Icon(Icons.Filled.Code, null, Modifier.size(15.dp), tint = MaterialTheme.colorScheme.primary)
                        Text("Coding 工作台", style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.primary, fontWeight = FontWeight.SemiBold)
                    }
                }
            } else {
                Text(
                    "Frog Bot",
                    style = MaterialTheme.typography.titleMedium,
                    fontWeight = FontWeight.SemiBold,
                    color = MaterialTheme.colorScheme.onSurface,
                )
            }

            // 右：调度器（Bot 模式）+ 新建菜单圆钮
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                if (!state.codingMode) {
                    Surface(
                        shape = CircleShape,
                        color = MaterialTheme.colorScheme.surfaceContainerLow,
                        border = BorderStroke(0.7.dp, MaterialTheme.colorScheme.outlineVariant),
                        onClick = { nav.push(Screen.Scheduler) },
                    ) {
                        Box(Modifier.size(40.dp), contentAlignment = Alignment.Center) {
                            Icon(Icons.Filled.Schedule, "自动调度器", Modifier.size(19.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                        }
                    }
                }
                Box {
                    Surface(
                        shape = CircleShape,
                        color = MaterialTheme.colorScheme.surfaceContainerLow,
                        border = BorderStroke(0.7.dp, MaterialTheme.colorScheme.outlineVariant),
                        onClick = {
                            if (state.codingMode) {
                                val srv = state.activeCodingServer()
                                if (srv != null && srv.online) {
                                    val store = state.agyStore(srv)
                                    scope.launch {
                                        runCatching { store.createSession() }
                                            .onSuccess { s -> nav.push(Screen.CodingChat(srv.id, s.id, s.projectId, s.title)) }
                                    }
                                } else {
                                    addMenu = true
                                }
                            } else {
                                addMenu = true
                            }
                        },
                    ) {
                        Box(Modifier.size(40.dp), contentAlignment = Alignment.Center) {
                            Icon(Icons.Filled.Add, "新建", Modifier.size(20.dp), tint = MaterialTheme.colorScheme.onSurface)
                        }
                    }
                    DropdownMenu(addMenu, { addMenu = false }) {
                        state.servers.filter { it.online }.forEach { s ->
                            DropdownMenuItem(
                                text = { Text("在「${s.name}」新建 Bot") },
                                onClick = {
                                    addMenu = false
                                    nav.push(Screen.BotEditor(s.id, "local"))
                                },
                            )
                        }
                        if (state.servers.any { it.online }) HorizontalDivider()
                        DropdownMenuItem(
                            text = { Text("添加主机 / 粘贴 froge2ee:// 连接码") },
                            onClick = { addMenu = false; nav.push(Screen.AddServer()) },
                        )
                    }
                }
            }
        }

        // 底部悬浮栏：左侧 `</>` 模式切换 + 右侧搜索胶囊
        Row(
            Modifier.align(Alignment.BottomCenter)
                .fillMaxWidth()
                .background(MaterialTheme.colorScheme.surface.copy(alpha = 0.94f))
                .navigationBarsPadding()
                .imePadding()
                .padding(horizontal = 16.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            Surface(
                shape = CircleShape,
                color = if (state.codingMode) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.surfaceContainerLow,
                border = BorderStroke(0.7.dp, if (state.codingMode) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outlineVariant),
                onClick = { state.codingMode = !state.codingMode },
            ) {
                Box(Modifier.size(44.dp), contentAlignment = Alignment.Center) {
                    Icon(
                        Icons.Filled.Code,
                        if (state.codingMode) "切换回 Bot 对话" else "进入 Coding 模式",
                        Modifier.size(21.dp),
                        tint = if (state.codingMode) MaterialTheme.colorScheme.onPrimary else MaterialTheme.colorScheme.onSurface,
                    )
                }
            }

            Surface(
                modifier = Modifier.weight(1f).height(44.dp),
                shape = RoundedCornerShape(999.dp),
                color = MaterialTheme.colorScheme.surfaceContainerLow,
                border = BorderStroke(0.7.dp, MaterialTheme.colorScheme.outlineVariant),
            ) {
                Row(
                    Modifier.fillMaxSize().padding(horizontal = 14.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    Icon(Icons.Filled.Search, null, Modifier.size(18.dp), tint = MaterialTheme.colorScheme.onSurfaceVariant)
                    Box(Modifier.weight(1f), contentAlignment = Alignment.CenterStart) {
                        if (query.isEmpty()) {
                            Text(
                                if (state.codingMode) "搜索 Coding 会话…" else "搜索 Bot 对话…",
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                fontSize = 14.sp,
                            )
                        }
                        BasicTextField(
                            value = query,
                            onValueChange = { query = it },
                            singleLine = true,
                            textStyle = TextStyle(color = MaterialTheme.colorScheme.onSurface, fontSize = 14.5.sp),
                            cursorBrush = SolidColor(MaterialTheme.colorScheme.primary),
                            modifier = Modifier.fillMaxWidth(),
                        )
                    }
                    if (query.isNotEmpty()) {
                        Icon(
                            Icons.Filled.Close, "清空",
                            Modifier.size(17.dp).clickable { query = "" },
                            tint = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun ServerHeader(s: ServerEntry) {
    val note = when (s.status) {
        ServerStatus.Online -> s.config.tunnelBadge ?: if (s.config.insecure) "HTTP 未加密" else null
        ServerStatus.Connecting -> "正在连接"
        ServerStatus.NeedsLogin -> "需要重新登录"
        is ServerStatus.Offline -> "离线"
    }
    Row(
        Modifier.padding(start = 16.dp, end = 16.dp, top = 16.dp, bottom = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        StatusDot(
            when (s.status) {
                ServerStatus.Online -> Frog.extra.ok
                ServerStatus.NeedsLogin -> Frog.extra.warn
                ServerStatus.Connecting -> MaterialTheme.colorScheme.outline
                is ServerStatus.Offline -> MaterialTheme.colorScheme.error
            },
            size = 6,
        )
        Text(s.name, style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurface, fontWeight = FontWeight.SemiBold)
        if (note != null) {
            Text(
                note,
                style = MaterialTheme.typography.labelSmall,
                color = when {
                    s.config.tunnelBadge != null && s.status == ServerStatus.Online -> MaterialTheme.colorScheme.primary
                    s.status == ServerStatus.Online -> Frog.extra.warn
                    else -> MaterialTheme.colorScheme.onSurfaceVariant
                },
            )
        }
    }
}

@Composable
private fun BotRow(state: AppState, s: ServerEntry, ref: BotRef, bot: BotProfile, onClick: () -> Unit) {
    val session = state.session(ref)
    val last = session?.lastMessage
    val pending = session?.pendingApprovals ?: 0
    val unread = state.unread[ref.key] ?: 0
    val sub = when {
        pending > 0 -> "有 $pending 个操作等你确认"
        session?.busy == true -> "正在回复…"
        session?.working == true -> session.running.firstOrNull()?.let { Fmt.nowDoing(it) } ?: "正在工作…"
        last != null -> (if (last.role == "user") "你：" else "") + last.text.replace('\n', ' ').ifEmpty { "[附件]" }
        session?.ready == false -> "正在连接…"
        else -> "还没有对话"
    }
    Row(
        Modifier.fillMaxWidth().clickable(onClick = onClick).heightIn(min = 70.dp).padding(horizontal = 16.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        BotAvatar(bot, 46.dp)
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Text(bot.name, style = MaterialTheme.typography.bodyLarge, fontWeight = if (unread > 0) FontWeight.SemiBold else FontWeight.Medium, maxLines = 1)
            Text(
                sub, style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis,
                color = if (pending > 0) Frog.extra.att else MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        Column(horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(6.dp)) {
            if (last != null) Text(Fmt.time(last.created), style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                if (pending > 0) Icon(Icons.Outlined.GppMaybe, "等你确认", Modifier.size(18.dp), tint = Frog.extra.att)
                else if (session?.working == true || session?.busy == true) StatusDot(MaterialTheme.colorScheme.primary)
                if (unread > 0) Badge { Text(if (unread > 99) "99+" else "$unread") }
            }
        }
    }
}
