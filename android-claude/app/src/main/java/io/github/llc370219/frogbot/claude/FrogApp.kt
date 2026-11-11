package io.github.llc370219.frogbot.claude

import android.Manifest
import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import io.github.llc370219.frogbot.claude.core.AppState
import io.github.llc370219.frogbot.claude.core.BotSession
import io.github.llc370219.frogbot.claude.core.NewsKind

class FrogApp : Application() {
    lateinit var state: AppState
        private set

    override fun onCreate() {
        super.onCreate()
        instance = this
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CH_ALERTS, "审批和任务", NotificationManager.IMPORTANCE_HIGH).apply {
            description = "有操作等你确认、任务完成或失败"
        })
        nm.createNotificationChannel(NotificationChannel(CH_REPLIES, "新回复", NotificationManager.IMPORTANCE_DEFAULT))
        nm.createNotificationChannel(NotificationChannel(CH_KEEP, "后台保持连接", NotificationManager.IMPORTANCE_MIN).apply {
            description = "常驻通知，让 App 在后台也能及时收到审批和任务提醒"
            setShowBadge(false)
        })
        state = AppState(this)
        state.onNotify = ::postNotice
        if (state.keepAlive) runCatching { KeepAliveService.start(this) }
    }

    private fun postNotice(session: BotSession, text: String, kind: NewsKind) {
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) return
        val open = Intent(this, MainActivity::class.java).apply {
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
            putExtra(MainActivity.EXTRA_REF, session.ref.key)
        }
        val pi = PendingIntent.getActivity(this, session.ref.key.hashCode(), open, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val title = if (state.servers.size == 1 && session.isLocalDevice) session.profile.name else "${session.profile.name} · ${session.deviceName}"
        val n = NotificationCompat.Builder(this, if (kind == NewsKind.Reply) CH_REPLIES else CH_ALERTS)
            .setSmallIcon(R.drawable.ic_stat_frog)
            .setContentTitle(title)
            .setContentText(text.replace('\n', ' ').take(160))
            .setStyle(NotificationCompat.BigTextStyle().bigText(text.take(400)))
            .setContentIntent(pi)
            .setAutoCancel(true)
            .setGroup(session.ref.key)
            .build()
        try {
            NotificationManagerCompat.from(this).notify(session.ref.key.hashCode(), n)
        } catch (_: SecurityException) {
        }
    }

    companion object {
        const val CH_ALERTS = "alerts"
        const val CH_REPLIES = "replies"
        const val CH_KEEP = "keepalive"
        lateinit var instance: FrogApp
            private set
    }
}
