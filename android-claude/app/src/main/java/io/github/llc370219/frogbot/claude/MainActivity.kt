package io.github.llc370219.frogbot.claude

import android.Manifest
import android.content.Intent
import android.os.Build
import android.os.Bundle
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
    private val askNotify = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        handle(intent)
        val state = (application as FrogApp).state
        setContent {
            FrogTheme(state.appearance) {
                FrogRoot(state, openRef)
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
        (application as FrogApp).state.foreground = true
    }

    override fun onStop() {
        super.onStop()
        (application as FrogApp).state.foreground = false
    }

    private fun handle(intent: Intent?) {
        intent?.getStringExtra(EXTRA_REF)?.let { openRef.value = BotRef.parse(it) }
    }

    companion object {
        const val EXTRA_REF = "ref"
    }
}
