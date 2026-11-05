import CoreImage
import UIKit
import WebRTC
import simd

/// A model's live view ("Showing you things" in docs/design.md): a WebRTC connection the phone
/// opens to the hub (/view), which receives the model as video, rendered on the computer
/// whose session showed it, and two data channels. Where you look from goes on "camera" as
/// your fingers move: unordered and never resent, so a late or lost one never holds up the
/// next, which says it all anew; the rest goes on "control", which is reliable, as does where
/// you look from once you let go: the frame you want, and play. Back on it come the renderer's
/// ready, with the model's home view, and its frame and whether it plays. It ends when you close the view, the hub says, or the
/// connection fails.
@Observable
final class ModelLink: NSObject, RTCPeerConnectionDelegate, RTCDataChannelDelegate {
    enum Status: Equatable {
        case connecting
        case live
        case ended(String)
    }

    /// What the renderer says once it has the model: how it moves, which way is up, and the view
    /// it starts from.
    struct Ready: Equatable {
        var frames = 1
        var fps = 24.0
        var animated = false
        var zUp = false
        var fov = 40.0
        var home = OrbitCamera()
    }

    private(set) var status = Status.connecting
    private(set) var ready: Ready?
    private(set) var frame = 0
    private(set) var playing = false
    /// The model's video, once the connection has it.
    private(set) var video: RTCVideoTrack?

    private var peer: RTCPeerConnection?
    private var channel: RTCDataChannel?
    private var camera: RTCDataChannel?
    private var stopped = false
    /// Keeps the last frame, to share.
    private let grabber = FrameGrabber()

    /// Opens the view of a model, rendered at width × height pixels.
    @MainActor
    func start(_ item: ShownItem, width: Int, height: Int) async {
        #if DEBUG
        if Seed.isOn {
            // Screenshots: ready, as if a computer rendered it, with no video.
            ready = Ready(frames: 48, fps: 24, animated: true)
            status = .live
            return
        }
        #endif
        guard let base = Shared.switchboard, let token = Shared.token else {
            return end("Your hub isn't set up.")
        }
        do {
            let overVPN = await AudioLink.overVPN(base)
            let options = RTCPeerConnectionFactoryOptions()
            if overVPN {
                options.ignoreWiFiNetworkAdapter = true
                options.ignoreCellularNetworkAdapter = true
                options.ignoreEthernetNetworkAdapter = true
                options.ignoreLoopbackNetworkAdapter = true
            }
            AudioLink.factory.setOptions(options)
            let config = RTCConfiguration()
            config.sdpSemantics = .unifiedPlan
            config.continualGatheringPolicy = .gatherOnce
            let none = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
            guard let peer = AudioLink.factory.peerConnection(with: config, constraints: none, delegate: self) else {
                throw URLError(.cannotConnectToHost)
            }
            self.peer = peer
            let receiving = RTCRtpTransceiverInit()
            receiving.direction = .recvOnly
            let transceiver = peer.addTransceiver(of: .video, init: receiving)
            video = transceiver?.receiver.track as? RTCVideoTrack
            video?.add(grabber)
            channel = peer.dataChannel(forLabel: "control", configuration: RTCDataChannelConfiguration())
            channel?.delegate = self
            let unreliable = RTCDataChannelConfiguration()
            unreliable.isOrdered = false
            unreliable.maxRetransmits = 0
            camera = peer.dataChannel(forLabel: "camera", configuration: unreliable)
            let offer = try await peer.offer(for: none)
            try await peer.setLocalDescription(offer)
            for _ in 0..<60 where peer.iceGatheringState != .complete {
                try await Task.sleep(for: .milliseconds(50))
            }
            guard !stopped, let local = peer.localDescription else { return }
            var request = URLRequest(url: base.appending(path: "view"), timeoutInterval: 45)
            request.httpMethod = "POST"
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONSerialization.data(withJSONObject: [
                "token": token, "key": Shared.key ?? "", "shown_id": item.id, "sdp": local.sdp, "width": width, "height": height,
            ])
            let (data, response) = try await URLSession.shared.data(for: request)
            let answer = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            guard (response as? HTTPURLResponse)?.statusCode == 200, let sdp = answer?["sdp"] as? String else {
                return end(Self.refusal(answer?["error"] as? String))
            }
            guard !stopped else { return }
            try await peer.setRemoteDescription(RTCSessionDescription(type: .answer, sdp: sdp))
        } catch {
            if !stopped { end("Your hub can't be reached.") }
        }
    }

    /// Why the hub, or the computer that has the model, said no, as the viewer says it.
    private static func refusal(_ error: String?) -> String {
        guard let error, !error.isEmpty, error != "not kept" else {
            return String(localized: "Your hub no longer keeps it: it keeps what sessions show you for a week.")
        }
        if error == "not shown from here" || error == "no computer has it" {
            return String(localized: "The computer that has the model isn't connected to your hub, as when it sleeps.")
        }
        return error.prefix(1).uppercased() + error.dropFirst() + "."
    }

    /// Sends the renderer the frame you want, play or pause, or where you look from once you
    /// let go.
    func send(_ input: [String: Any]) {
        send(input, on: channel)
    }

    /// Sends the renderer where you look from, while your fingers move.
    func moving(_ input: [String: Any]) {
        send(input, on: camera)
    }

    private func send(_ input: [String: Any], on channel: RTCDataChannel?) {
        guard let channel, channel.readyState == .open, let data = try? JSONSerialization.data(withJSONObject: input) else { return }
        channel.sendData(RTCDataBuffer(data: data, isBinary: false))
    }

    /// The picture as it shows now; nil before the first frame.
    func picture() -> UIImage? { grabber.picture() }

    /// Shows the frame and the playing at once, before the renderer says so.
    func expect(frame: Int, playing: Bool) {
        self.frame = frame
        self.playing = playing
    }

    func stop() {
        stopped = true
        video?.remove(grabber)
        camera?.close()
        channel?.close()
        peer?.close()
        peer = nil
    }

    private func end(_ why: String) {
        status = .ended(why)
        stop()
    }

    // MARK: RTCDataChannelDelegate (called on WebRTC's signalling thread)

    func dataChannelDidChangeState(_ channel: RTCDataChannel) {}

    func dataChannel(_ channel: RTCDataChannel, didReceiveMessageWith buffer: RTCDataBuffer) {
        guard let said = (try? JSONSerialization.jsonObject(with: buffer.data)) as? [String: Any] else { return }
        DispatchQueue.main.async { [self] in
            if said["ready"] as? Bool == true {
                var ready = Ready()
                ready.frames = said["frames"] as? Int ?? 1
                ready.fps = said["fps"] as? Double ?? 24
                ready.animated = said["animated"] as? Bool ?? false
                ready.zUp = said["up"] as? String == "Z"
                ready.fov = said["fov"] as? Double ?? 40
                if let home = said["home"] as? [String: Any], let target = home["target"] as? [Double], target.count == 3 {
                    ready.home = OrbitCamera(
                        yaw: home["yaw"] as? Double ?? 0, pitch: home["pitch"] as? Double ?? 0,
                        distance: home["distance"] as? Double ?? 1, target: SIMD3(target[0], target[1], target[2]),
                        zUp: ready.zUp, fov: ready.fov)
                }
                self.ready = ready
                if status == .connecting { status = .live }
            }
            if let playing = said["playing"] as? Bool {
                self.playing = playing
                frame = said["frame"] as? Int ?? frame
            }
            if said["status"] as? String == "ended" {
                let why = said["why"] as? String ?? ""
                status = .ended(why == "ended" || why.isEmpty ? String(localized: "The view ended.") : Self.refusal(why))
            }
        }
    }

    // MARK: RTCPeerConnectionDelegate (called on WebRTC's signalling thread)

    func peerConnection(_ peer: RTCPeerConnection, didChange newState: RTCIceConnectionState) {
        guard newState == .failed else { return }
        DispatchQueue.main.async { [self] in
            if !stopped { end(String(localized: "The connection to your hub was lost.")) }
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

/// Keeps the last frame of the model's video, and makes a picture of it.
private final class FrameGrabber: NSObject, RTCVideoRenderer {
    private let lock = NSLock()
    private var last: RTCVideoFrame?

    func setSize(_ size: CGSize) {}

    func renderFrame(_ frame: RTCVideoFrame?) {
        lock.lock()
        last = frame
        lock.unlock()
    }

    func picture() -> UIImage? {
        lock.lock()
        let frame = last
        lock.unlock()
        guard let pixels = (frame?.buffer as? RTCCVPixelBuffer)?.pixelBuffer else { return nil }
        let image = CIImage(cvPixelBuffer: pixels)
        guard let picture = CIContext().createCGImage(image, from: image.extent) else { return nil }
        return UIImage(cgImage: picture)
    }
}

/// Where you look at a model from: a camera orbiting a target point, at a distance, turned
/// around the model's up axis (yaw) and tilted towards it (pitch), in radians and the model's
/// units, as the renderer's View has it.
struct OrbitCamera: Equatable {
    var yaw = 0.6
    var pitch = 0.45
    var distance = 1.0
    var target = SIMD3<Double>(0, 0, 0)
    var zUp = false
    var fov = 40.0

    private var worldUp: SIMD3<Double> { zUp ? [0, 0, 1] : [0, 1, 0] }

    /// From the target towards the camera.
    private var back: SIMD3<Double> {
        let flat = cos(pitch), rise = sin(pitch)
        return zUp ? [flat * sin(yaw), -flat * cos(yaw), rise] : [flat * sin(yaw), rise, flat * cos(yaw)]
    }

    /// Turns it with a finger's drag (points): across turns it around the model, up and down
    /// tilts it, short of looking straight down or up.
    mutating func orbit(dx: Double, dy: Double) {
        yaw -= dx * 0.008
        pitch = min(max(pitch + dy * 0.008, -1.55), 1.55)
    }

    /// Moves what it looks at with two fingers' drag, so the model follows them, in a view
    /// `height` points tall.
    mutating func pan(dx: Double, dy: Double, height: Double) {
        let right = simd_normalize(simd_cross(worldUp, back))
        let up = simd_cross(back, right)
        let perPoint = 2 * distance * tan(fov * .pi / 360) / max(height, 1)
        target += (-right * dx + up * dy) * perPoint
    }

    /// Comes closer as you pinch apart, by its scale.
    mutating func zoom(by scale: Double) {
        guard scale > 0 else { return }
        distance = max(distance / scale, 1e-4)
    }

    var input: [String: Any] {
        ["camera": ["yaw": yaw, "pitch": pitch, "distance": distance, "target": [target.x, target.y, target.z]]]
    }
}
