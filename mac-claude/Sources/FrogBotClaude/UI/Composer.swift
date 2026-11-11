import AppKit
import SwiftUI
import UniformTypeIdentifiers

/// 输入框：多行自动长高（最多 8 行），Enter 发送 / Shift+Enter 换行。
/// 用系统的 NSTextView：中文输入法选词时按回车只会上屏，不会误发送。
@MainActor
struct Composer: View {
    @Bindable var session: BotSession
    var onError: (String) -> Void

    @State private var height: CGFloat = 22
    @State private var sending = false
    @State private var picking = false
    @State private var focus = 0

    private var uploading: Bool { session.uploads.contains { $0.uploading } }
    private var canSend: Bool {
        session.connected && session.ready && !sending && !uploading &&
            (!session.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || session.uploads.contains { $0.attachment != nil })
    }
    private var showStop: Bool { session.busy && session.draft.isEmpty && session.uploads.isEmpty }

    var body: some View {
        VStack(spacing: 6) {
            VStack(alignment: .leading, spacing: 8) {
                if !session.uploads.isEmpty { uploadsRow }
                HStack(alignment: .bottom, spacing: 8) {
                    Button { picking = true } label: { Image(systemName: "paperclip") }
                        .buttonStyle(IconButtonStyle(size: 30, tint: Theme.fg3))
                        .help("添加附件（也可以把文件拖进窗口、或直接粘贴图片）")
                    ZStack(alignment: .topLeading) {
                        if session.draft.isEmpty {
                            Text(session.connected ? "给 \(session.profile.name) 发消息" : "正在连接…")
                                .font(.system(size: 14))
                                .foregroundStyle(Theme.fg4)
                                .padding(.leading, 5)
                                .padding(.top, 4)
                                .allowsHitTesting(false)
                        }
                        ComposerTextView(text: $session.draft, height: $height, focusTrigger: focus,
                                         onSubmit: send, onPasteImage: { name, data in session.attach(name: name, data: data) })
                            .frame(height: height)
                    }
                    .padding(.vertical, 4)
                    sendButton
                }
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 8)
            .background(Theme.composer, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).strokeBorder(Theme.line2, lineWidth: 0.5))
            .shadow(color: Theme.shadow.opacity(0.7), radius: 8, y: 3)

            HStack(spacing: 10) {
                Text("Enter 发送 · Shift+Enter 换行 · Esc 停止回复")
                Spacer()
                if session.working || !session.paused.isEmpty {
                    Button { stopAll() } label: { Label("停止全部任务", systemImage: "stop.circle") }
                        .buttonStyle(.plain)
                        .foregroundStyle(Theme.fg4)
                        .help("停止全部任务（包括排队和暂停的）")
                }
            }
            .font(.system(size: 11))
            .foregroundStyle(Theme.fg4)
            .padding(.horizontal, 12)
        }
        .fileImporter(isPresented: $picking, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            if case .success(let urls) = result { session.attach(urls) }
        }
        .onAppear { focus += 1 }
        .onChange(of: session.ref) { focus += 1 }
    }

    private var sendButton: some View {
        Button {
            if showStop { Task { try? await session.stopReply() } } else { send() }
        } label: {
            Image(systemName: showStop ? "stop.fill" : "arrow.up")
                .font(.system(size: showStop ? 11 : 14, weight: .bold))
                .foregroundStyle(canSend || showStop ? Theme.onAccent : Theme.fg4)
                .frame(width: 30, height: 30)
                .background(canSend || showStop ? (showStop ? Theme.fg : Theme.accent) : Theme.s3, in: Circle())
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .disabled(!canSend && !showStop)
        .help(showStop ? "停止回复（Esc）" : "发送（Enter）")
        .overlay { if sending { ProgressView().controlSize(.small) } }
    }

    private var uploadsRow: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                ForEach(session.uploads) { u in
                    HStack(spacing: 6) {
                        if u.uploading {
                            ProgressView().controlSize(.mini)
                        } else if u.error != nil {
                            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(Theme.danger)
                        } else {
                            Image(systemName: u.attachment?.isImage == true ? "photo" : "doc").foregroundStyle(Theme.accent)
                        }
                        VStack(alignment: .leading, spacing: 0) {
                            Text(u.name).lineLimit(1).foregroundStyle(Theme.fg)
                            Text(u.error ?? Fmt.size(u.size)).lineLimit(1).foregroundStyle(u.error == nil ? Theme.fg4 : Theme.danger)
                        }
                        .frame(maxWidth: 180, alignment: .leading)
                        Button { session.uploads.removeAll { $0.id == u.id } } label: { Image(systemName: "xmark") }
                            .buttonStyle(IconButtonStyle(size: 18))
                    }
                    .font(.system(size: 11.5))
                    .padding(.leading, 8)
                    .padding(.trailing, 4)
                    .padding(.vertical, 4)
                    .background(Theme.s2, in: RoundedRectangle(cornerRadius: 9, style: .continuous))
                }
            }
        }
    }

    private func send() {
        guard canSend else { return }
        let text = session.draft.trimmingCharacters(in: .whitespacesAndNewlines)
        let files = session.uploads.compactMap(\.attachment)
        let savedDraft = session.draft
        let savedUploads = session.uploads
        session.draft = ""
        session.uploads = []
        sending = true
        Task {
            do {
                try await session.send(text, files: files)
            } catch {
                // 发送失败：把草稿和附件放回去
                if session.draft.isEmpty { session.draft = savedDraft }
                if session.uploads.isEmpty { session.uploads = savedUploads }
                onError(error.localizedDescription)
            }
            sending = false
        }
    }

    private func stopAll() {
        let n = session.tasks.values.filter(\.isOpen).count
        if n > 1 {
            let alert = NSAlert()
            alert.messageText = "停止全部 \(n) 个任务？"
            alert.informativeText = "包括排队和暂停的任务。"
            alert.addButton(withTitle: "全部停止")
            alert.addButton(withTitle: "取消")
            guard alert.runModal() == .alertFirstButtonReturn else { return }
        }
        Task {
            do { try await session.stopAll() } catch { onError(error.localizedDescription) }
        }
    }
}

/// 包一层 NSTextView：自动长高、Enter 发送、Shift/Option+Enter 换行、粘贴图片变成附件。
@MainActor
struct ComposerTextView: NSViewRepresentable {
    @Binding var text: String
    @Binding var height: CGFloat
    var focusTrigger: Int
    var onSubmit: () -> Void
    var onPasteImage: (String, Data) -> Void

    static let lineHeight: CGFloat = 19
    static let maxLines: CGFloat = 8

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSScrollView()
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.borderType = .noBorder

        let tv = PastingTextView(frame: NSRect(x: 0, y: 0, width: 400, height: ComposerTextView.lineHeight + 4))
        tv.minSize = NSSize(width: 0, height: 0)
        tv.maxSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: CGFloat.greatestFiniteMagnitude)
        tv.onPasteImage = onPasteImage
        tv.delegate = context.coordinator
        tv.isRichText = false
        tv.importsGraphics = false
        tv.allowsUndo = true
        tv.drawsBackground = false
        tv.font = .systemFont(ofSize: 14)
        tv.textColor = .labelColor
        tv.insertionPointColor = NSColor(Theme.accent)
        tv.textContainerInset = NSSize(width: 0, height: 2)
        tv.isAutomaticQuoteSubstitutionEnabled = false
        tv.isAutomaticDashSubstitutionEnabled = false
        tv.isAutomaticTextReplacementEnabled = false
        tv.isVerticallyResizable = true
        tv.isHorizontallyResizable = false
        tv.autoresizingMask = [.width]
        tv.textContainer?.widthTracksTextView = true
        tv.textContainer?.containerSize = NSSize(width: 400, height: CGFloat.greatestFiniteMagnitude)
        tv.string = text
        scroll.documentView = tv
        context.coordinator.textView = tv
        // 宽度变了（窗口缩放）要重新算高度
        tv.postsFrameChangedNotifications = true
        NotificationCenter.default.addObserver(context.coordinator, selector: #selector(Coordinator.frameChanged(_:)),
                                               name: NSView.frameDidChangeNotification, object: tv)
        DispatchQueue.main.async {
            tv.window?.makeFirstResponder(tv)
            context.coordinator.recalc()
        }
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        context.coordinator.parent = self
        guard let tv = context.coordinator.textView else { return }
        tv.onPasteImage = onPasteImage
        if tv.string != text && !tv.hasMarkedText() {
            tv.string = text
            context.coordinator.recalc()
        }
        if focusTrigger != context.coordinator.lastFocus {
            context.coordinator.lastFocus = focusTrigger
            DispatchQueue.main.async { tv.window?.makeFirstResponder(tv) }
        }
    }

    @MainActor
    final class Coordinator: NSObject, NSTextViewDelegate {
        var parent: ComposerTextView
        weak var textView: PastingTextView?
        var lastFocus = 0

        private var lastWidth: CGFloat = 0

        init(_ parent: ComposerTextView) { self.parent = parent }

        @objc func frameChanged(_ note: Notification) {
            guard let tv = textView, abs(tv.frame.width - lastWidth) > 0.5 else { return }
            lastWidth = tv.frame.width
            recalc()
        }

        func textDidChange(_ notification: Notification) {
            guard let tv = textView else { return }
            parent.text = tv.string
            recalc()
        }

        func textView(_ textView: NSTextView, doCommandBy selector: Selector) -> Bool {
            if selector == #selector(NSResponder.insertNewline(_:)) {
                let flags = NSApp.currentEvent?.modifierFlags ?? []
                if flags.contains(.shift) || flags.contains(.option) || flags.contains(.control) {
                    textView.insertNewlineIgnoringFieldEditor(nil)
                } else {
                    parent.onSubmit()
                }
                return true
            }
            return false
        }

        func recalc() {
            guard let tv = textView, let lm = tv.layoutManager, let tc = tv.textContainer else { return }
            lm.ensureLayout(for: tc)
            let used = lm.usedRect(for: tc).height + tv.textContainerInset.height * 2
            let h = min(max(used, ComposerTextView.lineHeight + 3), ComposerTextView.lineHeight * ComposerTextView.maxLines + 4)
            if abs(parent.height - h) > 0.5 {
                DispatchQueue.main.async { self.parent.height = h }
            }
        }
    }
}

/// 粘贴截图时把图片变成附件，其余照常粘贴文字。
final class PastingTextView: NSTextView {
    var onPasteImage: ((String, Data) -> Void)?

    override func paste(_ sender: Any?) {
        let pb = NSPasteboard.general
        // 复制的文件（Finder 里 ⌘C）
        if let urls = pb.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL], !urls.isEmpty {
            for url in urls {
                if let data = try? Data(contentsOf: url) { onPasteImage?(url.lastPathComponent, data) }
            }
            return
        }
        // 截图 / 图片数据
        if pb.string(forType: .string) == nil,
           let img = NSImage(pasteboard: pb),
           let tiff = img.tiffRepresentation,
           let rep = NSBitmapImageRep(data: tiff),
           let png = rep.representation(using: .png, properties: [:]) {
            let f = DateFormatter()
            f.dateFormat = "yyyyMMdd-HHmmss"
            onPasteImage?("截图-\(f.string(from: Date())).png", png)
            return
        }
        super.paste(sender)
    }
}
