import SwiftUI

/// The call history, as the Phone app's Recents: the calls that wait for your work hours at
/// the top, in orange, under "Waiting", what sessions showed you under "Shown" (ShownShelf),
/// then every call, most recent first, under "Calls" (without headers while there's only
/// that). Tapping a call calls its session, and its ⓘ
/// opens its details. A banner at the top says when calls can't reach the phone.
struct RecentsView: View {
    let state: AppState
    let push: PushService
    let calls: CallService
    @State private var details: Details?

    /// The call whose details are open, or all that sessions showed you.
    enum Details: Hashable {
        case call(String)
        case waiting(String)
        case shown
    }

    var body: some View {
        NavigationStack {
            List {
                Banners(state: state, push: push)
                if state.waiting.isEmpty && state.shown.isEmpty {
                    history
                } else {
                    // Headers keep the waiting calls, in orange, apart from the calls you missed,
                    // in red, and what sessions showed you between them.
                    if !state.waiting.isEmpty {
                        Section("Waiting") {
                            ForEach(state.waiting) { call in
                                WaitingRow(call: call, state: state, calls: calls) { details = .waiting(call.id) }
                            }
                        }
                    }
                    if !state.shown.isEmpty {
                        ShownShelf(state: state) { details = .shown }
                    }
                    if !state.calls.isEmpty {
                        Section("Calls") { history }
                    }
                }
            }
            .listStyle(.plain)
            .overlay {
                if state.calls.isEmpty && state.waiting.isEmpty && state.shown.isEmpty {
                    ContentUnavailableView(
                        "No Recents", systemImage: "clock",
                        description: Text("Calls from your sessions show here."))
                }
            }
            .navigationTitle("Recents")
            // Checks that calls can reach the phone, and fetches the waiting calls.
            .refreshable { await push.connect(force: true) }
            .navigationDestination(item: $details) { shown in
                switch shown {
                case .call(let id): CallDetailsView(id: id, state: state, calls: calls)
                case .waiting(let id): WaitingDetailsView(id: id, state: state, calls: calls)
                case .shown: ShownGallery(state: state)
                }
            }
        }
    }

    private var history: some View {
        ForEach(state.calls) { call in
            CallRow(call: call, state: state, calls: calls) { details = .call(call.id) }
        }
    }
}

/// One call in the history, as a row of the Phone app's Recents: the caller, red if you didn't
/// take it, with a small arrow if you made the call; its subject; when; and ⓘ. Tapping it calls
/// its session.
private struct CallRow: View {
    let call: CallRecord
    let state: AppState
    let calls: CallService
    let showDetails: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            if canCall {
                Button { calls.call(call) } label: { content }
                    .buttonStyle(RowStyle())
                    .accessibilityHint("Calls \(call.callerName)")
            } else {
                content
            }
            InfoButton(action: showDetails)
        }
    }

    /// Older calls in the history may have no address for their audio: those call the session
    /// from Sessions, while it's there.
    private var canCall: Bool {
        call.outcome != .ringing && call.outcome != .onCall && (call.voice != nil || state.contact(for: call) != nil)
    }

    private var content: some View {
        RowContent(
            name: call.callerName, nameColor: call.isMissed ? .red : .primary, outgoing: call.isOutgoing,
            detail: call.subject ?? CallDetailsView.kind(of: call), when: call.ringAt.recentsLabel)
    }
}

/// A call that waits for your work hours, at the top of the history, in orange, with when it
/// rings, and whether it has a question. Tapping it calls it now. Its swipe actions: "Cancel
/// Call", so it never rings (asked first if the session waits for your answer); and, with a
/// question, "Claude App", that sends the question to the session's own dialog, which Remote
/// Control shows in the Claude app (ringback turns it on in the session); the call then never
/// rings.
private struct WaitingRow: View {
    let call: WaitingCall
    let state: AppState
    let calls: CallService
    let showDetails: () -> Void
    @State private var isSending = false
    @State private var isCancelling = false
    @State private var confirming = false

    var body: some View {
        HStack(spacing: 8) {
            Button { calls.callNow(call) } label: {
                RowContent(name: call.callerName, nameColor: .orange, outgoing: false, detail: detail, when: "")
            }
            .buttonStyle(RowStyle())
            .accessibilityHint("Calls it now")
            InfoButton(action: showDetails)
        }
        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
            Button {
                if call.question { confirming = true } else { cancel() }
            } label: {
                Label("Cancel Call", systemImage: "phone.down.fill")
            }
            .tint(.red)
            if call.question {
                Button {
                    isSending = true
                    Task {
                        await ClaudeApp.send(call, state: state)
                        isSending = false
                    }
                } label: {
                    Label("Claude App", systemImage: "arrow.up.forward.app")
                }
                .tint(.indigo)
            }
        }
        .confirmationDialog(CancelCall.confirmTitle, isPresented: $confirming, titleVisibility: .visible) {
            Button("Cancel Call", role: .destructive) { cancel() }
            Button("Keep Call", role: .cancel) {}
        } message: {
            Text(CancelCall.confirmMessage)
        }
    }

    private func cancel() {
        isCancelling = true
        Task {
            await CancelCall.send(call, state: state)
            isCancelling = false
        }
    }

    /// "Rings at 08:00 · subject", or "Question · rings at 08:00 · subject"; "Not answered ·
    /// call it back" after a call's last try.
    private var detail: String {
        if isSending { return "Sending its question to the Claude app…" }
        if isCancelling { return "Cancelling…" }
        let rings: String
        if let when = call.rings(sentence: true) {
            rings = call.question ? "Question · rings \(when)" : "Rings \(when)"
        } else {
            rings = call.question ? "Question · not answered · call it back" : "Not answered · call it back"
        }
        return [rings, call.subject].compactMap { $0 }.joined(separator: " · ")
    }
}

/// What a row of Recents shows: the caller on the first line, after a small arrow for a call
/// you made, a second line in secondary, and on the right, when.
private struct RowContent: View {
    let name: String
    let nameColor: Color
    let outgoing: Bool
    let detail: String
    let when: String

    var body: some View {
        HStack(spacing: 6) {
            // As in the Phone app: a column for the arrow, so every name starts in the same place.
            Image(systemName: "phone.arrow.up.right")
                .font(.footnote.weight(.semibold))
                .foregroundStyle(.secondary)
                .frame(width: 16)
                .opacity(outgoing ? 1 : 0)
                .accessibilityHidden(!outgoing)
                .accessibilityLabel("Outgoing")
            VStack(alignment: .leading, spacing: 1) {
                Text(name).font(.headline).foregroundStyle(nameColor).lineLimit(1)
                Text(detail).font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: 8)
            Text(when).font(.subheadline).foregroundStyle(.secondary)
        }
        .contentShape(Rectangle())
    }
}

/// The ⓘ on the right of a row, in the accent colour.
struct InfoButton: View {
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: "info.circle").font(.title3)
        }
        .buttonStyle(.borderless)
        .accessibilityLabel("Details")
    }
}

/// A row you tap: it greys while pressed, edge to edge, as a list row does.
struct RowStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .background {
                if configuration.isPressed {
                    Color(.systemGray5).padding(.vertical, -10).padding(.leading, -20).padding(.trailing, -60)
                }
            }
    }
}

extension Date {
    /// When a call was, as the Phone app's Recents say it: the time today, "Yesterday", the
    /// weekday within the last week, and the date before that.
    var recentsLabel: String {
        let calendar = Calendar.current
        if calendar.isDateInToday(self) { return formatted(date: .omitted, time: .shortened) }
        if calendar.isDateInYesterday(self) { return String(localized: "Yesterday") }
        if let week = calendar.date(byAdding: .day, value: -6, to: calendar.startOfDay(for: .now)), self >= week {
            return formatted(.dateTime.weekday(.wide))
        }
        return formatted(date: .numeric, time: .omitted)
    }

    /// When a waiting call rings: "at 08:00" today, "tomorrow at 08:00", or "Sat at 08:00".
    var ringsLabel: String {
        let time = formatted(date: .omitted, time: .shortened)
        let calendar = Calendar.current
        if calendar.isDateInToday(self) { return "at \(time)" }
        if calendar.isDateInTomorrow(self) { return "tomorrow at \(time)" }
        return "\(formatted(.dateTime.weekday(.abbreviated))) at \(time)"
    }
}
