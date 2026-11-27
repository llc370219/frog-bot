package io.github.llc370219.frogbot.claude.core

import android.os.Build
import android.util.Base64
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.math.BigInteger
import java.net.URI
import java.net.URLDecoder
import java.security.SecureRandom
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.TimeUnit
import javax.crypto.Cipher
import javax.crypto.Mac
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/** 解析自 `froge2ee://` 配对码的端到端加密连接参数。 */
data class E2EEConfig(
    val peerId: String,      // 16 hex chars (8 bytes), lowercased
    val psk: ByteArray,      // 32 bytes
    val hostId: String,      // lowercased host ID
    val serverPub: ByteArray,// 32 bytes X25519 public key
    val relayUrl: String,    // optional WSS blind relay URL
    val relayToken: String,  // optional relay access token
    val lanAddr: String,     // optional LAN direct ip:port
    val serverName: String,  // optional friendly name
) {
    fun buildRelayWsUrl(): String? {
        var raw = relayUrl.trim()
        if (raw.isEmpty()) return null
        val httpRaw = when {
            raw.startsWith("wss://", ignoreCase = true) -> "https://" + raw.substring(6)
            raw.startsWith("ws://", ignoreCase = true) -> "http://" + raw.substring(5)
            raw.contains("://") -> raw
            else -> "https://$raw"
        }
        val parsed = httpRaw.toHttpUrlOrNull() ?: return null
        val b = parsed.newBuilder()
        if (parsed.encodedPath.isEmpty() || parsed.encodedPath == "/") {
            b.encodedPath("/ws")
        }
        b.removeAllQueryParameters("role")
            .removeAllQueryParameters("room")
            .removeAllQueryParameters("peer")
            .removeAllQueryParameters("token")
            .addQueryParameter("role", "client")
            .addQueryParameter("room", hostId)
            .addQueryParameter("peer", peerId)
        if (relayToken.isNotEmpty()) {
            b.addQueryParameter("token", relayToken)
        }
        val built = b.build().toString()
        return when {
            built.startsWith("https://") -> "wss://" + built.substring(8)
            built.startsWith("http://") -> "ws://" + built.substring(7)
            else -> built
        }
    }

    fun buildLanWsUrl(): String? {
        val addr = lanAddr.trim()
        if (addr.isEmpty()) return null
        return "ws://$addr/ws"
    }

    companion object {
        fun parse(raw: String): E2EEConfig {
            val trimmed = raw.trim()
            val idx = trimmed.indexOf("froge2ee://", ignoreCase = true)
            if (idx < 0) throw ApiException("未识别到 froge2ee:// 连接码")
            val candidate = trimmed.substring(idx).takeWhile { !it.isWhitespace() }
            val uri = try {
                URI(candidate)
            } catch (_: Exception) {
                throw ApiException("无效的 E2EE 连接码格式")
            }
            val userInfo = uri.rawUserInfo ?: throw ApiException("E2EE 连接码缺少设备密钥")
            val colon = userInfo.indexOf(':')
            if (colon <= 0 || colon >= userInfo.length - 1) {
                throw ApiException("E2EE 连接码缺少设备密钥")
            }
            val peerId = decodeQueryComponent(userInfo.substring(0, colon)).trim().lowercase()
            val peerBytes = decodeHex(peerId)
            if (peerBytes == null || peerBytes.size != 8) {
                throw ApiException("E2EE PeerID 无效")
            }
            val pskRaw = decodeQueryComponent(userInfo.substring(colon + 1)).trim()
            val psk = decodeHex(pskRaw)?.takeIf { it.size == 32 }
                ?: decodeBase64Flexible(pskRaw)?.takeIf { it.size == 32 }
                ?: throw ApiException("E2EE PSK 密钥无效")

            val hostId = (uri.host ?: "").trim().lowercase()
            if (hostId.length < 8) throw ApiException("E2EE HostID 无效")

            val query = parseQuery(uri.rawQuery)
            val pubStr = (query["pub"] ?: "").trim()
            val pubBytes = decodeBase64Flexible(pubStr)
            if (pubBytes == null || pubBytes.size != 32) {
                throw ApiException("E2EE 主机公钥无效")
            }
            val relayUrl = (query["relay"] ?: "").trim()
            val relayToken = (query["rt"] ?: "").trim()
            val lanAddr = (query["lan"] ?: "").trim()
            if (relayUrl.isEmpty() && lanAddr.isEmpty()) {
                throw ApiException("E2EE 连接码缺少中继或局域网地址")
            }
            val name = uri.rawFragment?.let { decodeQueryComponent(it).trim() } ?: ""
            return E2EEConfig(
                peerId = peerId,
                psk = psk,
                hostId = hostId,
                serverPub = pubBytes,
                relayUrl = relayUrl,
                relayToken = relayToken,
                lanAddr = lanAddr,
                serverName = name,
            )
        }

        private fun parseQuery(rawQuery: String?): Map<String, String> {
            if (rawQuery.isNullOrEmpty()) return emptyMap()
            val out = LinkedHashMap<String, String>()
            for (part in rawQuery.split('&')) {
                if (part.isEmpty()) continue
                val eq = part.indexOf('=')
                if (eq < 0) {
                    out[decodeQueryComponent(part)] = ""
                } else {
                    val k = decodeQueryComponent(part.substring(0, eq))
                    val v = decodeQueryComponent(part.substring(eq + 1))
                    out[k] = v
                }
            }
            return out
        }

        private fun decodeQueryComponent(s: String): String = try {
            URLDecoder.decode(s, "UTF-8")
        } catch (_: Exception) {
            s
        }
    }
}

data class E2EEResponse(
    val status: Int,
    val headers: Map<String, String>,
    val body: ByteArray,
)

/**
 * 零外部依赖的纯 Kotlin RFC 7748 X25519 + HKDF-SHA256 + AES-256-GCM + OkHttp WebSocket 端到端加密隧道：
 * - 兼容 Android 8.0+ (API 26+) 全版本，无需引入任何 JNI/SO 动态库；
 * - 智能路由：优先 600ms 尝试同局域网直连，超时自动回退至标准 443 HTTPS/WSS 盲中继；
 * - 与 Go `internal/remote/e2ee_crypto.go` 协议逐字节兼容。
 */
class E2EETunnel(
    val config: E2EEConfig,
    private val deviceName: String = defaultDeviceName(),
) {
    private class ActiveConn(
        val id: String,
        val ws: WebSocket,
        val sessionKey: ByteArray,
        val ephPubB64: String,
        val isLan: Boolean,
        var pingJob: Job? = null,
    )

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val connectMutex = Mutex()
    private val streams = ConcurrentHashMap<String, Channel<JSONObject>>()
    @Volatile private var activeConn: ActiveConn? = null
    @Volatile private var closed = false

    companion object {
        private val rng = SecureRandom()
        private val wsClient: OkHttpClient = ServerClient.http.newBuilder()
            .connectTimeout(10, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .writeTimeout(30, TimeUnit.SECONDS)
            .pingInterval(0, TimeUnit.MILLISECONDS)
            .build()

        fun defaultDeviceName(): String {
            val m = (Build.MANUFACTURER ?: "").trim()
            val d = (Build.MODEL ?: "").trim()
            return if (d.startsWith(m, ignoreCase = true)) d.ifEmpty { "Android" } else "$m $d".trim().ifEmpty { "Android" }
        }
    }

    fun close() {
        closed = true
        val c = activeConn
        activeConn = null
        c?.pingJob?.cancel()
        c?.ws?.close(1000, "closed")
        failAllStreams(CancellationException("tunnel closed"))
        scope.cancel()
    }

    suspend fun request(
        method: String,
        path: String,
        headers: Map<String, String> = emptyMap(),
        body: ByteArray? = null,
        timeoutMs: Long = 30_000L,
    ): E2EEResponse = withContext(Dispatchers.IO) {
        if (closed) throw CancellationException("tunnel closed")
        try {
            requestOnce(method, path, headers, body, timeoutMs)
        } catch (e: ApiException) {
            if (e.status == 0 && !(e.message ?: "").contains("主机不在线")) {
                resetCurrentConnection()
                requestOnce(method, path, headers, body, timeoutMs)
            } else {
                throw e
            }
        }
    }

    private suspend fun requestOnce(
        method: String,
        path: String,
        headers: Map<String, String>,
        body: ByteArray?,
        timeoutMs: Long,
    ): E2EEResponse {
        val conn = ensureConnected()
        val sid = randomStreamId()
        val ch = Channel<JSONObject>(Channel.UNLIMITED)
        streams[sid] = ch

        val hObj = JSONObject()
        for ((k, v) in headers) hObj.put(k, v)
        if (!hObj.has("X-Frog-Device-Name") && deviceName.isNotEmpty()) {
            hObj.put("X-Frog-Device-Name", deviceName)
        }
        val reqFrame = JSONObject()
            .put("t", "req")
            .put("s", sid)
            .put("m", method)
            .put("p", path)
            .put("h", hObj)
        if (body != null && body.isNotEmpty()) {
            reqFrame.put("b", Base64.encodeToString(body, Base64.NO_WRAP))
        }

        if (!sendEncrypted(reqFrame, conn)) {
            streams.remove(sid)
            ch.close()
            handleDisconnect(conn.id, ApiException("加密隧道发送失败"))
            throw ApiException("加密隧道发送失败")
        }

        return try {
            withTimeout(maxOf(timeoutMs, 5_000L)) {
                var status = 200
                val respHeaders = LinkedHashMap<String, String>()
                val out = ByteArrayOutputStream()
                for (fr in ch) {
                    val errMsg = fr.optString("err", "")
                    if (errMsg.isNotEmpty()) {
                        throw ApiException(errMsg, fr.optInt("st", 502))
                    }
                    val st = fr.optInt("st", 0)
                    if (st > 0) status = st
                    fr.optJSONObject("h")?.let { ho ->
                        val keys = ho.keys()
                        while (keys.hasNext()) {
                            val k = keys.next()
                            respHeaders[k] = ho.optString(k, "")
                        }
                    }
                    val b64 = fr.optString("b", "")
                    if (b64.isNotEmpty()) {
                        val chunk = Base64.decode(b64, Base64.DEFAULT)
                        out.write(chunk)
                    }
                    if (fr.optBoolean("eof", false) || fr.optString("t") == "end") {
                        return@withTimeout E2EEResponse(status, respHeaders, out.toByteArray())
                    }
                }
                throw ApiException("加密隧道连接已断开")
            }
        } catch (e: CancellationException) {
            sendCancel(sid, conn)
            throw e
        } catch (e: ApiException) {
            throw e
        } catch (e: Exception) {
            sendCancel(sid, conn)
            throw ApiException(e.message ?: "请求超时")
        } finally {
            streams.remove(sid)
            ch.close()
        }
    }

    /**
     * 通过 E2EE 隧道建立实时 SSE 行流：
     * - 收到首帧响应头后回调 `onStatus(httpStatus)`；若非 200 则立刻结束；
     * - 按原始 UTF-8 字节缓冲并以 `\n` 拆行，避免中文多字节字符被 chunk 边界截断。
     */
    suspend fun streamSSE(
        path: String,
        headers: Map<String, String> = emptyMap(),
        onStatus: suspend (Int) -> Unit,
        onLine: suspend (String) -> Unit,
    ) = withContext(Dispatchers.IO) {
        if (closed) throw CancellationException("tunnel closed")
        val conn = ensureConnected()
        val sid = randomStreamId()
        val ch = Channel<JSONObject>(Channel.UNLIMITED)
        streams[sid] = ch

        val hObj = JSONObject()
        for ((k, v) in headers) hObj.put(k, v)
        hObj.put("Accept", "text/event-stream")
        if (!hObj.has("X-Frog-Device-Name") && deviceName.isNotEmpty()) {
            hObj.put("X-Frog-Device-Name", deviceName)
        }
        val reqFrame = JSONObject()
            .put("t", "req")
            .put("s", sid)
            .put("m", "GET")
            .put("p", path)
            .put("h", hObj)

        if (!sendEncrypted(reqFrame, conn)) {
            streams.remove(sid)
            ch.close()
            handleDisconnect(conn.id, ApiException("加密隧道断开"))
            throw ApiException("加密隧道发送失败")
        }

        try {
            val first = ch.receiveCatching().getOrNull() ?: throw ApiException("事件流已关闭")
            val status = first.optInt("st", 200)
            onStatus(status)
            if (status != 200) return@withContext

            val buf = ByteArrayOutputStream()
            suspend fun flushLines() {
                val arr = buf.toByteArray()
                var start = 0
                for (i in arr.indices) {
                    if (arr[i] == 0x0A.toByte()) {
                        var end = i
                        if (end > start && arr[end - 1] == 0x0D.toByte()) end--
                        onLine(String(arr, start, end - start, Charsets.UTF_8))
                        start = i + 1
                    }
                }
                if (start > 0) {
                    buf.reset()
                    if (start < arr.size) buf.write(arr, start, arr.size - start)
                }
            }

            val initB64 = first.optString("b", "")
            if (initB64.isNotEmpty()) {
                buf.write(Base64.decode(initB64, Base64.DEFAULT))
                flushLines()
            }
            if (first.optBoolean("eof", false) || first.optString("t") == "end") {
                val rem = buf.toByteArray()
                if (rem.isNotEmpty()) onLine(String(rem, Charsets.UTF_8))
                return@withContext
            }

            for (fr in ch) {
                val b64 = fr.optString("b", "")
                if (b64.isNotEmpty()) {
                    buf.write(Base64.decode(b64, Base64.DEFAULT))
                    flushLines()
                }
                if (fr.optBoolean("eof", false) || fr.optString("t") == "end") break
            }
            val rem = buf.toByteArray()
            if (rem.isNotEmpty()) onLine(String(rem, Charsets.UTF_8))
        } finally {
            streams.remove(sid)
            ch.close()
            sendCancel(sid, conn)
        }
    }

    // ---------- Connection & LAN-First Auto-Routing ----------

    private suspend fun ensureConnected(): ActiveConn {
        if (closed) throw CancellationException("tunnel closed")
        activeConn?.let { return it }
        return connectMutex.withLock {
            if (closed) throw CancellationException("tunnel closed")
            activeConn?.let { return@withLock it }
            val created = dialBestRoute()
            activeConn = created
            created.pingJob = scope.launch {
                while (isActive && activeConn?.id == created.id) {
                    delay(25_000L)
                    if (!created.ws.send("ping")) {
                        handleDisconnect(created.id, ApiException("心跳超时，正在重连"))
                        return@launch
                    }
                }
            }
            created
        }
    }

    private suspend fun dialBestRoute(): ActiveConn {
        var lanErr: Throwable? = null
        config.buildLanWsUrl()?.let { lanUrl ->
            try {
                return dialAndProbe(lanUrl, 600L, isLan = true)
            } catch (e: Throwable) {
                lanErr = e
            }
        }
        config.buildRelayWsUrl()?.let { relayUrl ->
            try {
                return dialAndProbe(relayUrl, 10_000L, isLan = false)
            } catch (e: Throwable) {
                throw ApiException("无法连接 E2EE 盲中继：${e.message ?: "连接失败"}")
            }
        }
        if (lanErr != null) {
            throw ApiException("局域网直连失败：${lanErr?.message ?: "超时"}")
        }
        throw ApiException("E2EE 连接码缺少可用的局域网或中继地址")
    }

    private suspend fun dialAndProbe(url: String, timeoutMs: Long, isLan: Boolean): ActiveConn {
        val (sessionKey, ephPubB64) = deriveSessionKey()
        val connId = UUID.randomUUID().toString()
        val probed = CompletableDeferred<Unit>()
        lateinit var connHolder: ActiveConn

        val req = Request.Builder().url(url).build()
        val ws = wsClient.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                webSocket.send("ping")
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                val trimmed = text.trim()
                if (trimmed == "pong") {
                    probed.complete(Unit)
                    return
                }
                if (trimmed == "ping") {
                    webSocket.send("pong")
                    return
                }
                val env = runCatching { JSONObject(text) }.getOrNull() ?: return
                val relayErr = env.optString("err", "")
                if (relayErr.isNotEmpty()) {
                    val msg = if (relayErr == "host_offline") {
                        "主机不在线，请确认电脑上的 Frog Bot 正在运行"
                    } else {
                        "中继错误：$relayErr"
                    }
                    failAllStreams(ApiException(msg))
                    return
                }
                val plain = runCatching { openEnvelope(env, sessionKey) }.getOrNull() ?: return
                val frame = runCatching { JSONObject(String(plain, Charsets.UTF_8)) }.getOrNull() ?: return
                val sid = frame.optString("s", "")
                if (sid.isEmpty()) return
                val ch = streams[sid] ?: return
                ch.trySend(frame)
                if (frame.optBoolean("eof", false) || frame.optString("t") == "end") {
                    streams.remove(sid)
                    ch.close()
                }
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(1000, null)
                probed.completeExceptionally(ApiException("连接关闭"))
                handleDisconnect(connId, ApiException("加密隧道已断开"))
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                probed.completeExceptionally(ApiException("连接关闭"))
                handleDisconnect(connId, ApiException("加密隧道已断开"))
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                probed.completeExceptionally(t)
                handleDisconnect(connId, ApiException("加密隧道断开：${t.message ?: "网络异常"}"))
            }
        })
        connHolder = ActiveConn(connId, ws, sessionKey, ephPubB64, isLan)

        try {
            withTimeout(timeoutMs) { probed.await() }
            return connHolder
        } catch (e: Throwable) {
            ws.cancel()
            throw e
        }
    }

    private fun resetCurrentConnection() {
        activeConn?.let { handleDisconnect(it.id, ApiException("重连加密隧道")) }
    }

    private fun handleDisconnect(connId: String, error: Throwable) {
        val current = activeConn
        if (current == null || current.id != connId) return
        activeConn = null
        current.pingJob?.cancel()
        current.ws.cancel()
        failAllStreams(error)
    }

    private fun failAllStreams(error: Throwable) {
        val keys = streams.keys.toList()
        for (k in keys) {
            streams.remove(k)?.close(error)
        }
    }

    private fun sendCancel(sid: String, conn: ActiveConn) {
        val fr = JSONObject().put("t", "cancel").put("s", sid)
        runCatching { sendEncrypted(fr, conn) }
    }

    // ---------- Crypto (Matches Go `internal/remote/e2ee_crypto.go` byte-for-byte) ----------

    private fun deriveSessionKey(): Pair<ByteArray, String> {
        val ephPriv = X25519.generatePrivateKey()
        val ephPub = X25519.publicKey(ephPriv)
        val shared = X25519.scalarMult(ephPriv, config.serverPub)
        if (shared.all { it == 0.toByte() }) {
            throw ApiException("E2EE 密钥协商失败")
        }
        val ikm = ByteArray(64)
        System.arraycopy(shared, 0, ikm, 0, 32)
        System.arraycopy(config.psk, 0, ikm, 32, 32)

        val salt = "frog-e2ee-v1".toByteArray(Charsets.UTF_8)
        val info = "${config.hostId}|${config.peerId}".toByteArray(Charsets.UTF_8)
        val key = hkdfSha256(ikm, salt, info, 32)
        val ephPubB64 = Base64.encodeToString(ephPub, Base64.URL_SAFE or Base64.NO_PADDING or Base64.NO_WRAP)
        return key to ephPubB64
    }

    private fun sendEncrypted(frame: JSONObject, conn: ActiveConn): Boolean {
        val plaintext = frame.toString().toByteArray(Charsets.UTF_8)
        val from = "client:${config.peerId}"
        val to = "host"
        val aad = "$from|$to|${conn.ephPubB64}".toByteArray(Charsets.UTF_8)
        val nonce = ByteArray(12).also { rng.nextBytes(it) }

        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(conn.sessionKey, "AES"), GCMParameterSpec(128, nonce))
        cipher.updateAAD(aad)
        val ctAndTag = cipher.doFinal(plaintext)

        val env = JSONObject()
            .put("to", to)
            .put("from", from)
            .put("ep", conn.ephPubB64)
            .put("n", Base64.encodeToString(nonce, Base64.URL_SAFE or Base64.NO_PADDING or Base64.NO_WRAP))
            .put("c", Base64.encodeToString(ctAndTag, Base64.URL_SAFE or Base64.NO_PADDING or Base64.NO_WRAP))
        return conn.ws.send(env.toString())
    }

    private fun openEnvelope(env: JSONObject, sessionKey: ByteArray): ByteArray {
        val from = env.optString("from", "")
        val to = env.optString("to", "")
        val ep = env.optString("ep", "")
        val nStr = env.optString("n", "")
        val cStr = env.optString("c", "")
        val nonce = decodeBase64Flexible(nStr) ?: throw ApiException("无效的 E2EE nonce")
        val ctAndTag = decodeBase64Flexible(cStr) ?: throw ApiException("无效的 E2EE 密文")
        if (nonce.size != 12 || ctAndTag.size < 16) throw ApiException("无效的 E2EE 密文长度")

        val aad = "$from|$to|$ep".toByteArray(Charsets.UTF_8)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(sessionKey, "AES"), GCMParameterSpec(128, nonce))
        cipher.updateAAD(aad)
        return cipher.doFinal(ctAndTag)
    }

    private fun randomStreamId(): String =
        UUID.randomUUID().toString().replace("-", "").lowercase().take(12)
}

/** 纯 Kotlin RFC 7748 X25519 实现（兼容 Android API 26+，零外部依赖）。 */
internal object X25519 {
    private val P = BigInteger.ONE.shiftLeft(255).subtract(BigInteger.valueOf(19))
    private val P_MINUS_2 = P.subtract(BigInteger.valueOf(2))
    private val A24 = BigInteger.valueOf(121665)
    private val BASEPOINT = ByteArray(32).also { it[0] = 9 }
    private val rng = SecureRandom()

    fun generatePrivateKey(): ByteArray = ByteArray(32).also { rng.nextBytes(it) }

    fun publicKey(privateKey: ByteArray): ByteArray = scalarMult(privateKey, BASEPOINT)

    fun scalarMult(scalar: ByteArray, uBytes: ByteArray): ByteArray {
        require(scalar.size == 32 && uBytes.size == 32) { "X25519 expects 32-byte inputs" }
        val kBytes = scalar.copyOf()
        kBytes[0] = (kBytes[0].toInt() and 248).toByte()
        kBytes[31] = (kBytes[31].toInt() and 127).toByte()
        kBytes[31] = (kBytes[31].toInt() or 64).toByte()

        val uCopy = uBytes.copyOf()
        uCopy[31] = (uCopy[31].toInt() and 127).toByte()

        val k = decodeLE(kBytes)
        val u = decodeLE(uCopy).mod(P)

        var x2 = BigInteger.ONE
        var z2 = BigInteger.ZERO
        var x3 = u
        var z3 = BigInteger.ONE
        var swap = 0

        for (t in 254 downTo 0) {
            val kt = if (k.testBit(t)) 1 else 0
            swap = swap xor kt
            if (swap != 0) {
                val tx = x2; x2 = x3; x3 = tx
                val tz = z2; z2 = z3; z3 = tz
            }
            swap = kt

            val a = x2.add(z2).mod(P)
            val aa = a.multiply(a).mod(P)
            val b = x2.subtract(z2).mod(P)
            val bb = b.multiply(b).mod(P)
            val e = aa.subtract(bb).mod(P)
            val c = x3.add(z3).mod(P)
            val d = x3.subtract(z3).mod(P)
            val da = d.multiply(a).mod(P)
            val cb = c.multiply(b).mod(P)
            val sum = da.add(cb).mod(P)
            val diff = da.subtract(cb).mod(P)

            x3 = sum.multiply(sum).mod(P)
            z3 = u.multiply(diff.multiply(diff).mod(P)).mod(P)
            x2 = aa.multiply(bb).mod(P)
            z2 = e.multiply(aa.add(A24.multiply(e).mod(P)).mod(P)).mod(P)
        }
        if (swap != 0) {
            val tx = x2; x2 = x3; x3 = tx
            val tz = z2; z2 = z3; z3 = tz
        }
        val res = x2.multiply(z2.modPow(P_MINUS_2, P)).mod(P)
        return encodeLE32(res)
    }

    private fun decodeLE(b: ByteArray): BigInteger {
        val be = ByteArray(b.size)
        for (i in b.indices) be[b.size - 1 - i] = b[i]
        return BigInteger(1, be)
    }

    private fun encodeLE32(v: BigInteger): ByteArray {
        val out = ByteArray(32)
        val be = v.toByteArray()
        var pos = 0
        for (i in be.size - 1 downTo 0) {
            if (pos >= 32) break
            out[pos++] = be[i]
        }
        return out
    }
}

private fun hmacSha256(key: ByteArray, data: ByteArray): ByteArray {
    val mac = Mac.getInstance("HmacSHA256")
    mac.init(SecretKeySpec(key, "HmacSHA256"))
    return mac.doFinal(data)
}

private fun hkdfSha256(ikm: ByteArray, salt: ByteArray, info: ByteArray, length: Int): ByteArray {
    val effectiveSalt = if (salt.isEmpty()) ByteArray(32) else salt
    val prk = hmacSha256(effectiveSalt, ikm)
    val out = ByteArray(length)
    var offset = 0
    var prev = ByteArray(0)
    var counter = 1
    while (offset < length) {
        val input = ByteArray(prev.size + info.size + 1)
        System.arraycopy(prev, 0, input, 0, prev.size)
        System.arraycopy(info, 0, input, prev.size, info.size)
        input[prev.size + info.size] = counter.toByte()
        prev = hmacSha256(prk, input)
        val take = minOf(prev.size, length - offset)
        System.arraycopy(prev, 0, out, offset, take)
        offset += take
        counter++
    }
    return out
}

private fun decodeHex(raw: String): ByteArray? {
    val s = raw.trim()
    if (s.isEmpty() || s.length % 2 != 0) return null
    val out = ByteArray(s.length / 2)
    for (i in out.indices) {
        val hi = Character.digit(s[i * 2], 16)
        val lo = Character.digit(s[i * 2 + 1], 16)
        if (hi < 0 || lo < 0) return null
        out[i] = ((hi shl 4) or lo).toByte()
    }
    return out
}

private fun decodeBase64Flexible(raw: String): ByteArray? {
    var s = raw.trim().replace('-', '+').replace('_', '/')
    if (s.isEmpty()) return null
    val rem = s.length % 4
    if (rem != 0) s += "=".repeat(4 - rem)
    return runCatching { Base64.decode(s, Base64.DEFAULT) }.getOrNull()
}
