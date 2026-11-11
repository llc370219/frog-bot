import AppKit
import SwiftUI

/// 新建 / 编辑 Bot：名字、职责，以及这个 Bot 自己的聊天模型和任务模型。
@MainActor
struct BotEditorSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    let server: ServerEntry
    let device: String
    var profile: BotProfile?

    @State private var name = ""
    @State private var instructions = ""
    @State private var settings: APISettings?
    @State private var original: APISettings?
    @State private var cli = ""
    @State private var loadingModels = false
    @State private var busy = false
    @State private var error: String?
    @State private var confirmDelete = false
    @State private var purge = false

    private var ref: BotRef? { profile.map { BotRef(server: server.id, device: device, bot: $0.id) } }
    private var session: BotSession? { ref.flatMap { model.session($0) } }
    /// 互联设备上的 Bot 只能改模型，名字和职责要在那台设备上改
    private var profileEditable: Bool { device == "local" }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(spacing: 12) {
                if let profile {
                    BotAvatar(client: server.client, profile: profile, size: 36)
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(profile == nil ? "新建 Bot" : "编辑 Bot").font(.system(size: 14, weight: .semibold))
                    Text(profile == nil
                         ? "在「\(deviceName)」上创建一个独立的助理，有自己的对话、任务、记忆、工作区和模型设置。"
                         : "改名字、职责或模型，不影响已有的对话和任务。")
                        .font(.system(size: 12))
                        .foregroundStyle(Theme.fg3)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }

            Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 10) {
                GridRow {
                    Text("名称").gridColumnAlignment(.trailing)
                    TextField("", text: $name, prompt: Text("例如：代码审查"))
                        .textFieldStyle(.roundedBorder)
                        .disabled(!profileEditable)
                }
                GridRow(alignment: .top) {
                    Text("职责").padding(.top, 4)
                    TextEditor(text: $instructions)
                        .font(.system(size: 13))
                        .scrollContentBackground(.hidden)
                        .padding(5)
                        .frame(height: 96)
                        .background(Color(nsColor: .textBackgroundColor), in: RoundedRectangle(cornerRadius: 6))
                        .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(Theme.line2, lineWidth: 0.5))
                        .disabled(!profileEditable)
                }
                if profile != nil {
                    GridRow {
                        Text("聊天模型")
                        modelPicker(chat: true)
                    }
                    GridRow {
                        Text("任务模型")
                        if cli.isEmpty {
                            modelPicker(chat: false)
                        } else {
                            Text("由 \(cliName) 执行（在设置 › Agent 里修改）")
                                .font(.system(size: 12.5))
                                .foregroundStyle(Theme.fg3)
                        }
                    }
                }
            }
            .font(.system(size: 13))

            Text(note)
                .font(.system(size: 11.5))
                .foregroundStyle(Theme.fg4)
                .fixedSize(horizontal: false, vertical: true)

            if let error {
                Label(error, systemImage: "exclamationmark.circle.fill")
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.danger)
                    .fixedSize(horizontal: false, vertical: true)
            }

            HStack {
                if let profile, profile.id != "main", device == "local" {
                    Button("删除 Bot…") { confirmDelete = true }
                        .buttonStyle(.plain)
                        .foregroundStyle(Theme.danger)
                }
                Spacer()
                Button("取消") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(action: save) {
                    HStack(spacing: 6) {
                        if busy { ProgressView().controlSize(.small) }
                        Text(profile == nil ? "创建" : "保存")
                    }
                    .frame(minWidth: 50)
                }
                .keyboardShortcut(.defaultAction)
                .disabled(busy || name.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(22)
        .frame(width: 480)
        .onAppear {
            name = profile?.name ?? ""
            instructions = profile?.instructions ?? ""
        }
        .task { await loadModels() }
        .sheet(isPresented: $confirmDelete) { deleteConfirm }
    }

    private var deviceName: String {
        device == "local" ? server.name : (server.devices.first(where: { $0.id == device })?.name ?? device)
    }

    private var cliName: String { ["codex": "Codex CLI", "claude": "Claude Code"][cli] ?? cli }

    private var note: String {
        if profile == nil { return "创建后，可以在聊天窗口顶部给它单独选模型。" }
        if !profileEditable { return "这个 Bot 在互联设备「\(deviceName)」上，名字和职责请在那台设备上修改；模型可以在这里改。" }
        return "聊天模型负责回话和派活，可以选快一点的；任务模型给后台 Agent 干活用。每个 Bot 各选各的模型；API 渠道是整台设备共用的。"
    }

    @ViewBuilder
    private func modelPicker(chat: Bool) -> some View {
        if let settings {
            let channels = settings.channels.filter(\.enabled)
            if channels.isEmpty {
                Text("还没有 API 渠道（在设置 › 服务器 › API 渠道 里添加）")
                    .font(.system(size: 12.5))
                    .foregroundStyle(Theme.fg3)
            } else {
                Picker("", selection: binding(chat: chat)) {
                    ForEach(channels) { ch in
                        Section(ch.name) {
                            ForEach(ch.visibleModels, id: \.self) { m in
                                Text(m).tag(ch.id + "\u{1F}" + m)
                            }
                        }
                    }
                }
                .labelsHidden()
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        } else if loadingModels {
            ProgressView().controlSize(.small)
        } else {
            Text("读不到模型设置").font(.system(size: 12.5)).foregroundStyle(Theme.fg3)
        }
    }

    private func binding(chat: Bool) -> Binding<String> {
        Binding(
            get: {
                guard let s = settings else { return "" }
                return chat ? s.chatChannelID + "\u{1F}" + s.chatModel : s.agentChannelID + "\u{1F}" + s.agentModelID
            },
            set: { tag in
                let parts = tag.components(separatedBy: "\u{1F}")
                guard parts.count == 2 else { return }
                if chat {
                    settings?.default_provider_id = parts[0]
                    settings?.model = parts[1]
                } else {
                    settings?.agent_provider_id = parts[0]
                    settings?.agent_model = parts[1]
                }
            })
    }

    private func loadModels() async {
        guard let session else { return }
        loadingModels = true
        if var s = try? await session.loadSettings() {
            if s.agent_provider_id == nil { s.agent_provider_id = s.chatChannelID }
            if s.agent_model == nil { s.agent_model = s.chatModel }
            settings = s
            original = s
        }
        cli = (try? await session.loadConnections())?.cli ?? ""
        loadingModels = false
    }

    private var deleteConfirm: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("删除「\(profile?.name ?? "")」？").font(.system(size: 14, weight: .semibold))
            Text("会停止它的任务和定时计划。默认只从列表里移除，聊天记录、记忆、密钥和工作区文件仍留在服务器上。")
                .font(.system(size: 12.5))
                .foregroundStyle(Theme.fg3)
                .fixedSize(horizontal: false, vertical: true)
            Toggle("彻底删除（连聊天记录、记忆、密钥和工作区文件一起删掉，无法恢复）", isOn: $purge)
                .font(.system(size: 12.5))
            HStack {
                Spacer()
                Button("取消") { confirmDelete = false }.keyboardShortcut(.cancelAction)
                Button("删除 Bot", role: .destructive) { delete() }
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(22)
        .frame(width: 420)
    }

    private func save() {
        busy = true; error = nil
        Task {
            do {
                let trimmed = name.trimmingCharacters(in: .whitespaces)
                if profileEditable && (profile == nil || trimmed != profile?.name || instructions != (profile?.instructions ?? "")) {
                    try await model.saveBot(server: server, device: device, existing: profile, name: trimmed, instructions: instructions)
                }
                if let session, let settings, settings != original {
                    try await session.saveRoutes(chatChannel: settings.chatChannelID, chatModel: settings.chatModel,
                                                 agentChannel: settings.agentChannelID, agentModel: settings.agentModelID)
                }
                dismiss()
            } catch { self.error = error.localizedDescription }
            busy = false
        }
    }

    private func delete() {
        guard let profile else { return }
        confirmDelete = false
        busy = true; error = nil
        Task {
            do {
                try await model.deleteBot(BotRef(server: server.id, device: device, bot: profile.id), purge: purge)
                dismiss()
            } catch { self.error = error.localizedDescription }
            busy = false
        }
    }
}
