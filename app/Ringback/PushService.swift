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

    /// Sends the token to the switchboard. On the main actor, because it updates what the
    /// settings screen shows.
    @MainActor
    func register() async {
        guard let token = state.token else { return }
        guard !(UserDefaults.standard.string(forKey: Switchboard.urlKey) ?? "").isEmpty else {
            state.registration = "Enter the switchboard's address above."
            return
        }
        state.registering = true
        defer { state.registering = false }
        do {
            try await Switchboard.register(token: token)
            state.registration = "Registered at \(Date.now.formatted(date: .omitted, time: .standard))"
        } catch {
            state.registration = "Couldn't reach the switchboard: \(error.localizedDescription)"
        }
    }

    func pushRegistry(_ registry: PKPushRegistry, didUpdate credentials: PKPushCredentials, for type: PKPushType) {
        state.token = credentials.token.map { String(format: "%02x", $0) }.joined()
        Task { await register() }
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
