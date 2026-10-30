import AVFoundation
import Foundation
import WebRTC

/// A call's audio: a WebRTC connection that the phone opens to the calling Mac's voice server
/// over Nebula. (On mobile data only the phone can open a Nebula tunnel.) It's made while the
/// phone rings, so the voice can start as soon as you answer.
///
/// CallKit owns the audio session, so WebRTC runs in manual-audio mode: CallKit activates
/// the session and `activate`/`deactivate` hand it to WebRTC.
final class AudioLink: NSObject, RTCPeerConnectionDelegate {
    private static let factory: RTCPeerConnectionFactory = {
        RTCInitializeSSL()
        return RTCPeerConnectionFactory()
    }()

    /// How easily your speech stops the voice, from 0 to 1, set in the app and sent with each
    /// call. The Mac turns it into the thresholds of its voice-activity detection.
    static let sensitivityKey = "interruptionSensitivity"
    static let defaultSensitivity = 0.5
    /// Whether the Mac ignores speech quieter than yours, such as people nearby, and how
    /// strongly, from 0.1 to 1. Set in the app and sent with each call, as 0 when it's off.
    /// The Mac turns it into a volume threshold.
    static let backgroundFilterOnKey = "ignoreBackgroundNoise"
    static let backgroundFilterKey = "backgroundFilter"
    static let defaultBackgroundFilter = 0.5

    /// The background filter to send: 0 when it's off.
    static var backgroundFilter: Double {
        let defaults = UserDefaults.standard
        guard defaults.object(forKey: backgroundFilterOnKey) as? Bool ?? true else { return 0 }
        return defaults.object(forKey: backgroundFilterKey) as? Double ?? defaultBackgroundFilter
    }

    private var peer: RTCPeerConnection?
    private var microphone: RTCAudioTrack?
    /// What the voice server sends.
    private var voice: RTCMediaStreamTrack?
    private var muted = false
    private var stopped = false
    /// Which addresses the offer gave, for the switchboard's log.
    private(set) var addresses = ""
    /// True while the phone rings. The link then neither sends your microphone nor plays the
    /// voice: a call you're on may be using them (call waiting). An offer that goes while it's
    /// true says so, and the voice server then waits for `answered` before it talks.
    var ringing = false {
        didSet { updateTracks() }
    }
    /// Called once, on the main queue, when audio is connected; with when.
    var onConnected: ((Date) -> Void)?
    var onFailed: ((String) -> Void)?

    /// Tries to reach the voice server over Nebula until it answers or `deadline` passes, and
    /// returns when it answered and after how many tries; nil if it never did. Any HTTP answer
    /// counts. After the phone moves from Wi-Fi to mobile data, its Nebula tunnel to the Mac
    /// can stay stale for more than 15 s; each try sends fresh packets, so Nebula notices
    /// sooner and goes through the lighthouse's relay. It starts when the phone rings.
    static func reach(_ voiceURL: URL, until deadline: Date) async -> (at: Date, tries: Int)? {
        var tries = 0
        while Date.now < deadline, !Task.isCancelled {
            tries += 1
            let request = URLRequest(url: voiceURL.appending(path: "ping"), timeoutInterval: 3)
            if (try? await URLSession.shared.data(for: request)) != nil { return (.now, tries) }
            try? await Task.sleep(for: .milliseconds(500))
        }
        return nil
    }

    /// Before a connection is made while the phone rings: WebRTC must not start audio by
    /// itself, because CallKit owns the audio session until you answer and then hands it over
    /// (`activate`). This sets only WebRTC's own flags, not the audio session, so the ringtone
    /// and CallKit carry on as before. While you're on another ringback call, its audio stays on.
    static func useManualAudio(keepingCallAudio: Bool) {
        let config = RTCAudioSessionConfiguration.webRTC()
        config.category = AVAudioSession.Category.playAndRecord.rawValue
        config.mode = AVAudioSession.Mode.voiceChat.rawValue
        config.categoryOptions = [.allowBluetoothHFP]
        RTCAudioSessionConfiguration.setWebRTC(config)

        let session = RTCAudioSession.sharedInstance()
        session.useManualAudio = true
        if !keepingCallAudio { session.isAudioEnabled = false }
    }

    /// When you answer, before CallKit activates the audio session: WebRTC must not start
    /// audio by itself, and uses the same voice-chat settings as the call.
    static func prepareAudioSession() {
        useManualAudio(keepingCallAudio: false)
        let session = RTCAudioSession.sharedInstance()
        session.lockForConfiguration()
        try? session.setConfiguration(RTCAudioSessionConfiguration.webRTC())
        session.unlockForConfiguration()
    }

    static func activate(_ audioSession: AVAudioSession) {
        RTCAudioSession.sharedInstance().audioSessionDidActivate(audioSession)
        RTCAudioSession.sharedInstance().isAudioEnabled = true
    }

    static func deactivate(_ audioSession: AVAudioSession) {
        RTCAudioSession.sharedInstance().audioSessionDidDeactivate(audioSession)
        RTCAudioSession.sharedInstance().isAudioEnabled = false
    }

    /// Offers audio to the voice server and applies its answer. The Mac's server doesn't
    /// take ICE candidates one by one, so the offer waits until they're all gathered. The
    /// offer also carries your interruption sensitivity and background filter, and whether the
    /// phone still rings (`ringing` as the offer goes). Returns whether it did. On the main
    /// actor, as `stop` is, so once the link is stopped no offer goes and no answer is applied.
    /// `back` calls back the call `callID`, which the voice server answers at once; it throws
    /// `RefusedCall` if the server doesn't know the call.
    @MainActor
    func start(voiceURL: URL, callID: String, back: Bool = false) async throws -> Bool {
        var local = try await gather(nebulaOnly: true)
        guard !stopped else { throw CancellationError() }
        var found = Self.candidates(local)
        addresses = "Nebula only (\(found))"
        if found == 0 {
            // iOS didn't show Nebula to WebRTC as a VPN: offer every adapter instead.
            peer?.close()
            local = try await gather(nebulaOnly: false)
            found = Self.candidates(local)
            addresses = "every adapter (\(found)), as Nebula alone gave none"
        }
        guard !stopped else { throw CancellationError() }

        let ringing = ringing
        var body: [String: Any] = [
            "sdp": local.sdp, "type": "offer", "call_id": callID,
            "sensitivity": UserDefaults.standard.object(forKey: Self.sensitivityKey) as? Double ?? Self.defaultSensitivity,
            "background_filter": Self.backgroundFilter,
        ]
        if ringing { body["ringing"] = true }
        if back { body["back"] = true }
        var request = URLRequest(url: voiceURL.appending(path: "offer"), timeoutInterval: 15)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (data, response) = try await URLSession.shared.data(for: request)
        guard !stopped else { throw CancellationError() }
        if (response as? HTTPURLResponse)?.statusCode == 403 { throw RefusedCall() }
        guard let answer = try JSONSerialization.jsonObject(with: data) as? [String: Any],
            let sdp = answer["sdp"] as? String
        else { throw URLError(.badServerResponse) }
        guard let peer else { throw CancellationError() }
        try await peer.setRemoteDescription(RTCSessionDescription(type: .answer, sdp: sdp))
        return ringing
    }

    /// Makes the peer connection and its offer, with all its addresses gathered. Nebula shows
    /// to WebRTC as a VPN adapter, and the offer reaches the voice server over Nebula, so that
    /// path is known to work. With every adapter, WebRTC also checks pairs of addresses that
    /// can't connect, such as mobile data against the Mac's Wi-Fi; audio then took 2.5–2.8 s
    /// to connect over Nebula (Stage 3).
    @MainActor
    private func gather(nebulaOnly: Bool) async throws -> RTCSessionDescription {
        let options = RTCPeerConnectionFactoryOptions()
        if nebulaOnly {
            options.ignoreWiFiNetworkAdapter = true
            options.ignoreCellularNetworkAdapter = true
            options.ignoreEthernetNetworkAdapter = true
            options.ignoreLoopbackNetworkAdapter = true
        }
        Self.factory.setOptions(options)
        let config = RTCConfiguration()
        config.sdpSemantics = .unifiedPlan
        config.continualGatheringPolicy = .gatherOnce
        let none = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        guard let peer = Self.factory.peerConnection(with: config, constraints: none, delegate: self) else {
            throw URLError(.cannotConnectToHost)
        }
        self.peer = peer
        let track = Self.factory.audioTrack(with: Self.factory.audioSource(with: none), trackId: "mic")
        peer.add(track, streamIds: ["call"])
        microphone = track
        voice = peer.transceivers.first { $0.mediaType == .audio }?.receiver.track
        updateTracks()

        let offer = try await peer.offer(for: none)
        try await peer.setLocalDescription(offer)
        for _ in 0..<60 where peer.iceGatheringState != .complete {
            try await Task.sleep(for: .milliseconds(50))
        }
        guard let local = peer.localDescription else { throw URLError(.cannotConnectToHost) }
        return local
    }

    private static func candidates(_ description: RTCSessionDescription) -> Int {
        description.sdp.components(separatedBy: "a=candidate:").count - 1
    }

    /// Tells the voice server you answered a call whose offer went while the phone rang, so
    /// it starts talking. Tries four times within about 15 s. A 404 means the voice server
    /// wasn't offered the call, so that isn't retried.
    static func answered(_ voiceURL: URL, callID: String) async throws {
        var request = URLRequest(url: voiceURL.appending(path: "answered"), timeoutInterval: 3)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["call_id": callID])
        var failure: Error = URLError(.badServerResponse)
        for delay in [0, 0.5, 1, 2] {
            try await Task.sleep(for: .seconds(delay))
            do {
                let (_, response) = try await URLSession.shared.data(for: request)
                switch (response as? HTTPURLResponse)?.statusCode {
                case 200: return
                case 404: throw UnknownCall()
                default: failure = URLError(.badServerResponse)
                }
            } catch let error as UnknownCall {
                throw error
            } catch {
                failure = error
            }
        }
        throw failure
    }

    func setMuted(_ muted: Bool) {
        self.muted = muted
        updateTracks()
    }

    func stop() {
        stopped = true
        onConnected = nil
        onFailed = nil
        peer?.close()
        peer = nil
        microphone = nil
        voice = nil
    }

    private func updateTracks() {
        microphone?.isEnabled = !ringing && !muted
        voice?.isEnabled = !ringing
    }

    // MARK: RTCPeerConnectionDelegate (called on WebRTC's signalling thread)

    func peerConnection(_ peer: RTCPeerConnection, didChange newState: RTCIceConnectionState) {
        let at = Date.now
        DispatchQueue.main.async { [self] in
            switch newState {
            case .connected:
                onConnected?(at)
                onConnected = nil
            case .failed:
                onFailed?("ICE failed")
                onFailed = nil
            default: break
            }
        }
    }

    func peerConnection(_ peer: RTCPeerConnection, didChange stateChanged: RTCSignalingState) {}
    func peerConnection(_ peer: RTCPeerConnection, didAdd stream: RTCMediaStream) {}
    func peerConnection(_ peer: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    func peerConnectionShouldNegotiate(_ peer: RTCPeerConnection) {}
    func peerConnection(_ peer: RTCPeerConnection, didChange newState: RTCIceGatheringState) {}
    func peerConnection(_ peer: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {}
    func peerConnection(_ peer: RTCPeerConnection, didRemove candidates: [RTCIceCandidate]) {}
    func peerConnection(_ peer: RTCPeerConnection, didOpen dataChannel: RTCDataChannel) {}
}

/// The voice server answered `answered` with 404: it wasn't offered this call.
private struct UnknownCall: LocalizedError {
    var errorDescription: String? { "The voice server doesn't know this call" }
}

/// The voice server refused an offer (403): it didn't place the call, or for a call back, no
/// longer remembers it. It forgets calls a day after they end, and all of them when it restarts.
struct RefusedCall: LocalizedError {
    var errorDescription: String? { "The voice server doesn't know this call" }
}
