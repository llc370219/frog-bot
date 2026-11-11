package io.github.llc370219.frogbot.claude.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/** 轻量 Markdown：段落、标题、列表、引用、代码块；行内的粗体、斜体、代码、链接 */
sealed interface MdBlock {
    data class Para(val text: String) : MdBlock
    data class Heading(val level: Int, val text: String) : MdBlock
    data class Code(val lang: String, val code: String) : MdBlock
    data class ListBlock(val ordered: Boolean, val start: Int, val items: List<String>) : MdBlock
    data class Quote(val text: String) : MdBlock
    data object Rule : MdBlock
}

object Markdown {
    private val listRe = Regex("""^\s*([-*+]|(\d{1,5})[.)])\s+(.*)$""")

    fun parse(text: String): List<MdBlock> {
        val lines = text.replace("\r\n", "\n").split("\n")
        val out = ArrayList<MdBlock>()
        val para = ArrayList<String>()
        fun flush() { if (para.isNotEmpty()) { out.add(MdBlock.Para(para.joinToString("\n"))); para.clear() } }
        var i = 0
        while (i < lines.size) {
            val line = lines[i]
            val t = line.trim()
            when {
                t.startsWith("```") -> {
                    flush()
                    val lang = t.removePrefix("```").trim()
                    val code = ArrayList<String>()
                    i++
                    while (i < lines.size && !lines[i].trim().startsWith("```")) { code.add(lines[i]); i++ }
                    out.add(MdBlock.Code(lang, code.joinToString("\n")))
                }
                t.isEmpty() -> flush()
                t.startsWith("#") && t.trimStart('#').startsWith(" ") && t.takeWhile { it == '#' }.length <= 6 -> {
                    flush()
                    out.add(MdBlock.Heading(t.takeWhile { it == '#' }.length, t.trimStart('#').trim()))
                }
                t == "---" || t == "***" || t == "___" -> { flush(); out.add(MdBlock.Rule) }
                t.startsWith(">") -> {
                    flush()
                    val rows = ArrayList<String>()
                    while (i < lines.size && lines[i].trim().startsWith(">")) { rows.add(lines[i].trim().removePrefix(">").trim()); i++ }
                    out.add(MdBlock.Quote(rows.joinToString("\n")))
                    continue
                }
                listRe.matches(line) -> {
                    flush()
                    val first = listRe.find(line)!!
                    val ordered = first.groupValues[2].isNotEmpty()
                    val start = first.groupValues[2].toIntOrNull() ?: 1
                    val items = arrayListOf(first.groupValues[3])
                    i++
                    while (i < lines.size) {
                        val m = listRe.find(lines[i])
                        if (m != null && m.groupValues[2].isNotEmpty() == ordered) { items.add(m.groupValues[3]); i++ }
                        else if ((lines[i].startsWith("  ") || lines[i].startsWith("\t")) && lines[i].isNotBlank()) {
                            items[items.lastIndex] = items.last() + "\n" + lines[i].trim(); i++
                        } else break
                    }
                    out.add(MdBlock.ListBlock(ordered, start, items))
                    continue
                }
                else -> para.add(line)
            }
            i++
        }
        flush()
        return out
    }

    private val inlineRe = Regex("""`([^`]+)`|\*\*(.+?)\*\*|(?<![*\w])\*(?!\s)(.+?)(?<!\s)\*(?!\w)|\[([^\]]+)]\(([^)\s]+)\)""")

    fun inline(s: String, codeBg: Color, link: Color): AnnotatedString = buildAnnotatedString {
        var last = 0
        for (m in inlineRe.findAll(s)) {
            append(s.substring(last, m.range.first))
            val g = m.groupValues
            when {
                g[1].isNotEmpty() -> withStyle(SpanStyle(fontFamily = FontFamily.Monospace, background = codeBg, fontSize = 13.sp)) { append(g[1]) }
                g[2].isNotEmpty() -> withStyle(SpanStyle(fontWeight = FontWeight.SemiBold)) { append(g[2]) }
                g[3].isNotEmpty() -> withStyle(SpanStyle(fontStyle = FontStyle.Italic)) { append(g[3]) }
                g[4].isNotEmpty() -> withStyle(SpanStyle(color = link, textDecoration = TextDecoration.Underline)) { append(g[4]) }
            }
            last = m.range.last + 1
        }
        append(s.substring(last))
    }
}

@Composable
fun InlineText(text: String, color: Color = MaterialTheme.colorScheme.onSurface, caret: Boolean = false) {
    val cs = MaterialTheme.colorScheme
    val a = Markdown.inline(text, cs.surfaceContainerHighest, cs.primary)
    val full = if (caret) buildAnnotatedString { append(a); withStyle(SpanStyle(color = cs.primary)) { append(" ▍") } } else a
    Text(full, color = color, fontSize = 15.sp, lineHeight = 22.sp)
}

@Composable
fun MdBlockView(b: MdBlock, color: Color = MaterialTheme.colorScheme.onSurface, caret: Boolean = false) {
    val cs = MaterialTheme.colorScheme
    when (b) {
        is MdBlock.Para -> InlineText(b.text, color, caret)
        is MdBlock.Heading -> Text(
            Markdown.inline(b.text, cs.surfaceContainerHighest, cs.primary), color = color, fontWeight = FontWeight.SemiBold,
            fontSize = if (b.level <= 1) 19.sp else if (b.level == 2) 17.sp else 15.sp,
        )
        is MdBlock.Code -> CodeBox(b.code)
        is MdBlock.ListBlock -> Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
            b.items.forEachIndexed { i, item ->
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(if (b.ordered) "${b.start + i}." else "•", color = cs.onSurfaceVariant, fontSize = 15.sp, lineHeight = 22.sp)
                    InlineText(item, color, caret && i == b.items.lastIndex)
                }
            }
        }
        is MdBlock.Quote -> Row {
            Text("│ ", color = cs.outline, fontSize = 15.sp)
            InlineText(b.text, cs.onSurfaceVariant)
        }
        MdBlock.Rule -> Text("———", color = cs.outlineVariant)
    }
}

@Composable
fun CodeBox(code: String) {
    Text(
        code, fontFamily = FontFamily.Monospace, fontSize = 12.5.sp, lineHeight = 18.sp,
        color = MaterialTheme.colorScheme.onSurface,
        modifier = Modifier.fillMaxWidth()
            .background(MaterialTheme.colorScheme.surfaceContainerHighest, Shapes12)
            .horizontalScroll(rememberScrollState())
            .padding(horizontal = 12.dp, vertical = 10.dp),
        softWrap = false,
    )
}
