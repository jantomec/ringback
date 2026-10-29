import AVFoundation
import Foundation
import Observation

/// Your voice sample: a short recording of you reading a few sentences, kept on the phone.
/// Each call sends 3 s of it to the Mac, where VoxLock uses it to keep only your voice from
/// your first word. Until it's recorded, VoxLock learns your voice from your first 3 s of
/// speech on each call.
@Observable
final class YourVoice: NSObject, AVAudioPlayerDelegate {
    /// What the Mac hears calls at: 16 kHz mono, 16-bit.
    nonisolated static let sampleRate = 16000
    /// VoxLock uses exactly this much of your voice.
    nonisolated static let sampleSeconds = 3
    /// Shorter recordings aren't kept: the script takes about 15 s to read.
    static let minimumSeconds = 8.0
    /// Recording stops by itself after this long.
    static let maximumSeconds = 30.0
    private static let recordingFile = URL.applicationSupportDirectory.appending(path: "your-voice.wav")
    nonisolated private static let sampleFile = URL.applicationSupportDirectory.appending(path: "your-voice-sample.pcm")

    static let script = """
        Hi Claude, I'm back. Tell me what you finished and what needs my attention. \
        If the tests passed, go ahead and merge the branch. Otherwise, wait for me and don't \
        push anything. Call me again when you're done.
        """

    private(set) var recordedAt: Date? = YourVoice.sampleDate()
    private(set) var isRecording = false
    private(set) var isPlaying = false
    /// While recording: seconds so far, and the input level from 0 to 1.
    private(set) var elapsed: TimeInterval = 0
    private(set) var level: Float = 0
    var error: String?

    @ObservationIgnored private var engine: AVAudioEngine?
    @ObservationIgnored private var collector: Collector?
    @ObservationIgnored private var timer: Timer?
    @ObservationIgnored private var player: AVAudioPlayer?

    /// The 3 s sent with each call: base64 of 16-bit little-endian PCM. Nil until recorded.
    nonisolated static func sampleForCall() -> String? {
        (try? Data(contentsOf: sampleFile))?.base64EncodedString()
    }

    // MARK: Recording

    /// Records as on a call: through voice processing, from your AirPods or other headset if
    /// one is connected, so the sample sounds like you do on calls.
    func startRecording() {
        guard !isRecording else { return }
        stopPlaying()
        error = nil
        guard AVAudioApplication.shared.recordPermission == .granted else {
            error = "Microphone is off. Turn it on in Ringback's settings."
            return
        }
        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.playAndRecord, mode: .voiceChat, options: [.allowBluetoothHFP])
            try session.setActive(true)
            let engine = AVAudioEngine()
            let input = engine.inputNode
            try input.setVoiceProcessingEnabled(true)
            let format = input.outputFormat(forBus: 0)
            guard let collector = Collector(from: format) else { throw CocoaError(.featureUnsupported) }
            input.installTap(onBus: 0, bufferSize: 4096, format: format) { buffer, _ in collector.add(buffer) }
            try engine.start()
            self.engine = engine
            self.collector = collector
        } catch {
            self.error = "Couldn't record: \(error.localizedDescription)"
            finishRecording()
            return
        }
        isRecording = true
        elapsed = 0
        timer = Timer.scheduledTimer(withTimeInterval: 0.1, repeats: true) { [weak self] _ in self?.tick() }
    }

    /// Stops recording and keeps it, if it's long enough.
    func stopRecording() {
        guard isRecording, let samples = finishRecording() else { return }
        guard Double(samples.count) >= Self.minimumSeconds * Double(Self.sampleRate) else {
            error = "That was too short. Read all of the text."
            return
        }
        do {
            try FileManager.default.createDirectory(at: .applicationSupportDirectory, withIntermediateDirectories: true)
            try Self.wav(samples).write(to: Self.recordingFile, options: .atomic)
            try Self.data(Self.loudest(samples)).write(to: Self.sampleFile, options: .atomic)
            recordedAt = Self.sampleDate()
        } catch {
            self.error = "Couldn't save it: \(error.localizedDescription)"
        }
    }

    /// Stops recording without keeping it, e.g. when you leave the screen.
    func cancel() {
        finishRecording()
        stopPlaying()
    }

    private func tick() {
        guard let collector else { return }
        (elapsed, level) = collector.progress
        if elapsed >= Self.maximumSeconds { stopRecording() }
    }

    /// Stops the engine and returns what was recorded, if anything was.
    @discardableResult
    private func finishRecording() -> [Int16]? {
        timer?.invalidate()
        timer = nil
        engine?.inputNode.removeTap(onBus: 0)
        engine?.stop()
        engine = nil
        let samples = collector?.samples
        collector = nil
        isRecording = false
        level = 0
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        return samples
    }

    // MARK: Playing it back

    func play() {
        guard !isRecording else { return }
        do {
            try AVAudioSession.sharedInstance().setCategory(.playback)
            try AVAudioSession.sharedInstance().setActive(true)
            let player = try AVAudioPlayer(contentsOf: Self.recordingFile)
            player.delegate = self
            player.play()
            self.player = player
            isPlaying = true
        } catch {
            self.error = "Couldn't play it: \(error.localizedDescription)"
        }
    }

    func stopPlaying() {
        guard let player else { return }
        player.stop()
        audioPlayerDidFinishPlaying(player, successfully: false)
    }

    func audioPlayerDidFinishPlaying(_ player: AVAudioPlayer, successfully flag: Bool) {
        self.player = nil
        isPlaying = false
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    // MARK: Files

    private static func sampleDate() -> Date? {
        try? FileManager.default.attributesOfItem(atPath: sampleFile.path)[.modificationDate] as? Date
    }

    /// The loudest 3 s in the first 30 s, the way VoxLock picks its sample from a longer
    /// recording (`loud_sample` in voxlock/core.py): the energy of each 10 ms, then the
    /// stretch with the most.
    static func loudest(_ samples: [Int16]) -> ArraySlice<Int16> {
        let frame = sampleRate / 100
        let window = sampleSeconds * 100
        let head = samples.prefix(30 * sampleRate)
        let frames = head.count / frame
        guard frames > window else { return head }
        let energy = (0..<frames).map { i in
            head[i * frame..<(i + 1) * frame].reduce(0.0) { $0 + Double($1) * Double($1) }
        }
        var sum = energy[..<window].reduce(0, +)
        var best = sum
        var start = 0
        for i in window..<frames {
            sum += energy[i] - energy[i - window]
            if sum > best {
                best = sum
                start = i - window + 1
            }
        }
        return head[start * frame..<start * frame + sampleSeconds * sampleRate]
    }

    private static func data(_ samples: some Collection<Int16>) -> Data {
        var data = Data(capacity: samples.count * 2)
        for sample in samples { withUnsafeBytes(of: sample.littleEndian) { data.append(contentsOf: $0) } }
        return data
    }

    /// A WAV file of 16 kHz mono 16-bit samples.
    private static func wav(_ samples: [Int16]) -> Data {
        let body = data(samples)
        var header = Data()
        func tag(_ text: String) { header.append(contentsOf: Array(text.utf8)) }
        func int<T: FixedWidthInteger>(_ value: T) { withUnsafeBytes(of: value.littleEndian) { header.append(contentsOf: $0) } }
        tag("RIFF"); int(UInt32(36 + body.count)); tag("WAVE")
        tag("fmt "); int(UInt32(16)); int(UInt16(1)); int(UInt16(1))  // PCM, mono
        int(UInt32(sampleRate)); int(UInt32(sampleRate * 2)); int(UInt16(2)); int(UInt16(16))
        tag("data"); int(UInt32(body.count))
        return header + body
    }
}

/// Converts the microphone's audio to 16 kHz mono 16-bit as it arrives, on the audio thread,
/// and keeps it.
private final class Collector: @unchecked Sendable {
    private let converter: AVAudioConverter
    private let format: AVAudioFormat
    private let lock = NSLock()
    private var collected: [Int16] = []
    private var lastLevel: Float = 0

    init?(from input: AVAudioFormat) {
        guard
            let format = AVAudioFormat(
                commonFormat: .pcmFormatInt16, sampleRate: Double(YourVoice.sampleRate), channels: 1, interleaved: true),
            let converter = AVAudioConverter(from: input, to: format)
        else { return nil }
        converter.downmix = true
        self.converter = converter
        self.format = format
    }

    var samples: [Int16] { lock.withLock { collected } }

    /// Seconds collected, and the level of the latest audio from 0 (-60 dBFS) to 1.
    var progress: (TimeInterval, Float) {
        lock.withLock { (Double(collected.count) / Double(YourVoice.sampleRate), lastLevel) }
    }

    func add(_ buffer: AVAudioPCMBuffer) {
        let capacity = AVAudioFrameCount(Double(buffer.frameLength) * format.sampleRate / buffer.format.sampleRate) + 32
        guard let out = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else { return }
        var given = false
        var error: NSError?
        converter.convert(to: out, error: &error) { _, status in
            if given {
                status.pointee = .noDataNow
                return nil
            }
            given = true
            status.pointee = .haveData
            return buffer
        }
        guard error == nil, let channel = out.int16ChannelData else { return }
        let samples = UnsafeBufferPointer(start: channel[0], count: Int(out.frameLength))
        let power = samples.reduce(0.0) { $0 + Double($1) * Double($1) } / Double(max(samples.count, 1))
        let decibels = 10 * log10(power / (32768.0 * 32768.0) + 1e-10)
        lock.withLock {
            collected.append(contentsOf: samples)
            lastLevel = Float(min(max((decibels + 60) / 60, 0), 1))
        }
    }
}
