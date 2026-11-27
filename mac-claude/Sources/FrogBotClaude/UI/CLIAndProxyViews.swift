import AppKit
import SwiftUI

// MARK: - 官方 Coding CLI 登录模型

struct MacCLIAuthSession: Codable, Hashable, Identifiable {
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

struct MacCLIAuthItem: Codable, Hashable, Identifiable {
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
    let active_session: MacCLIAuthSession?
}

// MARK: - 官方 CLI 登录面板（Codex / Claude Code / Gemini CLI / Grok CLI）

@MainActor
struct CLIAuthPane: View {
    let server: ServerEntry

    @State private var items: [MacCLIAuthItem] = []
    @State private var loading = true
    @State private var error: String?
    @State private var selected: MacCLIAuthItem?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            PaneTitle(
                title: "官方 CLI 账号登录",
                desc: "直接登录这台主机上的官方 Coding CLI（Codex / Claude Code / Gemini / Grok）。凭证仅写入官方原生目录或主机加密密钥库，与前置 Bot 账号反代完全独立。"
            )

            HStack {
                Spacer()
                Button("刷新状态") { Task { await refresh() } }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                    .disabled(loading)
            }
            .padding(.top, 10)

            if let error {
                Caption(text: error, tone: Theme.danger)
            }

            if loading && items.isEmpty {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("正在检测主机官方 CLI…").font(.system(size: 12.5)).foregroundStyle(Theme.fg3)
                }
                .padding(.top, 20)
            } else {
                GroupHeader(text: "已支持 CLI")
                FormGroup {
                    ForEach(Array(items.enumerated()), id: \.element.id) { idx, item in
                        if idx > 0 { RowDivider() }
                        HStack(spacing: 12) {
                            Image(systemName: "terminal.fill")
                                .font(.system(size: 14))
                                .foregroundStyle(item.logged_in ? Theme.ok : (item.installed ? Theme.accent : Theme.fg4))
                                .frame(width: 28, height: 28)
                                .background((item.logged_in ? Theme.ok : Theme.accent).opacity(0.12), in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                            VStack(alignment: .leading, spacing: 2) {
                                HStack(spacing: 6) {
                                    Text(item.name).font(.system(size: 13, weight: .semibold))
                                    if item.logged_in {
                                        Chip(text: "已登录", tint: Theme.ok, fill: Theme.ok.opacity(0.14))
                                    } else if item.installed {
                                        Chip(text: "未登录", tint: Theme.warn, fill: Theme.warnSoft)
                                    } else {
                                        Chip(text: "未安装", tint: Theme.fg4, fill: Theme.s2)
                                    }
                                }
                                Text(item.account_hint?.isEmpty == false ? item.account_hint! : (item.installed ? item.login_hint : item.install_hint))
                                    .font(.system(size: 11.5))
                                    .foregroundStyle(Theme.fg3)
                                    .lineLimit(1)
                            }
                            Spacer(minLength: 8)
                            Button(item.logged_in ? "管理…" : "登录…") {
                                selected = item
                            }
                            .buttonStyle(PillButtonStyle(kind: item.logged_in ? .secondary : .primary, compact: true))
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 10)
                    }
                }
            }
        }
        .task(id: server.id) { await refresh() }
        .sheet(item: $selected) { item in
            MacCLIAuthDetailSheet(server: server, initialItem: item) {
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

@MainActor
struct CLIAuthModalSheet: View {
    @Environment(\.dismiss) private var dismiss
    let server: ServerEntry

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("官方 CLI 登录 · \(server.name)")
                    .font(.system(size: 14, weight: .semibold))
                Spacer()
                Button("完成") { dismiss() }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
            Rectangle().fill(Theme.line).frame(height: 0.5)
            ScrollView {
                CLIAuthPane(server: server)
                    .padding(.horizontal, 22)
                    .padding(.bottom, 22)
            }
            .background(Theme.settingsBG)
        }
        .frame(width: 600, height: 520)
    }
}

@MainActor
private struct MacCLIAuthDetailSheet: View {
    @Environment(\.dismiss) private var dismiss
    let server: ServerEntry
    let initialItem: MacCLIAuthItem
    let onChanged: () -> Void

    @State private var item: MacCLIAuthItem
    @State private var session: MacCLIAuthSession?
    @State private var pollTask: Task<Void, Never>?
    @State private var busy = false
    @State private var pasteInput = ""
    @State private var credKind: String
    @State private var credValue = ""
    @State private var copiedCode = false
    @State private var notice: (text: String, isError: Bool)?

    init(server: ServerEntry, initialItem: MacCLIAuthItem, onChanged: @escaping () -> Void) {
        self.server = server
        self.initialItem = initialItem
        self.onChanged = onChanged
        _item = State(initialValue: initialItem)
        _session = State(initialValue: initialItem.active_session)
        _credKind = State(initialValue: initialItem.supports_oauth_token ? "oauth_token" : "api_key")
    }

    private var isActive: Bool {
        guard let s = session else { return false }
        return s.status == "running" || s.status == "waiting_user"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text("\(item.name) 官方登录")
                    .font(.system(size: 15, weight: .semibold))
                Spacer()
                if item.logged_in {
                    Chip(text: "已登录", tint: Theme.ok, fill: Theme.ok.opacity(0.14))
                }
                Button("关闭") { dismiss() }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
            }

            Text(item.login_hint)
                .font(.system(size: 12))
                .foregroundStyle(Theme.fg3)

            if !item.installed {
                HStack(spacing: 8) {
                    Text(item.install_hint)
                        .font(.system(size: 11.5, design: .monospaced))
                        .textSelection(.enabled)
                    Spacer()
                    Button("复制安装命令") { copy(item.install_hint) }
                        .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                }
                .padding(10)
                .background(Theme.code, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
            }

            if let notice {
                Label(notice.text, systemImage: notice.isError ? "exclamationmark.circle.fill" : "checkmark.circle.fill")
                    .font(.system(size: 12.5))
                    .foregroundStyle(notice.isError ? Theme.danger : Theme.ok)
            }

            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if let sess = session, isActive {
                        VStack(alignment: .leading, spacing: 10) {
                            HStack(spacing: 8) {
                                ProgressView().controlSize(.small)
                                Text(sess.mode == "device" ? "等待在浏览器输入一次性设备码…" : "等待浏览器授权并回传…")
                                    .font(.system(size: 13, weight: .medium))
                            }
                            if let code = sess.user_code, !code.isEmpty {
                                HStack {
                                    Text(code)
                                        .font(.system(size: 22, weight: .bold, design: .monospaced))
                                        .foregroundStyle(Theme.accent)
                                        .textSelection(.enabled)
                                    Spacer()
                                    Button(copiedCode ? "已复制" : "复制验证码") {
                                        copy(code)
                                        copiedCode = true
                                    }
                                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                                }
                                .padding(10)
                                .background(Theme.code, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                            }
                            if let urlStr = sess.auth_url, let url = URL(string: urlStr) {
                                HStack(spacing: 8) {
                                    Button("在浏览器中打开授权页") { NSWorkspace.shared.open(url) }
                                        .buttonStyle(PillButtonStyle(kind: .primary))
                                    Button("复制链接") {
                                        copy(urlStr)
                                        notice = ("已复制官方登录链接", false)
                                    }
                                    .buttonStyle(PillButtonStyle(kind: .secondary))
                                }
                            }
                            if sess.needs_input {
                                if let p = sess.input_prompt, !p.isEmpty {
                                    Text(p).font(.system(size: 11.5)).foregroundStyle(Theme.fg3)
                                }
                                TextField("粘贴授权码或 http://localhost:xxxx/... 回调地址", text: $pasteInput)
                                    .textFieldStyle(.roundedBorder)
                                    .font(.system(size: 12, design: .monospaced))
                                HStack {
                                    Button("提交回传") { submitInput(sessionID: sess.id) }
                                        .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
                                        .disabled(busy || pasteInput.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                                    Spacer()
                                    Button("取消授权") { cancelSession(sessionID: sess.id) }
                                        .buttonStyle(PillButtonStyle(kind: .danger, compact: true))
                                }
                            } else {
                                Button("取消授权") { cancelSession(sessionID: sess.id) }
                                    .buttonStyle(PillButtonStyle(kind: .danger, compact: true))
                            }
                        }
                        .padding(14)
                        .card(fill: Theme.s2, border: Theme.accent.opacity(0.35), radius: 10)
                    } else if item.installed && (item.supports_device_auth || item.supports_oauth_bridge) {
                        HStack(spacing: 10) {
                            if item.supports_device_auth {
                                Button("启动官方设备码登录（推荐）") { startAuth(mode: "device") }
                                    .buttonStyle(PillButtonStyle(kind: .primary))
                                    .disabled(busy)
                            }
                            if item.supports_oauth_bridge {
                                Button("启动浏览器 OAuth 桥接登录") { startAuth(mode: "oauth_bridge") }
                                    .buttonStyle(PillButtonStyle(kind: item.supports_device_auth ? .secondary : .primary))
                                    .disabled(busy)
                            }
                        }
                    }

                    // Token / API Key 加密写入
                    VStack(alignment: .leading, spacing: 8) {
                        Text("直接写入官方 Token / API Key（加密存储于主机）")
                            .font(.system(size: 12.5, weight: .semibold))
                        if item.supports_oauth_token {
                            Picker("", selection: $credKind) {
                                Text("OAuth Token (\(item.oauth_token_env ?? ""))").tag("oauth_token")
                                Text("API Key (\(item.api_key_env))").tag("api_key")
                            }
                            .pickerStyle(.segmented)
                            .labelsHidden()
                        }
                        HStack(spacing: 8) {
                            SecureField("粘贴 \(credKind == "oauth_token" ? (item.oauth_token_env ?? "Token") : item.api_key_env)", text: $credValue)
                                .textFieldStyle(.roundedBorder)
                            Button("加密保存") { saveCredential() }
                                .buttonStyle(PillButtonStyle(kind: .primary))
                                .disabled(busy || credValue.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                        }
                    }
                    .padding(12)
                    .card(fill: Theme.s2, border: Theme.line, radius: 10)

                    if item.logged_in {
                        HStack {
                            Spacer()
                            Button("退出 \(item.name) 登录") { logout() }
                                .buttonStyle(PillButtonStyle(kind: .danger, compact: true))
                                .disabled(busy)
                        }
                    }
                }
            }
        }
        .padding(20)
        .frame(width: 540, height: 470)
        .onAppear {
            if let s = session, isActive { startPolling(sessionID: s.id) }
        }
        .onDisappear { pollTask?.cancel() }
    }

    private func startAuth(mode: String) {
        busy = true
        notice = nil
        Task {
            do {
                let s: MacCLIAuthSession = try await server.client.send("POST", "/api/v2/cli/auth/\(item.id)/start", body: ["mode": mode])
                session = s
                startPolling(sessionID: s.id)
            } catch {
                notice = (error.localizedDescription, true)
            }
            busy = false
        }
    }

    private func startPolling(sessionID: String) {
        pollTask?.cancel()
        pollTask = Task {
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 1_500_000_000)
                if Task.isCancelled { return }
                do {
                    let s: MacCLIAuthSession = try await server.client.get("/api/v2/cli/auth/sessions/\(sessionID)")
                    session = s
                    if s.status == "succeeded" {
                        notice = ("登录成功！已写入主机官方凭证。", false)
                        onChanged()
                        return
                    } else if s.status == "failed" || s.status == "cancelled" {
                        notice = (s.error ?? "授权未完成（\(s.status)）", true)
                        return
                    }
                } catch {
                    return
                }
            }
        }
    }

    private func submitInput(sessionID: String) {
        let val = pasteInput.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !val.isEmpty else { return }
        busy = true
        Task {
            do {
                try await server.client.post("/api/v2/cli/auth/sessions/\(sessionID)/input", body: ["value": val])
                pasteInput = ""
                notice = ("已回传给主机 CLI，正在完成授权…", false)
            } catch {
                notice = (error.localizedDescription, true)
            }
            busy = false
        }
    }

    private func cancelSession(sessionID: String) {
        pollTask?.cancel()
        Task {
            _ = try? await server.client.post("/api/v2/cli/auth/sessions/\(sessionID)/cancel")
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
                try await server.client.post("/api/v2/cli/auth/\(item.id)/credential", body: ["kind": credKind, "value": val])
                credValue = ""
                notice = ("凭证已加密写入主机。", false)
                onChanged()
            } catch {
                notice = (error.localizedDescription, true)
            }
            busy = false
        }
    }

    private func logout() {
        busy = true
        Task {
            do {
                try await server.client.post("/api/v2/cli/auth/\(item.id)/logout")
                notice = ("已清除登录凭证。", false)
                onChanged()
            } catch {
                notice = (error.localizedDescription, true)
            }
            busy = false
        }
    }
}

// MARK: - 前置 Bot 账号反代网关模型与面板（含强制免责声明）

struct MacProxyEngine: Codable, Hashable, Identifiable {
    let id: String
    let name: String
    let installed: Bool
    let binary_path: String?
    let default_url: String
    let default_args: String?
    let probe_alive: Bool
    let description: String?
}

struct MacProxyConfig: Codable, Hashable {
    var enabled: Bool
    var disclaimer_accepted: Bool
    var engine: String
    var base_url: String
    var channel_name: String
    var auth_required: Bool
    var has_api_key: Bool
    var auto_start: Bool
    var binary_path: String?
    var start_args: String?
}

struct MacProxyStatus: Codable, Hashable {
    let config: MacProxyConfig
    let disclaimer_version: String
    let disclaimer_text: String
    let detected_engines: [MacProxyEngine]
    let managed_running: Bool
    let managed_pid: Int?
    let last_logs: [String]?
    let probe_ok: Bool
    let probe_error: String?
    let live_models: [String]?
}

private struct MacProxySavePayload: Encodable {
    let enabled: Bool
    let disclaimer_accepted: Bool
    let engine: String
    let base_url: String
    let channel_name: String
    let auth_required: Bool
    let api_key: String?
    let auto_start: Bool
    let binary_path: String
    let start_args: String
    let sync_channel: Bool
}

@MainActor
struct ProxyGatewayPane: View {
    let server: ServerEntry

    @State private var status: MacProxyStatus?
    @State private var loading = true
    @State private var busy = false
    @State private var enabled = false
    @State private var accepted = false
    @State private var engine = "cliproxyapi"
    @State private var baseURL = "http://127.0.0.1:8317/v1"
    @State private var channelName = "账号反代 · CLIProxyAPI"
    @State private var authRequired = false
    @State private var apiKey = ""
    @State private var binaryPath = ""
    @State private var startArgs = ""
    @State private var notice: (text: String, isError: Bool)?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            PaneTitle(
                title: "前置 Bot 账号反代网关",
                desc: "可选接入 CLIProxyAPI / OpenCodex / Magpie 等外部开源订阅反代，将其同步为一条独立 API 渠道，与「自定义 API 渠道」完全并存、互不覆盖。"
            )

            if loading && status == nil {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("正在探测主机反代状态…").font(.system(size: 12.5)).foregroundStyle(Theme.fg3)
                }
                .padding(.top, 20)
            } else if let st = status {
                GroupHeader(text: "风险提示与免责声明（必读）")
                VStack(alignment: .leading, spacing: 10) {
                    Text(st.disclaimer_text)
                        .font(.system(size: 12))
                        .foregroundStyle(Theme.fg2)
                        .lineSpacing(3)
                        .fixedSize(horizontal: false, vertical: true)
                    Toggle("我已阅读并同意以上免责声明，自愿承担账号合规与使用风险", isOn: $accepted)
                        .font(.system(size: 12.5, weight: .medium))
                        .disabled(busy)
                }
                .padding(12)
                .card(fill: Theme.warnSoft.opacity(0.55), border: Theme.warn.opacity(0.35), radius: 10)

                if let notice {
                    Caption(text: notice.text, tone: notice.isError ? Theme.danger : Theme.ok)
                }

                GroupHeader(text: "开源反代引擎")
                FormGroup {
                    ForEach(Array(st.detected_engines.enumerated()), id: \.element.id) { idx, eng in
                        if idx > 0 { RowDivider() }
                        Button {
                            engine = eng.id
                            baseURL = eng.default_url
                            channelName = "账号反代 · \(eng.name)"
                            if let p = eng.binary_path, !p.isEmpty { binaryPath = p }
                            startArgs = eng.default_args ?? ""
                        } label: {
                            HStack(spacing: 10) {
                                Image(systemName: engine == eng.id ? "checkmark.circle.fill" : "circle")
                                    .foregroundStyle(engine == eng.id ? Theme.accent : Theme.fg4)
                                VStack(alignment: .leading, spacing: 2) {
                                    HStack(spacing: 6) {
                                        Text(eng.name).font(.system(size: 13, weight: .semibold)).foregroundStyle(Theme.fg)
                                        if eng.probe_alive {
                                            Chip(text: "端口运行中", tint: Theme.ok, fill: Theme.ok.opacity(0.14))
                                        } else if eng.installed {
                                            Chip(text: "已安装", tint: Theme.accent, fill: Theme.accentSoft)
                                        }
                                    }
                                    if let d = eng.description {
                                        Text(d).font(.system(size: 11.5)).foregroundStyle(Theme.fg3)
                                    }
                                }
                                Spacer()
                            }
                            .padding(.horizontal, 12)
                            .padding(.vertical, 9)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                    }
                }

                GroupHeader(text: "反代参数与渠道同步")
                FormGroup {
                    FormRow(title: "渠道名称") {
                        TextField("账号反代 · CLIProxyAPI", text: $channelName)
                            .textFieldStyle(.roundedBorder)
                            .frame(width: 260)
                    }
                    RowDivider()
                    FormRow(title: "Base URL") {
                        TextField("http://127.0.0.1:8317/v1", text: $baseURL)
                            .textFieldStyle(.roundedBorder)
                            .font(.system(size: 12, design: .monospaced))
                            .frame(width: 260)
                    }
                    RowDivider()
                    FormRow(title: "需要访问密钥") {
                        Toggle("", isOn: $authRequired).toggleStyle(.switch).controlSize(.small).labelsHidden()
                    }
                    if authRequired {
                        RowDivider()
                        FormRow(title: "API Key") {
                            SecureField(st.config.has_api_key ? "已加密保存，留空保留" : "输入密钥", text: $apiKey)
                                .textFieldStyle(.roundedBorder)
                                .frame(width: 260)
                        }
                    }
                    RowDivider()
                    FormRow(title: "连通状态") {
                        if st.probe_ok {
                            Text("已连通 · \(st.live_models?.count ?? 0) 个模型").foregroundStyle(Theme.ok)
                        } else if st.config.enabled {
                            Text(st.probe_error ?? "未连通").foregroundStyle(Theme.warn).lineLimit(1)
                        } else {
                            Text("未启用").foregroundStyle(Theme.fg3)
                        }
                    }
                }

                HStack(spacing: 8) {
                    Button(enabled ? "保存并同步渠道" : "同意免责声明并启用") { save(enableTarget: true) }
                        .buttonStyle(PillButtonStyle(kind: .primary))
                        .disabled(busy || !accepted)
                    if st.managed_running {
                        Button("停止托管进程 (PID \(st.managed_pid ?? 0))") { stopManaged() }
                            .buttonStyle(PillButtonStyle(kind: .danger))
                            .disabled(busy)
                    } else {
                        Button("托管启动并同步") { startManaged() }
                            .buttonStyle(PillButtonStyle(kind: .secondary))
                            .disabled(busy || !accepted)
                    }
                    if enabled {
                        Button("停用反代") { save(enableTarget: false) }
                            .buttonStyle(PillButtonStyle(kind: .danger))
                            .disabled(busy)
                    }
                    Spacer()
                    Button("刷新") { Task { await load(initFields: false) } }
                        .buttonStyle(PillButtonStyle(kind: .secondary))
                        .disabled(busy)
                }
                .padding(.top, 14)
            }
        }
        .task(id: server.id) { await load(initFields: true) }
    }

    private func load(initFields: Bool) async {
        loading = true
        do {
            let st: MacProxyStatus = try await server.client.get("/api/v2/proxy/status")
            status = st
            if initFields {
                enabled = st.config.enabled
                accepted = st.config.disclaimer_accepted
                engine = st.config.engine
                baseURL = st.config.base_url
                channelName = st.config.channel_name
                authRequired = st.config.auth_required
                binaryPath = st.config.binary_path ?? ""
                startArgs = st.config.start_args ?? ""
            }
        } catch {
            notice = (error.localizedDescription, true)
        }
        loading = false
    }

    private func save(enableTarget: Bool) {
        if enableTarget && !accepted {
            notice = ("启用前必须勾选同意免责声明", true)
            return
        }
        busy = true
        notice = nil
        Task {
            do {
                let payload = MacProxySavePayload(
                    enabled: enableTarget,
                    disclaimer_accepted: accepted,
                    engine: engine,
                    base_url: baseURL,
                    channel_name: channelName,
                    auth_required: authRequired,
                    api_key: authRequired ? (apiKey.isEmpty ? nil : apiKey) : "",
                    auto_start: false,
                    binary_path: binaryPath,
                    start_args: startArgs,
                    sync_channel: true
                )
                let st: MacProxyStatus = try await server.client.send("POST", "/api/v2/proxy/config", body: payload)
                status = st
                enabled = st.config.enabled
                apiKey = ""
                notice = (
                    st.config.enabled
                        ? (st.probe_ok ? "已启用并同步为前置 Bot 渠道（\(st.live_models?.count ?? 0) 个模型）" : "已保存配置，请先启动反代后刷新")
                        : "已停用反代渠道，自定义 API 不受影响。",
                    false
                )
            } catch {
                notice = (error.localizedDescription, true)
            }
            busy = false
        }
    }

    private func startManaged() {
        busy = true
        notice = nil
        Task {
            do {
                let st: MacProxyStatus = try await server.client.send("POST", "/api/v2/proxy/start", body: ["disclaimer_accepted": accepted])
                status = st
                enabled = st.config.enabled
                notice = ("托管反代进程已启动", false)
            } catch {
                notice = (error.localizedDescription, true)
            }
            busy = false
        }
    }

    private func stopManaged() {
        busy = true
        notice = nil
        Task {
            do {
                let st: MacProxyStatus = try await server.client.send("POST", "/api/v2/proxy/stop", body: [String: String]())
                status = st
                notice = ("托管反代进程已停止", false)
            } catch {
                notice = (error.localizedDescription, true)
            }
            busy = false
        }
    }
}

@MainActor
struct ProxyGatewayModalSheet: View {
    @Environment(\.dismiss) private var dismiss
    let server: ServerEntry

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("账号反代网关 · \(server.name)")
                    .font(.system(size: 14, weight: .semibold))
                Spacer()
                Button("完成") { dismiss() }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
            Rectangle().fill(Theme.line).frame(height: 0.5)
            ScrollView {
                ProxyGatewayPane(server: server)
                    .padding(.horizontal, 22)
                    .padding(.bottom, 22)
            }
            .background(Theme.settingsBG)
        }
        .frame(width: 640, height: 580)
    }
}
