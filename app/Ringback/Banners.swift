import SwiftUI
import UIKit

/// What Recents shows above the calls, only while something's wrong: the app isn't set up or
/// the hub hasn't paired with it, calls can't reach the phone (the hub can't be reached, or Apple refused its push), calls
/// can't be talked through (no speech computer), On-device voice isn't ready, or microphone
/// access is off. Only the most serious shows, with the others a tap away.
struct Banners: View {
    let state: AppState
    let push: PushService
    @AppStorage(ConnectionCheck.lastWarnedKey) private var lastConnectionWarning = 0.0
    /// Try Again is checking, after a failed check: the banner stays meanwhile.
    @State private var isRetrying = false
    /// Every problem shows, not only the most serious.
    @State private var showsAll = false
    @Environment(\.openURL) private var openURL

    /// What's wrong, most serious first.
    private enum Problem: Hashable {
        case notSetUp, notPaired, unreachable, push(String), speech(waiting: Int), onDevice(String), microphone
    }

    private var problems: [Problem] {
        var found: [Problem] = []
        // The state starts as not set up, until the first check: the address says.
        if state.connection == .notSetUp && Switchboard.url == nil {
            found.append(.notSetUp)
        } else if state.connection == .notPaired {
            found.append(.notPaired)
        } else if lastConnectionWarning > 0 || state.connection.failed || isRetrying {
            // A connection check in the background warned you, or the hub can't be
            // reached now. It repeats the warning, which is gone once you tap it, and goes
            // away by itself once the hub is reachable again.
            found.append(.unreachable)
        } else if let status = state.status {
            // Only while the hub answers: otherwise what it said last may be stale.
            if let problem = status.pushProblem { found.append(.push(problem)) }
            if !status.speechOK { found.append(.speech(waiting: status.waitingForSpeech)) }
        }
        if let problem = state.onDeviceProblem { found.append(.onDevice(problem)) }
        if state.microphoneOff { found.append(.microphone) }
        return found
    }

    /// The hub says something's wrong, which mending elsewhere (the speech computer starting,
    /// the push key fixed) puts right without the app hearing of it.
    private var hubProblem: Bool {
        guard let status = state.status else { return false }
        return status.pushProblem != nil || !status.speechOK
    }

    var body: some View {
        let found = problems
        ForEach(showsAll ? found : Array(found.prefix(1)), id: \.self, content: banner)
            // So the banner goes once it's mended: ask again every 10 s while it shows.
            .task(id: hubProblem) {
                while hubProblem, !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(10))
                    await state.refreshWaiting()
                }
            }
        if found.count > 1 {
            Button(showsAll ? "Show Less" : found.count == 2 ? "1 More Problem" : "\(found.count - 1) More Problems") {
                withAnimation { showsAll.toggle() }
            }
            .font(.subheadline)
            .foregroundStyle(.tint)
            .listRowSeparator(.hidden)
        }
    }

    @ViewBuilder
    private func banner(_ problem: Problem) -> some View {
        switch problem {
        case .notSetUp:
            Banner(
                systemImage: "gearshape.fill", title: "Set up Ringback",
                message: "Enter your hub's address in Ringback's settings, such as 10.0.0.1."
            ) {
                Button("Open Settings", action: openSettings)
            }
        case .notPaired:
            Banner(
                systemImage: "lock.iphone", title: "Pair this iPhone",
                message: "Your hub needs this iPhone paired before calls can ring it. Pair opens the hub's portal in "
                    + "Safari: sign in, tap Pair this phone, and Ringback opens again to finish."
            ) {
                Button("Pair", action: openPairing)
            }
        case .unreachable:
            Banner(systemImage: "phone.down.fill", title: ConnectionCheck.title, message: message) {
                if isRetrying || state.connection == .connecting {
                    ProgressView()
                } else {
                    Button("Try Again") { checkAgain() }
                }
            }
        case .push(let why):
            Banner(systemImage: "bell.slash.fill", title: "Calls can't ring this phone", message: pushMessage(why)) {
                Button(why == "phone" ? "Register Again" : "Check Again") { checkAgain() }
            }
        case .speech(let waiting):
            Banner(
                systemImage: "waveform.slash", title: "Claude can't talk on calls",
                message: "Your speech computer isn't connected to your hub, so calls wait instead of ringing. "
                    + "They ring once it's back, or now if you turn on On-device Voice in Ringback's settings."
                    + (waiting == 0 ? "" : waiting == 1 ? " One call is waiting." : " \(waiting) calls are waiting.")
            ) {
                Button("Check Again") { checkAgain() }
            }
        case .onDevice(let problem):
            Banner(
                systemImage: "waveform.slash", title: "On-device voice isn't ready",
                message: "\(problem) While your speech computer is offline, calls can't be talked through on this iPhone."
            ) {
                Button("Try Again") { Task { await OnDeviceVoice.prepare(state) } }
            }
        case .microphone:
            Banner(
                systemImage: "mic.slash.fill", title: "Microphone is off",
                message: "Claude can't hear you on calls. Turn on Microphone in Ringback's settings."
            ) {
                Button("Open Settings", action: openSettings)
            }
        }
    }

    /// Why the last push didn't reach this phone, and what to do, by what the hub says.
    private func pushMessage(_ why: String) -> String {
        switch why {
        case "key": "Apple refused your hub's push key, or it has none, so calls can't reach this phone. Check the key in your hub's config."
        case "phone": "Apple no longer knows this phone's registration with your hub, so calls can't reach it. Register again."
        case "network": "Your hub couldn't reach Apple the last time it tried to ring this phone. Check that the hub is online."
        default: "Apple refused your hub's last push to this phone, so calls may not ring."
        }
    }

    /// Checks again, registering this phone and fetching what the hub says.
    private func checkAgain() {
        isRetrying = true
        Task {
            await push.connect(force: true)
            isRetrying = false
        }
    }

    /// The warning, with when the hub last confirmed this phone.
    private var message: String {
        let last = state.lastConnected.map {
            " Last connected \(Calendar.current.isDateInToday($0) ? $0.formatted(date: .omitted, time: .shortened) : $0.recentsLabel)."
        }
        return ConnectionCheck.message + (last ?? "")
    }

    /// The hub's pairing page, in Safari: it signs you in if need be, and its button opens
    /// Ringback with a pairing link (ringback://pair).
    private func openPairing() {
        if let url = Switchboard.url?.appending(path: "portal/pair") { openURL(url) }
    }

    /// Ringback's page in the Settings app, where its settings and the Microphone switch are;
    /// apps can't link any deeper than that.
    private func openSettings() {
        if let url = URL(string: UIApplication.openSettingsURLString) { openURL(url) }
    }
}

/// One banner: an orange icon, what's wrong, and what to do about it.
private struct Banner<Action: View>: View {
    let systemImage: String
    let title: String
    let message: String
    @ViewBuilder let action: Action

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: systemImage).font(.title2).foregroundStyle(.orange).frame(width: 32)
            VStack(alignment: .leading, spacing: 6) {
                Text(title).font(.headline)
                Text(message).font(.subheadline).foregroundStyle(.secondary)
                action.buttonStyle(.bordered).controlSize(.small)
            }
            Spacer(minLength: 0)
        }
        .padding(14)
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 16))
        .listRowSeparator(.hidden)
        .listRowInsets(EdgeInsets(top: 6, leading: 16, bottom: 6, trailing: 16))
    }
}
