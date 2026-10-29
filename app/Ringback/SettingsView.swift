import AVFoundation
import SwiftUI
import UIKit

struct SettingsView: View {
    let state: AppState
    let push: PushService
    let calls: CallService
    @AppStorage(Switchboard.urlKey) private var switchboardURL = ""
    @AppStorage(AudioLink.sensitivityKey) private var sensitivity = AudioLink.defaultSensitivity
    @Environment(\.scenePhase) private var scenePhase
    @State private var microphoneOff = false

    var body: some View {
        NavigationStack {
            Form {
                if microphoneOff {
                    MicrophoneBanner()
                }
                Section {
                    if switchboardURL.isEmpty {
                        // First run: nothing to glance at yet, so ask for the address right here.
                        StatusRow(connection: state.connection, lastConnected: nil, detailed: true)
                        AddressFields(push: push)
                    } else {
                        NavigationLink {
                            SwitchboardView(state: state, push: push)
                        } label: {
                            StatusRow(connection: state.connection, lastConnected: state.lastConnected, detailed: false)
                        }
                        if case .failed = state.connection {
                            Button("Try Again") { Task { await push.connect(force: true) } }
                        }
                    }
                } footer: {
                    if switchboardURL.isEmpty { AddressFields.footer }
                }
                Section {
                    VStack(alignment: .leading) {
                        Text("Sensitivity")
                        Slider(value: $sensitivity, in: 0...1, step: 0.1) {
                            Text("Sensitivity")
                        } minimumValueLabel: {
                            Text("Less")
                        } maximumValueLabel: {
                            Text("More")
                        }
                    }
                } header: {
                    Text("Interruptions")
                } footer: {
                    Text(
                        "How easily you stop Claude by speaking. More stops it at quieter or shorter speech, for quiet places. Less needs clearer, longer speech, for noisy ones. Changes apply from the next call."
                    )
                }
                Section("Calls") {
                    if state.calls.isEmpty {
                        Text("No calls yet").foregroundStyle(.secondary)
                    }
                    ForEach(state.calls) { CallRow(call: $0) }
                }
            }
            .navigationTitle("Ringback")
            .refreshable { await push.connect(force: true) }
        }
        // Once, for both screens: only checks you started, never the automatic one on opening.
        .sensoryFeedback(.success, trigger: state.confirmations)
        .fullScreenCover(isPresented: .constant(state.activeCall != nil)) {
            InCallView(state: state, calls: calls)
        }
        .onChange(of: scenePhase, initial: true) { _, phase in
            guard phase == .active else { return }
            checkMicrophone()  // also after coming back from Settings
            Task { await push.connect() }
        }
        // Ask now: a locked phone in a call can't show the permission prompt.
        .task {
            _ = await AVAudioApplication.requestRecordPermission()
            checkMicrophone()
        }
    }

    private func checkMicrophone() {
        microphoneOff = AVAudioApplication.shared.recordPermission == .denied
    }
}

/// Shown while microphone access is off. Settings opens on Ringback's own page, where the
/// Microphone switch is; apps can't link any deeper than that.
private struct MicrophoneBanner: View {
    @Environment(\.openURL) private var openURL

    var body: some View {
        Section {
            HStack(spacing: 14) {
                Image(systemName: "mic.slash.fill").font(.title2).foregroundStyle(.orange).frame(width: 36)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Microphone is off").font(.headline)
                    Text("Claude can't hear you on calls. Turn on Microphone in Ringback's settings.")
                        .font(.subheadline).foregroundStyle(.secondary)
                }
            }
            .padding(.vertical, 6)
            Button("Open Settings") {
                if let url = URL(string: UIApplication.openSettingsURLString) { openURL(url) }
            }
        }
    }
}

/// Status and address of the switchboard, opened from the main screen's status row.
private struct SwitchboardView: View {
    let state: AppState
    let push: PushService

    var body: some View {
        Form {
            Section {
                StatusRow(connection: state.connection, lastConnected: state.lastConnected, detailed: true)
            }
            Section {
                AddressFields(push: push)
            } footer: {
                AddressFields.footer
            }
        }
        .navigationTitle("Switchboard")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await push.connect(force: true) }
    }
}

/// The address field and its button: "Connect" for a new address, "Check Now" for the
/// current one.
private struct AddressFields: View {
    let push: PushService
    @AppStorage(Switchboard.urlKey) private var switchboardURL = ""
    @State private var draft = Switchboard.displayHost ?? ""

    static let footer = Text(
        "The switchboard's Nebula address. Port \(Switchboard.defaultPort) is used unless you add another.")

    var body: some View {
        LabeledContent("Address") {
            TextField("10.0.0.1", text: $draft)
                .multilineTextAlignment(.trailing)
                .keyboardType(.numbersAndPunctuation)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.join)  // iOS has no custom labels; "Join" is closest to connecting
                .onSubmit(apply)
        }
        Button(isNew ? "Connect" : "Check Now", action: apply)
            .disabled(Switchboard.normalize(draft) == nil)
    }

    private var isNew: Bool {
        Switchboard.normalize(draft)?.absoluteString != switchboardURL
    }

    private func apply() {
        guard let url = Switchboard.normalize(draft) else { return }
        switchboardURL = url.absoluteString
        draft = Switchboard.displayHost ?? draft
        Task { await push.connect(force: true) }
    }
}

/// The answer to "can calls reach me right now?". The main screen's row is short; the
/// switchboard screen says more.
private struct StatusRow: View {
    let connection: Connection
    let lastConnected: Date?
    let detailed: Bool

    var body: some View {
        HStack(spacing: 14) {
            Group {
                if connection == .connecting || connection == .waitingForToken {
                    ProgressView()
                } else {
                    Image(systemName: icon).font(.title).foregroundStyle(color)
                }
            }
            .frame(width: 36)
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.headline)
                Text(subtitle).font(.subheadline).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 6)
    }

    private var host: String { Switchboard.displayHost ?? "the switchboard" }

    private var icon: String {
        switch connection {
        case .connected: "checkmark.circle.fill"
        case .failed: "exclamationmark.triangle.fill"
        default: "circle.dashed"
        }
    }

    private var color: Color {
        switch connection {
        case .connected: .green
        case .failed: .orange
        default: .secondary
        }
    }

    private var title: String {
        switch connection {
        case .notSetUp: "Not set up"
        case .waitingForToken: "Getting ready…"
        case .connecting: "Connecting…"
        case .connected: "Ready for calls"
        case .failed: "Can't reach the switchboard"
        }
    }

    private var subtitle: String {
        switch connection {
        case .notSetUp:
            return "Enter your switchboard's address."
        case .waitingForToken:
            return "Waiting for iOS to enable calls."
        case .connecting:
            return "Switchboard \(host)"
        case .connected(let at):
            return detailed ? "Checked \(Self.when(at))" : "Switchboard \(host)"
        case .failed(let reason):
            let last = lastConnected.map { " Last connected \(Self.when($0))." } ?? ""
            return detailed ? "\(reason) Is Nebula connected?\(last)" : "Is Nebula connected?\(last)"
        }
    }

    private static func when(_ date: Date) -> String {
        Calendar.current.isDateInToday(date)
            ? "at \(date.formatted(date: .omitted, time: .shortened))"
            : date.formatted(date: .abbreviated, time: .shortened)
    }
}

private struct CallRow: View {
    let call: CallRecord

    var body: some View {
        HStack {
            Image(systemName: icon).foregroundStyle(color).frame(width: 24)
            VStack(alignment: .leading, spacing: 2) {
                Text(call.callerName).font(.body.weight(.medium))
                if let subject = call.subject {
                    Text(subject).font(.subheadline)
                }
                Text(detail).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            Text(when).font(.caption).foregroundStyle(.secondary)
        }
    }

    private var when: String {
        Calendar.current.isDateInToday(call.ringAt)
            ? call.ringAt.formatted(date: .omitted, time: .shortened)
            : call.ringAt.formatted(date: .abbreviated, time: .shortened)
    }

    private var icon: String {
        switch call.outcome {
        case .answered, .onCall, .ringing: "phone.fill"
        case .declined, .missed, .busy, .failed: "phone.down.fill"
        }
    }

    private var color: Color {
        switch call.outcome {
        case .answered, .onCall: .green
        case .ringing: .secondary
        case .declined, .missed, .busy, .failed: .red
        }
    }

    private var detail: String {
        let waiting = call.onAnotherCall == true ? " · while on another call" : ""
        return outcomeText + waiting
    }

    private var outcomeText: String {
        switch call.outcome {
        case .ringing: "Ringing…"
        case .onCall: "On call"
        case .answered:
            "Answered · " + Duration.seconds(call.duration ?? 0).formatted(.time(pattern: .minuteSecond))
        case .declined: "Declined"
        case .missed: "Missed"
        case .busy: "You were on another call"
        case .failed: "Couldn't ring"
        }
    }
}
