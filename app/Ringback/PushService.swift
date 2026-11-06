import Foundation
import PushKit

/// Receives VoIP pushes. iOS requires every one to be reported to CallKit straight away, or it
/// stops delivering them, so each push goes directly to CallService.
final class PushService: NSObject, PKPushRegistryDelegate {
    private let registry = PKPushRegistry(queue: .main)
    private let calls: CallService
    private let state: AppState
    /// The notification token the hub last confirmed.
    private var sentNotificationToken: String?
    /// The work hours the hub last confirmed, On-device voice then, and its address.
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

    /// Sends the token, the work hours and On-device voice to the hub, which is also how
    /// the app checks that calls can reach it, then fetches the calls that wait. On the main
    /// actor, because it updates what the app shows. Automatic checks (app opened, token
    /// arrived, settings changed) skip a check that just succeeded with the same address,
    /// tokens and settings, and only fetch the waiting calls; things you do yourself (Try Again,
    /// pull to refresh) always check.
    @MainActor
    func connect(force: Bool = false) async {
        #if DEBUG
        if Seed.isOn { return }  // screenshots, with sample calls and no hub
        #endif
        guard Switchboard.url != nil else { state.connection = .notSetUp; return }
        guard let token = state.token else { state.connection = .waitingForToken; return }
        guard state.connection != .connecting else { return }
        if !force, state.pairingCode == nil, case .connected(let at) = state.connection, Date.now.timeIntervalSince(at) < 10,
            state.notificationToken == sentNotificationToken, WorkHours.fields == sentHours,
            OnDeviceVoice.isOn == sentOnDevice, Switchboard.url == sentURL
        {
            await state.refreshWaiting()
            await state.refreshShown()
            return
        }
        state.connection = .connecting
        do {
            let notificationToken = state.notificationToken
            let hours = WorkHours.fields
            let onDevice = OnDeviceVoice.isOn
            let url = Switchboard.url
            if let code = state.pairingCode {
                // A pairing link opened the app: the code makes the phone its key. If the hub
                // can't be reached, the code stays for the next try, within its 10 minutes.
                Shared.key = try await Switchboard.pair(
                    code: code, token: token, notificationToken: notificationToken, hours: hours, onDevice: onDevice)
                state.pairingCode = nil
                state.justPaired = true
            } else {
                try await Switchboard.register(token: token, notificationToken: notificationToken, hours: hours, onDevice: onDevice)
            }
            state.needsPairing = false
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
        } catch is NotPaired {
            state.connection = .notPaired
            state.needsPairing = true
            return
        } catch let refused as PairingRefused {
            state.pairingCode = nil
            state.connection = .notPaired
            state.needsPairing = true
            state.pairingProblem = "\(refused.localizedDescription). Pair again from your hub's portal."
            return
        } catch {
            state.connection = .failed(reason: error.localizedDescription)
            return
        }
        // Settings changed while it registered, which skipped that change: send them too.
        if WorkHours.fields != sentHours || OnDeviceVoice.isOn != sentOnDevice || Switchboard.url != sentURL {
            return await connect()
        }
        await state.refreshWaiting()
        await state.refreshShown()
    }

    /// A setting changed: in the Settings app, which the app hears when it next runs, or by
    /// the app itself. If it's the hub's address, the work hours or On-device voice, the
    /// hub gets them now, so they apply from the next call. (Turned on, On-device voice
    /// also lets the calls that wait for the speech computer ring now.)
    @MainActor
    func settingsChanged() async {
        guard Switchboard.url != sentURL || WorkHours.fields != sentHours || OnDeviceVoice.isOn != sentOnDevice
        else { return }
        await connect()
    }

    /// A pairing link (ringback://pair?hub=…&code=…, from the hub's portal): sets the hub's
    /// address, as the Settings app would, and pairs with the code, now or once the push
    /// token has come. Not a link of that shape: nothing happens.
    @MainActor
    func open(_ url: URL) async {
        // From the Settings app, whose page has a link to the app's own settings.
        if url.scheme == "ringback", url.host() == "model" {
            state.showsSettings = true
            return
        }
        guard url.scheme == "ringback", url.host() == "pair",
            let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems,
            let hub = items.first(where: { $0.name == "hub" })?.value.flatMap(Switchboard.normalize),
            let code = items.first(where: { $0.name == "code" })?.value, !code.isEmpty
        else { return }
        UserDefaults.standard.set(Switchboard.displayHost(of: hub), forKey: Switchboard.urlKey)
        state.pairingCode = code
        await connect(force: true)
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
