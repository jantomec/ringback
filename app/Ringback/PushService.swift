import Foundation
import PushKit

/// Receives VoIP pushes. iOS requires every one to be reported to CallKit straight away, or it
/// stops delivering them, so each push goes directly to CallService.
final class PushService: NSObject, PKPushRegistryDelegate {
    private let registry = PKPushRegistry(queue: .main)
    private let calls: CallService
    private let state: AppState
    /// The notification token the switchboard last confirmed.
    private var sentNotificationToken: String?
    /// The work hours the switchboard last confirmed, On-device voice then, and its address.
    private var sentHours: [String: String]?
    private var sentOnDevice: Bool?
    private var sentURL: URL?

    init(calls: CallService, state: AppState) {
        self.calls = calls
        self.state = state
        super.init()
        registry.delegate = self
        registry.desiredPushTypes = [.voIP]
    }

    /// Sends the token, the work hours and On-device voice to the switchboard, which is also how
    /// the app checks that calls can reach it, then fetches the calls that wait. On the main
    /// actor, because it updates what the app shows. Automatic checks (app opened, token
    /// arrived, settings changed) skip a check that just succeeded with the same address,
    /// tokens and settings, and only fetch the waiting calls; things you do yourself (Try Again,
    /// pull to refresh) always check.
    @MainActor
    func connect(force: Bool = false) async {
        #if DEBUG
        if Seed.isOn { return }  // screenshots, with sample calls and no switchboard
        #endif
        guard Switchboard.url != nil else { state.connection = .notSetUp; return }
        guard let token = state.token else { state.connection = .waitingForToken; return }
        guard state.connection != .connecting else { return }
        if !force, case .connected(let at) = state.connection, Date.now.timeIntervalSince(at) < 10,
            state.notificationToken == sentNotificationToken, WorkHours.fields == sentHours,
            OnDeviceVoice.isOn == sentOnDevice, Switchboard.url == sentURL
        {
            await state.refreshWaiting()
            return
        }
        state.connection = .connecting
        do {
            let notificationToken = state.notificationToken
            let hours = WorkHours.fields
            let onDevice = OnDeviceVoice.isOn
            let url = Switchboard.url
            try await Switchboard.register(token: token, notificationToken: notificationToken, hours: hours, onDevice: onDevice)
            sentNotificationToken = notificationToken
            sentHours = hours
            sentOnDevice = onDevice
            sentURL = url
            state.lastConnected = .now
            state.connection = .connected(at: .now)
            // For the Control Center control, which can't read the app's settings.
            Shared.switchboard = url
            Shared.token = token
            ConnectionCheck.clear()
            if force { state.confirmations += 1 }
        } catch {
            state.connection = .failed(reason: error.localizedDescription)
            return
        }
        // Settings changed while it registered, which skipped that change: send them too.
        if WorkHours.fields != sentHours || OnDeviceVoice.isOn != sentOnDevice || Switchboard.url != sentURL {
            return await connect()
        }
        await state.refreshWaiting()
    }

    /// A setting changed: in the Settings app, which the app hears when it next runs, or by
    /// the app itself. If it's the switchboard's address, the work hours or On-device voice, the
    /// switchboard gets them now, so they apply from the next call. (Turned on, On-device voice
    /// also lets the calls that wait for the speech computer ring now.)
    @MainActor
    func settingsChanged() async {
        guard Switchboard.url != sentURL || WorkHours.fields != sentHours || OnDeviceVoice.isOn != sentOnDevice
        else { return }
        await connect()
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
