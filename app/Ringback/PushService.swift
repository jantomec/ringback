import Foundation
import PushKit

/// Receives VoIP pushes. iOS requires every one to be reported to CallKit straight away, or it
/// stops delivering them, so each push goes directly to CallService.
final class PushService: NSObject, PKPushRegistryDelegate {
    private let registry = PKPushRegistry(queue: .main)
    private let calls: CallService
    private let state: AppState

    init(calls: CallService, state: AppState) {
        self.calls = calls
        self.state = state
        super.init()
        registry.delegate = self
        registry.desiredPushTypes = [.voIP]
    }

    /// Sends the token to the switchboard, which is also how the app checks that calls can
    /// reach it. On the main actor, because it updates what the settings screen shows.
    /// Automatic checks (app opened, token arrived) skip a check that just succeeded; things
    /// you do yourself (Try Again, pull to refresh, Connect) always check.
    @MainActor
    func connect(force: Bool = false) async {
        guard Switchboard.url != nil else { state.connection = .notSetUp; return }
        guard let token = state.token else { state.connection = .waitingForToken; return }
        guard state.connection != .connecting else { return }
        if !force, case .connected(let at) = state.connection, Date.now.timeIntervalSince(at) < 10 { return }
        state.connection = .connecting
        do {
            try await Switchboard.register(token: token)
            state.lastConnected = .now
            state.connection = .connected(at: .now)
            if force { state.confirmations += 1 }
        } catch {
            state.connection = .failed(reason: error.localizedDescription)
        }
    }

    func pushRegistry(_ registry: PKPushRegistry, didUpdate credentials: PKPushCredentials, for type: PKPushType) {
        state.token = credentials.token.map { String(format: "%02x", $0) }.joined()
        Task { await connect() }
    }

    func pushRegistry(
        _ registry: PKPushRegistry,
        didReceiveIncomingPushWith payload: PKPushPayload,
        for type: PKPushType,
        completion: @escaping () -> Void
    ) {
        calls.reportIncoming(payload.dictionaryPayload, completion: completion)
    }
}
