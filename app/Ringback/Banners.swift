import SwiftUI
import UIKit

/// What Recents shows above the calls, only while something's wrong: microphone access is off,
/// the app isn't set up, or calls can't reach the phone.
struct Banners: View {
    let state: AppState
    let push: PushService
    @AppStorage(ConnectionCheck.lastWarnedKey) private var lastConnectionWarning = 0.0
    /// Try Again is checking, after a failed check: the banner stays meanwhile.
    @State private var isRetrying = false
    @Environment(\.openURL) private var openURL

    var body: some View {
        if state.microphoneOff {
            Banner(
                systemImage: "mic.slash.fill", title: "Microphone is off",
                message: "Claude can't hear you on calls. Turn on Microphone in Ringback's settings."
            ) {
                Button("Open Settings", action: openSettings)
            }
        }
        // The state starts as not set up, until the first check: the address says.
        if state.connection == .notSetUp && Switchboard.url == nil {
            Banner(
                systemImage: "gearshape.fill", title: "Set up Ringback",
                message: "Enter your switchboard's Nebula address in Ringback's settings, such as 10.0.0.1."
            ) {
                Button("Open Settings", action: openSettings)
            }
        } else if lastConnectionWarning > 0 || state.connection.failed || isRetrying {
            // A connection check in the background warned you, or the switchboard can't be
            // reached now. It repeats the warning, which is gone once you tap it, and goes away
            // by itself once the switchboard is reachable again.
            Banner(systemImage: "phone.down.fill", title: ConnectionCheck.title, message: message) {
                if isRetrying || state.connection == .connecting {
                    ProgressView()
                } else {
                    Button("Try Again") {
                        isRetrying = true
                        Task {
                            await push.connect(force: true)
                            isRetrying = false
                        }
                    }
                }
            }
        }
    }

    /// The warning, with when the switchboard last confirmed this phone.
    private var message: String {
        let last = state.lastConnected.map {
            " Last connected \(Calendar.current.isDateInToday($0) ? $0.formatted(date: .omitted, time: .shortened) : $0.recentsLabel)."
        }
        return ConnectionCheck.message + (last ?? "")
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
