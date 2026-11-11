import SwiftUI

let reactionEmojis = ["👍", "❤️", "😄", "🎉", "🤔", "👀"]

/// 助理这一侧：头像贴着最后一个气泡（像聊天软件），附件、卡片等跟在下面。
struct AssistantRow<Bubbles: View, Extras: View>: View {
    let session: BotSession
    @ViewBuilder var bubbles: () -> Bubbles
    @ViewBuilder var extras: () -> Extras

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .bottom, spacing: 8) {
                BotAvatar(client: session.client, profile: session.profile, size: 30)
                VStack(alignment: .leading, spacing: 6) { bubbles() }
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            VStack(alignment: .leading, spacing: 8) { extras() }
                .padding(.leading, 38)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

struct MessageRow: View {
    let session: BotSession
    let msg: Msg
    var canRegenerate = false
    var onError: (String) -> Void

    var body: some View {
        if msg.role == "user" { userBody } else { assistantBody }
    }

    // MARK: 用户

    private var userBody: some View {
        VStack(alignment: .trailing, spacing: 5) {
            if !msg.files.isEmpty { AttachmentList(session: session, files: msg.files, alignTrailing: true) }
            if !msg.text.isEmpty {
                Text(msg.text)
                    .font(.system(size: 16))
                    .foregroundStyle(Theme.onAccent)
                    .lineSpacing(2)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 8)
                    .background(Theme.accent, in: UnevenRoundedRectangle(topLeadingRadius: 18, bottomLeadingRadius: 18, bottomTrailingRadius: 5, topTrailingRadius: 18, style: .continuous))
                    .contentShape(.contextMenuPreview, RoundedRectangle(cornerRadius: 18, style: .continuous))
                    .contextMenu { menu }
                    .frame(maxWidth: 520, alignment: .trailing)
                    .padding(.leading, 48)
            }
            ReactionsRow(session: session, msg: msg)
            time
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
                    .contentShape(.contextMenuPreview, RoundedRectangle(cornerRadius: 18, style: .continuous))
                    .contextMenu { menu }
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
            HStack(spacing: 14) {
                time
                if canRegenerate {
                    Button { act { try await session.regenerate() } } label: {
                        Label("重新生成", systemImage: "arrow.clockwise").font(.system(size: 12))
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.fg4)
                }
            }
        }
    }

    private var time: some View {
        Text(Fmt.localTime(msg.created))
            .font(.system(size: 11))
            .monospacedDigit()
            .foregroundStyle(Theme.fg4)
    }

    @ViewBuilder
    private var menu: some View {
        if !msg.text.isEmpty {
            Button { copy(msg.text) } label: { Label("复制", systemImage: "doc.on.doc") }
        }
        if msg.role == "assistant" {
            Menu {
                ForEach(reactionEmojis, id: \.self) { e in
                    Button(e) { act { try await session.react(msg.id, e) } }
                }
            } label: { Label("回应", systemImage: "face.smiling") }
            if canRegenerate {
                Button { act { try await session.regenerate() } } label: { Label("重新生成", systemImage: "arrow.clockwise") }
            }
        }
    }

    private func act(_ f: @escaping () async throws -> Void) {
        Haptics.tap()
        Task { do { try await f() } catch { onError(error.localizedDescription) } }
    }
}

struct ReactionsRow: View {
    let session: BotSession
    let msg: Msg

    var body: some View {
        if !msg.reactions.isEmpty {
            HStack(spacing: 5) {
                ForEach(msg.reactions, id: \.by) { r in
                    Button {
                        if r.by == "user" { Task { try? await session.react(msg.id, r.emoji) } }
                    } label: {
                        HStack(spacing: 3) {
                            Text(r.emoji).font(.system(size: 14))
                            if r.by == "bot" {
                                BotAvatar(client: session.client, profile: session.profile, size: 15)
                            }
                        }
                        .padding(.horizontal, 7)
                        .padding(.vertical, 3)
                        .background(r.by == "user" ? Theme.accentSoft : Theme.s2, in: Capsule())
                        .overlay(Capsule().strokeBorder(r.by == "user" ? Theme.accent.opacity(0.4) : Theme.line, lineWidth: 1))
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }
}

/// 聊天层的思考过程：思考中展开并计时；回答后收起成“已思考 N 秒”，点一下再展开。
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
                    Image(systemName: "brain").font(.system(size: 13))
                    if thinkingNow {
                        TimelineView(.periodic(from: .now, by: 0.5)) { ctx in
                            Text("思考中 · \(seconds(ctx.date.timeIntervalSince1970 * 1000 - startedAt)) 秒")
                        }
                    } else {
                        Text("已思考 \(seconds(ms ?? (endedAt - startedAt))) 秒")
                    }
                    Image(systemName: "chevron.down")
                        .font(.system(size: 10, weight: .semibold))
                        .rotationEffect(.degrees(open ? 180 : 0))
                }
                .font(.system(size: 13.5))
                .foregroundStyle(Theme.fg3)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            if open {
                ScrollViewReader { proxy in
                    ScrollView {
                        Text(text)
                            .font(.system(size: 13.5))
                            .italic()
                            .foregroundStyle(Theme.fg3)
                            .lineSpacing(3)
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

    private var estimatedHeight: CGFloat {
        let rows = text.components(separatedBy: "\n").reduce(0) { $0 + max(1, $1.count / 28 + 1) }
        return min(CGFloat(rows) * 19 + 4, thinkingNow ? 120 : 260)
    }
}

/// 附件：图片显示缩略图，其他文件显示成小卡片；点一下下载并预览。
struct AttachmentList: View {
    let session: BotSession
    let files: [Attachment]
    var alignTrailing = false

    var body: some View {
        let images = files.filter(\.isImage)
        let others = files.filter { !$0.isImage }
        VStack(alignment: alignTrailing ? .trailing : .leading, spacing: 6) {
            if !images.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(images) { f in ImageThumb(session: session, file: f) }
                    }
                }
                .frame(height: 140)
            }
            ForEach(others) { f in FileChip(session: session, path: f.path, name: f.name, size: f.size) }
        }
    }
}

struct ImageThumb: View {
    let session: BotSession
    let file: Attachment

    var body: some View {
        Button { FilePreview.shared.open(session: session, path: file.path, name: file.name) } label: {
            RemoteImage(client: session.client, path: session.basePath + "/files",
                        query: [URLQueryItem(name: "path", value: file.path)], contentMode: .fill) {
                ZStack { Theme.s2; ProgressView() }
            }
            .frame(width: 180, height: 140)
            .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Theme.line, lineWidth: 0.5))
        }
        .buttonStyle(.plain)
    }
}

struct FileChip: View {
    let session: BotSession
    let path: String
    var name: String? = nil
    var size: Int? = nil

    var body: some View {
        Button { FilePreview.shared.open(session: session, path: path, name: name ?? Fmt.base(path)) } label: {
            HStack(spacing: 9) {
                Image(systemName: icon)
                    .font(.system(size: 14))
                    .foregroundStyle(Theme.accent)
                    .frame(width: 30, height: 30)
                    .background(Theme.canvas, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                VStack(alignment: .leading, spacing: 1) {
                    Text(name ?? Fmt.base(path))
                        .font(.system(size: 14, weight: .medium))
                        .foregroundStyle(Theme.fg)
                        .lineLimit(1)
                    Text(size.map(Fmt.size) ?? path)
                        .font(.system(size: 12))
                        .foregroundStyle(Theme.fg4)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                if FilePreview.shared.loading == path { ProgressView().controlSize(.small) }
            }
            .padding(.leading, 6)
            .padding(.trailing, 12)
            .padding(.vertical, 6)
            .frame(maxWidth: 300, alignment: .leading)
            .background(Theme.s2, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
        .buttonStyle(.plain)
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

/// 下载工作区里的文件到临时目录，用系统的快速查看打开（可以分享、存到「文件」）。
@MainActor
@Observable
final class FilePreview {
    static let shared = FilePreview()

    var url: URL?
    var error: String?
    var loading: String?

    func open(session: BotSession, path: String, name: String) {
        guard loading == nil else { return }
        loading = path
        Task {
            defer { loading = nil }
            do {
                let data = try await session.fileData(path, download: true)
                let dir = FileManager.default.temporaryDirectory
                    .appendingPathComponent("preview", isDirectory: true)
                    .appendingPathComponent(String(UUID().uuidString.prefix(8)), isDirectory: true)
                try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
                let file = dir.appendingPathComponent(name.isEmpty ? "file" : name)
                try data.write(to: file)
                url = file
            } catch {
                self.error = "打不开「\(name)」：\(error.localizedDescription)"
            }
        }
    }
}
