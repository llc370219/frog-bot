package io.github.llc370219.frogbot.claude.ui

import android.graphics.BitmapFactory
import android.util.Base64
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectDragGestures
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Tab
import androidx.compose.material3.TabRow
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.llc370219.frogbot.claude.core.GatewayClient

/**
 * Frog Bot V2 移动端单点总控面板：
 * 1) 审查审批卡片（批准一次 / 批准并加白名单 / 拒绝）
 * 2) 远程过验证触控画布（CDP 视频帧等比缩放 + 滑块平滑拖曳 + 验证码输入框）
 * 3) Coding CLI 实时日志抽屉 + 一键 Cancel 叫停
 * 4) FrogLink Mesh 跨服节点漫游
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun V2ControlSheet(gw: GatewayClient, onDismiss: () -> Unit) {
    var tab by remember { mutableIntStateOf(0) }
    var selectedTask by remember { mutableStateOf<String?>(null) }
    var captchaInput by remember { mutableStateOf("") }

    LaunchedEffect(Unit) { gw.refreshStatus() }

    ModalBottomSheet(onDismissRequest = onDismiss) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 16.dp)
                .padding(bottom = 24.dp),
        ) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.SpaceBetween,
            ) {
                Text(
                    "Frog V2 总控中心${if (gw.nodeId.isNotEmpty()) " · ${gw.nodeId}" else ""}",
                    style = MaterialTheme.typography.titleMedium,
                    fontWeight = FontWeight.SemiBold,
                )
                TextButton(onClick = { gw.refreshStatus() }) { Text("刷新") }
            }

            TabRow(selectedTabIndex = tab) {
                Tab(selected = tab == 0, onClick = { tab = 0 }, text = { Text("审批(${gw.pendingApprovals.size})") })
                Tab(selected = tab == 1, onClick = { tab = 1 }, text = { Text("过验证(${gw.verifySessions.size})") })
                Tab(selected = tab == 2, onClick = { tab = 2 }, text = { Text("CLI(${gw.cliTasks.size})") })
                Tab(selected = tab == 3, onClick = { tab = 3 }, text = { Text("Mesh(${gw.meshPeers.size})") })
            }

            Spacer(Modifier.height(12.dp))

            when (tab) {
                0 -> V2ApprovalsTab(gw)
                1 -> V2VerifyCanvasTab(gw, captchaInput) { captchaInput = it }
                2 -> V2CLILogTab(gw, selectedTask) { tid ->
                    selectedTask = tid
                    gw.subscribeCLILog(tid)
                }
                else -> V2MeshTab(gw)
            }
        }
    }
}

@Composable
private fun V2ApprovalsTab(gw: GatewayClient) {
    if (gw.pendingApprovals.isEmpty()) {
        Text("暂无待审批安全项", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(vertical = 24.dp))
        return
    }
    LazyColumn(verticalArrangement = Arrangement.spacedBy(10.dp), modifier = Modifier.heightIn(max = 380.dp)) {
        items(gw.pendingApprovals, key = { it.id }) { card ->
            Card(colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainerLow)) {
                Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(card.action, fontWeight = FontWeight.SemiBold, fontSize = 14.sp)
                        if (card.tainted) {
                            Spacer(Modifier.width(8.dp))
                            Text(
                                "外部污点",
                                fontSize = 11.sp,
                                color = Color(0xFFD97706),
                                modifier = Modifier
                                    .background(Color(0x22D97706), RoundedCornerShape(6.dp))
                                    .padding(horizontal = 6.dp, vertical = 2.dp),
                            )
                        }
                    }
                    if (card.targetDomain.isNotEmpty()) {
                        Text("目标域名: ${card.targetDomain}", fontFamily = FontFamily.Monospace, fontSize = 12.sp)
                    }
                    Text(card.summary, fontSize = 13.sp)
                    Text("触发源: ${card.triggerSource} · ${card.reason}", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.fillMaxWidth()) {
                        Button(onClick = { gw.decideApproval(card.id, "allow_once") }) { Text("批准一次") }
                        if (card.canWhitelist && card.whitelistDomain.isNotEmpty()) {
                            FilledTonalButton(onClick = { gw.decideApproval(card.id, "allow_whitelist", card.whitelistDomain) }) {
                                Text("加白名单")
                            }
                        }
                        Spacer(Modifier.weight(1f))
                        OutlinedButton(onClick = { gw.decideApproval(card.id, "deny") }) {
                            Text("拒绝", color = MaterialTheme.colorScheme.error)
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun V2VerifyCanvasTab(gw: GatewayClient, captchaInput: String, onCaptchaChange: (String) -> Unit) {
    val activeId = gw.activeVerifyId
    if (activeId != null) {
        var canvasSize by remember { mutableStateOf(IntSize(1, 1)) }
        var dragStart by remember { mutableStateOf(Offset.Zero) }
        var dragEnd by remember { mutableStateOf(Offset.Zero) }
        val bitmap = remember(gw.latestFrameBase64) {
            gw.latestFrameBase64?.let { b64 ->
                runCatching {
                    val bytes = Base64.decode(b64, Base64.DEFAULT)
                    BitmapFactory.decodeByteArray(bytes, 0, bytes.size)?.asImageBitmap()
                }.getOrNull()
            }
        }

        Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .aspectRatio(16f / 10f)
                    .clip(RoundedCornerShape(10.dp))
                    .background(Color.Black)
                    .onSizeChanged { canvasSize = it }
                    .pointerInput(activeId) {
                        detectTapGestures { offset ->
                            val w = canvasSize.width.coerceAtLeast(1).toDouble()
                            val h = canvasSize.height.coerceAtLeast(1).toDouble()
                            gw.sendVerifyClick(activeId, offset.x / w, offset.y / h)
                        }
                    }
                    .pointerInput(activeId) {
                        detectDragGestures(
                            onDragStart = { start -> dragStart = start; dragEnd = start },
                            onDrag = { change, _ -> dragEnd = change.position },
                            onDragEnd = {
                                val w = canvasSize.width.coerceAtLeast(1).toDouble()
                                val h = canvasSize.height.coerceAtLeast(1).toDouble()
                                gw.sendVerifyDrag(
                                    activeId,
                                    dragStart.x / w,
                                    dragStart.y / h,
                                    dragEnd.x / w,
                                    dragEnd.y / h,
                                )
                            },
                        )
                    },
                contentAlignment = Alignment.Center,
            ) {
                if (bitmap != null) {
                    Image(bitmap = bitmap, contentDescription = "远程验证画面", contentScale = ContentScale.Fit, modifier = Modifier.fillMaxWidth())
                } else {
                    CircularProgressIndicator(color = Color.White)
                }
            }

            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    value = captchaInput,
                    onValueChange = onCaptchaChange,
                    placeholder = { Text("输入图形/短信验证码") },
                    singleLine = true,
                    modifier = Modifier.weight(1f),
                )
                FilledTonalButton(onClick = {
                    if (captchaInput.isNotEmpty()) {
                        gw.sendVerifyText(activeId, captchaInput)
                        onCaptchaChange("")
                    }
                }) { Text("输入") }
            }

            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(onClick = { gw.completeVerify(activeId) }) { Text("验证完成") }
                OutlinedButton(onClick = { gw.cancelVerify(activeId) }) { Text("取消") }
            }
        }
        return
    }

    if (gw.verifySessions.isEmpty()) {
        Text("暂无待过验证任务（浏览器闲置 0 内存）", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(vertical = 24.dp))
        return
    }
    LazyColumn(verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.heightIn(max = 360.dp)) {
        items(gw.verifySessions, key = { it.id }) { sess ->
            Card {
                Row(
                    Modifier.fillMaxWidth().padding(12.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.SpaceBetween,
                ) {
                    Column(Modifier.weight(1f)) {
                        Text(sess.reason, fontWeight = FontWeight.Medium)
                        Text("${sess.botId} · ${sess.url}", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    Button(onClick = { gw.startVerify(sess.id) }) { Text("开始验证") }
                }
            }
        }
    }
}

@Composable
private fun V2CLILogTab(gw: GatewayClient, selectedTask: String?, onSelect: (String) -> Unit) {
    if (gw.cliTasks.isEmpty()) {
        Text("暂无 Coding CLI 任务", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(vertical = 24.dp))
        return
    }
    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        LazyColumn(verticalArrangement = Arrangement.spacedBy(6.dp), modifier = Modifier.heightIn(max = 150.dp)) {
            items(gw.cliTasks, key = { it.id }) { task ->
                Row(
                    Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(8.dp))
                        .background(
                            if (selectedTask == task.id) MaterialTheme.colorScheme.secondaryContainer
                            else MaterialTheme.colorScheme.surfaceContainerLow,
                        )
                        .clickable { onSelect(task.id) }
                        .padding(10.dp),
                    horizontalArrangement = Arrangement.SpaceBetween,
                ) {
                    Text("${task.cli}/${task.mode} · ${task.promptBrief.ifEmpty { task.id }}", fontSize = 13.sp, modifier = Modifier.weight(1f))
                    Text(task.status, fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        }

        if (selectedTask != null) {
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
                Text(selectedTask, fontFamily = FontFamily.Monospace, fontSize = 12.sp)
                Button(
                    onClick = { gw.cancelCLITask(selectedTask) },
                    colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error),
                ) { Text("紧急叫停 Cancel") }
            }
            Box(
                Modifier
                    .fillMaxWidth()
                    .height(180.dp)
                    .clip(RoundedCornerShape(8.dp))
                    .background(Color(0xFF111317))
                    .verticalScroll(rememberScrollState())
                    .padding(10.dp),
            ) {
                Text(
                    gw.cliLogs[selectedTask] ?: "正在订阅实时日志流…",
                    color = Color(0xFFDDE3EA),
                    fontFamily = FontFamily.Monospace,
                    fontSize = 11.sp,
                )
            }
        }
    }
}

@Composable
private fun V2MeshTab(gw: GatewayClient) {
    if (gw.meshPeers.isEmpty()) {
        Text("暂无已配对 FrogLink 节点", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(vertical = 24.dp))
        return
    }
    LazyColumn(verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.heightIn(max = 320.dp)) {
        items(gw.meshPeers, key = { it.nodeId }) { peer ->
            Card {
                Row(Modifier.fillMaxWidth().padding(12.dp), horizontalArrangement = Arrangement.SpaceBetween) {
                    Column {
                        Text("${peer.nodeId} ${peer.networkTag}", fontWeight = FontWeight.Medium)
                        Text("Bots: ${peer.bots.joinToString(", ")}", fontSize = 12.sp, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                    Text(if (peer.online) "在线" else "离线", color = if (peer.online) Color(0xFF16A34A) else Color.Gray)
                }
            }
        }
    }
}
