import SwiftUI

/// 轻量 Markdown：段落、标题、列表、引用、代码块、分隔线；行内的粗体、斜体、代码、链接交给系统解析。
enum MDBlock: Hashable {
    case paragraph(String)
    case heading(Int, String)
    case code(String, String)
    case list(ordered: Bool, start: Int, items: [String])
    case quote(String)
    case rule
}

enum Markdown {
    private final class Box { let blocks: [MDBlock]; init(_ b: [MDBlock]) { blocks = b } }
    private static let cache = NSCache<NSString, Box>()

    static func blocks(_ text: String) -> [MDBlock] {
        let key = text as NSString
        if let hit = cache.object(forKey: key) { return hit.blocks }
        let out = parse(text)
        cache.setObject(Box(out), forKey: key)
        return out
    }

    static func inline(_ s: String) -> AttributedString {
        let opts = AttributedString.MarkdownParsingOptions(
            allowsExtendedAttributes: false,
            interpretedSyntax: .inlineOnlyPreservingWhitespace,
            failurePolicy: .returnPartiallyParsedIfPossible)
        return (try? AttributedString(markdown: s, options: opts)) ?? AttributedString(s)
    }

    private static func listItem(_ line: String) -> (ordered: Bool, number: Int, text: String)? {
        let t = line.drop(while: { $0 == " " })
        if let c = t.first, "-*+".contains(c), t.dropFirst().first == " " {
            return (false, 0, String(t.dropFirst(2)))
        }
        let digits = t.prefix(while: { $0.isASCII && $0.isNumber })
        if !digits.isEmpty, digits.count < 6 {
            let rest = t.dropFirst(digits.count)
            if let c = rest.first, c == "." || c == ")", rest.dropFirst().first == " " {
                return (true, Int(digits) ?? 1, String(rest.dropFirst(2)))
            }
        }
        return nil
    }

    private static func heading(_ t: String) -> (Int, String)? {
        let hashes = t.prefix(while: { $0 == "#" })
        guard (1...6).contains(hashes.count), t.dropFirst(hashes.count).first == " " else { return nil }
        return (hashes.count, String(t.dropFirst(hashes.count + 1)))
    }

    static func parse(_ text: String) -> [MDBlock] {
        let lines = text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
        var blocks: [MDBlock] = []
        var para: [String] = []
        func flush() {
            if !para.isEmpty {
                blocks.append(.paragraph(para.joined(separator: "\n")))
                para.removeAll()
            }
        }
        var i = 0
        while i < lines.count {
            let line = lines[i]
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.hasPrefix("```") {
                flush()
                let lang = String(t.dropFirst(3)).trimmingCharacters(in: .whitespaces)
                var code: [String] = []
                i += 1
                while i < lines.count, !lines[i].trimmingCharacters(in: .whitespaces).hasPrefix("```") {
                    code.append(lines[i]); i += 1
                }
                i += 1
                blocks.append(.code(lang, code.joined(separator: "\n")))
                continue
            }
            if t.isEmpty { flush(); i += 1; continue }
            if let h = heading(t) { flush(); blocks.append(.heading(h.0, h.1)); i += 1; continue }
            if t == "---" || t == "***" || t == "___" { flush(); blocks.append(.rule); i += 1; continue }
            if t.hasPrefix(">") {
                flush()
                var rows: [String] = []
                while i < lines.count {
                    let q = lines[i].trimmingCharacters(in: .whitespaces)
                    guard q.hasPrefix(">") else { break }
                    rows.append(String(q.dropFirst()).trimmingCharacters(in: .whitespaces))
                    i += 1
                }
                blocks.append(.quote(rows.joined(separator: "\n")))
                continue
            }
            if let first = listItem(line), para.isEmpty || first.ordered == false || first.number == 1 {
                flush()
                var items: [String] = [first.text]
                i += 1
                while i < lines.count {
                    let l = lines[i]
                    if let next = listItem(l), next.ordered == first.ordered {
                        items.append(next.text); i += 1
                    } else if l.hasPrefix("  ") || l.hasPrefix("\t"), !l.trimmingCharacters(in: .whitespaces).isEmpty {
                        // 缩进的续行（含嵌套列表）并进上一项
                        let cont = l.trimmingCharacters(in: .whitespaces)
                        if let sub = listItem(cont) {
                            items[items.count - 1] += "\n" + (sub.ordered ? "\(sub.number). " : "◦ ") + sub.text
                        } else {
                            items[items.count - 1] += "\n" + cont
                        }
                        i += 1
                    } else {
                        break
                    }
                }
                blocks.append(.list(ordered: first.ordered, start: first.number, items: items))
                continue
            }
            para.append(line)
            i += 1
        }
        flush()
        return blocks
    }
}

extension Markdown {
    /// 把一段话拆成一句一句（和网页版 splitSentences 一样）：句号、叹号、问号、换行处断开，行内代码里不断。
    static func splitSentences(_ text: String) -> [String] {
        var out: [String] = []
        var cur = ""
        var inCode = false
        let chars = Array(text)
        var i = 0
        func push() {
            let t = cur.trimmingCharacters(in: .whitespacesAndNewlines)
            if !t.isEmpty { out.append(t) }
            cur = ""
        }
        while i < chars.count {
            let ch = chars[i]
            cur.append(ch)
            if ch == "`" { inCode.toggle() }
            if inCode { i += 1; continue }
            if ch == "\n" {
                push()
            } else if ch == "。" || ch == "！" || ch == "？" {
                if i + 1 < chars.count, "”\"’'".contains(chars[i + 1]) {
                    i += 1
                    cur.append(chars[i])
                }
                push()
            } else if ch == "!" || ch == "?" {
                if i + 1 == chars.count || chars[i + 1].isWhitespace { push() }
            }
            i += 1
        }
        push()
        return out
    }
}

/// 助理的回复：每句话一个气泡（像聊天软件），代码、列表、标题各占一个气泡。
@MainActor
struct AssistantBubbles: View {
    let text: String
    var streaming = false

    private enum Piece: Hashable {
        case sentence(String)
        case block(MDBlock)
    }

    private var pieces: [Piece] {
        var out: [Piece] = []
        for b in Markdown.blocks(text) {
            if case .paragraph(let p) = b {
                let sents = Markdown.splitSentences(p)
                if sents.isEmpty { out.append(.sentence(p)) } else { out += sents.map(Piece.sentence) }
            } else {
                out.append(.block(b))
            }
        }
        return out
    }

    var body: some View {
        let list = pieces
        VStack(alignment: .leading, spacing: 3) {
            if list.isEmpty && streaming {
                bubble(last: true) { TypingDots(size: 6).padding(.vertical, 4) }
            }
            ForEach(Array(list.enumerated()), id: \.offset) { i, piece in
                let last = i == list.count - 1
                switch piece {
                case .sentence(let s):
                    bubble(last: last) {
                        inline(s, caret: streaming && last)
                            .font(.system(size: 16))
                            .foregroundStyle(Theme.fg)
                            .lineSpacing(3)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                case .block(let b):
                    if case .code(let lang, let code) = b {
                        CodeBlock(lang: lang, code: code).frame(maxWidth: .infinity, alignment: .leading)
                    } else {
                        bubble(last: last) {
                            MarkdownView(text: Self.source(b), streaming: streaming && last, fontSize: 16)
                        }
                    }
                }
            }
        }
    }

    private func inline(_ s: String, caret: Bool) -> Text {
        var t = Text(Markdown.inline(s))
        if caret { t = t + Text(" ▍").foregroundColor(Theme.accent) }
        return t
    }

    private func bubble<C: View>(last: Bool, @ViewBuilder _ content: () -> C) -> some View {
        content()
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .background(Theme.bubble, in: UnevenRoundedRectangle(topLeadingRadius: 18, bottomLeadingRadius: last ? 5 : 18,
                                                                   bottomTrailingRadius: 18, topTrailingRadius: 18, style: .continuous))
            .frame(maxWidth: 560, alignment: .leading)
    }

    /// 把一个块还原成 Markdown 文本，交给 MarkdownView 显示
    private static func source(_ b: MDBlock) -> String {
        switch b {
        case .paragraph(let s): return s
        case .heading(let l, let s): return String(repeating: "#", count: l) + " " + s
        case .code(let lang, let c): return "```\(lang)\n\(c)\n```"
        case .list(let ordered, let start, let items):
            return items.enumerated().map { (ordered ? "\(start + $0.offset). " : "- ") + $0.element }.joined(separator: "\n")
        case .quote(let s): return s.components(separatedBy: "\n").map { "> " + $0 }.joined(separator: "\n")
        case .rule: return "---"
        }
    }
}

@MainActor
struct MarkdownView: View {
    let text: String
    var streaming = false
    var fontSize: CGFloat = 16

    var body: some View {
        let blocks = Markdown.blocks(text)
        VStack(alignment: .leading, spacing: 9) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { idx, block in
                blockView(block, last: idx == blocks.count - 1)
            }
            if blocks.isEmpty && streaming { Text("▍").foregroundStyle(Theme.accent) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func inlineText(_ s: String, last: Bool) -> Text {
        var t = Text(Markdown.inline(s))
        if streaming && last { t = t + Text(" ▍").foregroundColor(Theme.accent) }
        return t
    }

    @ViewBuilder
    private func blockView(_ block: MDBlock, last: Bool) -> some View {
        switch block {
        case .paragraph(let s):
            inlineText(s, last: last)
                .font(.system(size: fontSize))
                .foregroundStyle(Theme.fg)
                .lineSpacing(4)
                .fixedSize(horizontal: false, vertical: true)
        case .heading(let level, let s):
            inlineText(s, last: last)
                .font(.system(size: level == 1 ? fontSize + 4.5 : level == 2 ? fontSize + 2.5 : fontSize + 0.5, weight: .semibold))
                .foregroundStyle(Theme.fg)
                .padding(.top, 4)
                .fixedSize(horizontal: false, vertical: true)
        case .code(let lang, let code):
            CodeBlock(lang: lang, code: code)
        case .list(let ordered, let start, let items):
            VStack(alignment: .leading, spacing: 5) {
                ForEach(Array(items.enumerated()), id: \.offset) { i, item in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(ordered ? "\(start + i)." : "•")
                            .font(.system(size: fontSize, weight: ordered ? .regular : .bold))
                            .monospacedDigit()
                            .foregroundStyle(Theme.fg3)
                            .frame(minWidth: 14, alignment: .trailing)
                        inlineText(item, last: last && i == items.count - 1)
                            .font(.system(size: fontSize))
                            .foregroundStyle(Theme.fg)
                            .lineSpacing(3)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
        case .quote(let s):
            HStack(alignment: .top, spacing: 10) {
                RoundedRectangle(cornerRadius: 1.5).fill(Theme.line2).frame(width: 3)
                inlineText(s, last: last)
                    .font(.system(size: fontSize))
                    .foregroundStyle(Theme.fg3)
                    .lineSpacing(3)
                    .fixedSize(horizontal: false, vertical: true)
            }
        case .rule:
            Rectangle().fill(Theme.line).frame(height: 1).padding(.vertical, 4)
        }
    }
}

@MainActor
struct CodeBlock: View {
    let lang: String
    let code: String
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text(lang.isEmpty ? "代码" : lang)
                    .font(.system(size: 12, weight: .medium))
                    .foregroundStyle(Theme.fg4)
                Spacer()
                Button {
                    copy(code)
                    copied = true
                    DispatchQueue.main.asyncAfter(deadline: .now() + 1.4) { copied = false }
                } label: {
                    Label(copied ? "已复制" : "复制", systemImage: copied ? "checkmark" : "doc.on.doc")
                        .font(.system(size: 12))
                }
                .buttonStyle(.plain)
                .foregroundStyle(copied ? Theme.ok : Theme.fg4)
            }
            .padding(.horizontal, 12)
            .padding(.top, 8)
            ScrollView(.horizontal, showsIndicators: false) {
                Text(code)
                    .font(.system(size: 13, design: .monospaced))
                    .foregroundStyle(Theme.fg2)
                    .lineSpacing(3)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: true, vertical: true)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
            }
        }
        .background(Theme.code, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
    }
}
