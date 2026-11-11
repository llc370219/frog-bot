import PhotosUI
import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// 输入框：多行文字、附件（照片 / 文件）、发送；助理正在回复时，空输入框的发送键变成停止键。
struct Composer: View {
    let session: BotSession
    var onError: (String) -> Void

    @FocusState private var focused: Bool
    @State private var photos: [PhotosPickerItem] = []
    @State private var pickingPhotos = false
    @State private var pickingFiles = false
    @State private var sending = false

    private var text: String { session.draft }
    private var canSend: Bool {
        let hasText = !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let ready = session.uploads.allSatisfy { !$0.uploading }
        return (hasText || session.uploads.contains { $0.attachment != nil }) && ready && !sending
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !session.uploads.isEmpty { uploadsRow }
            HStack(alignment: .bottom, spacing: 8) {
                attachMenu
                TextField(placeholder, text: Binding(get: { session.draft }, set: { session.draft = $0 }), axis: .vertical)
                    .font(.system(size: 16))
                    .lineLimit(1...6)
                    .focused($focused)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 9)
                    .background(Theme.field, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: 20, style: .continuous).strokeBorder(Theme.line2, lineWidth: 0.5))
                sendButton
            }
        }
        .photosPicker(isPresented: $pickingPhotos, selection: $photos, maxSelectionCount: 8, matching: .any(of: [.images, .screenshots]))
        .onChange(of: photos) { loadPhotos() }
        .fileImporter(isPresented: $pickingFiles, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            switch result {
            case .success(let urls): attachFiles(urls)
            case .failure(let e): onError(e.localizedDescription)
            }
        }
    }

    private var placeholder: String {
        session.server.isOnline ? "发消息给 \(session.profile.name)" : "服务器不在线"
    }

    private var attachMenu: some View {
        Menu {
            Button { pickingPhotos = true } label: { Label("照片", systemImage: "photo.on.rectangle") }
            Button { pickingFiles = true } label: { Label("文件", systemImage: "folder") }
            if UIPasteboard.general.hasImages {
                Button {
                    if let img = UIPasteboard.general.image, let data = img.jpegData(compressionQuality: 0.9) {
                        session.attach(name: "粘贴的图片.jpg", data: data)
                    }
                } label: { Label("粘贴图片", systemImage: "doc.on.clipboard") }
            }
        } label: {
            Image(systemName: "plus")
                .font(.system(size: 18, weight: .medium))
                .foregroundStyle(Theme.fg2)
                .frame(width: 38, height: 38)
                .background(Theme.s2, in: Circle())
        }
        .accessibilityLabel("添加附件")
    }

    @ViewBuilder
    private var sendButton: some View {
        if session.busy && !canSend {
            Button { Haptics.tap(); Task { try? await session.stopReply() } } label: {
                Image(systemName: "stop.fill")
                    .font(.system(size: 14, weight: .bold))
                    .foregroundStyle(Theme.canvas)
                    .frame(width: 38, height: 38)
                    .background(Theme.fg, in: Circle())
            }
            .accessibilityLabel("停止回复")
        } else {
            Button(action: send) {
                Group {
                    if sending { ProgressView().tint(Theme.onAccent) } else {
                        Image(systemName: "arrow.up").font(.system(size: 17, weight: .bold))
                    }
                }
                .foregroundStyle(Theme.onAccent)
                .frame(width: 38, height: 38)
                .background(canSend ? Theme.accent : Theme.fg4.opacity(0.5), in: Circle())
            }
            .disabled(!canSend)
            .accessibilityLabel("发送")
        }
    }

    private var uploadsRow: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(session.uploads) { u in
                    HStack(spacing: 6) {
                        if u.uploading {
                            ProgressView().controlSize(.small)
                        } else if u.error != nil {
                            Image(systemName: "exclamationmark.circle.fill").foregroundStyle(Theme.danger)
                        } else {
                            Image(systemName: "paperclip").foregroundStyle(Theme.accent)
                        }
                        VStack(alignment: .leading, spacing: 0) {
                            Text(u.name).font(.system(size: 13, weight: .medium)).lineLimit(1)
                            Text(u.error ?? Fmt.size(u.size)).font(.system(size: 11)).foregroundStyle(u.error == nil ? Theme.fg4 : Theme.danger).lineLimit(1)
                        }
                        .frame(maxWidth: 160, alignment: .leading)
                        Button { session.uploads.removeAll { $0.id == u.id } } label: {
                            Image(systemName: "xmark.circle.fill").foregroundStyle(Theme.fg4)
                        }
                        .buttonStyle(.plain)
                    }
                    .padding(.horizontal, 10)
                    .padding(.vertical, 7)
                    .background(Theme.s2, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
            }
        }
    }

    private func send() {
        guard canSend else { return }
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let files = session.uploads.compactMap(\.attachment)
        let keepDraft = session.draft
        let keepUploads = session.uploads
        session.draft = ""
        session.uploads = []
        sending = true
        Haptics.tap()
        Task {
            do {
                try await session.send(t, files: files)
            } catch {
                session.draft = keepDraft
                session.uploads = keepUploads
                onError(error.localizedDescription)
            }
            sending = false
        }
    }

    private func loadPhotos() {
        let items = photos
        photos = []
        for (i, item) in items.enumerated() {
            Task {
                guard let data = try? await item.loadTransferable(type: Data.self) else {
                    onError("读不到这张照片")
                    return
                }
                // HEIC 等格式统一转成 JPEG，服务器和模型都认
                let jpeg = UIImage(data: data)?.jpegData(compressionQuality: 0.85) ?? data
                let stamp = Int(Date().timeIntervalSince1970)
                session.attach(name: "照片-\(stamp)-\(i + 1).jpg", data: jpeg)
            }
        }
    }

    private func attachFiles(_ urls: [URL]) {
        for url in urls {
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            do {
                let data = try Data(contentsOf: url)
                session.attach(name: url.lastPathComponent, data: data)
            } catch {
                onError("读不到「\(url.lastPathComponent)」：\(error.localizedDescription)")
            }
        }
    }
}
