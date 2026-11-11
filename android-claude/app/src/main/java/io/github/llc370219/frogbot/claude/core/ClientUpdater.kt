package io.github.llc370219.frogbot.claude.core

import android.content.ClipData
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInfo
import android.content.pm.PackageManager
import android.content.pm.Signature
import android.os.Build
import android.os.SystemClock
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.core.content.FileProvider
import androidx.core.content.pm.PackageInfoCompat
import io.github.llc370219.frogbot.claude.BuildConfig
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

sealed interface ClientUpdateStatus {
    data object Idle : ClientUpdateStatus
    data object Checking : ClientUpdateStatus
    data class Downloading(val version: String) : ClientUpdateStatus
    data class Ready(val version: String, val apk: File) : ClientUpdateStatus
    data object Current : ClientUpdateStatus
    data class Failed(val message: String) : ClientUpdateStatus
}

class ClientUpdater(private val context: Context) {
    val enabled = BuildConfig.OFFICIAL_UPDATES
    var status by mutableStateOf<ClientUpdateStatus>(ClientUpdateStatus.Idle)
        private set

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val downloads = File(context.cacheDir, "client-updates")
    private val http = OkHttpClient.Builder()
        .followSslRedirects(false)
        .readTimeout(30, TimeUnit.SECONDS)
        .callTimeout(5, TimeUnit.MINUTES)
        .build()
    private var nextCheck = 0L

    // Activity 启动时调用。应用在后台时不额外启动更新任务。
    fun check(manual: Boolean = false) {
        if (!enabled || status is ClientUpdateStatus.Checking || status is ClientUpdateStatus.Downloading) return
        val now = SystemClock.elapsedRealtime()
        if (!manual && now < nextCheck) return
        status = ClientUpdateStatus.Checking
        scope.launch {
            try {
                val asset = withContext(Dispatchers.IO) { latestAsset() }
                status = if (asset == null) {
                    withContext(Dispatchers.IO) { removeCachedApks() }
                    ClientUpdateStatus.Current
                } else {
                    status = ClientUpdateStatus.Downloading(asset.version)
                    val apk = withContext(Dispatchers.IO) { download(asset) }
                    ClientUpdateStatus.Ready(asset.version, apk)
                }
                nextCheck = SystemClock.elapsedRealtime() + TimeUnit.HOURS.toMillis(6)
            } catch (error: CancellationException) {
                throw error
            } catch (error: Exception) {
                status = ClientUpdateStatus.Failed(error.message ?: "更新失败，请稍后重试")
                nextCheck = SystemClock.elapsedRealtime() + TimeUnit.MINUTES.toMillis(15)
            }
        }
    }

    fun installerIntent(): Intent? {
        val ready = status as? ClientUpdateStatus.Ready ?: return null
        if (!ready.apk.isFile) {
            check(manual = true)
            return null
        }
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.updates", ready.apk)
        return Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            clipData = ClipData.newRawUri("frog", uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
    }

    private fun latestAsset(): UpdateAsset? {
        val request = Request.Builder().url("https://api.github.com/repos/$REPOSITORY/releases/latest")
            .header("Accept", "application/vnd.github+json")
            .build()
        val release = http.newCall(request).execute().use { response ->
            if (response.code == 404) return null
            check(response.isSuccessful) { "检查客户端更新失败（HTTP ${response.code}）" }
            val body = response.peekBody(MAX_RELEASE_BYTES + 1)
            check(body.contentLength() <= MAX_RELEASE_BYTES) { "发布信息过大" }
            JSONObject(body.string())
        }
        if (release.optBoolean("draft") || release.optBoolean("prerelease")) return null
        val tag = release.optString("tag_name")
        val match = STABLE_TAG.matchEntire(tag) ?: return null
        val (major, minor, patch) = match.groupValues.drop(1).map(String::toInt)
        val versionCode = major * 1_000_000 + minor * 1_000 + patch
        if (versionCode <= BuildConfig.VERSION_CODE) return null

        val assets = release.optJSONArray("assets") ?: return null
        val apk = (0 until assets.length()).asSequence().mapNotNull(assets::optJSONObject)
            .firstOrNull { it.optString("name") == APK_ASSET && it.optString("state") == "uploaded" }
            ?: return null
        val size = apk.optLong("size")
        val digest = SHA256.matchEntire(apk.optString("digest"))
        check(size in 1L..MAX_APK_BYTES && digest != null) { "安装包校验信息无效" }
        return UpdateAsset(tag.removePrefix("v"), versionCode, size, digest.groupValues[1])
    }

    private fun download(asset: UpdateAsset): File {
        check(downloads.isDirectory || downloads.mkdirs()) { "无法创建安装包缓存" }
        val apk = File(downloads, "frog-${asset.version}.apk")
        if (apk.length() == asset.size && sha256(apk) == asset.checksum) {
            verifyPackage(apk, asset)
            return apk
        }

        val temp = File.createTempFile("frog-download-", ".apk", downloads)
        try {
            val request = Request.Builder().url("https://github.com/$REPOSITORY/releases/download/v${asset.version}/$APK_ASSET").build()
            http.newCall(request).execute().use { response ->
                check(response.isSuccessful) { "下载客户端更新失败（HTTP ${response.code}）" }
                val body = checkNotNull(response.body) { "安装包下载内容为空" }
                body.byteStream().use { input ->
                    temp.outputStream().buffered().use { output ->
                        val buffer = ByteArray(DEFAULT_BUFFER_SIZE)
                        var remaining = asset.size
                        while (true) {
                            val count = input.read(buffer)
                            if (count == -1) break
                            remaining -= count
                            check(remaining >= 0) { "安装包大小与发布信息不符" }
                            output.write(buffer, 0, count)
                        }
                        check(remaining == 0L) { "安装包下载不完整" }
                    }
                }
            }
            check(sha256(temp) == asset.checksum) { "安装包校验失败" }
            verifyPackage(temp, asset)
            check(temp.renameTo(apk)) { "无法保存已校验的安装包" }
            removeCachedApks(keep = apk)
            return apk
        } finally {
            temp.delete()
        }
    }

    @Suppress("DEPRECATION")
    private fun verifyPackage(apk: File, asset: UpdateAsset) {
        val manager = context.packageManager
        val flags = if (Build.VERSION.SDK_INT >= 28) PackageManager.GET_SIGNING_CERTIFICATES else PackageManager.GET_SIGNATURES
        val candidate = checkNotNull(manager.getPackageArchiveInfo(apk.absolutePath, flags)) { "无法识别安装包" }
        val installed = manager.getPackageInfo(context.packageName, flags)
        check(candidate.packageName == installed.packageName && candidate.versionName == asset.version &&
            PackageInfoCompat.getLongVersionCode(candidate) == asset.versionCode.toLong()) {
            "安装包与客户端版本不符"
        }
        val signers = installed.apkSigners()
        check(signers.isNotEmpty() && signers == candidate.apkSigners()) { "安装包签名与当前客户端不符" }
    }

    @Suppress("DEPRECATION")
    private fun PackageInfo.apkSigners(): Set<Signature> {
        val signers = if (Build.VERSION.SDK_INT >= 28) signingInfo?.apkContentsSigners else signatures
        return signers.orEmpty().toSet()
    }

    private fun removeCachedApks(keep: File? = null) {
        downloads.listFiles()?.filter { it != keep }?.forEach { it.delete() }
    }

    private fun sha256(file: File): String {
        val digest = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buffer = ByteArray(DEFAULT_BUFFER_SIZE)
            while (true) {
                val count = input.read(buffer)
                if (count == -1) break
                digest.update(buffer, 0, count)
            }
        }
        return digest.digest().joinToString("") { (it.toInt() and 0xff).toString(16).padStart(2, '0') }
    }

    private data class UpdateAsset(val version: String, val versionCode: Int, val size: Long, val checksum: String)

    private companion object {
        const val REPOSITORY = "llc370219/frog-bot"
        const val APK_ASSET = "frog-android.apk"
        const val MAX_RELEASE_BYTES = 1024L * 1024
        const val MAX_APK_BYTES = 256L * 1024 * 1024
        val STABLE_TAG = Regex("v(0|[1-9][0-9]{0,2})\\.(0|[1-9][0-9]{0,2})\\.(0|[1-9][0-9]{0,2})")
        val SHA256 = Regex("sha256:([0-9a-f]{64})")
    }
}

class UpdateFileProvider : FileProvider()
