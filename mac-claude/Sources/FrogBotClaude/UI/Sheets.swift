import SwiftUI

/// 添加服务器 / 重新登录 / 编辑连接
@MainActor
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
        case .add: return "连接一台 frog 服务器"
        case .login: return "重新登录「\(server?.name ?? "")」"
        case .edit: return "编辑连接"
        }
    }

    private var insecure: Bool {
        guard let u = ServerConfig.normalize(address) else { return false }
        return u.scheme == "http" && !ServerConfig.isLoopback(u.host ?? "")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 12) {
                VStack(alignment: .leading, spacing: 3) {
                    Text(title).font(.system(size: 14, weight: .semibold))
                    Text(mode == .login ? "会话过期或密码改过了，输入密码重新连接。" : "填写安装时打印的访问地址、用户名和密码。")
                        .font(.system(size: 12))
                        .foregroundStyle(.secondary)
                }
            }
            .padding(.bottom, 16)

            Form {
                if mode != .login {
                    TextField("名称", text: $name, prompt: Text("可选，例如：东京 VPS"))
                        .focused($focused, equals: .name)
                    TextField("地址", text: $address, prompt: Text("https://bot.example.com 或 1.2.3.4:8080"))
                        .focused($focused, equals: .address)
                }
                TextField("用户名", text: $username)
                    .focused($focused, equals: .username)
                    .textContentType(.username)
                SecureField("密码", text: $password, prompt: Text(mode == .edit ? "不改就留空" : "安装时打印的初始密码"))
                    .focused($focused, equals: .password)
                    .textContentType(.password)
                    .onSubmit(connect)
            }
            .formStyle(.columns)

            if insecure {
                Label("这是明文 HTTP 连接，密码和聊天内容在网络上不加密。建议给服务器配一个域名并开启 HTTPS。", systemImage: "lock.open.fill")
                    .font(.system(size: 11.5))
                    .foregroundStyle(Theme.warn)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.top, 14)
            }
            if let error {
                Label(error, systemImage: "exclamationmark.circle.fill")
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.danger)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.top, 12)
            }
            if mode == .add {
                Text("忘记密码：在服务器上运行 sudo frogbot reset-password。面板不保存密码，只保存登录会话（7 天有效）。")
                    .font(.system(size: 11))
                    .foregroundStyle(.tertiary)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.top, 14)
            }

            HStack {
                Spacer()
                Button("取消") { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button(action: connect) {
                    HStack(spacing: 6) {
                        if busy { ProgressView().controlSize(.small) }
                        Text(mode == .add ? "连接" : mode == .login ? "登录" : "保存")
                    }
                    .frame(minWidth: 60)
                }
                .keyboardShortcut(.defaultAction)
                .disabled(busy || (mode != .login && address.trimmingCharacters(in: .whitespaces).isEmpty))
            }
            .padding(.top, 22)
        }
        .padding(24)
        .frame(width: 460)
        .onAppear {
            if let s = server {
                name = s.config.name
                address = s.config.url
                username = s.config.username
            }
            focused = mode == .add ? .address : .password
        }
    }

    private func connect() {
        guard !busy else { return }
        busy = true
        error = nil
        Task {
            do {
                try await model.connect(existing: server, name: name, address: address, username: username, password: password)
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
            busy = false
        }
    }
}

