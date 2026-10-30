// What Google's WebRTC, the package and version the app uses, offers for audio and takes from
// an answer, run on this Mac from the package's macOS build. It makes an offer as the app does
// (AudioLink.gather), prints its audio lines, applies an answer like the voice server's with
// the Opus parameters given (none if empty), and prints what its sender then uses for Opus.
// No audio flows. See README.md for how to build and run it.
import Foundation
import WebRTC

final class Quiet: NSObject, RTCPeerConnectionDelegate {
    func peerConnection(_ peer: RTCPeerConnection, didChange stateChanged: RTCSignalingState) {}
    func peerConnection(_ peer: RTCPeerConnection, didAdd stream: RTCMediaStream) {}
    func peerConnection(_ peer: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    func peerConnectionShouldNegotiate(_ peer: RTCPeerConnection) {}
    func peerConnection(_ peer: RTCPeerConnection, didChange newState: RTCIceConnectionState) {}
    func peerConnection(_ peer: RTCPeerConnection, didChange newState: RTCIceGatheringState) {}
    func peerConnection(_ peer: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {}
    func peerConnection(_ peer: RTCPeerConnection, didRemove candidates: [RTCIceCandidate]) {}
    func peerConnection(_ peer: RTCPeerConnection, didOpen dataChannel: RTCDataChannel) {}
}

RTCInitializeSSL()
let factory = RTCPeerConnectionFactory()
let config = RTCConfiguration()
config.sdpSemantics = .unifiedPlan
let none = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
let quiet = Quiet()
let peer = factory.peerConnection(with: config, constraints: none, delegate: quiet)!
let sender = peer.add(factory.audioTrack(with: factory.audioSource(with: none), trackId: "mic"), streamIds: ["call"])!

let done = DispatchSemaphore(value: 0)
var offer = ""
peer.offer(for: none) { sdp, _ in
    offer = sdp?.sdp ?? ""
    peer.setLocalDescription(sdp!) { _ in done.signal() }
}
done.wait()
let lines = offer.components(separatedBy: "\r\n")
print("the offer's audio:")
for line in lines where line.hasPrefix("m=audio") || line.hasPrefix("a=rtpmap") || line.hasPrefix("a=fmtp") {
    print("  \(line)")
}

let fmtp = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : ""
let fingerprint = lines.first { $0.hasPrefix("a=fingerprint:") } ?? ""
var answer = [
    "v=0", "o=- 1 1 IN IP4 0.0.0.0", "s=-", "t=0 0", "a=group:BUNDLE 0", "m=audio 9 UDP/TLS/RTP/SAVPF 111",
    "c=IN IP4 0.0.0.0", "a=sendrecv", "a=mid:0", "a=rtcp-mux", "a=ssrc:1 cname:mac", "a=rtpmap:111 opus/48000/2",
]
if !fmtp.isEmpty { answer.append("a=fmtp:111 \(fmtp)") }
answer += ["a=ice-ufrag:abcd", "a=ice-pwd:abcdefghijklmnopqrstuvwx", fingerprint, "a=setup:active", ""]
peer.setRemoteDescription(RTCSessionDescription(type: .answer, sdp: answer.joined(separator: "\r\n"))) { error in
    if let error { print("the answer failed: \(error)") }
    done.signal()
}
done.wait()
print("with an answer whose Opus parameters are \"\(fmtp)\", the sender uses:")
for codec in sender.parameters.codecs where codec.name == "opus" {
    print("  opus \(codec.clockRate ?? 0)/\(codec.numChannels ?? 0), parameters \(codec.parameters)")
}
peer.close()
