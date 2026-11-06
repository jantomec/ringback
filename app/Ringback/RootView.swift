import AVFoundation
import SwiftUI

/// The app, like the iPhone's Phone app: Recents, the call history, and Sessions, the sessions
/// you can call, as tabs. Its settings are in the Settings app (AppSettings), but for those that
/// can't be, on a sheet from the gear (SettingsSheet). The in-call screen covers both while you're
/// on a call.
struct RootView: View {
    let state: AppState
    let push: PushService
    let calls: CallService
    @Environment(\.scenePhase) private var scenePhase
    @State private var showsCallBackProblem = false
    @State private var page = Page.recents

    enum Page: Hashable {
        case recents, sessions
    }

    var body: some View {
        // The tabs are pills in BottomBar, with the hold switch beside them, instead of the
        // system tab bar.
        TabView(selection: $page) {
            Tab("Recents", systemImage: "clock.fill", value: Page.recents) {
                RecentsView(state: state, push: push, calls: calls)
                    .toolbarVisibility(.hidden, for: .tabBar)
            }
            Tab("Sessions", systemImage: "terminal.fill", value: Page.sessions) {
                SessionsView(state: state, calls: calls)
                    .toolbarVisibility(.hidden, for: .tabBar)
            }
        }
        // On hold, the whole app takes up a faint share of the pill's breathing light.
        .overlay { BreathWash(on: state.hold?.on == true) }
        .safeAreaInset(edge: .bottom) { BottomBar(page: $page, state: state) }
        // Only checks you started, never the automatic one on opening.
        .sensoryFeedback(.success, trigger: state.confirmations)
        .fullScreenCover(isPresented: .constant(state.activeCall != nil), onDismiss: showCallBackProblem) {
            InCallView(state: state, calls: calls)
        }
        // Why a call you made ended by itself, or couldn't start: once the in-call screen is gone.
        .onChange(of: state.callBackProblem) { if state.activeCall == nil { showCallBackProblem() } }
        .alert(state.callBackProblem ?? "", isPresented: $showsCallBackProblem) {
            Button("OK") { state.callBackProblem = nil }
        }
        // The hub's portal pairs this phone with a link to the app (ringback://pair).
        .onOpenURL { url in Task { await push.open(url) } }
        .fullScreenCover(item: Binding(get: { state.openShown }, set: { state.openShown = $0 })) { item in
            ShownView(item: item)
        }
        .sheet(isPresented: Binding(get: { state.justPaired }, set: { state.justPaired = $0 })) {
            PairedView { state.justPaired = false }
        }
        .sheet(isPresented: Binding(get: { state.showsSettings }, set: { state.showsSettings = $0 })) {
            SettingsSheet(state: state)
        }
        .alert("Couldn't Pair", isPresented: .constant(state.pairingProblem != nil)) {
            Button("OK") { state.pairingProblem = nil }
        } message: {
            Text(state.pairingProblem ?? "")
        }
        .onChange(of: scenePhase, initial: true) { _, phase in
            guard phase == .active else { return }
            checkMicrophone()  // also after coming back from Settings
            Task { await push.connect() }  // with the work hours, which may have changed in Settings
            Task { await OnDeviceVoice.prepare(state) }  // its model, while it's on
        }
        // A setting changed in the Settings app while the app ran, or the app wrote one: new work
        // hours, a new address or On-device voice go to the hub at once, and On-device
        // voice turned on gets its model.
        .onReceive(NotificationCenter.default.publisher(for: UserDefaults.didChangeNotification)) { _ in
            Task { await push.settingsChanged() }
            Task { await OnDeviceVoice.prepare(state, always: false) }
        }
        // Ask now: a locked phone in a call can't show the permission prompt. Notifications
        // are for the warning when calls can't reach the phone (ConnectionCheck), for a
        // waiting call's question, and the badge counts the waiting calls.
        .task {
            #if DEBUG
            if Seed.isOn { return }  // screenshots: no system prompts over the app
            #endif
            _ = await AVAudioApplication.requestRecordPermission()
            checkMicrophone()
            await ConnectionCheck.requestPermission()
        }
    }

    private func checkMicrophone() {
        #if DEBUG
        if Seed.isOn { return }  // screenshots: the samples say whether it's off
        #endif
        state.microphoneOff = AVAudioApplication.shared.recordPermission == .denied
    }

    private func showCallBackProblem() {
        showsCallBackProblem = state.callBackProblem != nil
    }
}

/// The phone has just paired with the hub, from the portal's link.
private struct PairedView: View {
    let done: () -> Void

    var body: some View {
        VStack(spacing: 16) {
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 64))
                .foregroundStyle(.green)
                .symbolEffect(.bounce, options: .nonRepeating)
            Text("Paired")
                .font(.title.bold())
            Text(hub.map { "Your hub at \($0) can ring this iPhone now." } ?? "Your hub can ring this iPhone now.")
                .font(.body)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            Button(action: done) {
                Text("Done").frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .padding(.top, 8)
        }
        .padding(.horizontal, 32)
        .padding(.vertical, 24)
        .presentationDetents([.height(320)])
        .presentationDragIndicator(.visible)
    }

    /// The hub's address as you'd recognise it: its host, without the scheme and port.
    private var hub: String? { Switchboard.url?.host() }
}

