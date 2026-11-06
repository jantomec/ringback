import SwiftUI

/// The models Claude Code offers on your Macs, and your choice, as the hub keeps them (`/model`
/// in docs/protocol.md). The hub keeps the choice because a call needs it before the phone rings:
/// the voice writes its opening meanwhile.
struct ModelChoices: Codable, Equatable {
    struct Model: Codable, Equatable, Identifiable {
        let value: String
        let name: String
        let about: String
        let `default`: Bool
        /// The Macs that don't offer it, whose calls use their default.
        let missing: [String]
        var id: String { value }
    }

    struct Mac: Codable, Equatable, Identifiable {
        let name: String
        let linked: Bool
        let at: String
        let `default`: String
        var id: String { name }
    }

    var choice: String
    let models: [Model]
    let macs: [Mac]

    private static let key = "modelChoices"
    /// What the Settings app shows, read-only.
    static let labelKey = "defaultModelLabel"

    /// The last list the hub gave, to show at once.
    static var saved: ModelChoices? {
        if Seed.isOn { return Seed.modelChoices }
        return UserDefaults.standard.data(forKey: key).flatMap { try? JSONDecoder().decode(ModelChoices.self, from: $0) }
    }

    func save() {
        let defaults = UserDefaults.standard
        if let data = try? JSONEncoder().encode(self), data != defaults.data(forKey: Self.key) {
            defaults.set(data, forKey: Self.key)
        }
        if label != defaults.string(forKey: Self.labelKey) {
            defaults.set(label, forKey: Self.labelKey)
        }
    }

    var chosen: Model? { models.first { $0.value == choice } }
    /// You chose a model no Mac offers any more: calls use the default, and it comes back by itself
    /// if a Mac offers it again.
    var gone: Bool { choice != "default" && chosen == nil && !macs.isEmpty }

    /// What the default is on each Mac, in a few words.
    var defaultLine: String {
        let names = Set(macs.map(\.default)).filter { !$0.isEmpty }
        if names.count > 1 {
            return macs.filter { !$0.default.isEmpty }.map { "\($0.default) on \($0.name)" }.joined(separator: ", ")
        }
        return names.first.map { "\($0), as in your sessions" } ?? "The model Claude Code uses on your Mac"
    }

    /// What calls use, for the Settings app: "Default (Opus 5.5)", or "Sonnet 5.5".
    var label: String {
        if choice != "default", let chosen { return chosen.name }
        let names = Set(macs.map(\.default)).filter { !$0.isEmpty }
        return names.count == 1 ? "Default (\(names.first!))" : "Default"
    }
}

/// The app's own settings: what can't be in the Settings app, whose lists are fixed when the app
/// is built, and which reach the hub only when the app next runs (AppSettings). Everything else
/// stays there.
struct SettingsSheet: View {
    let state: AppState
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL
    @State private var choices = ModelChoices.saved
    @State private var reached = false
    @State private var unreachable = false
    @State private var problem: String?

    var body: some View {
        NavigationStack {
            List {
                Section {
                    rows
                } header: {
                    Text("Default Model for Conversation")
                } footer: {
                    Text(footer)
                }
                Section {
                    Button {
                        if let url = URL(string: UIApplication.openSettingsURLString) { openURL(url) }
                    } label: {
                        Label("Work Hours and More", systemImage: "arrow.up.forward.app")
                    }
                } footer: {
                    Text("Opens Ringback in the Settings app.")
                }
            }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button(role: .close) { dismiss() }
                }
            }
            .refreshable { await load(refresh: true) }
            // While it shows: a new model appears in place.
            .task {
                if Seed.isOn { return }
                await load(refresh: true)
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(3))
                    if !Task.isCancelled { await load(refresh: false) }
                }
            }
            .alert(problem ?? "", isPresented: Binding(get: { problem != nil }, set: { if !$0 { problem = nil } })) {
                Button("OK") { problem = nil }
            }
        }
    }

    @ViewBuilder private var rows: some View {
        if let choices {
            if choices.gone {
                row(title: choices.choice, line: "No longer offered. Calls use \(defaultName(choices)).", warns: true, ticked: false, value: nil)
            }
            if choices.models.isEmpty {
                row(title: "Default", line: choices.defaultLine, ticked: true, value: nil)
            }
            ForEach(choices.models) { model in
                row(title: model.default ? "Default" : model.name, line: line(model, in: choices),
                    ticked: model.value == choices.choice, value: model.value)
            }
        } else if unreachable {
            Text("Shows once your hub answers.").foregroundStyle(.secondary)
        } else {
            HStack(spacing: 10) {
                ProgressView()
                Text("Asking your hub…").foregroundStyle(.secondary)
            }
        }
    }

    private func row(title: String, line: String?, warns: Bool = false, ticked: Bool, value: String?) -> some View {
        Button {
            if let value { choose(value) }
        } label: {
            HStack {
                if warns { Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange) }
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).foregroundStyle(.primary)
                    if let line, !line.isEmpty {
                        Text(line).font(.footnote).foregroundStyle(.secondary)
                    }
                }
                Spacer()
                if ticked { Image(systemName: "checkmark").foregroundStyle(.tint).fontWeight(.semibold) }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(value == nil || unreachable)
    }

    private func line(_ model: ModelChoices.Model, in choices: ModelChoices) -> String {
        if model.default { return choices.defaultLine }
        guard !model.missing.isEmpty else { return model.about }
        let names = model.missing.joined(separator: ", ")
        return [model.about, "Not on \(names): \(model.missing.count == 1 ? "its" : "their") calls use Default."]
            .filter { !$0.isEmpty }.joined(separator: "\n")
    }

    private func defaultName(_ choices: ModelChoices) -> String { choices.label }

    private var footer: String {
        var lines = ["The model Claude talks to you with on calls. The list comes from Claude Code on your Macs. Changes apply from the next call."]
        if let choices {
            if reached && choices.macs.isEmpty {
                lines = ["No Mac is linked to your hub yet. Your models show here once one is."]
            }
            let iso = ISO8601DateFormatter()
            for mac in choices.macs where !mac.linked {
                let day = iso.date(from: mac.at)?.formatted(.dateTime.day().month(.abbreviated))
                lines.append("\(mac.name) is asleep\(day.map { ": its list is from \($0)" } ?? "").")
            }
        }
        if unreachable {
            lines.append("Can't reach your hub, so the model can't be changed now. \(ConnectionCheck.hint)")
        }
        return lines.joined(separator: "\n")
    }

    private func load(refresh: Bool) async {
        guard let token = state.token else { return }
        do {
            let found = try await Switchboard.models(token: token, refresh: refresh)
            reached = true
            unreachable = false
            if found != choices { choices = found }
            found.save()
        } catch {
            unreachable = true
        }
    }

    private func choose(_ value: String) {
        guard var changed = choices, value != changed.choice else { return }
        let before = changed
        changed.choice = value
        choices = changed
        Task {
            do {
                guard let token = state.token else { throw URLError(.userAuthenticationRequired) }
                let found = try await Switchboard.models(token: token, set: value)
                choices = found
                found.save()
            } catch {
                choices = before
                problem = "Can't reach your hub to change the model. \(ConnectionCheck.hint)"
            }
        }
    }
}

/// The gear at the top of Recents and Sessions.
struct SettingsButton: View {
    let state: AppState

    var body: some View {
        Button("Settings", systemImage: "gearshape") { state.showsSettings = true }
    }
}
