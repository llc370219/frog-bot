package io.github.llc370219.frogbot.claude.ui

import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import io.github.llc370219.frogbot.claude.core.AppState
import io.github.llc370219.frogbot.claude.core.BotRef
import io.github.llc370219.frogbot.claude.core.ClientUpdateStatus
import io.github.llc370219.frogbot.claude.core.ClientUpdater

/** 页面路由：全屏极简导航栈（主页含 Bot / Coding 双核悬浮切换，不再使用旧式底部三 Tab） */
sealed interface Screen {
    data object Home : Screen
    data object Scheduler : Screen
    data object Settings : Screen
    data class Chat(val ref: BotRef) : Screen
    data class CodingChat(val server: String, val sessionId: String, val projectId: String, val title: String) : Screen
    data class Server(val id: String) : Screen
    data class AddServer(val existing: String? = null) : Screen
    data class Channels(val ref: BotRef) : Screen
    data class Agents(val ref: BotRef) : Screen
    data class Account(val server: String) : Screen
    data class CliAuth(val server: String) : Screen
    data class ProxyGateway(val server: String) : Screen
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
    fun tab(s: Screen) {
        if (s == Screen.Home) {
            stack.clear()
            stack.add(Screen.Home)
        } else {
            stack.clear()
            stack.add(Screen.Home)
            stack.add(s)
        }
    }
    fun replaceTop(s: Screen) { stack[stack.lastIndex] = s }
}

@Composable
fun FrogRoot(state: AppState, openRef: MutableState<BotRef?>, updater: ClientUpdater, onInstall: () -> Unit) {
    val nav = remember { Nav() }
    BackHandler(enabled = nav.stack.size > 1) { nav.pop() }
    ClientUpdatePrompt(updater.status as? ClientUpdateStatus.Ready, onInstall)

    LaunchedEffect(openRef.value) {
        openRef.value?.let {
            state.codingMode = false
            nav.tab(Screen.Home)
            nav.push(Screen.Chat(it))
            openRef.value = null
        }
    }
    val top = nav.top
    LaunchedEffect(top) {
        state.viewing = (top as? Screen.Chat)?.ref
        (top as? Screen.Chat)?.ref?.let { state.markRead(it) }
    }

    if (state.servers.isEmpty() && top == Screen.Home) {
        FirstRunScreen(onAdd = { nav.push(Screen.AddServer()) }, onSettings = { nav.push(Screen.Settings) })
        return
    }

    Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.surface)) {
        AnimatedContent(top, transitionSpec = { fadeIn() togetherWith fadeOut() }, label = "screen") { s ->
            ScreenContent(s, state, nav, updater, onInstall)
        }
    }
}

@Composable
private fun ScreenContent(s: Screen, state: AppState, nav: Nav, updater: ClientUpdater, onInstall: () -> Unit) {
    when (s) {
        Screen.Home -> HomeScreen(state, nav)
        Screen.Scheduler -> SchedulerScreen(state, nav)
        Screen.Settings -> SettingsScreen(state, nav, updater, onInstall)
        is Screen.Chat -> {
            val session = state.session(s.ref)
            if (session != null) ChatScreen(state, session, nav) else MissingScreen("这个 Bot 现在连不上", nav)
        }
        is Screen.CodingChat -> {
            val srv = state.server(s.server)
            if (srv != null) {
                val chat = state.agySession(srv, s.sessionId, s.projectId, s.title)
                CodingChatScreen(state, srv, chat, nav)
            } else {
                MissingScreen("服务器已移除", nav)
            }
        }
        is Screen.Server -> state.server(s.id)?.let { ServerScreen(state, it, nav) } ?: MissingScreen("服务器已移除", nav)
        is Screen.AddServer -> AddServerScreen(state, s.existing?.let { state.server(it) }, nav)
        is Screen.Channels -> state.session(s.ref)?.let { ChannelsScreen(it, nav) } ?: MissingScreen("这个 Bot 现在连不上", nav)
        is Screen.Agents -> state.session(s.ref)?.let { AgentsScreen(it, nav) } ?: MissingScreen("这个 Bot 现在连不上", nav)
        is Screen.Account -> state.server(s.server)?.let { AccountScreen(state, it, nav) } ?: MissingScreen("服务器已移除", nav)
        is Screen.CliAuth -> state.server(s.server)?.let { CliAuthScreen(it, nav) } ?: MissingScreen("服务器已移除", nav)
        is Screen.ProxyGateway -> state.server(s.server)?.let { ProxyGatewayScreen(it, nav) } ?: MissingScreen("服务器已移除", nav)
        is Screen.BotEditor -> state.server(s.server)?.let { BotEditorScreen(state, it, s.device, s.ref, nav) } ?: MissingScreen("服务器已移除", nav)
        Screen.Appearance -> AppearanceScreen(state, nav)
        Screen.Notifications -> NotificationsScreen(state, nav)
        Screen.Relay -> RelayScreen(state, nav)
    }
}

@Composable
private fun ClientUpdatePrompt(update: ClientUpdateStatus.Ready?, onInstall: () -> Unit) {
    var dismissedVersion by rememberSaveable { mutableStateOf<String?>(null) }
    if (update == null || update.version == dismissedVersion) return

    AlertDialog(
        onDismissRequest = { dismissedVersion = update.version },
        title = { Text("frog ${update.version} 已下载") },
        text = { Text("请在 Android 安装界面确认更新。") },
        confirmButton = {
            TextButton(onClick = {
                dismissedVersion = update.version
                onInstall()
            }) { Text("安装更新") }
        },
        dismissButton = { TextButton(onClick = { dismissedVersion = update.version }) { Text("稍后") } },
    )
}
