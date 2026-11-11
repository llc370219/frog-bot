import AppKit
import SwiftUI

let reactionEmojis = ["👍", "❤️", "😄", "🎉", "🤔", "👀"]

/// 助理这一侧的一行：气泡在右，头像贴着最后一个气泡（像聊天软件）；附件、卡片等跟在下面。
@MainActor
struct AssistantRow<Bubbles: View, Extras: View>: View {
    let session: BotSession
    @ViewBuilder var bubbles: () -> Bubbles
    @ViewBuilder var extras: () -> Extras

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .bottom, spacing: 9) {
                BotAvatar(client: session.client, profile: session.profile, size: 28)
                VStack(alignment: .leading, spacing: 6) { bubbles() }
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            VStack(alignment: .leading, spacing: 8) { extras() }
                .padding(.leading, 37)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

@MainActor
struct MessageRow: View {
    let session: BotSession
    let msg: Msg
    var canRegenerate = false
    var onError: (String) -> Void

    @State private var hover = false

    var body: some View {
        Group {
            if msg.role == "user" { userBody } else { assistantBody }
        }
        .onHover { hover = $0 }
        .contextMenu { menu }
    }

    // MARK: 用户

    private var userBody: some View {
        VStack(alignment: .trailing, spacing: 6) {
            if !msg.files.isEmpty { AttachmentList(session: session, files: msg.files, alignTrailing: true) }
            if !msg.text.isEmpty {
                Text(msg.text)
                    .font(.system(size: 14))
                    .foregroundStyle(Theme.onAccent)
                    .lineSpacing(3)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, 13)
                    .padding(.vertical, 7)
                    .background(Theme.accent, in: UnevenRoundedRectangle(topLeadingRadius: 17, bottomLeadingRadius: 17, bottomTrailingRadius: 5, topTrailingRadius: 17, style: .continuous))
                    .frame(maxWidth: 480, alignment: .trailing)
            }
            ReactionsRow(session: session, msg: msg)
            footer
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
    }

    // MARK: 助理

    private var assistantBody: some View {
        AssistantRow(session: session) {
            if let t = msg.thinking, !t.isEmpty {
                ThinkingBlock(text: t, ms: msg.thinkingMs)
            }
            if !msg.text.isEmpty {
                AssistantBubbles(text: msg.text)
            } else if msg.card == nil && msg.secret == nil && msg.files.isEmpty {
                AssistantBubbles(text: "…")
            }
        } extras: {
            if !msg.files.isEmpty { AttachmentList(session: session, files: msg.files) }
            if !msg.tools.isEmpty {
                FlowLayout(spacing: 6) {
                    ForEach(msg.tools, id: \.self) { t in Chip(text: t, systemImage: "eye") }
                }
            }
            if let card = msg.card { OptionCardView(session: session, card: card) }
            if let secret = msg.secret { SecretCardView(session: session, request: secret) }
            ReactionsRow(session: session, msg: msg)
            footer
        }
    }

    // MARK: 时间与操作

    private var footer: some View {
        HStack(spacing: 2) {
            Text(Fmt.localTime(msg.created))
                .font(.system(size: 11))
                .monospacedDigit()
                .foregroundStyle(Theme.fg4)
                .padding(.trailing, 4)
            if !msg.text.isEmpty {
                Button { copy(msg.text) } label: { Image(systemName: "doc.on.doc") }
                    .buttonStyle(IconButtonStyle(size: 22, tint: Theme.fg4))
                    .help("复制")
            }
            if msg.role == "assistant" {
                Menu {
                    ForEach(reactionEmojis, id: \.self) { e in
                        Button(e) { act { try await session.react(msg.id, e) } }
                    }
                } label: {
                    Image(systemName: "face.smiling")
                }
                .menuStyle(.borderlessButton)
                .menuIndicator(.hidden)
                .fixedSize()
                .foregroundStyle(Theme.fg4)
                .help("回应")
            }
            if canRegenerate {
                Button { act { try await session.regenerate() } } label: { Image(systemName: "arrow.clockwise") }
                    .buttonStyle(IconButtonStyle(size: 22, tint: Theme.fg4))
                    .help("重新生成")
            }
        }
        .opacity(hover ? 1 : 0)
        .frame(height: 22)
    }

    @ViewBuilder
    private var menu: some View {
        if !msg.text.isEmpty { Button("复制") { copy(msg.text) } }
        if msg.role == "assistant" {
            Menu("回应") {
                ForEach(reactionEmojis, id: \.self) { e in
                    Button(e) { act { try await session.react(msg.id, e) } }
                }
            }
        }
        if canRegenerate { Button("重新生成") { act { try await session.regenerate() } } }
    }

    private func act(_ f: @escaping () async throws -> Void) {
        Task { do { try await f() } catch { onError(error.localizedDescription) } }
    }
}

@MainActor
struct ReactionsRow: View {
    let session: BotSession
    let msg: Msg

    var body: some View {
        if !msg.reactions.isEmpty {
            HStack(spacing: 4) {
                ForEach(msg.reactions, id: \.by) { r in
                    Button {
                        if r.by == "user" { Task { try? await session.react(msg.id, r.emoji) } }
                    } label: {
                        HStack(spacing: 3) {
                            Text(r.emoji).font(.system(size: 13))
                            if r.by == "bot" {
                                BotAvatar(client: session.client, profile: session.profile, size: 14)
                            }
                        }
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(r.by == "user" ? Theme.accentSoft : Theme.s2, in: Capsule())
                        .overlay(Capsule().strokeBorder(r.by == "user" ? Theme.accent.opacity(0.4) : Theme.line, lineWidth: 1))
                    }
                    .buttonStyle(.plain)
                    .help(r.by == "user" ? "你的回应（点一下取消）" : "助理的回应")
                }
            }
        }
    }
}

/// 聊天层的思考过程：思考中展开并计时；回答后收起成“已思考 N 秒”，点一下再展开。
@MainActor
struct ThinkingBlock: View {
    let text: String
    var live = false
    var startedAt: Double = 0
    var endedAt: Double = 0
    var ms: Double? = nil

    @State private var userOpen: Bool?

    private var thinkingNow: Bool { live && endedAt == 0 }
    private var open: Bool { userOpen ?? thinkingNow }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button {
                withAnimation(.easeOut(duration: 0.15)) { userOpen = !open }
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: "brain")
                        .font(.system(size: 12))
                    if thinkingNow {
                        TimelineView(.periodic(from: .now, by: 0.5)) { ctx in
                            Text("思考中 · \(seconds(ctx.date.timeIntervalSince1970 * 1000 - startedAt)) 秒")
                        }
                    } else {
                        Text("已思考 \(seconds(ms ?? (endedAt - startedAt))) 秒")
                    }
                    Image(systemName: "chevron.down")
                        .font(.system(size: 9, weight: .semibold))
                        .rotationEffect(.degrees(open ? 180 : 0))
                }
                .font(.system(size: 12.5))
                .foregroundStyle(Theme.fg3)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            if open {
                ScrollViewReader { proxy in
                    ScrollView {
                        Text(text)
                            .font(.system(size: 12.5))
                            .italic()
                            .foregroundStyle(Theme.fg3)
                            .lineSpacing(3)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .id("end")
                    }
                    .frame(height: estimatedHeight)
                    .onChange(of: text) { if thinkingNow { proxy.scrollTo("end", anchor: .bottom) } }
                }
                .padding(.leading, 10)
                .overlay(alignment: .leading) { Rectangle().fill(Theme.line2).frame(width: 2) }
            }
        }
        .onChange(of: thinkingNow) { if !thinkingNow { userOpen = nil } }
    }

    private func seconds(_ ms: Double) -> Int { max(1, Int((ms / 1000).rounded())) }

    /// 内层滚动区要给确定的高度（嵌在聊天的滚动视图里时，不给高度会塌掉）
    private var estimatedHeight: CGFloat {
        let rows = text.components(separatedBy: "\n").reduce(0) { $0 + max(1, $1.count / 60 + 1) }
        return min(CGFloat(rows) * 17 + 4, thinkingNow ? 120 : 260)
    }
}

/// 附件：图片显示缩略图，其他文件显示成小卡片；点一下下载并用系统默认程序打开。
@MainActor
struct AttachmentList: View {
    let session: BotSession
    let files: [Attachment]
    var alignTrailing = false

    var body: some View {
        let images = files.filter(\.isImage)
        let others = files.filter { !$0.isImage }
        VStack(alignment: alignTrailing ? .trailing : .leading, spacing: 6) {
            if !images.isEmpty {
                HStack(spacing: 6) {
                    ForEach(images) { f in ImageThumb(session: session, file: f) }
                }
            }
            ForEach(others) { f in FileChip(session: session, path: f.path, name: f.name, size: f.size) }
        }
    }
}

@MainActor
struct ImageThumb: View {
    let session: BotSession
    let file: Attachment

    var body: some View {
        Button { FileOpener.open(session: session, path: file.path, name: file.name) } label: {
            RemoteImage(client: session.client, path: session.basePath + "/files",
                        query: [URLQueryItem(name: "path", value: file.path)], contentMode: .fill) {
                ZStack { Theme.s2; ProgressView().controlSize(.small) }
            }
            .frame(width: 180, height: 130)
            .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).strokeBorder(Theme.line, lineWidth: 0.5))
        }
        .buttonStyle(.plain)
        .help(file.name)
    }
}

@MainActor
struct FileChip: View {
    let session: BotSession
    let path: String
    var name: String? = nil
    var size: Int? = nil
    @State private var hover = false

    var body: some View {
        Button { FileOpener.open(session: session, path: path, name: name ?? Fmt.base(path)) } label: {
            HStack(spacing: 8) {
                Image(systemName: icon)
                    .font(.system(size: 13))
                    .foregroundStyle(Theme.accent)
                    .frame(width: 26, height: 26)
                    .background(Theme.s1, in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                VStack(alignment: .leading, spacing: 1) {
                    Text(name ?? Fmt.base(path))
                        .font(.system(size: 12.5, weight: .medium))
                        .foregroundStyle(Theme.fg)
                        .lineLimit(1)
                    Text(size.map(Fmt.size) ?? path)
                        .font(.system(size: 11))
                        .foregroundStyle(Theme.fg4)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
            }
            .padding(.leading, 6)
            .padding(.trailing, 12)
            .padding(.vertical, 6)
            .frame(maxWidth: 300, alignment: .leading)
            .background(hover ? Theme.s3 : Theme.s2, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
        .buttonStyle(.plain)
        .onHover { hover = $0 }
        .help("下载并打开 \(path)")
    }

    private var icon: String {
        switch (path as NSString).pathExtension.lowercased() {
        case "md", "txt", "log": return "doc.text"
        case "go", "py", "js", "ts", "tsx", "swift", "rs", "sh", "json", "yaml", "yml", "html", "css": return "chevron.left.forwardslash.chevron.right"
        case "png", "jpg", "jpeg", "gif", "webp": return "photo"
        case "zip", "tar", "gz": return "archivebox"
        case "pdf": return "doc.richtext"
        default: return "doc"
        }
    }
}

/// 从服务器下载工作区里的文件到临时目录，再用系统默认程序打开。
@MainActor
enum FileOpener {
    static func open(session: BotSession, path: String, name: String) {
        Task {
            do {
                let data = try await session.fileData(path, download: true)
                let dir = FileManager.default.temporaryDirectory
                    .appendingPathComponent("FrogBotClaude", isDirectory: true)
                    .appendingPathComponent(UUID().uuidString.prefix(8).description, isDirectory: true)
                try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
                let url = dir.appendingPathComponent(name.isEmpty ? "file" : name)
                try data.write(to: url)
                NSWorkspace.shared.open(url)
            } catch {
                let alert = NSAlert()
                alert.messageText = "打不开「\(name)」"
                alert.informativeText = error.localizedDescription
                alert.runModal()
            }
        }
    }
}

/// 自动换行的横向排列（工具标签、选项按钮用）
struct FlowLayout: Layout {
    var spacing: CGFloat = 6

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, rowH: CGFloat = 0, maxX: CGFloat = 0
        for v in subviews {
            let s = v.sizeThatFits(.unspecified)
            if x > 0 && x + s.width > width { x = 0; y += rowH + spacing; rowH = 0 }
            x += s.width + spacing
            rowH = max(rowH, s.height)
            maxX = max(maxX, x - spacing)
        }
        return CGSize(width: min(maxX, width), height: y + rowH)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX, y = bounds.minY, rowH: CGFloat = 0
        for v in subviews {
            let s = v.sizeThatFits(.unspecified)
            if x > bounds.minX && x + s.width > bounds.maxX { x = bounds.minX; y += rowH + spacing; rowH = 0 }
            v.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(s))
            x += s.width + spacing
            rowH = max(rowH, s.height)
        }
    }
}
