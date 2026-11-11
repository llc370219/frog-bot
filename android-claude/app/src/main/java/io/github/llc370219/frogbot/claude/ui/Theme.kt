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

/** Material 3 配色：青蛙绿做主色；「等你确认」用青色（不用棕色），警告用橙色 */
private val Light = lightColorScheme(
    primary = Color(0xFF2A6A47), onPrimary = Color.White,
    primaryContainer = Color(0xFFAEF2C4), onPrimaryContainer = Color(0xFF002111),
    secondaryContainer = Color(0xFFD1E8D6), onSecondaryContainer = Color(0xFF0C1F14),
    surface = Color(0xFFF6FBF3), onSurface = Color(0xFF171D19),
    surfaceVariant = Color(0xFFDEE4DC), onSurfaceVariant = Color(0xFF414942),
    surfaceContainerLowest = Color.White, surfaceContainerLow = Color(0xFFF0F5EE),
    surfaceContainer = Color(0xFFEAEFE8), surfaceContainerHigh = Color(0xFFE4E9E2),
    surfaceContainerHighest = Color(0xFFDEE4DC),
    background = Color(0xFFF6FBF3), onBackground = Color(0xFF171D19),
    outline = Color(0xFF717971), outlineVariant = Color(0xFFC1C9C0),
    error = Color(0xFFBA1A1A), onError = Color.White,
)

private val Dark = darkColorScheme(
    primary = Color(0xFF93D5A9), onPrimary = Color(0xFF00391F),
    primaryContainer = Color(0xFF0D5131), onPrimaryContainer = Color(0xFFAEF2C4),
    secondaryContainer = Color(0xFF374B3D), onSecondaryContainer = Color(0xFFD1E8D6),
    surface = Color(0xFF0F1511), onSurface = Color(0xFFDFE4DD),
    surfaceVariant = Color(0xFF414942), onSurfaceVariant = Color(0xFFC1C9C0),
    surfaceContainerLowest = Color(0xFF0A0F0C), surfaceContainerLow = Color(0xFF171D19),
    surfaceContainer = Color(0xFF1B211D), surfaceContainerHigh = Color(0xFF252B27),
    surfaceContainerHighest = Color(0xFF303632),
    background = Color(0xFF0F1511), onBackground = Color(0xFFDFE4DD),
    outline = Color(0xFF8B938B), outlineVariant = Color(0xFF414942),
    error = Color(0xFFFFB4AB), onError = Color(0xFF690005),
)

@Immutable
data class Extra(val att: Color, val attContainer: Color, val onAttContainer: Color, val warn: Color, val ok: Color, val avatarBg: Color)

private val LightExtra = Extra(Color(0xFF3A6473), Color(0xFFC2E8F8), Color(0xFF001F28), Color(0xFF7D4A00), Color(0xFF2A6A47), Color.White)
private val DarkExtra = Extra(Color(0xFFA4CDDD), Color(0xFF234C59), Color(0xFFC2E8F8), Color(0xFFFFB86C), Color(0xFF93D5A9), Color(0xFFF2F2F2))

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
