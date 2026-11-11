package io.github.llc370219.frogbot.claude.core

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.MultipartBody
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.ConnectException
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLException

/** 一台 frog 服务器的连接设置（保存在手机上）。不保存密码。 */
data class ServerConfig(val id: String, val name: String, val url: String, val username: String) {
    val baseUrl: HttpUrl? get() = url.toHttpUrlOrNull()

    /** 明文 HTTP 且不是本机地址：密码和聊天内容在网络上不加密 */
    val insecure: Boolean get() = baseUrl?.let { it.scheme == "http" && !isLoopback(it.host) } ?: false

    fun json(): JSONObject = JSONObject().put("id", id).put("name", name).put("url", url).put("username", username)

    companion object {
        fun from(j: JSONObject) = ServerConfig(j.str("id"), j.str("name"), j.str("url"), j.str("username"))

        fun isLoopback(host: String): Boolean {
            val h = host.lowercase()
            return h == "localhost" || h.endsWith(".localhost") || h.startsWith("127.") || h == "::1"
        }

        /** 没写协议时：IP、带端口或本机地址按 http，域名按 https */
        fun normalize(raw: String): String? {
            var s = raw.trim().trimEnd('/')
            if (s.isEmpty()) return null
            if (!s.contains("://")) {
                val hostPort = s.substringBefore('/')
                val host = hostPort.substringBefore(':')
                val isIp = host.isNotEmpty() && host.all { it.isDigit() || it == '.' }
                val plain = hostPort.contains(':') || isIp || isLoopback(host)
                s = (if (plain) "http://" else "https://") + s
            }
            val u = s.toHttpUrlOrNull() ?: return null
            return u.toString().trimEnd('/')
        }
    }
}

class ApiException(message: String, val status: Int = 0) : Exception(message) {
    val unauthorized: Boolean get() = status == 401
}

/** 和一台服务器说话：普通请求、登录、上传、事件流。登录靠服务器发的会话 Cookie，手动放进请求头。 */
class ServerClient(config: ServerConfig, var cookie: String?) {
    @Volatile var config: ServerConfig = config

    companion object {
        const val COOKIE = "frogbot_session"
        private val JSON_TYPE = "application/json; charset=utf-8".toMediaType()

        val http: OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(30, TimeUnit.SECONDS)
            .followRedirects(false)
            .build()

        /** 事件流：服务器每 20 秒心跳，90 秒读不到东西就当断线重连 */
        val stream: OkHttpClient = http.newBuilder().readTimeout(90, TimeUnit.SECONDS).build()

        /** 互联长轮询：服务器最多等 25 秒才返回 */
        val longPoll: OkHttpClient = http.newBuilder().readTimeout(40, TimeUnit.SECONDS).build()

        /** 互联的短请求（转交消息、退出时通知），不能卡太久 */
        val quick: OkHttpClient = http.newBuilder().connectTimeout(5, TimeUnit.SECONDS).readTimeout(8, TimeUnit.SECONDS).build()

        fun describe(e: Throwable): String = when (e) {
            is ApiException -> e.message ?: "请求失败"
            is UnknownHostException -> "找不到这个地址，请检查域名或 IP"
            is ConnectException -> "连不上服务器：端口没开或服务没启动，或者手机和服务器不在一个网络"
            is SocketTimeoutException -> "连接超时：请检查网络、防火墙或安全组"
            is SSLException -> "HTTPS 证书有问题，无法建立安全连接"
            is IOException -> "网络错误：${e.message ?: "连接断开"}"
            else -> e.message ?: e.toString()
        }
    }

    fun url(path: String, query: Map<String, String> = emptyMap()): HttpUrl {
        val base = config.baseUrl ?: throw ApiException("地址格式不对")
        val b = base.newBuilder()
        path.trim('/').split('/').filter { it.isNotEmpty() }.forEach { b.addPathSegment(it) }
        query.forEach { (k, v) -> b.addQueryParameter(k, v) }
        return b.build()
    }

    fun request(path: String, query: Map<String, String> = emptyMap()): Request.Builder {
        val r = Request.Builder().url(url(path, query)).header("Accept", "application/json")
        cookie?.let { r.header("Cookie", "$COOKIE=$it") }
        return r
    }

    private fun check(resp: Response): String {
        val body = resp.body?.string() ?: ""
        if (resp.code == 401) throw ApiException("登录已过期，请重新登录", 401)
        if (resp.code !in 200..299) throw ApiException(message(body, resp.code), resp.code)
        return body
    }

    private fun message(body: String, status: Int): String {
        runCatching { JSONObject(body).str("error") }.getOrNull()?.takeIf { it.isNotEmpty() }?.let { return it }
        val t = body.trim()
        if (t.isNotEmpty() && !t.startsWith("<")) return t.take(200)
        return when (status) {
            403 -> "服务器拒绝了请求（403）"
            404 -> "找不到这个接口（404），请确认地址指向 frog 并且版本够新"
            429 -> "尝试太频繁，请稍后再试"
            502, 503, 504 -> "服务器暂时不可用（$status）"
            else -> "请求失败（$status）"
        }
    }

    private suspend fun exec(r: Request, client: OkHttpClient = http): String = withContext(Dispatchers.IO) {
        try {
            client.newCall(r).execute().use { check(it) }
        } catch (e: ApiException) {
            throw e
        } catch (e: Exception) {
            throw ApiException(describe(e))
        }
    }

    private fun errorIn(body: String) {
        val err = runCatching { JSONObject(body).str("error") }.getOrNull()
        if (!err.isNullOrEmpty()) throw ApiException(err, 400)
    }

    suspend fun getText(path: String, query: Map<String, String> = emptyMap()): String = exec(request(path, query).get().build())
    suspend fun getObject(path: String, query: Map<String, String> = emptyMap()): JSONObject = JSONObject(getText(path, query).ifBlank { "{}" })
    suspend fun getArray(path: String, query: Map<String, String> = emptyMap()): JSONArray = JSONArray(getText(path, query).ifBlank { "[]" })

    suspend fun post(
        path: String, body: JSONObject = JSONObject(), method: String = "POST", query: Map<String, String> = emptyMap(),
        client: OkHttpClient = http,
    ): JSONObject {
        val text = exec(request(path, query).method(method, body.toString().toRequestBody(JSON_TYPE)).build(), client)
        errorIn(text)
        return runCatching { JSONObject(text) }.getOrElse { JSONObject() }
    }

    suspend fun bytes(path: String, query: Map<String, String>): ByteArray = withContext(Dispatchers.IO) {
        try {
            http.newCall(request(path, query).header("Accept", "*/*").get().build()).execute().use { resp ->
                if (resp.code == 401) throw ApiException("登录已过期，请重新登录", 401)
                if (resp.code !in 200..299) throw ApiException("下载失败（${resp.code}）", resp.code)
                resp.body?.bytes() ?: ByteArray(0)
            }
        } catch (e: ApiException) {
            throw e
        } catch (e: Exception) {
            throw ApiException(describe(e))
        }
    }

    suspend fun me(): JSONObject = try {
        getObject("/api/auth/me")
    } catch (e: ApiException) {
        if (e.status == 404) JSONObject().put("enabled", false).put("authenticated", true) else throw e
    }

    /** 登录成功后从 Set-Cookie 里取出会话 */
    suspend fun login(username: String, password: String) = withContext(Dispatchers.IO) {
        val body = JSONObject().put("username", username).put("password", password).toString().toRequestBody(JSON_TYPE)
        val r = Request.Builder().url(url("/api/auth/login")).post(body).header("Accept", "application/json").build()
        val resp = try { http.newCall(r).execute() } catch (e: Exception) { throw ApiException(describe(e)) }
        resp.use {
            val text = it.body?.string() ?: ""
            if (it.code !in 200..299) throw ApiException(message(text, it.code), it.code)
            val value = it.headers("Set-Cookie").firstNotNullOfOrNull { h ->
                h.split(';').first().trim().takeIf { c -> c.startsWith("$COOKIE=") }?.substringAfter('=')
            } ?: throw ApiException("登录成功了，但服务器没有给出会话，请确认地址指向 frog")
            cookie = value
        }
    }

    suspend fun upload(path: String, name: String, mime: String, data: ByteArray): Attachment = withContext(Dispatchers.IO) {
        val form = MultipartBody.Builder().setType(MultipartBody.FORM)
            .addFormDataPart("file", name, data.toRequestBody(mime.toMediaType()))
            .build()
        val r = request(path).post(form).build()
        val text = try {
            http.newBuilder().writeTimeout(5, TimeUnit.MINUTES).build().newCall(r).execute().use { check(it) }
        } catch (e: ApiException) {
            throw e
        } catch (e: Exception) {
            throw ApiException(describe(e))
        }
        errorIn(text)
        val first = runCatching { JSONArray(text).optJSONObject(0) }.getOrNull() ?: throw ApiException("上传失败")
        Attachment.from(first)
    }
}
