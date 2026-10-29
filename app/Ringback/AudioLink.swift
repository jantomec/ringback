import AVFoundation
import Foundation
import WebRTC

/// A call's audio: a WebRTC connection that the phone opens to the calling Mac's voice server
/// over Nebula. (On mobile data only the phone can open a Nebula tunnel.)
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

    private var peer: RTCPeerConnection?
    private var microphone: RTCAudioTrack?
    private let started = Date.now
    /// Called once, on the main queue, when audio is connected; with the seconds it took.
    var onConnected: ((TimeInterval) -> Void)?
    var onFailed: ((String) -> Void)?

    /// Before CallKit activates the audio session: WebRTC must not start audio by itself,
    /// and uses the same voice-chat settings as the call.
    static func prepareAudioSession() {
        let config = RTCAudioSessionConfiguration.webRTC()
        config.category = AVAudioSession.Category.playAndRecord.rawValue
        config.mode = AVAudioSession.Mode.voiceChat.rawValue
        config.categoryOptions = [.allowBluetoothHFP]
        RTCAudioSessionConfiguration.setWebRTC(config)

        let session = RTCAudioSession.sharedInstance()
        session.useManualAudio = true
        session.isAudioEnabled = false
        session.lockForConfiguration()
        try? session.setConfiguration(config)
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
    /// offer also carries your interruption sensitivity and your voice sample.
    func start(voiceURL: URL, callID: String) async throws {
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

        let offer = try await peer.offer(for: none)
        try await peer.setLocalDescription(offer)
        for _ in 0..<60 where peer.iceGatheringState != .complete {
            try await Task.sleep(for: .milliseconds(50))
        }
        guard let local = peer.localDescription else { throw URLError(.cannotConnectToHost) }

        var request = URLRequest(url: voiceURL.appending(path: "offer"), timeoutInterval: 15)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        var body: [String: Any] = [
            "sdp": local.sdp, "type": "offer", "call_id": callID,
            "sensitivity": UserDefaults.standard.object(forKey: Self.sensitivityKey) as? Double ?? Self.defaultSensitivity,
        ]
        if let sample = YourVoice.sampleForCall() { body["voice_sample"] = sample }
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (data, _) = try await URLSession.shared.data(for: request)
        guard let answer = try JSONSerialization.jsonObject(with: data) as? [String: Any],
            let sdp = answer["sdp"] as? String
        else { throw URLError(.badServerResponse) }
        try await peer.setRemoteDescription(RTCSessionDescription(type: .answer, sdp: sdp))
    }

    func setMuted(_ muted: Bool) {
        microphone?.isEnabled = !muted
    }

    func stop() {
        peer?.close()
        peer = nil
        microphone = nil
    }

    // MARK: RTCPeerConnectionDelegate (called on WebRTC's signalling thread)

    func peerConnection(_ peer: RTCPeerConnection, didChange newState: RTCIceConnectionState) {
        DispatchQueue.main.async { [self] in
            switch newState {
            case .connected:
                onConnected?(Date.now.timeIntervalSince(started))
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
