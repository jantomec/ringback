// The model's live view (`serve`, render/README.md): the camera and the time the agent gives it
// on stdin, rendered on Metal straight into the encoder's buffers and streamed as H.264 on
// stdout. The agent relays it to the phone through the hub.
//
// What the agent says on stdin, one JSON object a line:
//   {"camera": {"yaw", "pitch", "distance", "target": [x, y, z]}}: where to look from, as View
//       has it (radians, and the model's units);
//   {"frame": n}: go to frame n (from 0), and pause;
//   {"play": true | false};
//   {"keyframe": true}: the next frame is a key frame, as when the phone lost one.
// What it writes on stdout: packets of a 4-byte big-endian length, then a kind, then the rest:
//   "R", JSON: it's ready, with the model's facts, its home view and the field of view;
//   "S", JSON: {"frame", "playing"}, when they change, and a few times a second while playing;
//   "V": a frame, H.264 in Annex B, with its parameter sets before each key frame.
// It renders only when something changes, as soon as it does, or while it plays, at most 60
// frames a second, with 4× multisampling, and a few more after a change, the last of them a key frame: the frames
// just after a change get few bits, and the same picture again adds no detail, so without one
// the view would stay as blurry as it first came. It ends when stdin closes.
import CoreMedia
import CoreVideo
import Foundation
import Metal
import SceneKit
import VideoToolbox

final class Streamer {
    static let fps = 60
    /// Frames sent after the last change, for the encoder to sharpen a still picture.
    static let settleFrames = 15

    let model: Model
    let width: Int, height: Int
    let renderer: Renderer
    let queue: MTLCommandQueue
    /// What it renders into, 4 samples a pixel, resolved into the encoder's buffer.
    let color: MTLTexture
    let depth: MTLTexture
    var cache: CVMetalTextureCache?
    var session: VTCompressionSession?
    let writing = NSLock()
    /// Wakes the render loop when the agent says something.
    let wake = DispatchSemaphore(value: 0)

    /// What stdin's thread changes, under lock.
    let lock = NSLock()
    var view: View
    var frame = 0
    var playing: (from: Int, at: Date)?
    var changed = true
    var keyframe = true
    var settle = 0
    var encoded = 0

    init(_ model: Model, width: Int, height: Int) {
        self.model = model
        self.width = width
        self.height = height
        renderer = Renderer(model)
        let device = renderer.renderer.device!
        guard let queue = device.makeCommandQueue() else { fail("Metal has no command queue") }
        self.queue = queue
        func multisampled(_ format: MTLPixelFormat) -> MTLTexture? {
            let described = MTLTextureDescriptor.texture2DDescriptor(pixelFormat: format, width: width, height: height, mipmapped: false)
            described.textureType = .type2DMultisample
            described.sampleCount = 4
            described.usage = .renderTarget
            described.storageMode = .private
            return device.makeTexture(descriptor: described)
        }
        guard let color = multisampled(.bgra8Unorm_srgb), let depth = multisampled(.depth32Float) else {
            fail("Metal can't make the buffers to render into")
        }
        self.color = color
        self.depth = depth
        view = View.framing(model, aspect: Double(width) / Double(height))
        CVMetalTextureCacheCreate(nil, nil, device, nil, &cache)
        startEncoder()
    }

    /// An H.264 encoder for a stream the phone decodes as it comes: Constrained Baseline (which
    /// the phone's decoder and the hub agree on), in real time, with no frames reordered.
    private func startEncoder() {
        let source: [CFString: Any] = [
            kCVPixelBufferPixelFormatTypeKey: kCVPixelFormatType_32BGRA, kCVPixelBufferWidthKey: width,
            kCVPixelBufferHeightKey: height, kCVPixelBufferIOSurfacePropertiesKey: [:] as [CFString: Any],
            kCVPixelBufferMetalCompatibilityKey: true,
        ]
        let status = VTCompressionSessionCreate(
            allocator: nil, width: Int32(width), height: Int32(height), codecType: kCMVideoCodecType_H264,
            encoderSpecification: nil, imageBufferAttributes: source as CFDictionary, compressedDataAllocator: nil,
            outputCallback: nil, refcon: nil, compressionSessionOut: &session)
        guard status == noErr, let session else { fail("VideoToolbox can't encode H.264 here (\(status))") }
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_RealTime, value: kCFBooleanTrue)
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_AllowFrameReordering, value: kCFBooleanFalse)
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_ProfileLevel, value: kVTProfileLevel_H264_ConstrainedBaseline_AutoLevel)
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_ExpectedFrameRate, value: Self.fps as CFNumber)
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_AverageBitRate, value: 8_000_000 as CFNumber)
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration, value: 4 as CFNumber)
        VTCompressionSessionPrepareToEncodeFrames(session)
    }

    func run() -> Never {
        let home = view
        send("R", json: [
            "ready": true, "animated": model.animated, "frames": model.frames, "fps": model.fps, "up": model.zUp ? "Z" : "Y",
            "fov": home.fov, "width": width, "height": height,
            "home": ["yaw": home.azimuth, "pitch": home.elevation, "distance": home.distance,
                     "target": [home.target.x, home.target.y, home.target.z]],
        ])
        Thread.detachNewThread { self.listen() }
        let interval = 1.0 / Double(Self.fps)
        var last = Date.distantPast
        while true {
            // Idle, it waits for the agent; busy, for its next frame's time, or sooner if the
            // agent says something: a turn is rendered as soon as it comes.
            lock.lock()
            let busy = changed || settle > 0 || playing != nil
            lock.unlock()
            _ = wake.wait(timeout: busy ? .now() + interval : .distantFuture)
            let since = Date().timeIntervalSince(last)
            if since < interval { Thread.sleep(forTimeInterval: interval - since) }
            last = Date()
            renderIfNeeded()
        }
    }

    /// Reads what the agent says, until stdin closes, which ends the stream.
    private func listen() {
        while let line = readLine() {
            guard let said = (try? JSONSerialization.jsonObject(with: Data(line.utf8))) as? [String: Any] else { continue }
            lock.lock()
            if let camera = said["camera"] as? [String: Any] {
                if let yaw = camera["yaw"] as? Double { view.azimuth = yaw }
                if let pitch = camera["pitch"] as? Double { view.elevation = min(max(pitch, -1.55), 1.55) }
                if let distance = camera["distance"] as? Double, distance > 0 { view.distance = distance }
                if let target = camera["target"] as? [Double], target.count == 3 { view.target = [target[0], target[1], target[2]] }
                changed = true
            }
            if let to = said["frame"] as? Int {
                frame = min(max(to, 0), model.frames - 1)
                playing = nil
                changed = true
                tellState()
            }
            if let play = said["play"] as? Bool, model.animated {
                // Played from the end, it starts again.
                playing = play ? (frame >= model.frames - 1 ? 0 : frame, Date()) : nil
                if !play { changed = true }
                tellState()
            }
            if said["keyframe"] as? Bool == true {
                keyframe = true
                changed = true
            }
            lock.unlock()
            wake.signal()
        }
        exit(0)
    }

    /// Says the frame and whether it plays. The caller holds lock.
    private func tellState() {
        send("S", json: ["frame": frame, "playing": playing != nil])
    }

    private func renderIfNeeded() {
        lock.lock()
        if let playing {
            let next = (playing.from + Int(Date().timeIntervalSince(playing.at) * model.fps)) % model.frames
            if next != frame {
                frame = next
                changed = true
                if next % 6 == 0 { tellState() }
            }
        }
        if changed {
            settle = Self.settleFrames
            changed = false
        } else if playing != nil {
            // Playing, it renders the model's own frames only.
            lock.unlock()
            return
        } else if settle > 0 {
            settle -= 1
        } else {
            lock.unlock()
            return
        }
        // The last frame after a change is a key frame, for the picture to come out sharp.
        let sharpen = settle == 0 && playing == nil
        let view = self.view, frame = self.frame, keyframe = self.keyframe || sharpen
        self.keyframe = false
        lock.unlock()
        encode(view: view, frame: frame, keyframe: keyframe)
    }

    /// Renders the model into one of the encoder's buffers, on Metal, and encodes it.
    private func encode(view: View, frame: Int, keyframe: Bool) {
        guard let session, let pool = VTCompressionSessionGetPixelBufferPool(session), let cache else { return }
        var buffer: CVPixelBuffer?
        CVPixelBufferPoolCreatePixelBuffer(nil, pool, &buffer)
        var wrapped: CVMetalTexture?
        guard let buffer,
            CVMetalTextureCacheCreateTextureFromImage(nil, cache, buffer, nil, .bgra8Unorm_srgb, width, height, 0, &wrapped) == kCVReturnSuccess,
            let wrapped, let texture = CVMetalTextureGetTexture(wrapped), let commands = queue.makeCommandBuffer()
        else { return }
        let pass = MTLRenderPassDescriptor()
        pass.colorAttachments[0].texture = color
        pass.colorAttachments[0].resolveTexture = texture
        pass.colorAttachments[0].loadAction = .clear
        // The still's background, in linear light, as the sRGB texture takes it.
        pass.colorAttachments[0].clearColor = MTLClearColor(red: 0.0130, green: 0.0130, blue: 0.0168, alpha: 1)
        pass.colorAttachments[0].storeAction = .multisampleResolve
        pass.depthAttachment.texture = depth
        pass.depthAttachment.loadAction = .clear
        pass.depthAttachment.clearDepth = 1
        pass.depthAttachment.storeAction = .dontCare
        renderer.place(view)
        // Rendering this way, SceneKit sees the camera move only once its transaction is flushed.
        SCNTransaction.flush()
        renderer.renderer.render(
            atTime: renderer.time(of: frame), viewport: CGRect(x: 0, y: 0, width: width, height: height),
            commandBuffer: commands, passDescriptor: pass)
        commands.commit()
        commands.waitUntilCompleted()
        let properties: CFDictionary? = keyframe ? [kVTEncodeFrameOptionKey_ForceKeyFrame: true] as CFDictionary : nil
        let at = CMTime(value: CMTimeValue(encoded), timescale: CMTimeScale(Self.fps))
        encoded += 1
        VTCompressionSessionEncodeFrame(
            session, imageBuffer: buffer, presentationTimeStamp: at, duration: .invalid, frameProperties: properties,
            infoFlagsOut: nil
        ) { [weak self] status, _, sample in
            guard status == noErr, let sample, let data = Self.annexB(sample) else { return }
            self?.send("V", data: data)
        }
    }

    /// An encoded frame as H.264 in Annex B: start codes in place of lengths, with the
    /// parameter sets before a key frame, which the phone's decoder needs to start from it.
    static func annexB(_ sample: CMSampleBuffer) -> Data? {
        guard let block = CMSampleBufferGetDataBuffer(sample), let format = CMSampleBufferGetFormatDescription(sample) else { return nil }
        let start: [UInt8] = [0, 0, 0, 1]
        var data = Data()
        let attachments = CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: false) as? [[CFString: Any]]
        let key = !(attachments?.first?[kCMSampleAttachmentKey_NotSync] as? Bool ?? false)
        var sets = 0
        var lengthSize: Int32 = 4
        CMVideoFormatDescriptionGetH264ParameterSetAtIndex(
            format, parameterSetIndex: 0, parameterSetPointerOut: nil, parameterSetSizeOut: nil,
            parameterSetCountOut: &sets, nalUnitHeaderLengthOut: &lengthSize)
        if key {
            for i in 0..<sets {
                var set: UnsafePointer<UInt8>?
                var size = 0
                CMVideoFormatDescriptionGetH264ParameterSetAtIndex(
                    format, parameterSetIndex: i, parameterSetPointerOut: &set, parameterSetSizeOut: &size,
                    parameterSetCountOut: nil, nalUnitHeaderLengthOut: nil)
                if let set {
                    data.append(contentsOf: start)
                    data.append(set, count: size)
                }
            }
        }
        var total = 0
        var pointer: UnsafeMutablePointer<CChar>?
        guard CMBlockBufferGetDataPointer(block, atOffset: 0, lengthAtOffsetOut: nil, totalLengthOut: &total, dataPointerOut: &pointer) == noErr,
            let pointer
        else { return nil }
        let bytes = UnsafeRawPointer(pointer).assumingMemoryBound(to: UInt8.self)
        var offset = 0
        while offset + Int(lengthSize) <= total {
            var length = 0
            for i in 0..<Int(lengthSize) { length = length << 8 | Int(bytes[offset + i]) }
            offset += Int(lengthSize)
            guard offset + length <= total else { break }
            data.append(contentsOf: start)
            data.append(bytes + offset, count: length)
            offset += length
        }
        return data
    }

    private func send(_ kind: String, json: [String: Any]) {
        if let data = try? JSONSerialization.data(withJSONObject: json) { send(kind, data: data) }
    }

    private func send(_ kind: String, data: Data) {
        var packet = Data()
        let length = UInt32(data.count + 1).bigEndian
        withUnsafeBytes(of: length) { packet.append(contentsOf: $0) }
        packet.append(contentsOf: kind.utf8)
        packet.append(data)
        writing.lock()
        FileHandle.standardOutput.write(packet)
        writing.unlock()
    }
}
