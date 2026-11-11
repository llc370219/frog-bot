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
