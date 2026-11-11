package io.github.llc370219.frogbot.claude

import android.Manifest
import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.mutableStateOf
import io.github.llc370219.frogbot.claude.core.BotRef
import io.github.llc370219.frogbot.claude.ui.FrogRoot
import io.github.llc370219.frogbot.claude.ui.FrogTheme

class MainActivity : ComponentActivity() {
    private val openRef = mutableStateOf<BotRef?>(null)
    private val frogApp get() = application as FrogApp
    private val askNotify = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}
    private val allowUpdateInstall = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) {
        if (packageManager.canRequestPackageInstalls()) installUpdate()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        handle(intent)
        val state = frogApp.state
        setContent {
            FrogTheme(state.appearance) {
                FrogRoot(state, openRef, frogApp.updater, ::installUpdate)
            }
        }
        if (Build.VERSION.SDK_INT >= 33) askNotify.launch(Manifest.permission.POST_NOTIFICATIONS)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handle(intent)
    }

    override fun onStart() {
        super.onStart()
        frogApp.state.foreground = true
        frogApp.updater.check()
    }

    override fun onStop() {
        super.onStop()
        frogApp.state.foreground = false
    }

    private fun installUpdate() {
        val installer = frogApp.updater.installerIntent() ?: return
        try {
            if (packageManager.canRequestPackageInstalls()) {
                startActivity(installer)
            } else {
                allowUpdateInstall.launch(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")))
            }
        } catch (_: ActivityNotFoundException) {
            Toast.makeText(this, "找不到系统安装程序", Toast.LENGTH_LONG).show()
        } catch (_: SecurityException) {
            Toast.makeText(this, "系统限制了安装，请检查设备的安装权限", Toast.LENGTH_LONG).show()
        }
    }

    private fun handle(intent: Intent?) {
        intent?.getStringExtra(EXTRA_REF)?.let { openRef.value = BotRef.parse(it) }
    }

    companion object {
        const val EXTRA_REF = "ref"
    }
}
