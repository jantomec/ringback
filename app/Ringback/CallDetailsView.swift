import SwiftUI

/// A call's details, as the Phone app's call info: who, a button to call, then the call itself
/// under its day (when, what kind, how long), and what it was about.
struct CallDetailsView: View {
    let id: String
    let state: AppState
    let calls: CallService

    var body: some View {
        List {
            if let call {
                Section {
                    DetailsHeader(name: call.callerName, subtitle: nil)
                    ActionButtons {
                        ActionButton(title: "call", systemImage: "phone.fill", enabled: canCall(call)) { calls.call(call) }
                    }
                }
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
                Section(call.ringAt.dayLabel) {
                    HStack(alignment: .firstTextBaseline) {
                        Text(call.ringAt.formatted(date: .omitted, time: .shortened))
                            .frame(minWidth: 52, alignment: .leading)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(Self.kind(of: call)).foregroundStyle(call.isMissed ? .red : .primary)
                            if call.onAnotherCall == true {
                                Text("While on another call").font(.subheadline).foregroundStyle(.secondary)
                            }
                        }
                        Spacer()
                        if let duration = call.duration {
                            Text(Self.length(duration)).foregroundStyle(.secondary)
                        }
                    }
                }
                if let subject = call.subject {
                    Section(call.callsBack != nil ? "Called Back About" : "Subject") {
                        Text(subject)
                    }
                }
            }
        }
        .listStyle(.insetGrouped)
        .navigationBarTitleDisplayMode(.inline)
    }

    private var call: CallRecord? { state.calls.first { $0.id == id } }

    private func canCall(_ call: CallRecord) -> Bool {
        call.outcome != .ringing && call.outcome != .onCall && (call.voice != nil || state.contact(for: call) != nil)
    }

    /// What kind of call it was, as the Phone app names them, with ours.
    static func kind(of call: CallRecord) -> String {
        if call.isOutgoing {
            if call.outcome == .onCall || call.outcome == .ringing { return "On Call" }
            if call.outcome == .failed { return "Didn't Connect" }
            if call.contact == true { return "Called from Sessions" }
            return call.calledNow == true ? "Called While Waiting" : "Called Back"
        }
        return switch call.outcome {
        case .ringing: "Ringing…"
        case .onCall: "On Call"
        case .answered: "Incoming Call"
        case .declined: "Declined Call"
        case .missed: "Missed Call"
        case .busy: "You Were on Another Call"
        case .failed: "Couldn't Ring"
        }
    }

    /// "2 minutes", "45 seconds", as the Phone app says how long a call lasted.
    static func length(_ seconds: TimeInterval) -> String {
        Duration.seconds(seconds.rounded())
            .formatted(.units(allowed: [.hours, .minutes, .seconds], width: .wide, maximumUnitCount: 1))
    }
}

/// A call that waits for your work hours: who, buttons to call it now and, with a question,
/// to send it to the Claude app, when it rings, and what it's about.
struct WaitingDetailsView: View {
    let id: String
    let state: AppState
    let calls: CallService
    @State private var isSending = false

    var body: some View {
        List {
            if let call {
                Section {
                    DetailsHeader(name: call.callerName, subtitle: nil)
                    ActionButtons {
                        ActionButton(title: "call now", systemImage: "phone.fill") { calls.callNow(call) }
                        if call.question {
                            ActionButton(title: isSending ? "sending…" : "Claude App", systemImage: "arrow.up.forward.app") {
                                isSending = true
                                Task {
                                    await ClaudeApp.send(call, state: state)
                                    isSending = false
                                }
                            }
                        }
                    }
                }
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
                Section(call.heldUntil.dayLabel) {
                    LabeledContent("Rings", value: call.heldUntil.formatted(date: .omitted, time: .shortened))
                        .foregroundStyle(.orange)
                    if call.question {
                        Text("The session asks you a question, and waits for your answer.")
                    }
                }
                if let subject = call.subject {
                    Section("Subject") { Text(subject) }
                }
            }
        }
        .listStyle(.insetGrouped)
        .navigationBarTitleDisplayMode(.inline)
    }

    private var call: WaitingCall? { state.waiting.first { $0.id == id } }
}

/// The top of a details screen, as the Phone app's: a large round monogram, and the name.
struct DetailsHeader: View {
    let name: String
    let subtitle: String?

    var body: some View {
        VStack(spacing: 8) {
            Text(Self.monogram(name))
                .font(.system(size: 40, weight: .semibold, design: .rounded))
                .foregroundStyle(.white)
                .frame(width: 96, height: 96)
                .background(
                    LinearGradient(colors: [Color(white: 0.66), Color(white: 0.47)], startPoint: .top, endPoint: .bottom),
                    in: Circle())
                .accessibilityHidden(true)
            Text(name).font(.title.weight(.semibold)).multilineTextAlignment(.center)
            if let subtitle {
                Text(subtitle).font(.subheadline).foregroundStyle(.secondary).multilineTextAlignment(.center)
            }
        }
        .frame(maxWidth: .infinity)
    }

    /// Up to two letters: the first of each word in a project's name ("docs-site" is "DS").
    static func monogram(_ name: String) -> String {
        let words = name.split { !$0.isLetter && !$0.isNumber }
        return String(words.prefix(2).compactMap(\.first)).uppercased()
    }
}

/// The row of buttons under a details screen's name.
struct ActionButtons<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        HStack(spacing: 8) { content }
            .listRowInsets(EdgeInsets(top: 4, leading: 0, bottom: 4, trailing: 0))
    }
}

/// One of them, as the Phone app's: an icon over a small label, on a rounded card.
struct ActionButton: View {
    let title: String
    let systemImage: String
    var enabled = true
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(spacing: 4) {
                Image(systemName: systemImage).font(.title3)
                Text(title).font(.caption)
            }
            .frame(maxWidth: .infinity, minHeight: 56)
            .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 12))
        }
        .buttonStyle(.borderless)
        .disabled(!enabled)
    }
}

extension Date {
    /// The day a details screen shows a call under: "Today", "Yesterday", or the date.
    var dayLabel: String {
        let calendar = Calendar.current
        if calendar.isDateInToday(self) { return String(localized: "Today") }
        if calendar.isDateInYesterday(self) { return String(localized: "Yesterday") }
        if calendar.isDateInTomorrow(self) { return String(localized: "Tomorrow") }
        return formatted(.dateTime.weekday(.wide).day().month(.wide))
    }
}
