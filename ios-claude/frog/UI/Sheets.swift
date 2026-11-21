import AVFoundation
import SwiftUI
import UIKit

/// 添加服务器 / 重新登录 / 编辑连接：顶部支持扫码或粘贴配对码一键建立端到端加密连接。
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
    @State private var proxyMode: ProxyRouteMode = .system
    @State private var customProxy = ""
    @State private var busy = false
    @State private var error: String?
    @State private var showingScanner = false
    @State private var showManual = false
    @FocusState private var focused: Field?

    private enum Field { case name, address, username, password }

    private var title: String {
        switch mode {
        case .add: return "连接主机"
        case .login: return "重新验证"
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
                        Text("「\(server.name)」的授权会话已过期。可在电脑端「设置 › 远程连接」生成新配对码一键恢复，或在下方输入密码登录。")
                            .font(.subheadline)
                            .foregroundStyle(Theme.fg3)
                    }
                }

                // 一键扫码 / 剪贴板配对（add 与 login 均可用）
                if mode != .edit {
                    Section {
                        VStack(spacing: 10) {
                            Button {
                                Haptics.tap()
                                showingScanner = true
                            } label: {
                                Label("扫描电脑端二维码一键连接", systemImage: "qrcode.viewfinder")
                                    .font(.system(size: 15.5, weight: .semibold))
                                    .frame(maxWidth: .infinity)
                            }
                            .buttonStyle(PillButtonStyle(kind: .primary))
                            .disabled(busy)

                            Button {
                                pasteAndPair()
                            } label: {
                                Label("粘贴剪贴板配对码连接", systemImage: "doc.on.clipboard")
                                    .font(.system(size: 14.5, weight: .medium))
                                    .frame(maxWidth: .infinity)
                            }
                            .buttonStyle(PillButtonStyle(kind: .secondary))
                            .disabled(busy)
                        }
                        .padding(.vertical, 4)
                        .listRowBackground(Color.clear)
                        .listRowInsets(EdgeInsets(top: 6, leading: 4, bottom: 6, trailing: 4))
                    } header: {
                        Text("一键加密配对（推荐 · 免配 VPN）")
                    } footer: {
                        Text("在电脑浏览器打开 frog「设置 › 远程连接」，开启云穿透或直连加密后点击「生成手机配对码」即可。")
                    }
                }

                if mode == .add && !showManual {
                    Section {
                        Button {
                            withAnimation { showManual = true }
                            focused = .address
                        } label: {
                            HStack {
                                Label("手动输入服务器地址与账号密码", systemImage: "keyboard")
                                    .foregroundStyle(Theme.fg2)
                                Spacer()
                                Image(systemName: "chevron.right")
                                    .font(.caption)
                                    .foregroundStyle(Theme.fg4)
                            }
                        }
                    }
                } else {
                    if mode != .login {
                        Section {
                            TextField("https://bot.example.com 或粘贴 frogpair://…", text: $address)
                                .keyboardType(.URL)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                                .textContentType(.URL)
                                .focused($focused, equals: .address)
                                .submitLabel(.next)
                                .onSubmit { focused = .username }
                            TextField("显示名称（可选，例如：家里 Mac）", text: $name)
                                .focused($focused, equals: .name)
                        } header: {
                            Text("手动地址")
                        } footer: {
                            if insecure {
                                Label("这是明文 HTTP 连接。建议在电脑端开启内置「远程连接」使用加密配对。", systemImage: "lock.open.fill")
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
                    }

                    Section {
                        Picker("路由方式", selection: $proxyMode) {
                            ForEach(ProxyRouteMode.allCases, id: \.self) { m in
                                Text(m.label).tag(m)
                            }
                        }
                        if proxyMode == .custom {
                            TextField("socks5://127.0.0.1:7890 或 http://192.168.1.2:7890", text: $customProxy)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                                .font(.system(.subheadline, design: .monospaced))
                        }
                    } header: {
                        Text("网络与代理")
                    } footer: {
                        Text("本软件内置加密连接不会顶掉手机上的其他 VPN 软件；也可在此手动选择是否绕过系统代理。")
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
                if mode != .add || showManual {
                    ToolbarItem(placement: .confirmationAction) {
                        if busy {
                            ProgressView()
                        } else {
                            Button(mode == .add ? "连接" : mode == .login ? "登录" : "保存", action: connect)
                                .disabled(mode != .login && address.trimmingCharacters(in: .whitespaces).isEmpty)
                        }
                    }
                } else if busy {
                    ToolbarItem(placement: .confirmationAction) { ProgressView() }
                }
            }
            .sheet(isPresented: $showingScanner) {
                QRScannerSheet { scanned in
                    showingScanner = false
                    pairWith(scanned)
                }
            }
            .onAppear {
                if let s = server {
                    name = s.config.name
                    address = s.config.url
                    username = s.config.username
                    proxyMode = s.config.effectiveProxyMode
                    customProxy = s.config.customProxy ?? ""
                }
                if mode == .edit { showManual = true }
            }
        }
        .interactiveDismissDisabled(busy)
    }

    private func pasteAndPair() {
        Haptics.tap()
        guard let clip = UIPasteboard.general.string, !clip.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            error = "剪贴板是空的，请先在电脑端复制配对码"
            return
        }
        pairWith(clip)
    }

    private func pairWith(_ raw: String) {
        guard !busy else { return }
        busy = true
        error = nil
        Task {
            do {
                try await model.connectWithPairCode(raw, existing: server, customName: name, proxyMode: proxyMode, customProxy: customProxy)
                Haptics.success()
                dismiss()
            } catch {
                self.error = error.localizedDescription
                Haptics.error()
            }
            busy = false
        }
    }

    private func connect() {
        guard !busy else { return }
        busy = true
        error = nil
        Task {
            do {
                try await model.connect(existing: server, name: name, address: address, username: username, password: password, proxyMode: proxyMode, customProxy: customProxy)
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

/// 相机扫描配对二维码弹窗
struct QRScannerSheet: View {
    var onCode: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var camError: String?

    var body: some View {
        NavigationStack {
            ZStack {
                Color.black.ignoresSafeArea()
                if let camError {
                    VStack(spacing: 14) {
                        Image(systemName: "camera.Macro.slash")
                            .font(.system(size: 38))
                            .foregroundStyle(.white.opacity(0.75))
                        Text(camError)
                            .font(.subheadline)
                            .foregroundStyle(.white.opacity(0.85))
                            .multilineTextAlignment(.center)
                            .padding(.horizontal, 32)
                    }
                } else {
                    QRCameraPreview(onFound: onCode, onError: { camError = $0 })
                        .ignoresSafeArea()
                    RoundedRectangle(cornerRadius: 24, style: .continuous)
                        .strokeBorder(Theme.accent, lineWidth: 3)
                        .frame(width: 240, height: 240)
                    VStack {
                        Spacer()
                        Text("对准电脑「设置 › 远程连接」显示的二维码")
                            .font(.footnote.weight(.medium))
                            .foregroundStyle(.white)
                            .padding(.horizontal, 16)
                            .padding(.vertical, 8)
                            .background(.black.opacity(0.6), in: Capsule())
                            .padding(.bottom, 40)
                    }
                }
            }
            .navigationTitle("扫码配对")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("关闭") { dismiss() }.tint(.white)
                }
            }
        }
    }
}

private struct QRCameraPreview: UIViewControllerRepresentable {
    var onFound: (String) -> Void
    var onError: (String) -> Void

    func makeUIViewController(context: Context) -> ScannerVC {
        let vc = ScannerVC()
        vc.onFound = onFound
        vc.onError = onError
        return vc
    }

    func updateUIViewController(_ uiViewController: ScannerVC, context: Context) {}

    final class ScannerVC: UIViewController, AVCaptureMetadataOutputObjectsDelegate {
        var onFound: ((String) -> Void)?
        var onError: ((String) -> Void)?
        private let session = AVCaptureSession()
        private var preview: AVCaptureVideoPreviewLayer?
        private var didEmit = false

        override func viewDidLoad() {
            super.viewDidLoad()
            view.backgroundColor = .black
            switch AVCaptureDevice.authorizationStatus(for: .video) {
            case .authorized:
                setupCamera()
            case .notDetermined:
                AVCaptureDevice.requestAccess(for: .video) { [weak self] granted in
                    DispatchQueue.main.async {
                        if granted { self?.setupCamera() }
                        else { self?.onError?("未开启相机权限。请在系统设置中允许访问相机，或直接使用「粘贴剪贴板配对码连接」。") }
                    }
                }
            default:
                onError?("未开启相机权限。请在系统设置中允许访问相机，或直接使用「粘贴剪贴板配对码连接」。")
            }
        }

        private func setupCamera() {
            guard let device = AVCaptureDevice.default(for: .video),
                  let input = try? AVCaptureDeviceInput(device: device),
                  session.canAddInput(input) else {
                onError?("当前设备无法启用摄像头，请改用「粘贴剪贴板配对码连接」。")
                return
            }
            session.addInput(input)
            let output = AVCaptureMetadataOutput()
            guard session.canAddOutput(output) else { return }
            session.addOutput(output)
            output.setMetadataObjectsDelegate(self, queue: .main)
            output.metadataObjectTypes = [.qr]

            let layer = AVCaptureVideoPreviewLayer(session: session)
            layer.videoGravity = .resizeAspectFill
            layer.frame = view.bounds
            view.layer.addSublayer(layer)
            preview = layer

            DispatchQueue.global(qos: .userInitiated).async { [weak self] in
                self?.session.startRunning()
            }
        }

        override func viewDidLayoutSubviews() {
            super.viewDidLayoutSubviews()
            preview?.frame = view.bounds
        }

        override func viewWillDisappear(_ animated: Bool) {
            super.viewWillDisappear(animated)
            if session.isRunning {
                DispatchQueue.global(qos: .userInitiated).async { [weak self] in
                    self?.session.stopRunning()
                }
            }
        }

        func metadataOutput(_ output: AVCaptureMetadataOutput, didOutput metadataObjects: [AVMetadataObject], from connection: AVCaptureConnection) {
            guard !didEmit,
                  let obj = metadataObjects.first as? AVMetadataMachineReadableCodeObject,
                  let str = obj.stringValue, !str.isEmpty else { return }
            didEmit = true
            session.stopRunning()
            onFound?(str)
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
