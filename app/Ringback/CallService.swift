import AVFoundation
import CallKit
import Foundation

/// Rings through CallKit for each call the switchboard places, and reports back what happened.
final class CallService: NSObject, CXProviderDelegate {
    /// How long the phone rings before the call counts as missed.
    static let ringTimeout: TimeInterval = 30
    /// While you're on a call, the app notes every few seconds that it still is, so a call
    /// cut short by the app being closed can be recorded with about the right duration.
    private static let heartbeat: TimeInterval = 2
    private static let liveCallKey = "liveCall"
    private static let liveCallAtKey = "liveCallAt"

    private let provider: CXProvider
    private let controller = CXCallController()
    private let observer = CXCallObserver()
    private let state: AppState
    /// CallKit's UUID for each live call → the switchboard's call ID.
    private var callIDs: [UUID: String] = [:]
    /// Where each call's audio comes from: the calling Mac's voice server.
    private var voiceURLs: [UUID: URL] = [:]
    private var links: [UUID: AudioLink] = [:]
    private var heartbeatTimer: Timer?

    init(state: AppState) {
        let config = CXProviderConfiguration()
        config.supportsVideo = false
        config.maximumCallGroups = 1
        config.maximumCallsPerCallGroup = 1
        config.supportedHandleTypes = [.generic]
        config.includesCallsInRecents = false
        provider = CXProvider(configuration: config)
        self.state = state
        super.init()
        provider.setDelegate(self, queue: nil)  // main queue
        recoverInterruptedCalls()
    }

    /// Reports the call a VoIP push announced; Apple requires a call for every push. During
    /// another call it rings as call waiting and you choose: answering, declining and not
    /// answering all work as usual, and the switchboard retries a "not now".
    func reportIncoming(_ payload: [AnyHashable: Any], completion: @escaping () -> Void) {
        let callID = payload["call_id"] as? String ?? UUID().uuidString
        let project = payload["project"] as? String ?? "Claude Code"
        let subject = payload["subject"] as? String
        let onAnotherCall = observer.calls.contains { !$0.hasEnded }
        let uuid = UUID()
        let record = CallRecord(
            id: callID, project: project, subject: subject, ringAt: .now, outcome: .ringing,
            onAnotherCall: onAnotherCall)

        // The lock screen shows who's calling, so the voice doesn't have to say it.
        let update = CXCallUpdate()
        update.remoteHandle = CXHandle(type: .generic, value: project)
        update.localizedCallerName = record.callerName
        update.hasVideo = false
        update.supportsHolding = false
        update.supportsGrouping = false
        update.supportsUngrouping = false
        update.supportsDTMF = false

        callIDs[uuid] = callID
        if let voice = payload["voice"] as? String { voiceURLs[uuid] = URL(string: voice) }
        state.add(record)
        provider.reportNewIncomingCall(with: uuid, update: update) { [self] error in
            defer { completion() }
            if let error {
                finish(uuid, .failed, event: "failed", extra: ["error": error.localizedDescription])
            } else {
                // Also brings up the Nebula tunnel to the Mac while the phone rings.
                Switchboard.report(
                    callID: callID, event: "ringing", extra: onAnotherCall ? ["on_another_call": "yes"] : [:])
                DispatchQueue.main.asyncAfter(deadline: .now() + Self.ringTimeout) { [self] in
                    guard let id = callIDs[uuid], state.calls.first(where: { $0.id == id })?.outcome == .ringing
                    else { return }
                    provider.reportCall(with: uuid, endedAt: nil, reason: .unanswered)
                    finish(uuid, .missed, event: "missed")
                }
            }
        }
    }

    // MARK: The in-call screen's buttons, through CallKit so iOS and AirPods stay in step

    func endActiveCall() {
        guard let uuid = state.activeCall?.uuid else { return }
        controller.request(CXTransaction(action: CXEndCallAction(call: uuid))) { [self] error in
            // If CallKit no longer knows the call, end our side anyway.
            if error != nil { DispatchQueue.main.async { [self] in finish(uuid, .answered, event: "ended") } }
        }
    }

    func setMuted(_ muted: Bool) {
        guard let uuid = state.activeCall?.uuid else { return }
        controller.request(CXTransaction(action: CXSetMutedCallAction(call: uuid, muted: muted))) { _ in }
    }

    /// iOS is closing the app: note the exact time, for the record made at the next launch.
    func appWillTerminate() {
        if state.activeCall != nil { UserDefaults.standard.set(Date.now, forKey: Self.liveCallAtKey) }
    }

    // MARK: Ending calls

    /// Ends our record of a call and tells the switchboard.
    private func finish(_ uuid: UUID, _ outcome: CallRecord.Outcome, event: String, extra: [String: String] = [:]) {
        links.removeValue(forKey: uuid)?.stop()
        voiceURLs[uuid] = nil
        if state.activeCall?.uuid == uuid { endHeartbeat() }
        guard let callID = callIDs.removeValue(forKey: uuid) else { return }
        var extra = extra
        state.update(callID) { call in
            call.outcome = outcome
            call.endedAt = .now
            if let duration = call.duration { extra["duration_s"] = String(format: "%.1f", duration) }
        }
        Switchboard.report(callID: callID, event: event, extra: extra)
    }

    /// Calls left open because the app was closed (killed, crashed, or iOS ran out of
    /// memory) are recorded as ended at the last heartbeat, and the switchboard is told.
    private func recoverInterruptedCalls() {
        let defaults = UserDefaults.standard
        let liveID = defaults.string(forKey: Self.liveCallKey)
        let liveAt = defaults.object(forKey: Self.liveCallAtKey) as? Date
        for call in state.calls where call.outcome == .onCall || call.outcome == .ringing {
            let answered = call.answeredAt != nil
            let end = (call.id == liveID ? liveAt : nil) ?? call.answeredAt ?? call.ringAt
            var extra = ["note": "the app was closed during the call"]
            state.update(call.id) { call in
                call.outcome = answered ? .answered : .missed
                call.endedAt = end
                if let duration = call.duration { extra["duration_s"] = String(format: "%.1f", duration) }
            }
            Switchboard.report(callID: call.id, event: answered ? "ended" : "missed", extra: extra, at: end)
        }
        defaults.removeObject(forKey: Self.liveCallKey)
        defaults.removeObject(forKey: Self.liveCallAtKey)
    }

    private func startHeartbeat(_ callID: String) {
        let defaults = UserDefaults.standard
        defaults.set(callID, forKey: Self.liveCallKey)
        defaults.set(Date.now, forKey: Self.liveCallAtKey)
        heartbeatTimer = Timer.scheduledTimer(withTimeInterval: Self.heartbeat, repeats: true) { _ in
            defaults.set(Date.now, forKey: Self.liveCallAtKey)
        }
    }

    private func endHeartbeat() {
        heartbeatTimer?.invalidate()
        heartbeatTimer = nil
        state.activeCall = nil
        UserDefaults.standard.removeObject(forKey: Self.liveCallKey)
        UserDefaults.standard.removeObject(forKey: Self.liveCallAtKey)
    }

    // MARK: CXProviderDelegate

    func providerDidReset(_ provider: CXProvider) {
        for uuid in callIDs.keys { finish(uuid, .failed, event: "failed", extra: ["error": "CallKit reset"]) }
    }

    func provider(_ provider: CXProvider, perform action: CXAnswerCallAction) {
        AudioLink.prepareAudioSession()
        let uuid = action.callUUID
        if let callID = callIDs[uuid] {
            state.update(callID) { call in
                call.outcome = .onCall
                call.answeredAt = .now
            }
            if let call = state.calls.first(where: { $0.id == callID }) {
                state.activeCall = ActiveCall(
                    uuid: uuid, id: callID, callerName: call.callerName, subject: call.subject,
                    answeredAt: call.answeredAt ?? .now, audio: voiceURLs[uuid] == nil ? .none : .connecting)
            }
            startHeartbeat(callID)
            Switchboard.report(callID: callID, event: "answered")
            if let voiceURL = voiceURLs[uuid] { startAudio(uuid, callID, voiceURL) }
        }
        action.fulfill()
    }

    /// Connects the call's audio to the calling Mac, and reports how long that took from
    /// answering, or why it failed.
    private func startAudio(_ uuid: UUID, _ callID: String, _ voiceURL: URL) {
        let link = AudioLink()
        links[uuid] = link
        link.onConnected = { [self] seconds in
            state.activeCall?.audio = .connected
            Switchboard.report(
                callID: callID, event: "audio connected", extra: ["since_answer_s": String(format: "%.2f", seconds)])
        }
        link.onFailed = { [self] reason in
            state.activeCall?.audio = .failed(reason)
            Switchboard.report(callID: callID, event: "audio failed", extra: ["error": reason])
        }
        Task { @MainActor [self] in
            do {
                try await link.start(voiceURL: voiceURL, callID: callID)
            } catch {
                state.activeCall?.audio = .failed(error.localizedDescription)
                Switchboard.report(callID: callID, event: "audio failed", extra: ["error": error.localizedDescription])
            }
        }
    }

    func provider(_ provider: CXProvider, perform action: CXEndCallAction) {
        if let callID = callIDs[action.callUUID] {
            let answered = state.calls.first(where: { $0.id == callID })?.answeredAt != nil
            finish(action.callUUID, answered ? .answered : .declined, event: answered ? "ended" : "declined")
        }
        action.fulfill()
    }

    func provider(_ provider: CXProvider, perform action: CXSetMutedCallAction) {
        links[action.callUUID]?.setMuted(action.isMuted)
        if state.activeCall?.uuid == action.callUUID { state.activeCall?.muted = action.isMuted }
        action.fulfill()
    }

    func provider(_ provider: CXProvider, didActivate audioSession: AVAudioSession) {
        AudioLink.activate(audioSession)
    }

    func provider(_ provider: CXProvider, didDeactivate audioSession: AVAudioSession) {
        AudioLink.deactivate(audioSession)
    }
}
