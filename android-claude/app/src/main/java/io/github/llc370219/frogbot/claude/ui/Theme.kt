package io.github.llc370219.frogbot.claude.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color

/**
 * 现代纯中性配色（与 macOS / iOS / Web 对齐）：
 * 浅色纯白画布 + 微灰卡片；深色黑曜石画布 + 低噪浮层；品牌强调色为青蛙绿。
 */
private val Light = lightColorScheme(
    primary = Color(0xFF2F7D52), onPrimary = Color.White,
    primaryContainer = Color(0xFFD7F2E3), onPrimaryContainer = Color(0xFF0A2A19),
    secondaryContainer = Color(0xFFEAF0EC), onSecondaryContainer = Color(0xFF16241B),
    surface = Color(0xFFFFFFFF), onSurface = Color(0xFF1D1D1F),
    surfaceVariant = Color(0xFFF2F2F5), onSurfaceVariant = Color(0xFF6E6E73),
    surfaceContainerLowest = Color(0xFFFFFFFF), surfaceContainerLow = Color(0xFFF7F7F8),
    surfaceContainer = Color(0xFFF2F2F5), surfaceContainerHigh = Color(0xFFEAEAEF),
    surfaceContainerHighest = Color(0xFFE3E3E8),
    background = Color(0xFFFFFFFF), onBackground = Color(0xFF1D1D1F),
    outline = Color(0xFFB5B5BC), outlineVariant = Color(0xFFE4E4E8),
    error = Color(0xFFC4161C), onError = Color.White,
)

private val Dark = darkColorScheme(
    primary = Color(0xFF4CC38A), onPrimary = Color(0xFF0A2014),
    primaryContainer = Color(0xFF17452E), onPrimaryContainer = Color(0xFFC4F5DA),
    secondaryContainer = Color(0xFF26332C), onSecondaryContainer = Color(0xFFD5EADF),
    surface = Color(0xFF121214), onSurface = Color(0xFFF5F5F7),
    surfaceVariant = Color(0xFF26262B), onSurfaceVariant = Color(0xFFA1A1A6),
    surfaceContainerLowest = Color(0xFF0D0D0F), surfaceContainerLow = Color(0xFF1A1A1D),
    surfaceContainer = Color(0xFF1E1E22), surfaceContainerHigh = Color(0xFF26262B),
    surfaceContainerHighest = Color(0xFF2E2E34),
    background = Color(0xFF121214), onBackground = Color(0xFFF5F5F7),
    outline = Color(0xFF56565E), outlineVariant = Color(0xFF2D2D33),
    error = Color(0xFFFF6961), onError = Color(0xFF3E0505),
)

@Immutable
data class Extra(val att: Color, val attContainer: Color, val onAttContainer: Color, val warn: Color, val ok: Color, val avatarBg: Color)

private val LightExtra = Extra(Color(0xFF2B6F85), Color(0xFFD9F0FA), Color(0xFF092530), Color(0xFFA35400), Color(0xFF248A3D), Color.White)
private val DarkExtra = Extra(Color(0xFF7CC4DC), Color(0xFF1A3A47), Color(0xFFD5F0FA), Color(0xFFFFB340), Color(0xFF4CD964), Color(0xFFF2F2F2))

val LocalExtra = staticCompositionLocalOf { LightExtra }

object Frog {
    val extra: Extra @Composable get() = LocalExtra.current
}

@Composable
fun FrogTheme(appearance: String, content: @Composable () -> Unit) {
    val dark = when (appearance) {
        "light" -> false
        "dark" -> true
        else -> isSystemInDarkTheme()
    }
    CompositionLocalProvider(LocalExtra provides if (dark) DarkExtra else LightExtra) {
        MaterialTheme(colorScheme = if (dark) Dark else Light, content = content)
    }
}
