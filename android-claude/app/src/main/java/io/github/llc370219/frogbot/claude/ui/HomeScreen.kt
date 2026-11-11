package io.github.llc370219.frogbot.claude.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.outlined.GppMaybe
import androidx.compose.material.icons.outlined.LockClock
import androidx.compose.material3.Badge
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TextField
import androidx.compose.material3.TextFieldDefaults
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.llc370219.frogbot.claude.R
import io.github.llc370219.frogbot.claude.core.AppState
import io.github.llc370219.frogbot.claude.core.BotProfile
import io.github.llc370219.frogbot.claude.core.BotRef
import io.github.llc370219.frogbot.claude.core.Fmt
import io.github.llc370219.frogbot.claude.core.ServerEntry
import io.github.llc370219.frogbot.claude.core.ServerStatus

/** 首页：按服务器分组列出所有 Bot（像聊天软件的会话列表） */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun HomeScreen(state: AppState, nav: Nav) {
    var searching by remember { mutableStateOf(false) }
    var query by remember { mutableStateOf("") }
    var addMenu by remember { mutableStateOf(false) }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    if (searching) {
                        TextField(
                            query, { query = it }, placeholder = { Text("搜索 Bot") }, singleLine = true,
                            colors = TextFieldDefaults.colors(
                                focusedContainerColor = Color.Transparent, unfocusedContainerColor = Color.Transparent,
                                focusedIndicatorColor = Color.Transparent, unfocusedIndicatorColor = Color.Transparent,
                            ),
                            modifier = Modifier.fillMaxWidth(),
                        )
                    } else {
                        Text(stringResource(R.string.app_name))
                    }
                },
                actions = {
                    IconButton(onClick = { searching = !searching; if (!searching) query = "" }) {
                        Icon(if (searching) Icons.Filled.Close else Icons.Filled.Search, "搜索")
                    }
                    Box {
                        IconButton(onClick = { addMenu = true }) { Icon(Icons.Filled.Add, "新建") }
                        DropdownMenu(addMenu, { addMenu = false }) {
                            state.servers.filter { it.online }.forEach { s ->
                                DropdownMenuItem(text = { Text("在「${s.name}」新建 Bot") }, onClick = {
                                    addMenu = false
                                    nav.push(Screen.BotEditor(s.id, "local"))
                                })
                            }
                            DropdownMenuItem(text = { Text("添加服务器") }, onClick = { addMenu = false; nav.push(Screen.AddServer()) })
                        }
                    }
                },
            )
        },
        containerColor = MaterialTheme.colorScheme.surface,
    ) { pad ->
        LazyColumn(Modifier.padding(pad).fillMaxSize()) {
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
                        Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
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
                                style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant,
                                modifier = Modifier.padding(start = 16.dp, top = 8.dp, bottom = 2.dp),
                            )
                        }
                    }
                    items(bots, key = { "b-${s.id}-${g.id}-${it.id}" }) { b ->
                        BotRow(state, s, BotRef(s.id, g.id, b.id), b) { nav.push(Screen.Chat(BotRef(s.id, g.id, b.id))) }
                    }
                }
            }
        }
    }
}

@Composable
private fun ServerHeader(s: ServerEntry) {
    val note = when (s.status) {
        ServerStatus.Online -> if (s.config.insecure) "HTTP 未加密" else null
        ServerStatus.Connecting -> "正在连接"
        ServerStatus.NeedsLogin -> "需要重新登录"
        is ServerStatus.Offline -> "离线"
    }
    Row(Modifier.padding(start = 16.dp, end = 16.dp, top = 16.dp, bottom = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(s.name, style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.primary)
        if (note != null) {
            Text(
                " · $note", style = MaterialTheme.typography.labelMedium,
                color = if (s.status == ServerStatus.Online) Frog.extra.warn else MaterialTheme.colorScheme.onSurfaceVariant,
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
        Modifier.fillMaxWidth().clickable(onClick = onClick).heightIn(min = 72.dp).padding(horizontal = 16.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        BotAvatar(bot, 48.dp)
        Column(Modifier.weight(1f)) {
            Text(bot.name, style = MaterialTheme.typography.bodyLarge, fontWeight = if (unread > 0) FontWeight.SemiBold else FontWeight.Normal, maxLines = 1)
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
