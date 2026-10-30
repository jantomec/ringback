import AVFoundation
import SwiftUI

/// The app, like the iPhone's Phone app: Recents, the call history, and Sessions, the sessions
/// you can call, as tabs. Its settings are in the Settings app (AppSettings). The in-call
/// screen covers both while you're on a call.
struct RootView: View {
    let state: AppState
    let push: PushService
    let calls: CallService
    @Environment(\.scenePhase) private var scenePhase
    @State private var showsCallBackProblem = false

    var body: some View {
        TabView {
            Tab("Recents", systemImage: "clock.fill") {
                RecentsView(state: state, push: push, calls: calls)
            }
            Tab("Sessions", systemImage: "terminal.fill") {
                SessionsView(state: state, calls: calls)
            }
        }
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
        .onChange(of: scenePhase, initial: true) { _, phase in
            guard phase == .active else { return }
            checkMicrophone()  // also after coming back from Settings
            Task { await push.connect() }  // with the work hours, which may have changed in Settings
        }
        // A setting changed in the Settings app while the app ran, or the app wrote one: new work
        // hours or a new address go to the switchboard at once.
        .onReceive(NotificationCenter.default.publisher(for: UserDefaults.didChangeNotification)) { _ in
            Task { await push.settingsChanged() }
        }
        // Ask now: a locked phone in a call can't show the permission prompt. Notifications
        // are for the warning when calls can't reach the phone (ConnectionCheck), and for a
        // waiting call's question.
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
        state.microphoneOff = AVAudioApplication.shared.recordPermission == .denied
    }

    private func showCallBackProblem() {
        showsCallBackProblem = state.callBackProblem != nil
    }
}
