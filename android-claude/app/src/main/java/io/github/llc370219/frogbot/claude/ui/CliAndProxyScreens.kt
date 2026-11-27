package io.github.llc370219.frogbot.claude.ui

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.OpenInBrowser
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.RadioButtonUnchecked
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Stop
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
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
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.llc370219.frogbot.claude.core.ServerClient
import io.github.llc370219.frogbot.claude.core.ServerEntry
import io.github.llc370219.frogbot.claude.core.arr
import io.github.llc370219.frogbot.claude.core.bool
import io.github.llc370219.frogbot.claude.core.obj
import io.github.llc370219.frogbot.claude.core.objects
import io.github.llc370219.frogbot.claude.core.str
import io.github.llc370219.frogbot.claude.core.strOrNull
import io.github.llc370219.frogbot.claude.core.strings
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import org.json.JSONObject

// MARK: - 官方 CLI 无头登录（Codex / Claude Code / Gemini CLI / Grok CLI）

data class CliAuthSessionModel(
    val id: String,
    val cli: String,
    val mode: String,
    val status: String,
    val authUrl: String?,
    val userCode: String?,
    val needsInput: Boolean,
    val inputPrompt: String?,
    val error: String?,
) {
    companion object {
        fun from(j: JSONObject) = CliAuthSessionModel(
            id = j.str("id"),
            cli = j.str("cli"),
            mode = j.str("mode"),
            status = j.str("status"),
            authUrl = j.strOrNull("auth_url"),
            userCode = j.strOrNull("user_code"),
            needsInput = j.bool("needs_input"),
            inputPrompt = j.strOrNull("input_prompt"),
            error = j.strOrNull("error"),
        )
    }
}

data class CliAuthStatusItem(
    val id: String,
    val name: String,
    val installed: Boolean,
    val loggedIn: Boolean,
    val authMethod: String?,
    val accountHint: String?,
    val supportsDeviceAuth: Boolean,
    val supportsOAuthBridge: Boolean,
    val supportsOAuthToken: Boolean,
    val oauthTokenEnv: String?,
    val apiKeyEnv: String,
    val loginHint: String,
    val installHint: String,
    val activeSession: CliAuthSessionModel?,
) {
    companion object {
        fun from(j: JSONObject) = CliAuthStatusItem(
            id = j.str("id"),
            name = j.str("name"),
            installed = j.bool("installed"),
            loggedIn = j.bool("logged_in"),
            authMethod = j.strOrNull("auth_method"),
            accountHint = j.strOrNull("account_hint"),
            supportsDeviceAuth = j.bool("supports_device_auth"),
            supportsOAuthBridge = j.bool("supports_oauth_bridge"),
            supportsOAuthToken = j.bool("supports_oauth_token"),
            oauthTokenEnv = j.strOrNull("oauth_token_env"),
            apiKeyEnv = j.str("api_key_env"),
            loginHint = j.str("login_hint"),
            installHint = j.str("install_hint"),
            activeSession = j.obj("active_session")?.let(CliAuthSessionModel::from),
        )
    }
}

@Composable
fun CliAuthScreen(server: ServerEntry, nav: Nav) {
    val scope = rememberCoroutineScope()
    var items by remember { mutableStateOf<List<CliAuthStatusItem>>(emptyList()) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var selected by remember { mutableStateOf<CliAuthStatusItem?>(null) }

    fun refresh() {
        loading = true
        error = null
        scope.launch {
            runCatching { server.client.getArray("/api/v2/cli/auth").objects().map(CliAuthStatusItem::from) }
                .onSuccess { items = it }
                .onFailure { error = ServerClient.describe(it) }
            loading = false
        }
    }

    LaunchedEffect(server.id) { refresh() }

    SubPage(
        title = "官方 CLI 登录",
        subtitle = server.name,
        nav = nav,
        actions = {
            IconButton(onClick = { refresh() }, enabled = !loading) {
                Icon(Icons.Filled.Refresh, "刷新")
            }
        },
    ) {
        Note("直接完成远端主机或本机上的官方 Coding CLI 登录（Codex / Claude Code / Gemini / Grok）。凭证仅保存在官方原生目录或主机加密密钥库，与前置 Bot 反代完全独立。")
        error?.let { Note(it, MaterialTheme.colorScheme.error) }
        if (loading && items.isEmpty()) {
            Busy()
        } else {
            SectionLabel("官方 Coding CLI")
            items.forEach { item ->
                val stateLabel = when {
                    item.loggedIn -> "已登录" + (item.accountHint?.takeIf { it.isNotEmpty() }?.let { " · $it" } ?: "")
                    item.installed -> "未登录 · 点击进行设备码 / OAuth 或密钥登录"
                    else -> "主机未安装 · ${item.installHint}"
                }
                ListRow(
                    title = item.name,
                    supporting = stateLabel,
                    leading = {
                        Icon(
                            Icons.Filled.Terminal,
                            null,
                            tint = when {
                                item.loggedIn -> Frog.extra.ok
                                item.installed -> MaterialTheme.colorScheme.primary
                                else -> MaterialTheme.colorScheme.onSurfaceVariant
                            },
                        )
                    },
                    chevron = true,
                    onClick = { selected = item },
                )
            }
        }
    }

    selected?.let { item ->
        CliAuthDialog(
            server = server,
            item = item,
            onDismiss = { selected = null },
            onChanged = {
                selected = null
                refresh()
            },
        )
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun CliAuthDialog(
    server: ServerEntry,
    item: CliAuthStatusItem,
    onDismiss: () -> Unit,
    onChanged: () -> Unit,
) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val clipboard = LocalClipboardManager.current
    var session by remember { mutableStateOf(item.activeSession) }
    var busy by remember { mutableStateOf(false) }
    var pasteInput by remember { mutableStateOf("") }
    var credKind by remember { mutableStateOf(if (item.supportsOAuthToken) "oauth_token" else "api_key") }
    var credValue by remember { mutableStateOf("") }
    var notice by remember { mutableStateOf<Pair<String, Boolean>?>(null) }

    val sessionActive = session?.status == "running" || session?.status == "waiting_user"

    LaunchedEffect(session?.id, sessionActive) {
        val sid = session?.id ?: return@LaunchedEffect
        if (!sessionActive) return@LaunchedEffect
        while (isActive) {
            delay(1600)
            val res = runCatching { CliAuthSessionModel.from(server.client.getObject("/api/v2/cli/auth/sessions/$sid")) }.getOrNull() ?: break
            session = res
            if (res.status == "succeeded") {
                notice = "登录成功！凭证已写入主机。" to false
                onChanged()
                break
            } else if (res.status == "failed" || res.status == "cancelled") {
                notice = (res.error ?: "授权未完成（${res.status}）") to true
                break
            }
        }
    }

    DisposableEffect(Unit) { onDispose { } }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("${item.name} 官方登录") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                Text(item.loginHint, fontSize = 12.5.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                if (!item.installed) {
                    Surface(shape = RoundedCornerShape(8.dp), color = MaterialTheme.colorScheme.surfaceContainer) {
                        Row(Modifier.fillMaxWidth().padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
                            Text(item.installHint, fontFamily = FontFamily.Monospace, fontSize = 11.5.sp, modifier = Modifier.weight(1f))
                            IconButton(onClick = { clipboard.setText(AnnotatedString(item.installHint)) }, modifier = Modifier.size(28.dp)) {
                                Icon(Icons.Filled.ContentCopy, "复制", Modifier.size(16.dp))
                            }
                        }
                    }
                }
                notice?.let { (msg, isErr) ->
                    Text(msg, fontSize = 12.5.sp, color = if (isErr) MaterialTheme.colorScheme.error else Frog.extra.ok)
                }

                val sess = session
                if (sess != null && sessionActive) {
                    Surface(
                        shape = Shapes12,
                        color = MaterialTheme.colorScheme.surfaceContainerLow,
                        border = BorderStroke(0.8.dp, MaterialTheme.colorScheme.primary.copy(alpha = 0.4f)),
                    ) {
                        Column(Modifier.fillMaxWidth().padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp)
                                Text(if (sess.mode == "device") "等待在浏览器输入验证码…" else "等待浏览器授权并回传…", fontSize = 13.sp, fontWeight = FontWeight.Medium)
                            }
                            sess.userCode?.takeIf { it.isNotEmpty() }?.let { code ->
                                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                                    Text(code, fontFamily = FontFamily.Monospace, fontSize = 20.sp, fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.primary, modifier = Modifier.weight(1f))
                                    OutlinedButton(onClick = { clipboard.setText(AnnotatedString(code)); notice = "已复制验证码 $code" to false }) {
                                        Text("复制", fontSize = 12.sp)
                                    }
                                }
                            }
                            sess.authUrl?.takeIf { it.isNotEmpty() }?.let { url ->
                                Button(
                                    modifier = Modifier.fillMaxWidth(),
                                    onClick = { runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) } },
                                ) {
                                    Icon(Icons.Filled.OpenInBrowser, null, Modifier.size(16.dp))
                                    Spacer(Modifier.width(6.dp))
                                    Text("打开官方授权页")
                                }
                            }
                            if (sess.needsInput) {
                                OutlinedTextField(
                                    value = pasteInput,
                                    onValueChange = { pasteInput = it },
                                    label = { Text("粘贴授权码或回调 URL") },
                                    singleLine = false,
                                    maxLines = 3,
                                    modifier = Modifier.fillMaxWidth(),
                                )
                                Button(
                                    enabled = !busy && pasteInput.isNotBlank(),
                                    onClick = {
                                        busy = true
                                        scope.launch {
                                            runCatching { server.client.post("/api/v2/cli/auth/sessions/${sess.id}/input", JSONObject().put("value", pasteInput.trim())) }
                                                .onSuccess { pasteInput = ""; notice = "已回传，正在完成授权…" to false }
                                                .onFailure { notice = ServerClient.describe(it) to true }
                                            busy = false
                                        }
                                    },
                                ) { Text("提交回传") }
                            }
                            TextButton(onClick = {
                                scope.launch {
                                    runCatching { server.client.post("/api/v2/cli/auth/sessions/${sess.id}/cancel") }
                                    session = null
                                }
                            }) { Text("取消授权", color = MaterialTheme.colorScheme.error) }
                        }
                    }
                } else if (item.installed && (item.supportsDeviceAuth || item.supportsOAuthBridge)) {
                    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                        if (item.supportsDeviceAuth) {
                            Button(
                                modifier = Modifier.fillMaxWidth(),
                                enabled = !busy,
                                onClick = {
                                    busy = true
                                    scope.launch {
                                        runCatching { CliAuthSessionModel.from(server.client.post("/api/v2/cli/auth/${item.id}/start", JSONObject().put("mode", "device"))) }
                                            .onSuccess { session = it }
                                            .onFailure { notice = ServerClient.describe(it) to true }
                                        busy = false
                                    }
                                },
                            ) { Text("启动官方设备码登录（推荐）") }
                        }
                        if (item.supportsOAuthBridge) {
                            OutlinedButton(
                                modifier = Modifier.fillMaxWidth(),
                                enabled = !busy,
                                onClick = {
                                    busy = true
                                    scope.launch {
                                        runCatching { CliAuthSessionModel.from(server.client.post("/api/v2/cli/auth/${item.id}/start", JSONObject().put("mode", "oauth_bridge"))) }
                                            .onSuccess { session = it }
                                            .onFailure { notice = ServerClient.describe(it) to true }
                                        busy = false
                                    }
                                },
                            ) { Text("浏览器 OAuth 桥接登录") }
                        }
                    }
                }

                HorizontalDivider()
                Text("直接写入 Token / API Key（加密保存于主机）", fontSize = 12.sp, fontWeight = FontWeight.SemiBold)
                if (item.supportsOAuthToken) {
                    SingleChoiceSegmentedButtonRow(Modifier.fillMaxWidth()) {
                        SegmentedButton(selected = credKind == "oauth_token", onClick = { credKind = "oauth_token" }, shape = SegmentedButtonDefaults.itemShape(0, 2)) { Text("Token", fontSize = 12.sp) }
                        SegmentedButton(selected = credKind == "api_key", onClick = { credKind = "api_key" }, shape = SegmentedButtonDefaults.itemShape(1, 2)) { Text("API Key", fontSize = 12.sp) }
                    }
                }
                OutlinedTextField(
                    value = credValue,
                    onValueChange = { credValue = it },
                    label = { Text(if (credKind == "oauth_token") (item.oauthTokenEnv ?: "OAuth Token") else item.apiKeyEnv) },
                    visualTransformation = PasswordVisualTransformation(),
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                    if (item.loggedIn) {
                        TextButton(
                            enabled = !busy,
                            onClick = {
                                busy = true
                                scope.launch {
                                    runCatching { server.client.post("/api/v2/cli/auth/${item.id}/logout") }
                                        .onSuccess { onChanged() }
                                        .onFailure { notice = ServerClient.describe(it) to true }
                                    busy = false
                                }
                            },
                        ) { Text("退出登录", color = MaterialTheme.colorScheme.error) }
                    } else Spacer(Modifier.width(1.dp))

                    Button(
                        enabled = !busy && credValue.isNotBlank(),
                        onClick = {
                            busy = true
                            scope.launch {
                                runCatching {
                                    server.client.post(
                                        "/api/v2/cli/auth/${item.id}/credential",
                                        JSONObject().put("kind", credKind).put("value", credValue.trim()),
                                    )
                                }
                                    .onSuccess { credValue = ""; onChanged() }
                                    .onFailure { notice = ServerClient.describe(it) to true }
                                busy = false
                            }
                        },
                    ) { Text("加密保存") }
                }
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text("关闭") } },
    )
}

// MARK: - 前置 Bot 账号反代网关（含强制免责声明，与自定义 API 并存）

@Composable
fun ProxyGatewayScreen(server: ServerEntry, nav: Nav) {
    val scope = rememberCoroutineScope()
    var rawStatus by remember { mutableStateOf<JSONObject?>(null) }
    var loading by remember { mutableStateOf(true) }
    var busy by remember { mutableStateOf(false) }
    var enabled by remember { mutableStateOf(false) }
    var accepted by remember { mutableStateOf(false) }
    var engine by remember { mutableStateOf("cliproxyapi") }
    var baseUrl by remember { mutableStateOf("http://127.0.0.1:8317/v1") }
    var channelName by remember { mutableStateOf("账号反代 · CLIProxyAPI") }
    var authRequired by remember { mutableStateOf(false) }
    var apiKey by remember { mutableStateOf("") }
    var binaryPath by remember { mutableStateOf("") }
    var startArgs by remember { mutableStateOf("") }
    var notice by remember { mutableStateOf<Pair<String, Boolean>?>(null) }

    fun applyStatus(st: JSONObject, initFields: Boolean) {
        rawStatus = st
        val cfg = st.obj("config")
        if (cfg != null && initFields) {
            enabled = cfg.bool("enabled")
            accepted = cfg.bool("disclaimer_accepted")
            engine = cfg.str("engine").ifEmpty { "cliproxyapi" }
            baseUrl = cfg.str("base_url").ifEmpty { "http://127.0.0.1:8317/v1" }
            channelName = cfg.str("channel_name").ifEmpty { "账号反代 · CLIProxyAPI" }
            authRequired = cfg.bool("auth_required")
            binaryPath = cfg.str("binary_path")
            startArgs = cfg.str("start_args")
        }
    }

    fun load(initFields: Boolean) {
        loading = true
        scope.launch {
            runCatching { server.client.getObject("/api/v2/proxy/status") }
                .onSuccess { applyStatus(it, initFields) }
                .onFailure { notice = ServerClient.describe(it) to true }
            loading = false
        }
    }

    fun save(enableTarget: Boolean) {
        if (enableTarget && !accepted) {
            notice = "启用前必须阅读并同意免责声明" to true
            return
        }
        busy = true
        notice = null
        scope.launch {
            val payload = JSONObject()
                .put("enabled", enableTarget)
                .put("disclaimer_accepted", accepted)
                .put("engine", engine)
                .put("base_url", baseUrl.trim())
                .put("channel_name", channelName.trim())
                .put("auth_required", authRequired)
                .put("auto_start", false)
                .put("binary_path", binaryPath.trim())
                .put("start_args", startArgs.trim())
                .put("sync_channel", true)
            if (authRequired) {
                if (apiKey.isNotEmpty()) payload.put("api_key", apiKey)
            } else {
                payload.put("api_key", "")
            }
            runCatching { server.client.post("/api/v2/proxy/config", payload) }
                .onSuccess { st ->
                    applyStatus(st, initFields = false)
                    enabled = st.obj("config")?.bool("enabled") ?: enableTarget
                    apiKey = ""
                    val modelsCount = st.arr("live_models").length()
                    notice = (
                        if (enabled) {
                            if (st.bool("probe_ok")) "已启用并同步为前置 Bot 渠道（$modelsCount 个模型）"
                            else "已保存配置，请先在主机启动反代后刷新"
                        } else {
                            "已停用反代渠道，自定义 API 不受影响。"
                        }
                    ) to false
                }
                .onFailure { notice = ServerClient.describe(it) to true }
            busy = false
        }
    }

    LaunchedEffect(server.id) { load(initFields = true) }

    SubPage(
        title = "账号反代网关",
        subtitle = server.name,
        nav = nav,
        actions = {
            IconButton(onClick = { load(initFields = false) }, enabled = !busy) {
                Icon(Icons.Filled.Refresh, "刷新")
            }
        },
    ) {
        val st = rawStatus
        if (loading && st == null) {
            Busy()
        } else if (st != null) {
            // 强制免责声明卡片
            Surface(
                modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 10.dp),
                shape = Shapes12,
                color = Frog.extra.warn.copy(alpha = 0.08f),
                border = BorderStroke(0.8.dp, Frog.extra.warn.copy(alpha = 0.35f)),
            ) {
                Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        Icon(Icons.Filled.Warning, null, Modifier.size(18.dp), tint = Frog.extra.warn)
                        Text("风险提示与免责声明（必读）", fontWeight = FontWeight.SemiBold, color = Frog.extra.warn)
                    }
                    Text(st.str("disclaimer_text"), fontSize = 12.5.sp, lineHeight = 18.sp, color = MaterialTheme.colorScheme.onSurface)
                    Row(Modifier.fillMaxWidth().clickable(enabled = !busy) { accepted = !accepted }, verticalAlignment = Alignment.CenterVertically) {
                        Text("我已阅读并同意免责声明，自愿承担风险", fontSize = 13.sp, fontWeight = FontWeight.Medium, modifier = Modifier.weight(1f))
                        Switch(checked = accepted, onCheckedChange = { accepted = it }, enabled = !busy)
                    }
                }
            }

            Note("账号反代与「自定义 API 渠道」完全并存、互不覆盖，随时可在 Bot 模型设置中自由切换。")
            notice?.let { Note(it.first, if (it.second) MaterialTheme.colorScheme.error else Frog.extra.ok) }

            SectionLabel("开源反代引擎")
            st.arr("detected_engines").objects().forEach { eng ->
                val eid = eng.str("id")
                val ename = eng.str("name")
                val alive = eng.bool("probe_alive")
                val inst = eng.bool("installed")
                val desc = listOfNotNull(
                    if (alive) "端口运行中" else if (inst) "已安装" else null,
                    eng.str("description").ifEmpty { null },
                ).joinToString(" · ")
                ListRow(
                    title = ename,
                    supporting = desc.ifEmpty { eng.str("default_url") },
                    leading = {
                        Icon(
                            if (engine == eid) Icons.Filled.CheckCircle else Icons.Filled.RadioButtonUnchecked,
                            null,
                            tint = if (engine == eid) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    },
                    onClick = {
                        engine = eid
                        baseUrl = eng.str("default_url")
                        channelName = "账号反代 · $ename"
                        eng.str("binary_path").takeIf { it.isNotEmpty() }?.let { binaryPath = it }
                        startArgs = eng.str("default_args")
                    },
                )
            }

            SectionLabel("反代参数与状态")
            Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                OutlinedTextField(value = channelName, onValueChange = { channelName = it }, label = { Text("同步渠道名称") }, singleLine = true, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(value = baseUrl, onValueChange = { baseUrl = it }, label = { Text("Base URL") }, singleLine = true, modifier = Modifier.fillMaxWidth())
                Row(
                    Modifier.fillMaxWidth().clickable { authRequired = !authRequired },
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text("需要访问密钥", style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f))
                    Switch(checked = authRequired, onCheckedChange = { authRequired = it })
                }
                if (authRequired) {
                    val hasKey = st.obj("config")?.bool("has_api_key") == true
                    OutlinedTextField(
                        value = apiKey,
                        onValueChange = { apiKey = it },
                        label = { Text(if (hasKey) "已加密保存，留空保留" else "反代 API Key") },
                        visualTransformation = PasswordVisualTransformation(),
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }

                val probeOk = st.bool("probe_ok")
                val liveCount = st.arr("live_models").length()
                Text(
                    "连通状态：" + when {
                        probeOk -> "已连通 · $liveCount 个可用模型"
                        enabled -> st.str("probe_error").ifEmpty { "未连通" }
                        else -> "未启用"
                    },
                    fontSize = 13.sp,
                    color = if (probeOk) Frog.extra.ok else MaterialTheme.colorScheme.onSurfaceVariant,
                )

                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    Button(
                        enabled = !busy && accepted,
                        onClick = { save(true) },
                        modifier = Modifier.weight(1f),
                    ) { Text(if (enabled) "保存并同步渠道" else "同意并启用") }

                    val managedRunning = st.bool("managed_running")
                    OutlinedButton(
                        enabled = !busy && (managedRunning || accepted),
                        onClick = {
                            busy = true
                            scope.launch {
                                val req = if (managedRunning) {
                                    runCatching { server.client.post("/api/v2/proxy/stop") }
                                } else {
                                    runCatching { server.client.post("/api/v2/proxy/start", JSONObject().put("disclaimer_accepted", accepted)) }
                                }
                                req.onSuccess { applyStatus(it, false); notice = (if (managedRunning) "已停止托管反代" else "已启动托管反代") to false }
                                    .onFailure { notice = ServerClient.describe(it) to true }
                                busy = false
                            }
                        },
                    ) {
                        Icon(if (managedRunning) Icons.Filled.Stop else Icons.Filled.PlayArrow, null, Modifier.size(16.dp))
                        Spacer(Modifier.width(4.dp))
                        Text(if (managedRunning) "停止托管" else "托管启动")
                    }
                }

                if (enabled) {
                    OutlinedButton(
                        enabled = !busy,
                        colors = ButtonDefaults.outlinedButtonColors(contentColor = MaterialTheme.colorScheme.error),
                        onClick = { save(false) },
                        modifier = Modifier.fillMaxWidth(),
                    ) { Text("停用反代（保留自定义 API）") }
                }

                val logs = st.arr("last_logs").strings()
                if (logs.isNotEmpty()) {
                    Text(
                        logs.takeLast(8).joinToString("\n"),
                        fontFamily = FontFamily.Monospace,
                        fontSize = 11.sp,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceContainer, RoundedCornerShape(8.dp)).padding(8.dp),
                    )
                }
            }
        }
    }
}
