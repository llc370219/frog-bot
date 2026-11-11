package io.github.llc370219.frogbot.claude.ui

import android.graphics.BitmapFactory
import android.util.LruCache
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.Terminal
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.llc370219.frogbot.claude.core.BotProfile

/** 打包进 App 的图片（网页版的 Bot 头像、Agent 图标） */
object AssetImages {
    private val cache = LruCache<String, ImageBitmap>(64)
    private val missing = HashSet<String>()

    fun load(context: android.content.Context, path: String): ImageBitmap? {
        cache.get(path)?.let { return it }
        if (path in missing) return null
        val bmp = runCatching { context.assets.open(path).use { BitmapFactory.decodeStream(it) } }.getOrNull()
        if (bmp == null) { missing.add(path); return null }
        return bmp.asImageBitmap().also { cache.put(path, it) }
    }
}

private const val AVATARS = 224

/** FNV-1a，和网页版的 slot() 一样 */
fun avatarSlot(id: String): Int {
    var h = 0x811c9dc5.toInt()
    for (c in id) { h = (h xor c.code) * 0x01000193 }
    return (h.toUInt() % AVATARS.toUInt()).toInt()
}

fun avatarFile(p: BotProfile): String {
    val n = (p.avatarSlot ?: avatarSlot(p.id)) % AVATARS
    return "bot-avatars/avatar-" + (n + 1).toString().padStart(3, '0') + ".webp"
}

@Composable
fun BotAvatar(profile: BotProfile, size: Dp = 40.dp) {
    val context = LocalContext.current
    val img = remember(profile.id, profile.avatarSlot) { AssetImages.load(context, avatarFile(profile)) }
    Box(
        Modifier.size(size).clip(CircleShape).background(Frog.extra.avatarBg)
            .border(1.dp, MaterialTheme.colorScheme.outlineVariant, CircleShape),
        contentAlignment = Alignment.Center,
    ) {
        if (img != null) {
            Image(img, null, Modifier.padding(size * 0.1f).size(size * 0.8f))
        } else {
            Text(profile.name.take(1), color = Color.Black.copy(alpha = 0.6f), fontSize = (size.value * 0.42f).sp)
        }
    }
}

/** 内置 Frog Agent 用网页版选的那张头像（avatar-003） */
@Composable
fun FrogAgentAvatar(size: Dp = 40.dp) {
    BotAvatar(BotProfile(id = "frog", name = "Frog", avatarSlot = 2), size)
}

@Composable
fun AgentIcon(id: String, size: Dp = 40.dp) {
    val context = LocalContext.current
    val file = when (id) { "codex" -> "agent-icons/codex.png"; "claude" -> "agent-icons/claude.png"; else -> "agent-icons/$id.png" }
    val img = remember(id) { AssetImages.load(context, file) }
    Box(
        Modifier.size(size).clip(RoundedCornerShape(size * 0.25f)).background(MaterialTheme.colorScheme.surfaceContainerHighest),
        contentAlignment = Alignment.Center,
    ) {
        if (img != null) Image(img, null, Modifier.size(size * 0.8f))
        else Icon(Icons.Outlined.Terminal, null, tint = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}
