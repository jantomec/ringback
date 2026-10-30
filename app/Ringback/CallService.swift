import AVFoundation
import CallKit
import Foundation
import UIKit

/// Rings through CallKit for each call the switchboard places, and reports back what happened.
/// Calls you didn't answer can be called back from the call history, and calls that wait for
/// your work hours called now, as outgoing calls.
final class CallService: NSObject, CXProviderDelegate {
    /// How long the phone rings before the call counts as missed.
    static let ringTimeout: TimeInterval = 30
    /// How long after you answer the app keeps trying to reach the voice server before the
    /// call's audio fails. Nebula took more than 15 s once after the phone left Wi-Fi.
    static let reachTimeout: TimeInterval = 45
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
    /// Each call's audio, one link at a time: a new offer goes only after the last one has
    /// failed and its link is closed.
    private var links: [UUID: AudioLink] = [:]
    /// Reaching each call's voice server from when the phone rings, then connecting the call's
    /// audio while it still rings. Its value is when the voice server answered and after how
    /// many tries; nil if it never did.
    private var connecting: [UUID: Task<(at: Date, tries: Int)?, Never>] = [:]
    /// How each call's audio fared while the phone rang, for when you answer.
    private var ringingAudio: [UUID: RingingAudio] = [:]
    /// Each call back's record, or a waiting call's you called now, from tapping it until
    /// CallKit starts it.
    private var startingCallBacks: [UUID: CallRecord] = [:]
    /// The call each live call back calls back, by its ID.
    private var callingBack: [UUID: String] = [:]
    /// The live calls you called while they waited.
    private var callingNow: Set<UUID> = []
    /// Calls whose audio connected, so a call back that never did ends as not connected.
    private var connected: Set<UUID> = []
    private var heartbeatTimer: Timer?

    private struct RingingAudio {
        /// The offer went while the phone rang, so the voice server waits to hear you answered.
        var offered = false
        /// When the audio connected, if that was before you answered.
        var connectedAt: Date?
        /// Why connecting while ringing failed, if it did. Answering then makes a new offer.
        var failure: String?
    }

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
        let voiceURL = (payload["voice"] as? String).flatMap { URL(string: $0) }
        let record = CallRecord(
            id: callID, project: project, subject: subject, ringAt: .now, outcome: .ringing,
            onAnotherCall: onAnotherCall, voice: voiceURL)

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
        if let voice = voiceURL {
            voiceURLs[uuid] = voice
            // Brings the Nebula tunnel to the Mac up while the phone rings, then connects the
            // call's audio, so the voice can start as soon as you answer.
            let deadline = Date.now + Self.ringTimeout + Self.reachTimeout
            connecting[uuid] = Task { @MainActor [self] in
                let task = UIApplication.shared.beginBackgroundTask(withName: "connect while ringing")
                defer { UIApplication.shared.endBackgroundTask(task) }
                guard let reached = await AudioLink.reach(voice, until: deadline), callIDs[uuid] != nil
                else { return nil }
                reportReached(callID, reached)
                await connectWhileRinging(uuid, callID, voice)
                return reached
            }
        }
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
                    guard isRinging(uuid) else { return }
                    provider.reportCall(with: uuid, endedAt: nil, reason: .unanswered)
                    finish(uuid, .missed, event: "missed")
                }
            }
        }
    }

    /// Calls back a call you didn't answer, straight away, as an outgoing call without ringing:
    /// its audio goes to the voice server that placed it, with that call's ID, and the voice
    /// picks up. It's a new record of its own, which keeps the ID of the call it calls back.
    func callBack(_ call: CallRecord) {
        guard let voice = call.voice, state.activeCall == nil, startingCallBacks.isEmpty else { return }
        let uuid = UUID()
        let record = CallRecord(
            id: UUID().uuidString.lowercased(), project: call.project, subject: call.subject, ringAt: .now,
            outcome: .onCall, voice: voice, callsBack: call.callsBack ?? call.id)
        startCall(uuid, record)
    }

    /// Calls a call that waits for your work hours now, straight away, as a call back does: its
    /// audio goes to the voice server that placed it, with its ID, and the voice picks up. It's
    /// recorded under that ID, as the call itself.
    func callNow(_ call: WaitingCall) {
        guard state.activeCall == nil, startingCallBacks.isEmpty else { return }
        let record = CallRecord(
            id: call.id, project: call.project, subject: call.subject, ringAt: .now, outcome: .onCall,
            voice: call.voice, calledNow: true)
        startCall(UUID(), record)
    }

    private func startCall(_ uuid: UUID, _ record: CallRecord) {
        startingCallBacks[uuid] = record
        let start = CXStartCallAction(call: uuid, handle: CXHandle(type: .generic, value: record.callerName))
        controller.request(CXTransaction(action: start)) { [self] error in
            guard let error else { return }
            DispatchQueue.main.async { [self] in
                startingCallBacks[uuid] = nil
                let what = record.calledNow == true ? "Can't call" : "Can't call back"
                state.callBackProblem = "\(what): \(error.localizedDescription)"
            }
        }
    }

    // MARK: The in-call screen's buttons, through CallKit so iOS and AirPods stay in step

    func endActiveCall() {
        guard let uuid = state.activeCall?.uuid else { return }
        end(uuid)
    }

    private func end(_ uuid: UUID) {
        controller.request(CXTransaction(action: CXEndCallAction(call: uuid))) { [self] error in
            // If CallKit no longer knows the call, end our side anyway.
            if error != nil { DispatchQueue.main.async { [self] in finish(uuid, endedOutcome(uuid), event: "ended") } }
        }
    }

    /// How a call you were on ended: answered, or for a call you made whose audio never
    /// connected, failed.
    private func endedOutcome(_ uuid: UUID) -> CallRecord.Outcome {
        isOutgoing(uuid) && !connected.contains(uuid) ? .failed : .answered
    }

    /// A call back, or a waiting call you called now.
    private func isOutgoing(_ uuid: UUID) -> Bool {
        callingBack[uuid] != nil || callingNow.contains(uuid)
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
        connecting.removeValue(forKey: uuid)?.cancel()
        ringingAudio[uuid] = nil
        voiceURLs[uuid] = nil
        callingBack[uuid] = nil
        callingNow.remove(uuid)
        connected.remove(uuid)
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

    // MARK: The call's audio

    /// Whether the call still rings: not yet answered, declined or missed.
    private func isRinging(_ uuid: UUID) -> Bool {
        guard let callID = callIDs[uuid] else { return false }
        return state.calls.first(where: { $0.id == callID })?.outcome == .ringing
    }

    /// Connects the call's audio if the phone still rings. If you answer before the offer
    /// goes, it goes as it would after answering. If connecting fails before you answer, the
    /// link is dropped and answering makes a new offer.
    @MainActor
    private func connectWhileRinging(_ uuid: UUID, _ callID: String, _ voiceURL: URL) async {
        guard isRinging(uuid) else { return }
        AudioLink.useManualAudio(keepingCallAudio: state.activeCall != nil)
        let link = makeLink(uuid, callID)
        link.ringing = true
        ringingAudio[uuid] = RingingAudio()
        do {
            let offered = try await link.start(voiceURL: voiceURL, callID: callID)
            if links[uuid] === link { ringingAudio[uuid]?.offered = offered }
        } catch {
            dropRingingLink(uuid, link, error.localizedDescription)
        }
    }

    private func dropRingingLink(_ uuid: UUID, _ link: AudioLink, _ reason: String) {
        guard links[uuid] === link else { return }
        links[uuid] = nil
        link.stop()
        ringingAudio[uuid] = RingingAudio(failure: reason)
    }

    /// Once you've answered: tells the voice server, if the offer went while the phone rang,
    /// so it starts talking. Otherwise connects the call's audio as a new offer. It first waits
    /// for reaching the voice server and for an offer still on its way.
    private func startAudio(_ uuid: UUID, _ callID: String, _ voiceURL: URL) {
        let answeredAt = Date.now
        Task { @MainActor [self] in
            let reached = await connecting[uuid]?.value
            guard callIDs[uuid] != nil else { return }
            if links[uuid] != nil {
                if ringingAudio[uuid]?.offered == true { await tellAnswered(uuid, callID, voiceURL) }
                return
            }
            // The offer goes once the voice server has answered, so a retry can't start a
            // second conversation.
            guard let reached, reached.at.timeIntervalSince(answeredAt) < Self.reachTimeout else {
                return audioFailed(uuid, callID, ReachError().localizedDescription, note: nil)
            }
            let link = makeLink(uuid, callID)
            do {
                _ = try await link.start(voiceURL: voiceURL, callID: callID)
            } catch {
                if links[uuid] === link { audioFailed(uuid, callID, error.localizedDescription, note: offerNote(uuid)) }
            }
        }
    }

    /// A call back's audio: reaches the voice server, then offers with the ID of the call it
    /// calls back. If the voice server no longer knows that call, the call ends and the app
    /// says why.
    private func callBackAudio(_ uuid: UUID, _ callID: String, _ original: String, _ voiceURL: URL) {
        let deadline = Date.now + Self.reachTimeout
        connecting[uuid] = Task { @MainActor [self] in
            let task = UIApplication.shared.beginBackgroundTask(withName: "call back")
            defer { UIApplication.shared.endBackgroundTask(task) }
            let reached = await AudioLink.reach(voiceURL, until: deadline)
            guard callIDs[uuid] != nil else { return reached }
            guard let reached else {
                audioFailed(uuid, callID, ReachError().localizedDescription, note: offerNote(uuid))
                return nil
            }
            reportReached(callID, reached)
            let link = makeLink(uuid, callID)
            do {
                _ = try await link.start(voiceURL: voiceURL, callID: original, back: true)
            } catch is RefusedCall {
                guard links[uuid] === link else { return reached }
                audioFailed(uuid, callID, RefusedCall().localizedDescription, note: offerNote(uuid))
                state.callBackProblem = "Claude can't take this call back any more."
                end(uuid)
            } catch {
                if links[uuid] === link { audioFailed(uuid, callID, error.localizedDescription, note: offerNote(uuid)) }
            }
            return reached
        }
    }

    /// The audio of a waiting call you called now: reaches the voice server, then offers with
    /// the call's ID, marked `now`. If the voice server can't be reached, or no longer has the
    /// call waiting (it rang meanwhile and was forgotten, or it's gone), the call ends, the app
    /// says why, and the waiting calls are fetched again.
    private func callNowAudio(_ uuid: UUID, _ callID: String, _ voiceURL: URL) {
        let deadline = Date.now + Self.reachTimeout
        connecting[uuid] = Task { @MainActor [self] in
            let task = UIApplication.shared.beginBackgroundTask(withName: "call now")
            defer { UIApplication.shared.endBackgroundTask(task) }
            let reached = await AudioLink.reach(voiceURL, until: deadline)
            guard callIDs[uuid] != nil else { return reached }
            guard let reached else {
                audioFailed(uuid, callID, ReachError().localizedDescription, note: offerNote(uuid))
                callNowFailed(uuid, "Can't reach the Mac that's calling over Nebula. Check that Nebula is on.")
                return nil
            }
            reportReached(callID, reached)
            let link = makeLink(uuid, callID)
            do {
                _ = try await link.start(voiceURL: voiceURL, callID: callID, now: true)
            } catch is RefusedCall {
                guard links[uuid] === link else { return reached }
                audioFailed(uuid, callID, RefusedCall().localizedDescription, note: offerNote(uuid))
                callNowFailed(uuid, "This call isn't waiting any more.")
            } catch {
                if links[uuid] === link { audioFailed(uuid, callID, error.localizedDescription, note: offerNote(uuid)) }
            }
            return reached
        }
    }

    private func callNowFailed(_ uuid: UUID, _ problem: String) {
        state.callBackProblem = problem
        end(uuid)
        Task { await state.refreshWaiting() }
    }

    /// A new link for the call, which tells the in-call screen and the switchboard when its
    /// audio connects or fails. While the phone rings, a failure only drops it.
    private func makeLink(_ uuid: UUID, _ callID: String) -> AudioLink {
        let link = AudioLink()
        links[uuid] = link
        if state.activeCall?.uuid == uuid, state.activeCall?.muted == true { link.setMuted(true) }
        link.onConnected = { [self, weak link] at in
            guard let link, links[uuid] === link else { return }
            if isRinging(uuid) {
                ringingAudio[uuid]?.connectedAt = at
            } else {
                audioConnected(uuid, callID, at: at)
            }
        }
        link.onFailed = { [self, weak link] reason in
            guard let link, links[uuid] === link else { return }
            if isRinging(uuid) {
                dropRingingLink(uuid, link, reason)
            } else {
                audioFailed(uuid, callID, reason, note: offerNote(uuid))
            }
        }
        return link
    }

    /// Tells the voice server you answered, and the switchboard if that didn't get through.
    @MainActor
    private func tellAnswered(_ uuid: UUID, _ callID: String, _ voiceURL: URL) async {
        let task = UIApplication.shared.beginBackgroundTask(withName: "tell the voice you answered")
        defer { UIApplication.shared.endBackgroundTask(task) }
        do {
            try await AudioLink.answered(voiceURL, callID: callID)
        } catch {
            guard callIDs[uuid] != nil else { return }
            Switchboard.report(
                callID: callID, event: "answer not delivered", extra: ["error": error.localizedDescription])
        }
    }

    /// Tells the switchboard when the voice server answered, after the ring and after you
    /// answered, if you had.
    private func reportReached(_ callID: String, _ reached: (at: Date, tries: Int)) {
        guard let call = state.calls.first(where: { $0.id == callID }) else { return }
        let answered = call.answeredAt.map {
            String(format: "%.1f s after answering", max(0, reached.at.timeIntervalSince($0)))
        }
        let calling = call.calledNow == true ? "calling it now" : "calling back"
        let note = call.isOutgoing
            ? String(format: "%.1f s after \(calling), \(reached.tries) tries", reached.at.timeIntervalSince(call.ringAt))
            : String(format: "%.1f s after ringing, ", reached.at.timeIntervalSince(call.ringAt))
                + (answered ?? "before answering") + ", \(reached.tries) tries"
        Switchboard.report(callID: callID, event: "voice reached", extra: ["note": note])
    }

    /// The call's audio connected. `since_answer_s` is how long after you answered it could
    /// flow: 0 if it was ready when you did. A call back connects as an outgoing call does.
    private func audioConnected(_ uuid: UUID, _ callID: String, at: Date) {
        connected.insert(uuid)
        if state.activeCall?.uuid == uuid { state.activeCall?.audio = .connected }
        if isOutgoing(uuid) { provider.reportOutgoingCall(with: uuid, connectedAt: at) }
        guard let call = state.calls.first(where: { $0.id == callID }), let answeredAt = call.answeredAt else { return }
        let start = call.callsBack != nil ? "calling back" : call.calledNow == true ? "calling it now" : "ringing"
        var note = offerNote(uuid) + String(format: ", connected %.1f s after \(start)", at.timeIntervalSince(call.ringAt))
        if let link = links[uuid] { note += ", addresses: \(link.addresses)" }
        if at < answeredAt { note += String(format: ", %.1f s before answering", answeredAt.timeIntervalSince(at)) }
        Switchboard.report(
            callID: callID, event: "audio connected",
            extra: ["since_answer_s": String(format: "%.2f", max(0, at.timeIntervalSince(answeredAt))), "note": note])
    }

    private func audioFailed(_ uuid: UUID, _ callID: String, _ reason: String, note: String?) {
        if state.activeCall?.uuid == uuid { state.activeCall?.audio = .failed(reason) }
        var extra = ["error": reason]
        extra["note"] = note
        Switchboard.report(callID: callID, event: "audio failed", extra: extra)
    }

    /// When the call's offer went, for the switchboard's log.
    private func offerNote(_ uuid: UUID) -> String {
        if let original = callingBack[uuid] { return "a call back of \(original.prefix(8))" }
        if callingNow.contains(uuid) { return "called now, while it waited" }
        guard let ringing = ringingAudio[uuid] else { return "offered after answering" }
        if let failure = ringing.failure { return "offered after answering, as connecting while ringing failed (\(failure))" }
        return ringing.offered ? "offered while ringing" : "offered after answering"
    }

    // MARK: CXProviderDelegate

    func providerDidReset(_ provider: CXProvider) {
        startingCallBacks.removeAll()
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
            // A link made while ringing now sends your microphone and plays the voice.
            links[uuid]?.ringing = false
            if let at = ringingAudio[uuid]?.connectedAt { audioConnected(uuid, callID, at: at) }
            if let voiceURL = voiceURLs[uuid] { startAudio(uuid, callID, voiceURL) }
        }
        action.fulfill()
    }

    /// A call back you tapped in the call history, or a waiting call: it's on at once,
    /// connecting, and the voice picks up once its audio connects. A call back is reported to
    /// the switchboard under its own ID; a waiting call under the call's, which takes it off the
    /// switchboard's waiting calls.
    func provider(_ provider: CXProvider, perform action: CXStartCallAction) {
        let uuid = action.callUUID
        guard var record = startingCallBacks.removeValue(forKey: uuid), let voiceURL = record.voice,
            record.callsBack != nil || record.calledNow == true
        else { return action.fail() }
        AudioLink.prepareAudioSession()
        record.answeredAt = .now
        if state.calls.contains(where: { $0.id == record.id }) {
            state.update(record.id) { $0 = record }  // called again, after it didn't connect
        } else {
            state.add(record)
        }
        callIDs[uuid] = record.id
        voiceURLs[uuid] = voiceURL
        state.activeCall = ActiveCall(
            uuid: uuid, id: record.id, callerName: record.callerName, subject: record.subject,
            answeredAt: record.answeredAt ?? .now, audio: .connecting)
        startHeartbeat(record.id)
        if let original = record.callsBack {
            callingBack[uuid] = original
            Switchboard.report(callID: record.id, event: "called back", extra: ["note": "calls back \(original.prefix(8))"])
        } else {
            callingNow.insert(uuid)
            state.waiting.removeAll { $0.id == record.id }
            Switchboard.report(callID: record.id, event: "called now")
        }
        action.fulfill()
        provider.reportOutgoingCall(with: uuid, startedConnectingAt: .now)
        // The same caller name as the calls that ring.
        let update = CXCallUpdate()
        update.remoteHandle = action.handle
        update.localizedCallerName = record.callerName
        update.hasVideo = false
        update.supportsHolding = false
        update.supportsGrouping = false
        update.supportsUngrouping = false
        update.supportsDTMF = false
        provider.reportCall(with: uuid, updated: update)
        if let original = record.callsBack {
            callBackAudio(uuid, record.id, original, voiceURL)
        } else {
            callNowAudio(uuid, record.id, voiceURL)
        }
    }

    func provider(_ provider: CXProvider, perform action: CXEndCallAction) {
        if let callID = callIDs[action.callUUID] {
            let answered = state.calls.first(where: { $0.id == callID })?.answeredAt != nil
            let outcome = answered ? endedOutcome(action.callUUID) : .declined
            finish(action.callUUID, outcome, event: answered ? "ended" : "declined")
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

/// The voice server didn't answer over Nebula in time.
private struct ReachError: LocalizedError {
    var errorDescription: String? { "Can't reach the voice server over Nebula" }
}
