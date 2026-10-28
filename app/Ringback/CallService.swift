import AVFoundation
import CallKit
import Foundation

/// Rings through CallKit for each call the switchboard places, and reports back what happened.
final class CallService: NSObject, CXProviderDelegate {
    /// How long the phone rings before the call counts as missed.
    static let ringTimeout: TimeInterval = 30

    private let provider: CXProvider
    private let observer = CXCallObserver()
    private let state: AppState
    /// CallKit's UUID for each live call → the switchboard's call ID.
    private var callIDs: [UUID: String] = [:]

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
    }

    /// Reports the call a VoIP push announced. Apple requires a call for every push, so when
    /// another call is already active the new one is reported and ended at once, as "busy".
    func reportIncoming(_ payload: [AnyHashable: Any], completion: @escaping () -> Void) {
        let callID = payload["call_id"] as? String ?? UUID().uuidString
        let project = payload["project"] as? String ?? "Claude Code"
        let busy = observer.calls.contains { !$0.hasEnded }
        let uuid = UUID()

        // The lock screen shows who's calling, so the voice doesn't have to say it.
        let update = CXCallUpdate()
        update.remoteHandle = CXHandle(type: .generic, value: project)
        update.localizedCallerName = "Claude from \(project)"
        update.hasVideo = false
        update.supportsHolding = false
        update.supportsGrouping = false
        update.supportsUngrouping = false
        update.supportsDTMF = false

        callIDs[uuid] = callID
        state.add(CallRecord(id: callID, project: project, ringAt: .now, outcome: .ringing))
        provider.reportNewIncomingCall(with: uuid, update: update) { [self] error in
            defer { completion() }
            if let error {
                finish(uuid, .failed, event: "failed", extra: ["error": error.localizedDescription])
            } else if busy {
                provider.reportCall(with: uuid, endedAt: nil, reason: .unanswered)
                finish(uuid, .busy, event: "busy")
            } else {
                // Also brings up the Nebula tunnel to the Mac while the phone rings.
                Switchboard.report(callID: callID, event: "ringing")
                DispatchQueue.main.asyncAfter(deadline: .now() + Self.ringTimeout) { [self] in
                    guard let id = callIDs[uuid], state.calls.first(where: { $0.id == id })?.outcome == .ringing
                    else { return }
                    provider.reportCall(with: uuid, endedAt: nil, reason: .unanswered)
                    finish(uuid, .missed, event: "missed")
                }
            }
        }
    }

    /// Ends our record of a call and tells the switchboard.
    private func finish(_ uuid: UUID, _ outcome: CallRecord.Outcome, event: String, extra: [String: String] = [:]) {
        guard let callID = callIDs.removeValue(forKey: uuid) else { return }
        var extra = extra
        state.update(callID) { call in
            call.outcome = outcome
            call.endedAt = .now
            if let duration = call.duration { extra["duration_s"] = String(format: "%.1f", duration) }
        }
        Switchboard.report(callID: callID, event: event, extra: extra)
    }

    // MARK: CXProviderDelegate

    func providerDidReset(_ provider: CXProvider) {
        for uuid in callIDs.keys { finish(uuid, .failed, event: "failed", extra: ["error": "CallKit reset"]) }
    }

    func provider(_ provider: CXProvider, perform action: CXAnswerCallAction) {
        let session = AVAudioSession.sharedInstance()
        try? session.setCategory(.playAndRecord, mode: .voiceChat, options: [.allowBluetoothHFP])
        if let callID = callIDs[action.callUUID] {
            state.update(callID) { call in
                call.outcome = .onCall
                call.answeredAt = .now
            }
            Switchboard.report(callID: callID, event: "answered")
        }
        action.fulfill()
    }

    func provider(_ provider: CXProvider, perform action: CXEndCallAction) {
        if let callID = callIDs[action.callUUID] {
            let answered = state.calls.first(where: { $0.id == callID })?.answeredAt != nil
            finish(action.callUUID, answered ? .answered : .declined, event: answered ? "ended" : "declined")
        }
        action.fulfill()
    }

    func provider(_ provider: CXProvider, didActivate audioSession: AVAudioSession) {
        // Stage 1b: start the audio connection to the Mac here.
    }

    func provider(_ provider: CXProvider, didDeactivate audioSession: AVAudioSession) {}
}
