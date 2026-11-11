#if canImport(Sparkle)
import Sparkle
import SwiftUI

private struct ClientUpdaterKey: EnvironmentKey {
    static let defaultValue: SPUUpdater? = nil
}

extension EnvironmentValues {
    var clientUpdater: SPUUpdater? {
        get { self[ClientUpdaterKey.self] }
        set { self[ClientUpdaterKey.self] = newValue }
    }
}

private final class ClientUpdateMenuState: ObservableObject {
    @Published var canCheck = false

    init(updater: SPUUpdater) {
        updater.publisher(for: \.canCheckForUpdates).assign(to: &$canCheck)
    }
}

struct ClientUpdateMenuItem: View {
    @StateObject private var state: ClientUpdateMenuState
    let updater: SPUUpdater

    init(updater: SPUUpdater) {
        self.updater = updater
        _state = StateObject(wrappedValue: ClientUpdateMenuState(updater: updater))
    }

    var body: some View {
        Button("检查客户端更新…", action: updater.checkForUpdates)
            .disabled(!state.canCheck)
    }
}

/// 设置 › 通用 里的「客户端版本」行：版本号 + 检查更新按钮（和应用菜单里的「检查客户端更新…」是同一个动作）
struct ClientUpdateRow: View {
    @StateObject private var state: ClientUpdateMenuState
    let updater: SPUUpdater

    init(updater: SPUUpdater) {
        self.updater = updater
        _state = StateObject(wrappedValue: ClientUpdateMenuState(updater: updater))
    }

    private var version: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
    }

    var body: some View {
        FormRow(title: "客户端版本") {
            Text("frog \(version)")
                .font(.system(size: 12.5))
                .monospacedDigit()
                .foregroundStyle(Theme.fg3)
            Button {
                updater.checkForUpdates()
            } label: {
                Label("检查更新", systemImage: "arrow.triangle.2.circlepath")
            }
            .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
            .disabled(!state.canCheck)
        }
    }
}

struct ClientUpdatePreference: View {
    let updater: SPUUpdater
    @State private var automaticallyChecks: Bool

    init(updater: SPUUpdater) {
        self.updater = updater
        _automaticallyChecks = State(initialValue: updater.automaticallyChecksForUpdates)
    }

    var body: some View {
        FormRow(title: "自动检查客户端更新") {
            Toggle("自动检查客户端更新", isOn: $automaticallyChecks)
                .toggleStyle(.switch)
                .controlSize(.small)
                .labelsHidden()
        }
        .onChange(of: automaticallyChecks) {
            if updater.automaticallyChecksForUpdates != automaticallyChecks {
                updater.automaticallyChecksForUpdates = automaticallyChecks
            }
        }
        .onReceive(updater.publisher(for: \.automaticallyChecksForUpdates)) { automaticallyChecks = $0 }
    }
}
#endif
