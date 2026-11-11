import SwiftUI

/// 添加服务器 / 重新登录 / 编辑连接
struct ServerSheet: View {
    enum Mode { case add, login, edit }

    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    let mode: Mode
    var server: ServerEntry?

    @State private var name = ""
    @State private var address = ""
    @State private var username = "admin"
    @State private var password = ""
    @State private var busy = false
    @State private var error: String?
    @FocusState private var focused: Field?

    private enum Field { case name, address, username, password }

    private var title: String {
        switch mode {
        case .add: return "添加服务器"
        case .login: return "重新登录"
        case .edit: return "编辑连接"
        }
    }

    private var insecure: Bool {
        guard let u = ServerConfig.normalize(address) else { return false }
        return u.scheme == "http" && !ServerConfig.isLoopback(u.host ?? "")
    }

    var body: some View {
        NavigationStack {
            Form {
                if mode == .login, let server {
                    Section {
                        Text("「\(server.name)」的会话过期了，或者密码改过了。输入密码重新连接。")
                            .font(.subheadline)
                            .foregroundStyle(Theme.fg3)
                    }
                }
                if mode != .login {
                    Section {
                        TextField("https://bot.example.com 或 1.2.3.4:8080", text: $address)
                            .keyboardType(.URL)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .textContentType(.URL)
                            .focused($focused, equals: .address)
                            .submitLabel(.next)
                            .onSubmit { focused = .username }
                        TextField("名称（可选，例如：东京 VPS）", text: $name)
                            .focused($focused, equals: .name)
                    } header: {
                        Text("地址")
                    } footer: {
                        if insecure {
                            Label("这是明文 HTTP 连接，密码和聊天内容在网络上不加密。建议给服务器配一个域名并开启 HTTPS。", systemImage: "lock.open.fill")
                                .foregroundStyle(Theme.warn)
                        }
                    }
                }
                Section {
                    TextField("用户名", text: $username)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .textContentType(.username)
                        .focused($focused, equals: .username)
                        .submitLabel(.next)
                        .onSubmit { focused = .password }
                    SecureField(mode == .edit ? "密码（不改就留空）" : "密码", text: $password)
                        .textContentType(.password)
                        .focused($focused, equals: .password)
                        .submitLabel(.go)
                        .onSubmit(connect)
                } header: {
                    Text("账号")
                } footer: {
                    if mode == .add {
                        Text("填写安装时打印的用户名和密码。忘记密码：在服务器上运行 sudo frogbot reset-password。App 不保存密码，只保存登录会话（7 天有效）。")
                    }
                }
                if let error {
                    Section {
                        Label(error, systemImage: "exclamationmark.circle.fill")
                            .foregroundStyle(Theme.danger)
                    }
                }
            }
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if busy {
                        ProgressView()
                    } else {
                        Button(mode == .add ? "连接" : mode == .login ? "登录" : "保存", action: connect)
                            .disabled(mode != .login && address.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                }
            }
            .onAppear {
                if let s = server {
                    name = s.config.name
                    address = s.config.url
                    username = s.config.username
                }
                focused = mode == .add ? .address : .password
            }
        }
        .interactiveDismissDisabled(busy)
    }

    private func connect() {
        guard !busy else { return }
        busy = true
        error = nil
        Task {
            do {
                try await model.connect(existing: server, name: name, address: address, username: username, password: password)
                Haptics.success()
                dismiss()
            } catch {
                self.error = error.localizedDescription
                Haptics.error()
            }
            busy = false
        }
    }
}

/// 新建 Bot：名字和职责。模型创建后在聊天页顶部选。
struct NewBotSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let server: ServerEntry
    let device: String
    var onCreated: (BotRef) -> Void

    @State private var name = ""
    @State private var instructions = ""
    @State private var busy = false
    @State private var error: String?
    @FocusState private var nameFocused: Bool

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("名称，例如：代码审查", text: $name)
                        .focused($nameFocused)
                } footer: {
                    Text("在「\(server.name)」上创建一个独立的助理，有自己的对话、任务、记忆、工作区和模型设置。")
                }
                Section("职责（可选）") {
                    TextField("这个 Bot 负责什么、说话风格、注意事项…", text: $instructions, axis: .vertical)
                        .lineLimit(4...10)
                }
                if let error {
                    Section { Label(error, systemImage: "exclamationmark.circle.fill").foregroundStyle(Theme.danger) }
                }
            }
            .navigationTitle("新建 Bot")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if busy { ProgressView() } else {
                        Button("创建", action: create).disabled(name.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                }
            }
            .onAppear { nameFocused = true }
        }
    }

    private func create() {
        busy = true; error = nil
        Task {
            do {
                try await model.saveBot(server: server, device: device, existing: nil,
                                        name: name.trimmingCharacters(in: .whitespaces), instructions: instructions)
                dismiss()
                if let ref = model.selection, ref.server == server.id { onCreated(ref) }
            } catch {
                self.error = error.localizedDescription
            }
            busy = false
        }
    }
}
