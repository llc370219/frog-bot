package io.github.llc370219.frogbot.claude.ui

import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.padding
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Chat
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.outlined.Chat
import androidx.compose.material.icons.outlined.Schedule
import androidx.compose.material.icons.outlined.Settings
import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import io.github.llc370219.frogbot.claude.core.AppState
import io.github.llc370219.frogbot.claude.core.BotRef

/** 页面：底部三个标签（聊天 / 调度器 / 设置），其余页面压栈打开 */
sealed interface Screen {
    data object Home : Screen
    data object Scheduler : Screen
    data object Settings : Screen
    data class Chat(val ref: BotRef) : Screen
    data class Server(val id: String) : Screen
    data class AddServer(val existing: String? = null) : Screen
    data class Channels(val ref: BotRef) : Screen
    data class Agents(val ref: BotRef) : Screen
    data class Account(val server: String) : Screen
    data class BotEditor(val server: String, val device: String, val ref: BotRef? = null) : Screen
    data object Appearance : Screen
    data object Notifications : Screen
    data object Relay : Screen
}

class Nav {
    val stack = mutableStateListOf<Screen>(Screen.Home)
    val top: Screen get() = stack.last()
    fun push(s: Screen) { stack.add(s) }
    fun pop() { if (stack.size > 1) stack.removeAt(stack.lastIndex) }
    fun tab(s: Screen) { stack.clear(); stack.add(s) }
    fun replaceTop(s: Screen) { stack[stack.lastIndex] = s }
}

@Composable
fun FrogRoot(state: AppState, openRef: MutableState<BotRef?>) {
    val nav = remember { Nav() }
    BackHandler(enabled = nav.stack.size > 1) { nav.pop() }

    LaunchedEffect(openRef.value) {
        openRef.value?.let { nav.tab(Screen.Home); nav.push(Screen.Chat(it)); openRef.value = null }
    }
    val top = nav.top
    LaunchedEffect(top) {
        state.viewing = (top as? Screen.Chat)?.ref
        (top as? Screen.Chat)?.ref?.let { state.markRead(it) }
    }

    if (state.servers.isEmpty() && top == Screen.Home) {
        FirstRunScreen(onAdd = { nav.push(Screen.AddServer()) })
        return
    }

    val tabbed = top == Screen.Home || top == Screen.Scheduler || top == Screen.Settings
    Scaffold(
        bottomBar = { if (tabbed) BottomBar(state, nav) },
        containerColor = MaterialTheme.colorScheme.surface,
    ) { pad ->
        Box(Modifier.fillMaxSize().padding(if (tabbed) pad else PaddingValues(0.dp))) {
            AnimatedContent(top, transitionSpec = { fadeIn() togetherWith fadeOut() }, label = "screen") { s ->
                ScreenContent(s, state, nav)
            }
        }
    }
}

@Composable
private fun ScreenContent(s: Screen, state: AppState, nav: Nav) {
    when (s) {
        Screen.Home -> HomeScreen(state, nav)
        Screen.Scheduler -> SchedulerScreen(state, nav)
        Screen.Settings -> SettingsScreen(state, nav)
        is Screen.Chat -> {
            val session = state.session(s.ref)
            if (session != null) ChatScreen(state, session, nav) else MissingScreen("这个 Bot 现在连不上", nav)
        }
        is Screen.Server -> state.server(s.id)?.let { ServerScreen(state, it, nav) } ?: MissingScreen("服务器已移除", nav)
        is Screen.AddServer -> AddServerScreen(state, s.existing?.let { state.server(it) }, nav)
        is Screen.Channels -> state.session(s.ref)?.let { ChannelsScreen(it, nav) } ?: MissingScreen("这个 Bot 现在连不上", nav)
        is Screen.Agents -> state.session(s.ref)?.let { AgentsScreen(it, nav) } ?: MissingScreen("这个 Bot 现在连不上", nav)
        is Screen.Account -> state.server(s.server)?.let { AccountScreen(state, it, nav) } ?: MissingScreen("服务器已移除", nav)
        is Screen.BotEditor -> state.server(s.server)?.let { BotEditorScreen(state, it, s.device, s.ref, nav) } ?: MissingScreen("服务器已移除", nav)
        Screen.Appearance -> AppearanceScreen(state, nav)
        Screen.Notifications -> NotificationsScreen(state, nav)
        Screen.Relay -> RelayScreen(state, nav)
    }
}

@Composable
private fun BottomBar(state: AppState, nav: Nav) {
    val top = nav.top
    NavigationBar(containerColor = MaterialTheme.colorScheme.surfaceContainer) {
        NavigationBarItem(
            selected = top == Screen.Home, onClick = { nav.tab(Screen.Home) },
            icon = {
                val n = state.totalUnread
                BadgedBox(badge = { if (n > 0) Badge { Text(if (n > 99) "99+" else "$n") } }) {
                    Icon(if (top == Screen.Home) Icons.Filled.Chat else Icons.Outlined.Chat, null)
                }
            },
            label = { Text("聊天") },
        )
        NavigationBarItem(
            selected = top == Screen.Scheduler, onClick = { nav.tab(Screen.Scheduler) },
            icon = { Icon(if (top == Screen.Scheduler) Icons.Filled.Schedule else Icons.Outlined.Schedule, null) },
            label = { Text("调度器") },
        )
        NavigationBarItem(
            selected = top == Screen.Settings, onClick = { nav.tab(Screen.Settings) },
            icon = { Icon(if (top == Screen.Settings) Icons.Filled.Settings else Icons.Outlined.Settings, null) },
            label = { Text("设置") },
        )
    }
}
