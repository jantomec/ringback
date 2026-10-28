import SwiftUI

struct SettingsView: View {
    let state: AppState
    let push: PushService
    @AppStorage(Switchboard.urlKey) private var switchboardURL = ""

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("http://10.0.0.1:8787", text: $switchboardURL)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .submitLabel(.join)  // iOS has no custom labels; "Join" is closest to registering
                        .onSubmit { Task { await push.register() } }
                    Button(state.registering ? "Registering…" : "Register this phone") {
                        Task { await push.register() }
                    }
                    .disabled(switchboardURL.isEmpty || state.token == nil || state.registering)
                } header: {
                    Text("Switchboard")
                } footer: {
                    Text(state.token == nil ? "Waiting for a push token from iOS…" : state.registration)
                }
                Section("Calls") {
                    if state.calls.isEmpty {
                        Text("No calls yet").foregroundStyle(.secondary)
                    }
                    ForEach(state.calls) { CallRow(call: $0) }
                }
            }
            .navigationTitle("Ringback")
        }
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
