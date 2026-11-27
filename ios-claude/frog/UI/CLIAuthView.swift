import SafariServices
import SwiftUI
import UIKit

// MARK: - Models

struct CLIAuthSessionViewModel: Codable, Hashable, Identifiable {
    let id: String
    let cli: String
    let mode: String
    let status: String
    let auth_url: String?
    let user_code: String?
    let needs_input: Bool
    let input_prompt: String?
    let output: String?
    let error: String?
}

struct CLIAuthStatusItem: Codable, Hashable, Identifiable {
    let id: String
    let name: String
    let installed: Bool
    let path: String?
    let logged_in: Bool
    let auth_method: String?
    let account_hint: String?
    let supports_device_auth: Bool
    let supports_oauth_bridge: Bool
    let supports_oauth_token: Bool
    let oauth_token_env: String?
    let api_key_env: String
    let login_hint: String
    let install_hint: String
    let active_session: CLIAuthSessionViewModel?
}

private struct SafariURLItem: Identifiable {
    let id = UUID()
    let url: URL
}

private struct InAppSafariView: UIViewControllerRepresentable {
    let url: URL
    func makeUIViewController(context: Context) -> SFSafariViewController {
        let vc = SFSafariViewController(url: url)
        vc.dismissButtonStyle = .done
        return vc
    }
    func updateUIViewController(_ uiViewController: SFSafariViewController, context: Context) {}
}

// MARK: - 主列表：VPS / 本机官方 CLI 账号登录（Codex / Claude / Gemini / Grok）

/// 专门用于登录远端 VPS 或本机上的官方 Coding CLI（非前置 Bot 反代）。
struct CLIAuthView: View {
    let server: ServerEntry

    @State private var items: [CLIAuthStatusItem] = []
    @State private var loading = true
    @State private var error: String?
    @State private var selectedCLI: CLIAuthStatusItem?

    var body: some View {
        List {
            Section {
                Text("在这里直接完成远端主机上的官方 CLI 登录（Codex / Claude Code / Gemini CLI / Grok CLI）。登录凭证直接保存在官方 CLI 原生目录或宿主加密密钥库，与前置 Bot 账号反代完全独立。")
                    .font(.footnote)
                    .foregroundStyle(Theme.fg3)
            }

            if let error {
                Section {
                    Label(error, systemImage: "exclamationmark.circle.fill")
                        .foregroundStyle(Theme.danger)
                }
            }

            Section("官方 Coding CLI") {
                if loading && items.isEmpty {
                    HStack(spacing: 10) {
                        ProgressView()
                        Text("正在检测主机上的官方 CLI…").foregroundStyle(Theme.fg3)
                    }
                } else {
                    ForEach(items) { item in
                        Button {
                            Haptics.tap()
                            selectedCLI = item
                        } label: {
                            HStack(spacing: 12) {
                                Image(systemName: "terminal.fill")
                                    .font(.system(size: 15))
                                    .foregroundStyle(item.logged_in ? Theme.ok : (item.installed ? Theme.accent : Theme.fg4))
                                    .frame(width: 32, height: 32)
                                    .background(
                                        (item.logged_in ? Theme.ok : Theme.accent).opacity(0.12),
                                        in: RoundedRectangle(cornerRadius: 8, style: .continuous)
                                    )
                                VStack(alignment: .leading, spacing: 3) {
                                    HStack(spacing: 6) {
                                        Text(item.name)
                                            .font(.system(size: 15.5, weight: .semibold))
                                            .foregroundStyle(Theme.fg)
                                        if item.logged_in {
                                            Chip(text: "已登录", tint: Theme.ok, fill: Theme.ok.opacity(0.14))
                                        } else if item.installed {
                                            Chip(text: "未登录", tint: Theme.warn, fill: Theme.warnSoft)
                                        } else {
                                            Chip(text: "未安装", tint: Theme.fg4, fill: Theme.s2)
                                        }
                                    }
                                    if let hint = item.account_hint, !hint.isEmpty {
                                        Text(hint)
                                            .font(.caption)
                                            .foregroundStyle(Theme.fg3)
                                            .lineLimit(1)
                                    } else if !item.installed {
                                        Text(item.install_hint)
                                            .font(.system(size: 11.5, design: .monospaced))
                                            .foregroundStyle(Theme.fg4)
                                            .lineLimit(1)
                                    } else {
                                        Text("点击进行设备码 / OAuth 或密钥登录")
                                            .font(.caption)
                                            .foregroundStyle(Theme.fg3)
                                    }
                                }
                                Spacer()
                                Image(systemName: "chevron.right")
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(Theme.fg4)
                            }
                            .padding(.vertical, 2)
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle("官方 CLI 登录")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button { Task { await refresh() } } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .disabled(loading)
            }
        }
        .refreshable { await refresh() }
        .task { await refresh() }
        .sheet(item: $selectedCLI) { item in
            CLIAuthDetailSheet(server: server, initialItem: item) {
                Task { await refresh() }
            }
        }
    }

    private func refresh() async {
        loading = true
        error = nil
        do {
            items = try await server.client.get("/api/v2/cli/auth")
        } catch {
            self.error = error.localizedDescription
        }
        loading = false
    }
}

// MARK: - 单个官方 CLI 的混合无头登录弹窗（设备码 + OAuth 回传 + Token/API Key）

private struct CLIAuthDetailSheet: View {
    @Environment(\.dismiss) private var dismiss
    let server: ServerEntry
    let initialItem: CLIAuthStatusItem
    let onChanged: () -> Void

    @State private var item: CLIAuthStatusItem
    @State private var session: CLIAuthSessionViewModel?
    @State private var pollTask: Task<Void, Never>?
    @State private var busy = false
    @State private var pasteInput = ""
    @State private var credKind = "api_key"
    @State private var credValue = ""
    @State private var copiedCode = false
    @State private var safariItem: SafariURLItem?
    @State private var notice: (text: String, isError: Bool)?
    @State private var confirmLogout = false

    init(server: ServerEntry, initialItem: CLIAuthStatusItem, onChanged: @escaping () -> Void) {
        self.server = server
        self.initialItem = initialItem
        self.onChanged = onChanged
        self._item = State(initialValue: initialItem)
        self._session = State(initialValue: initialItem.active_session)
        self._credKind = State(initialValue: initialItem.supports_oauth_token ? "oauth_token" : "api_key")
    }

    private var isSessionActive: Bool {
        guard let s = session else { return false }
        return s.status == "running" || s.status == "waiting_user"
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    LabeledContent("状态") {
                        Text(item.logged_in ? "已登录 (\(item.auth_method ?? "oauth"))" : (item.installed ? "未登录" : "主机未安装"))
                            .foregroundStyle(item.logged_in ? Theme.ok : Theme.warn)
                    }
                    if let hint = item.account_hint, !hint.isEmpty {
                        LabeledContent("凭证") {
                            Text(hint).font(.caption).foregroundStyle(Theme.fg3)
                        }
                    }
                    if !item.installed {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("主机安装命令").font(.caption).foregroundStyle(Theme.fg3)
                            Text(item.install_hint)
                                .font(.system(size: 12.5, design: .monospaced))
                                .textSelection(.enabled)
                        }
                    }
                } footer: {
                    Text(item.login_hint)
                }

                if let notice {
                    Section {
                        Label(notice.text, systemImage: notice.isError ? "exclamationmark.circle.fill" : "checkmark.circle.fill")
                            .foregroundStyle(notice.isError ? Theme.danger : Theme.ok)
                    }
                }

                // 正在进行的无头登录会话卡片
                if let sess = session, (sess.status == "running" || sess.status == "waiting_user") {
                    Section("正在进行官方授权") {
                        HStack(spacing: 8) {
                            ProgressView()
                            Text(sess.mode == "device" ? "等待在浏览器输入设备验证码…" : "等待浏览器授权并回传…")
                                .font(.subheadline)
                                .foregroundStyle(Theme.fg2)
                        }

                        if let code = sess.user_code, !code.isEmpty {
                            VStack(alignment: .leading, spacing: 8) {
                                Text("一次性验证码").font(.caption).foregroundStyle(Theme.fg3)
                                HStack {
                                    Text(code)
                                        .font(.system(size: 24, weight: .bold, design: .monospaced))
                                        .foregroundStyle(Theme.accent)
                                        .textSelection(.enabled)
                                    Spacer()
                                    Button {
                                        UIPasteboard.general.string = code
                                        Haptics.tap()
                                        copiedCode = true
                                    } label: {
                                        Label(copiedCode ? "已复制" : "复制", systemImage: copiedCode ? "checkmark" : "doc.on.doc")
                                    }
                                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                                }
                            }
                            .padding(.vertical, 4)
                        }

                        if let urlStr = sess.auth_url, let url = URL(string: urlStr) {
                            Button {
                                Haptics.tap()
                                if sess.needs_input {
                                    // 需要从地址栏复制 localhost 回调 URL 时，系统 Safari 复制地址栏更方便
                                    UIApplication.shared.open(url)
                                } else {
                                    safariItem = SafariURLItem(url: url)
                                }
                            } label: {
                                Label(sess.needs_input ? "在 Safari 打开授权页（便于复制回调地址）" : "打开官方授权页面",
                                      systemImage: "safari.fill")
                                    .frame(maxWidth: .infinity)
                            }
                            .buttonStyle(PillButtonStyle(kind: .primary))

                            Button {
                                UIPasteboard.general.string = urlStr
                                Haptics.tap()
                                notice = ("已复制官方登录链接", false)
                            } label: {
                                Label("复制授权链接", systemImage: "link")
                            }
                        }

                        if sess.needs_input {
                            VStack(alignment: .leading, spacing: 8) {
                                if let prompt = sess.input_prompt, !prompt.isEmpty {
                                    Text(prompt).font(.caption).foregroundStyle(Theme.fg3)
                                }
                                TextField("粘贴授权码或 http://localhost:xxxx/... 回调 URL", text: $pasteInput, axis: .vertical)
                                    .font(.system(size: 13, design: .monospaced))
                                    .textInputAutocapitalization(.never)
                                    .autocorrectionDisabled()
                                    .lineLimit(2...4)
                                HStack(spacing: 10) {
                                    Button {
                                        if let clip = UIPasteboard.general.string?.trimmingCharacters(in: .whitespacesAndNewlines), !clip.isEmpty {
                                            pasteInput = clip
                                            Haptics.tap()
                                        }
                                    } label: {
                                        Label("粘贴剪贴板", systemImage: "doc.on.clipboard")
                                    }
                                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))

                                    Spacer()

                                    Button {
                                        submitInput(sessionID: sess.id)
                                    } label: {
                                        Label("回传完成", systemImage: "paperplane.fill")
                                    }
                                    .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
                                    .disabled(pasteInput.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || busy)
                                }
                            }
                            .padding(.vertical, 4)
                        }

                        if let out = sess.output, !out.isEmpty {
                            DisclosureGroup("查看 CLI 终端输出") {
                                Text(out)
                                    .font(.system(size: 11, design: .monospaced))
                                    .foregroundStyle(Theme.fg3)
                                    .textSelection(.enabled)
                            }
                        }

                        Button("取消本次登录", role: .destructive) {
                            cancelSession(sess.id)
                        }
                    }
                }

                // 方式一：官方无头交互登录
                if item.installed {
                    Section("方式一：官方账号无头登录") {
                        if item.supports_device_auth {
                            Button {
                                startLogin(mode: "device")
                            } label: {
                                HStack {
                                    Label("发起设备码登录（推荐）", systemImage: "qrcode")
                                    Spacer()
                                    if busy { ProgressView() }
                                }
                            }
                            .disabled(busy || isSessionActive)
                        }
                        if item.supports_oauth_bridge {
                            Button {
                                startLogin(mode: "oauth")
                            } label: {
                                HStack {
                                    Label("发起 OAuth 链接登录（支持回调回放）", systemImage: "safari")
                                    Spacer()
                                    if busy { ProgressView() }
                                }
                            }
                            .disabled(busy || isSessionActive)
                        }
                    }
                }

                // 方式二：直接导入官方 Token / API Key
                Section {
                    if item.supports_oauth_token {
                        Picker("凭证类型", selection: $credKind) {
                            Text("\(item.oauth_token_env ?? "OAuth Token") (setup-token)").tag("oauth_token")
                            Text(item.api_key_env).tag("api_key")
                        }
                        .pickerStyle(.segmented)
                    }
                    SecureField(
                        "粘贴 \(credKind == "oauth_token" ? (item.oauth_token_env ?? "OAuth Token") : item.api_key_env)",
                        text: $credValue
                    )
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()

                    Button {
                        saveCredential()
                    } label: {
                        HStack {
                            Label("加密保存到主机", systemImage: "lock.fill")
                            Spacer()
                            if busy { ProgressView() }
                        }
                    }
                    .disabled(credValue.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || busy)
                } header: {
                    Text("方式二：官方 Token / API Key")
                } footer: {
                    Text("保存后由宿主密钥库加密托管，运行该官方 CLI 时自动注入环境变量，不会暴露给对话上下文。")
                }

                if item.logged_in {
                    Section {
                        Button("退出 \(item.name) 登录…", role: .destructive) {
                            confirmLogout = true
                        }
                        .disabled(busy)
                    }
                }
            }
            .navigationTitle(item.name)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("关闭") { dismiss() }
                }
            }
            .sheet(item: $safariItem) { u in
                InAppSafariView(url: u.url).ignoresSafeArea()
            }
            .confirmationDialog("退出「\(item.name)」官方登录？", isPresented: $confirmLogout, titleVisibility: .visible) {
                Button("清除本机官方凭证", role: .destructive) { logout() }
            } message: {
                Text("会清除宿主密钥库中保存的该 CLI 密钥以及本机原生登录文件。")
            }
            .onAppear {
                if isSessionActive { startPolling() }
            }
            .onDisappear {
                pollTask?.cancel()
            }
        }
    }

    private func startLogin(mode: String) {
        busy = true
        notice = nil
        pasteInput = ""
        copiedCode = false
        Task {
            do {
                let sess: CLIAuthSessionViewModel = try await server.client.send(
                    "POST",
                    "/api/v2/cli/auth/\(item.id)/start",
                    body: ["mode": mode]
                )
                session = sess
                startPolling()
            } catch {
                notice = (error.localizedDescription, true)
                Haptics.error()
            }
            busy = false
        }
    }

    private func startPolling() {
        pollTask?.cancel()
        pollTask = Task {
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 1_800_000_000)
                guard !Task.isCancelled, let sid = session?.id else { return }
                do {
                    let next: CLIAuthSessionViewModel = try await server.client.get("/api/v2/cli/auth/sessions/\(sid)")
                    session = next
                    if next.status == "succeeded" {
                        Haptics.success()
                        notice = ("\(item.name) 官方登录成功！", false)
                        await reloadItem()
                        onChanged()
                        return
                    } else if next.status == "failed" {
                        Haptics.error()
                        notice = (next.error ?? "登录失败，请检查输出后重试", true)
                        return
                    } else if next.status == "cancelled" {
                        return
                    }
                } catch {
                    // 忽略单次轮询网络抖动
                }
            }
        }
    }

    private func submitInput(sessionID: String) {
        let trimmed = pasteInput.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        busy = true
        notice = nil
        Task {
            do {
                let next: CLIAuthSessionViewModel = try await server.client.send(
                    "POST",
                    "/api/v2/cli/auth/sessions/\(sessionID)/input",
                    body: ["input": trimmed]
                )
                session = next
                pasteInput = ""
                if next.status == "succeeded" {
                    Haptics.success()
                    notice = ("\(item.name) 官方登录已完成！", false)
                    await reloadItem()
                    onChanged()
                } else {
                    notice = ("已回传给 VPS，等待官方 CLI 校验完成…", false)
                }
            } catch {
                notice = (error.localizedDescription, true)
                Haptics.error()
            }
            busy = false
        }
    }

    private func cancelSession(_ sid: String) {
        pollTask?.cancel()
        Task {
            _ = try? await server.client.post("/api/v2/cli/auth/sessions/\(sid)/cancel")
            session = nil
        }
    }

    private func saveCredential() {
        let val = credValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !val.isEmpty else { return }
        busy = true
        notice = nil
        Task {
            do {
                let updated: CLIAuthStatusItem = try await server.client.send(
                    "POST",
                    "/api/v2/cli/auth/\(item.id)/credential",
                    body: ["kind": credKind, "value": val]
                )
                item = updated
                credValue = ""
                Haptics.success()
                notice = ("已加密保存，运行 \(item.name) 时自动生效。", false)
                onChanged()
            } catch {
                notice = (error.localizedDescription, true)
                Haptics.error()
            }
            busy = false
        }
    }

    private func logout() {
        busy = true
        notice = nil
        Task {
            do {
                let updated: CLIAuthStatusItem = try await server.client.send(
                    "POST",
                    "/api/v2/cli/auth/\(item.id)/logout",
                    body: ["clear_native": true]
                )
                item = updated
                session = nil
                Haptics.success()
                notice = ("已退出 \(item.name) 登录。", false)
                onChanged()
            } catch {
                notice = (error.localizedDescription, true)
                Haptics.error()
            }
            busy = false
        }
    }

    private func reloadItem() async {
        if let list: [CLIAuthStatusItem] = try? await server.client.get("/api/v2/cli/auth"),
           let fresh = list.first(where: { $0.id == item.id }) {
            item = fresh
        }
    }
}
