import SwiftUI

// MARK: - Models

struct ProxyEngineDetectedItem: Codable, Hashable, Identifiable {
    let id: String
    let name: String
    let installed: Bool
    let binary_path: String?
    let default_url: String
    let default_args: String?
    let probe_alive: Bool
    let repo_url: String?
    let install_hint: String?
    let description: String?
}

struct ProxyGatewayConfigModel: Codable, Hashable {
    var enabled: Bool
    var disclaimer_accepted: Bool
    var disclaimer_accepted_at: String?
    var disclaimer_version: String?
    var engine: String
    var base_url: String
    var channel_name: String
    var auth_required: Bool
    var has_api_key: Bool
    var auto_start: Bool
    var binary_path: String?
    var start_args: String?
    var models: [String]?
}

struct ProxyGatewayStatusModel: Codable, Hashable {
    let config: ProxyGatewayConfigModel
    let disclaimer_version: String
    let disclaimer_text: String
    let detected_engines: [ProxyEngineDetectedItem]
    let managed_running: Bool
    let managed_pid: Int?
    let last_logs: [String]?
    let probe_ok: Bool
    let probe_latency_ms: Int64?
    let probe_error: String?
    let live_models: [String]?
    let channel_synced: Bool
    let channel_id: String
}

private struct ProxySavePayload: Encodable {
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

// MARK: - 前置 Bot 订阅账号反代网关设置页（路线 A，带强制免责声明，与自定义 API 并存）

struct ProxyGatewayView: View {
    let server: ServerEntry

    @State private var status: ProxyGatewayStatusModel?
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
        Form {
            if loading && status == nil {
                HStack(spacing: 10) {
                    ProgressView()
                    Text("正在探测主机反代网关状态…").foregroundStyle(Theme.fg3)
                }
            } else if let st = status {
                // 强制风险免责声明
                Section {
                    VStack(alignment: .leading, spacing: 8) {
                        Label("风险提示与免责声明（必读）", systemImage: "exclamationmark.triangle.fill")
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(Theme.warn)
                        Text(st.disclaimer_text)
                            .font(.footnote)
                            .foregroundStyle(Theme.fg2)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    .padding(.vertical, 4)

                    Toggle("我已阅读并同意免责声明，自愿承担风险", isOn: $accepted)
                        .tint(Theme.accent)
                        .disabled(busy)
                } header: {
                    Text("免责声明")
                } footer: {
                    Text("账号反代与「自定义 API 渠道」完全并存，可随时在 Bot 模型设置中自由切换。")
                }

                if let notice {
                    Section {
                        Label(notice.text, systemImage: notice.isError ? "exclamationmark.circle.fill" : "checkmark.circle.fill")
                            .foregroundStyle(notice.isError ? Theme.danger : Theme.ok)
                    }
                }

                // 开源反代引擎选择（自动探测本机安装与端口状态）
                Section("反代引擎（路线 A）") {
                    ForEach(st.detected_engines) { eng in
                        Button {
                            selectEngine(eng)
                        } label: {
                            HStack(spacing: 10) {
                                Image(systemName: engine == eng.id ? "checkmark.circle.fill" : "circle")
                                    .foregroundStyle(engine == eng.id ? Theme.accent : Theme.fg4)
                                VStack(alignment: .leading, spacing: 2) {
                                    HStack(spacing: 6) {
                                        Text(eng.name).font(.subheadline.weight(.semibold)).foregroundStyle(Theme.fg)
                                        if eng.probe_alive {
                                            Chip(text: "端口运行中", tint: Theme.ok, fill: Theme.ok.opacity(0.14))
                                        } else if eng.installed {
                                            Chip(text: "已安装", tint: Theme.accent, fill: Theme.accentSoft)
                                        } else if eng.id != "custom" {
                                            Chip(text: "未检测", tint: Theme.fg4, fill: Theme.s2)
                                        }
                                    }
                                    if let desc = eng.description {
                                        Text(desc).font(.caption).foregroundStyle(Theme.fg3)
                                    }
                                }
                            }
                        }
                        .buttonStyle(.plain)
                    }
                }

                // 连接参数
                Section("反代地址与渠道同步") {
                    TextField("同步渠道名称", text: $channelName)
                    TextField("http://127.0.0.1:8317/v1", text: $baseURL)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    Toggle("需要反代 API Key", isOn: $authRequired)
                    if authRequired {
                        SecureField(st.config.has_api_key ? "已加密保存，留空保留" : "填写反代访问密钥", text: $apiKey)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                    }
                    TextField("可执行文件路径（可选，用于托管启停）", text: $binaryPath)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                }

                // 实时连通状态 & 操作
                Section("状态与操作") {
                    LabeledContent("连通状态") {
                        if st.probe_ok {
                            Text("已连通 · \(st.live_models?.count ?? 0) 个模型")
                                .foregroundStyle(Theme.ok)
                        } else if st.config.enabled {
                            Text(st.probe_error ?? "未连通").foregroundStyle(Theme.warn).lineLimit(1)
                        } else {
                            Text("未启用").foregroundStyle(Theme.fg3)
                        }
                    }

                    if st.managed_running {
                        Button(role: .destructive) {
                            stopManaged()
                        } label: {
                            Label("停止托管进程 (PID \(st.managed_pid ?? 0))", systemImage: "stop.circle.fill")
                        }
                        .disabled(busy)
                    } else {
                        Button {
                            startManaged()
                        } label: {
                            Label("托管启动反代并同步渠道", systemImage: "play.circle.fill")
                        }
                        .disabled(busy || !accepted)
                    }

                    Button {
                        save(enableTarget: true)
                    } label: {
                        HStack {
                            Label(enabled ? "保存并同步为前置 Bot 渠道" : "同意免责声明并启用", systemImage: "checkmark.shield.fill")
                            Spacer()
                            if busy { ProgressView() }
                        }
                    }
                    .disabled(busy || !accepted)

                    if enabled {
                        Button("停用反代（保留自定义 API）", role: .destructive) {
                            save(enableTarget: false)
                        }
                        .disabled(busy)
                    }
                }

                if let logs = st.last_logs, !logs.isEmpty {
                    Section("托管进程日志") {
                        Text(logs.suffix(10).joined(separator: "\n"))
                            .font(.system(size: 11, design: .monospaced))
                            .foregroundStyle(Theme.fg3)
                    }
                }
            }
        }
        .navigationTitle("账号反代网关")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button { Task { await load(initFields: false) } } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .disabled(busy)
            }
        }
        .task { await load(initFields: true) }
    }

    private func selectEngine(_ eng: ProxyEngineDetectedItem) {
        Haptics.tap()
        engine = eng.id
        baseURL = eng.default_url
        channelName = "账号反代 · \(eng.name)"
        if let p = eng.binary_path, !p.isEmpty { binaryPath = p }
        startArgs = eng.default_args ?? ""
    }

    private func load(initFields: Bool) async {
        loading = true
        do {
            let st: ProxyGatewayStatusModel = try await server.client.get("/api/v2/proxy/status")
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
                let payload = ProxySavePayload(
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
                let st: ProxyGatewayStatusModel = try await server.client.send("POST", "/api/v2/proxy/config", body: payload)
                status = st
                enabled = st.config.enabled
                apiKey = ""
                Haptics.success()
                notice = (
                    st.config.enabled
                        ? (st.probe_ok ? "已启用并同步为前置 Bot 渠道（\(st.live_models?.count ?? 0) 个模型）" : "已保存配置，请先在主机启动反代程序后刷新")
                        : "已停用反代渠道，自定义 API 渠道不受影响。",
                    false
                )
            } catch {
                notice = (error.localizedDescription, true)
                Haptics.error()
            }
            busy = false
        }
    }

    private func startManaged() {
        guard accepted else {
            notice = ("请先勾选同意免责声明", true)
            return
        }
        busy = true
        notice = nil
        Task {
            do {
                let payload = ProxySavePayload(
                    enabled: true,
                    disclaimer_accepted: true,
                    engine: engine,
                    base_url: baseURL,
                    channel_name: channelName,
                    auth_required: authRequired,
                    api_key: authRequired ? (apiKey.isEmpty ? nil : apiKey) : "",
                    auto_start: false,
                    binary_path: binaryPath,
                    start_args: startArgs,
                    sync_channel: false
                )
                let _: ProxyGatewayStatusModel = try await server.client.send("POST", "/api/v2/proxy/config", body: payload)
                let _: ProxyGatewayStatusModel = try await server.client.send("POST", "/api/v2/proxy/start", body: [String: String]())
                let synced: ProxyGatewayStatusModel = try await server.client.send("POST", "/api/v2/proxy/sync", body: [String: String]())
                status = synced
                enabled = true
                Haptics.success()
                notice = ("已启动反代网关并同步到 API 渠道列表。", false)
            } catch {
                notice = (error.localizedDescription, true)
                Haptics.error()
            }
            busy = false
        }
    }

    private func stopManaged() {
        busy = true
        notice = nil
        Task {
            do {
                let st: ProxyGatewayStatusModel = try await server.client.send("POST", "/api/v2/proxy/stop", body: [String: String]())
                status = st
                notice = ("已停止托管进程。", false)
            } catch {
                notice = (error.localizedDescription, true)
            }
            busy = false
        }
    }
}
