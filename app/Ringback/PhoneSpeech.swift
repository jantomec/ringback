import AVFoundation
import CoreMedia
import Foundation
import Speech

/// On-device voice, the switch in the Settings app: while no speech computer is linked to the
/// hub, the phone hears and speaks calls itself (PhoneSpeech), with Apple's speech recognition
/// and voices, and only text goes to the hub. The hub learns it from /register (PushService).
enum OnDeviceVoice {
    static let key = "onDeviceVoice"
    static let locale = Locale(identifier: "en-US")

    static var isOn: Bool { UserDefaults.standard.bool(forKey: key) }

    /// The switch as `prepare` last saw it, and whether it's at work now.
    @MainActor private static var prepared: Bool?
    @MainActor private static var preparing = false

    /// While the switch is on, makes sure Apple's English model is installed, downloading it if
    /// it isn't; if it can't, `state.onDeviceProblem` says why, and Recents shows it. Run when
    /// the app opens (`always`) and when a setting changes, which skips it unless the switch did.
    @MainActor
    static func prepare(_ state: AppState, always: Bool = true) async {
        let on = isOn
        guard always || on != prepared, !preparing else { return }
        prepared = on
        guard on else {
            state.onDeviceProblem = nil
            return
        }
        preparing = true
        defer { preparing = false }
        do {
            try await installModel()
            state.onDeviceProblem = nil
        } catch {
            state.onDeviceProblem = error.localizedDescription
        }
    }

    /// Apple's English model for SpeechTranscriber, installed if it isn't yet. The model runs in
    /// a system service, outside the app.
    static func installModel() async throws {
        guard SpeechTranscriber.isAvailable else { throw SpeechError("This iPhone can't recognise speech by itself.") }
        guard let locale = await SpeechTranscriber.supportedLocale(equivalentTo: locale) else {
            throw SpeechError("Apple has no English speech model for this iPhone.")
        }
        let modules = [SpeechTranscriber(locale: locale, preset: .transcription)]
        if await AssetInventory.status(forModules: modules) == .installed { return }
        if let request = try await AssetInventory.assetInstallationRequest(supporting: modules) {
            try await request.downloadAndInstall()
        }
        guard await AssetInventory.status(forModules: modules) == .installed else {
            throw SpeechError("Apple's English speech model didn't install.")
        }
    }
}

struct SpeechError: LocalizedError {
    let errorDescription: String?
    init(_ text: String) { errorDescription = text }
}

/// What an offer asks the hub for, which the phone's own speech says in its hello too: the call
/// it's about (for a call back, the call it calls back), and its kind, "ring", "now", "back" or
/// "contact", with the session for "contact".
struct CallOffer {
    var callID: String
    var kind = "ring"
    var session: String?
}

/// Converts audio from one format to another, keeping its state from one buffer to the next.
final class Converter {
    private let converter: AVAudioConverter
    private let format: AVAudioFormat

    init(from: AVAudioFormat, to: AVAudioFormat) throws {
        guard let converter = AVAudioConverter(from: from, to: to) else { throw SpeechError("can't convert \(from) to \(to)") }
        converter.primeMethod = .none
        converter.downmix = true
        self.converter = converter
        format = to
    }

    /// `last`: no more audio follows, so everything left comes out.
    func convert(_ buffer: AVAudioPCMBuffer, last: Bool = false) throws -> AVAudioPCMBuffer {
        let capacity = AVAudioFrameCount(Double(buffer.frameLength) * format.sampleRate / buffer.format.sampleRate) + 1024
        guard let out = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else { throw SpeechError("no buffer") }
        var given = false
        var error: NSError?
        converter.convert(to: out, error: &error) { _, status in
            if given {
                status.pointee = last ? .endOfStream : .noDataNow
                return nil
            }
            given = true
            status.pointee = .haveData
            return buffer
        }
        if let error { throw error }
        return out
    }
}

/// Finds when you start and stop speaking, and when your turn ends, from the level of the
/// microphone's audio after echo cancellation, as the speech trial did: the noise floor follows
/// the quiet down at once and up slowly, and speech is audio well above it and above a minimum.
/// A rough stand-in for the speech node's Silero and Smart Turn. Your Interruption Sensitivity
/// and Ignore Background Noise set its thresholds, and while the voice plays it's harder to set
/// off, so what's left of the voice's echo doesn't count as you interrupting.
struct LevelDetector {
    struct Thresholds: Equatable {
        /// The quietest audio that's speech, in dB below full scale.
        var minimum: Double
        /// How far above the noise floor speech is, in dB.
        var margin: Double
        /// How long speech lasts before it counts, in seconds.
        var onset: Double

        /// From the app's settings, each 0 to 1, the background filter 0 when it's off. At the
        /// defaults (0.5 and 0.5) they're the speech trial's (`tools/speech-trial`), whose 22
        /// starts of speech, at -52 to -19 dB with the floor at -81 to -94, all counted: -55 dB,
        /// 15 dB above the floor. Less sensitive needs louder and longer speech; the filter raises
        /// the minimum by up to 5 dB over the default.
        init(sensitivity: Double, backgroundFilter: Double) {
            let s = min(max(sensitivity, 0), 1)
            let g = min(max(backgroundFilter, 0), 1)
            onset = s < 0.5 ? 0.25 - 0.2 * s : 0.15 - 0.14 * (s - 0.5)  // 0.25, 0.15, 0.08 s
            margin = 15 + 6 * (0.5 - s)  // 18, 15, 12 dB
            minimum = -60 + 10 * g + 8 * (0.5 - s)  // -55 dB at the defaults
        }
    }

    /// Quiet this long is a pause (`speech_stopped`).
    static let pause = 0.3
    /// Quiet this long ends your turn.
    static let turnEnd = 0.8
    /// How fast the noise floor rises, in dB a second.
    static let floorRise = 2.0
    /// While the voice plays: this much louder, and this much longer.
    static let echoLevel = 8.0
    static let echoOnset = 0.1
    /// Sums of buffer lengths aren't exact (0.1 eight times is 0.7999…).
    private static let slack = 1e-6

    enum Event: Equatable {
        case started, stopped
        /// Your turn ended: it started, and its speech ended, at these times in the audio.
        case turn(start: Double, end: Double)
    }

    let thresholds: Thresholds
    private(set) var noiseFloor = -50.0
    private(set) var speaking = false
    private var loudFor = 0.0
    private var quietFor = 0.0
    private var turnStart: Double?
    private var lastLoud = 0.0

    init(_ thresholds: Thresholds) {
        self.thresholds = thresholds
    }

    /// One buffer: its level in dB below full scale (nil while muted: quiet, and the floor stays
    /// put), how long it lasts, where it ends in the audio, and whether the voice is playing.
    mutating func feed(level: Double?, duration: Double, end: Double, voicePlaying: Bool = false) -> [Event] {
        var events: [Event] = []
        var loud = false
        if let level {
            noiseFloor = min(level, noiseFloor + Self.floorRise * duration)
            let echo = voicePlaying ? Self.echoLevel : 0
            loud = level > max(noiseFloor + thresholds.margin, thresholds.minimum) + echo
        }
        if loud {
            loudFor += duration
            quietFor = 0
            if speaking {
                lastLoud = end
            } else if loudFor + Self.slack >= thresholds.onset + (voicePlaying ? Self.echoOnset : 0) {
                speaking = true
                lastLoud = end
                if turnStart == nil { turnStart = end - loudFor }
                events.append(.started)
            }
        } else {
            loudFor = 0
            quietFor += duration
            if speaking && quietFor + Self.slack >= Self.pause {
                speaking = false
                events.append(.stopped)
            }
            if !speaking, let start = turnStart, quietFor + Self.slack >= Self.turnEnd {
                turnStart = nil
                events.append(.turn(start: start, end: lastLoud))
            }
        }
        return events
    }
}

/// A weak reference that can go to another thread, for an object used only on the main queue:
/// it's read only there.
private struct Weak<Object: AnyObject>: @unchecked Sendable {
    weak var value: Object?
    init(_ value: Object) { self.value = value }
}

/// What the phone sends the hub, as JSON text, in the order sent, from any thread.
struct Outbox: Sendable {
    let continuation: AsyncStream<String>.Continuation

    func send(_ type: String, _ fields: [String: Any] = [:]) {
        continuation.yield(Self.encode(type, fields))
    }

    func finish() { continuation.finish() }

    static func encode(_ type: String, _ fields: [String: Any] = [:]) -> String {
        var message = fields
        message["type"] = type
        let data = (try? JSONSerialization.data(withJSONObject: message, options: [.sortedKeys])) ?? Data()
        return String(decoding: data, as: UTF8.self)
    }

    /// Seconds, to the millisecond, for the hub.
    static func secs(_ value: Double) -> Double { (value * 1000).rounded() / 1000 }
}

/// A call's speech on the phone ("The phone as the speech node" in docs/protocol.md): when the
/// hub has no speech computer and On-device voice is on, the phone hears you and speaks the
/// voice itself, and its link to the hub, a WebSocket to `/phone-speech`, carries only text.
/// CallService makes, starts and stops it as it does an AudioLink.
///
/// The link opens and says hello as an offer would, while the phone rings too; the hub welcomes
/// it once the session's Mac takes the call. Once you've answered and CallKit has activated the
/// audio session, one audio engine, with the call's echo cancellation, hears the microphone and
/// plays the voice, so the cancellation hears the voice too. It hears once the hub says `hear`.
/// Used on the main queue, as AudioLink is; hearing and speaking run on the engine's threads and
/// send the hub what they find through the outbox.
final class PhoneSpeech: CallLink {
    /// True while the phone rings. The hello says so, and answering then tells the hub.
    var ringing = false {
        didSet { if oldValue && !ringing { answered() } }
    }
    /// CallKit has activated the call's audio session.
    var audioActive = false {
        didSet { audioActive ? startEngineIfReady() : stopEngine() }
    }
    /// Called once, on the main queue, when the hub welcomes the link; with when.
    var onConnected: ((Date) -> Void)?
    /// Called once, on the main queue, when the link or the phone's speech fails after the
    /// welcome: the link closed without the hub ending the call, or hearing or speaking broke.
    var onFailed: ((String) -> Void)?
    /// The hub ended the call (`end`).
    var onEnded: (() -> Void)?
    let addresses = "none, the phone hears and speaks itself"

    private let outbox: Outbox
    private let outgoing: AsyncStream<String>
    private let speaking = Speaking()
    private var task: URLSessionWebSocketTask?
    private var sender: Task<Void, Never>?
    private var receiver: Task<Void, Never>?
    private var pinger: Task<Void, Never>?
    private var lastPong = Date.now
    private var welcomed = false
    /// The hello said the phone rang, so the hub waits for `answered`.
    private var helloRinging = false
    private var answerSent = false
    private var endedByHub = false
    private var stopped = false
    /// The hub said `hear`.
    private var listening = false
    private var muted = false
    private var preparing: Task<Hearing, Error>?
    private var hearing: Hearing?
    private var engine: AVAudioEngine?
    private var engineStarting = false
    private var configObserver: NSObjectProtocol?
    private var pieces = 0

    /// How long the hub has to welcome the link: its Mac is asked first, as for an offer.
    private static let welcomeTimeout = 20.0
    /// The link pings the hub this often, and gives up when it hasn't answered for this long.
    private static let pingEvery = 15.0
    private static let silentFor = 45.0

    init() {
        let (stream, continuation) = AsyncStream<String>.makeStream()
        outgoing = stream
        outbox = Outbox(continuation: continuation)
        speaking.outbox = outbox
        speaking.onFailed = failure
    }

    /// Fails the link, from any thread.
    private var failure: @Sendable (String) -> Void {
        let me = Weak(self)
        return { reason in DispatchQueue.main.async { me.value?.fail(reason) } }
    }

    /// Opens the link to the hub and says hello, as an offer would (`offer`), with your
    /// interruption sensitivity and background filter and whether the phone still rings, and
    /// returns whether it did. Meanwhile it readies Apple's speech recognition. Returns once the
    /// hub welcomes the call, and throws `RefusedCall` if it refuses it (403), `NoSpeech` for a
    /// 503, or the link's error.
    @MainActor
    func start(hub: URL, offer: CallOffer) async throws -> Bool {
        let sensitivity = UserDefaults.standard.object(forKey: AudioLink.sensitivityKey) as? Double ?? AudioLink.defaultSensitivity
        let filter = AudioLink.backgroundFilter
        let thresholds = LevelDetector.Thresholds(sensitivity: sensitivity, backgroundFilter: filter)
        preparing = Task { [outbox, speaking, failure] in
            try await Hearing(thresholds: thresholds, outbox: outbox, voicePlaying: { speaking.isPlaying }, failed: failure)
        }
        guard var parts = URLComponents(url: hub.appending(path: "phone-speech"), resolvingAgainstBaseURL: false)
        else { throw URLError(.badURL) }
        parts.scheme = parts.scheme == "https" ? "wss" : "ws"
        guard let url = parts.url else { throw URLError(.badURL) }
        // No timeout of its own while the call is quiet: the welcome has one, and a hub that
        // stops answering is noticed by the pings (`ping`).
        let task = URLSession.shared.webSocketTask(with: URLRequest(url: url, timeoutInterval: 24 * 3600))
        self.task = task
        task.resume()

        let ringing = ringing
        helloRinging = ringing
        var hello: [String: Any] = [
            "version": 1, "call_id": offer.callID, "kind": offer.kind, "sensitivity": sensitivity,
            "background_filter": filter,
        ]
        if ringing { hello["ringing"] = true }
        hello["session_id"] = offer.session
        try await task.send(.string(Outbox.encode("hello", hello)))
        let reply = try await Self.receive(task, within: Self.welcomeTimeout)
        guard !stopped else { throw CancellationError() }
        switch reply["type"] as? String {
        case "welcome":
            break
        case "refused":
            let status = reply["status"] as? Int ?? 0
            let error = reply["error"] as? String
            if status == 403 { throw RefusedCall(reason: error) }
            if status == 503 { throw NoSpeech() }
            throw SpeechError("The hub refused the call: \(error ?? "status \(status)")")
        default:
            throw URLError(.badServerResponse)
        }
        welcomed = true
        let at = Date.now
        sender = Task { [outgoing] in
            for await text in outgoing {
                guard (try? await task.send(.string(text))) != nil else { return }
            }
        }
        receiver = Task { @MainActor [weak self] in
            while true {
                do {
                    let message = try await task.receive()
                    self?.received(Self.fields(message) ?? [:])
                } catch {
                    self?.closed(error)
                    return
                }
            }
        }
        lastPong = .now
        pinger = Task { @MainActor [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(Self.pingEvery))
                self?.ping()
            }
        }
        DispatchQueue.main.async { [self] in
            onConnected?(at)
            onConnected = nil
        }
        startEngineIfReady()
        return ringing
    }

    func setMuted(_ muted: Bool) {
        self.muted = muted
        hearing?.setMuted(muted)
    }

    func stop() {
        guard !stopped else { return }
        stopped = true
        onConnected = nil
        onFailed = nil
        onEnded = nil
        pinger?.cancel()
        receiver?.cancel()
        sender?.cancel()
        outbox.finish()
        task?.cancel(with: .normalClosure, reason: nil)
        stopEngine()
        speaking.stop()
        hearing?.stop()
        // A recogniser still getting ready stops once it is.
        if hearing == nil, let preparing {
            Task { try? await preparing.value.stop() }
        }
        preparing?.cancel()
    }

    /// Stops, and gives `done` what the call's speech did, for the call's report.
    func stop(reporting done: @escaping ([String: String]) -> Void) {
        var stats = ["speech": "on the phone", "pieces": "\(pieces)", "voice": speaking.voiceName]
        if let hearing { stats["turns"] = "\(hearing.turns)" }
        stop()
        done(stats)
    }

    // MARK: The link

    /// What the hub says, once it has welcomed the link.
    @MainActor
    private func received(_ message: [String: Any]) {
        guard !stopped else { return }
        switch message["type"] as? String {
        case "hear":
            listening = true
            hearing?.setListening(true)
        case "speak":
            guard let piece = message["piece"] as? Int else { return }
            pieces += 1
            speaking.speak(piece: piece, text: message["text"] as? String ?? "")
        case "speak_cancel":
            speaking.cancel()
        case "end":
            endedByHub = true
            onEnded?()
            onEnded = nil
        default:
            break
        }
    }

    /// The link closed: after the hub's `end` that's the call over; otherwise it failed.
    @MainActor
    private func closed(_ error: Error) {
        guard !stopped, !endedByHub else { return }
        fail("The link to the hub closed: \(error.localizedDescription)")
    }

    private func answered() {
        guard helloRinging, !answerSent, !stopped else { return }
        answerSent = true
        outbox.send("answered")  // goes once the hub has welcomed the link
        startEngineIfReady()
    }

    /// Pings the hub, and fails once it hasn't answered for a while, as a link silent for 45 s is
    /// closed (docs/protocol.md).
    @MainActor
    private func ping() {
        guard let task, !stopped else { return }
        if Date.now.timeIntervalSince(lastPong) > Self.silentFor {
            return fail("The hub stopped answering")
        }
        let me = Weak(self)
        task.sendPing { error in
            guard error == nil else { return }
            DispatchQueue.main.async { me.value?.lastPong = .now }
        }
    }

    private func fail(_ reason: String) {
        guard !stopped, let onFailed else { return }
        self.onFailed = nil
        onFailed(reason)
    }

    /// The first message, within `seconds`.
    private static func receive(_ task: URLSessionWebSocketTask, within seconds: Double) async throws -> [String: Any] {
        let deadline = Date.now + seconds
        let timeout = Task {
            try await Task.sleep(for: .seconds(seconds))
            task.cancel(with: .goingAway, reason: nil)
        }
        defer { timeout.cancel() }
        do {
            guard let fields = fields(try await task.receive()) else { throw URLError(.cannotParseResponse) }
            return fields
        } catch {
            if Date.now >= deadline { throw URLError(.timedOut) }
            throw error
        }
    }

    private static func fields(_ message: URLSessionWebSocketTask.Message) -> [String: Any]? {
        let data: Data
        switch message {
        case .string(let text): data = Data(text.utf8)
        case .data(let bytes): data = bytes
        @unknown default: return nil
        }
        return (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
    }

    // MARK: The audio engine

    /// Starts the engine once the hub has welcomed the link, you've answered, and CallKit has
    /// activated the audio session, with the recogniser ready.
    private func startEngineIfReady() {
        guard welcomed, audioActive, !ringing, !stopped, engine == nil, !engineStarting, let preparing else { return }
        engineStarting = true
        Task { @MainActor [self] in
            defer { engineStarting = false }
            do {
                let hearing = try await preparing.value
                guard !stopped else { return }
                self.hearing = hearing
                hearing.setMuted(muted)
                hearing.setListening(listening)
                guard audioActive, !ringing, engine == nil else { return }
                try startEngine(hearing)
            } catch {
                fail("Can't start hearing on the phone: \(error.localizedDescription)")
            }
        }
    }

    /// Voice processing on the input (echo cancellation) puts the output through it too, so the
    /// voice, played through the same engine, is what it cancels.
    private func startEngine(_ hearing: Hearing) throws {
        let engine = AVAudioEngine()
        try engine.inputNode.setVoiceProcessingEnabled(true)
        engine.attach(speaking.player)
        engine.connect(speaking.player, to: engine.mainMixerNode, format: speaking.format)
        do {
            try tap(engine, hearing)
            engine.prepare()
            try engine.start()
        } catch {
            // The player can be in only one engine.
            engine.inputNode.removeTap(onBus: 0)
            engine.detach(speaking.player)
            throw error
        }
        self.engine = engine
        speaking.started()
        // A new route, such as headphones put in, stops the engine and may change its formats.
        configObserver = NotificationCenter.default.addObserver(
            forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main
        ) { [weak self] _ in self?.restartEngine() }
    }

    private func tap(_ engine: AVAudioEngine, _ hearing: Hearing) throws {
        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0, format.channelCount > 0 else { throw SpeechError("the microphone isn't available") }
        try hearing.attach(format)
        input.installTap(onBus: 0, bufferSize: 1024, format: format) { buffer, _ in hearing.heard(buffer) }
    }

    /// After a change of route the voice starts again with its next piece: what was playing
    /// counts as heard.
    private func restartEngine() {
        guard let engine, let hearing, !stopped else { return }
        speaking.halted()
        engine.inputNode.removeTap(onBus: 0)
        do {
            try tap(engine, hearing)
            try engine.start()
            speaking.started()
        } catch {
            fail("The phone's audio stopped: \(error.localizedDescription)")
        }
    }

    private func stopEngine() {
        if let configObserver { NotificationCenter.default.removeObserver(configObserver) }
        configObserver = nil
        guard let engine else { return }
        self.engine = nil
        speaking.halted()
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        engine.detach(speaking.player)
    }
}

/// One call's hearing: the microphone after echo cancellation, through SpeechTranscriber set up
/// as the speech trial's live run set it up, and the level detector, which says when you start
/// and stop speaking, and when your turn ends. The analyzer then finalizes your words through
/// the end of your speech (it finds no end itself), and the turn goes to the hub with them.
private final class Hearing: @unchecked Sendable {  // its shared state is under `lock`
    private let analyzer: SpeechAnalyzer
    private let transcriber: SpeechTranscriber
    private let feed: AsyncStream<AnalyzerInput>.Continuation
    private let rate: Double
    private let format: AVAudioFormat
    private let outbox: Outbox
    private let voicePlaying: @Sendable () -> Bool
    private let failed: @Sendable (String) -> Void
    private var results: Task<Void, Never>?

    private let lock = NSLock()
    private var converter: Converter?
    private var detector: LevelDetector
    private var listening = false
    private var muted = false
    private var stopped = false
    /// Audio given to the analyzer, in its frames.
    private var frames: Int64 = 0
    /// Final words not yet in a turn, with where they are in the audio.
    private var finals: [(text: String, start: Double, end: Double)] = []
    /// How far the analyzer's results are final.
    private var finalizedThrough = 0.0
    /// The last turn being finished; the next waits for it.
    private var finishing: Task<Void, Never>?
    private var turnCount = 0

    var turns: Int { lock.withLock { turnCount } }

    init(
        thresholds: LevelDetector.Thresholds, outbox: Outbox, voicePlaying: @escaping @Sendable () -> Bool,
        failed: @escaping @Sendable (String) -> Void
    ) async throws {
        self.outbox = outbox
        self.voicePlaying = voicePlaying
        self.failed = failed
        detector = LevelDetector(thresholds)
        guard let locale = await SpeechTranscriber.supportedLocale(equivalentTo: OnDeviceVoice.locale) else {
            throw SpeechError("Apple has no English speech model for this iPhone")
        }
        transcriber = SpeechTranscriber(
            locale: locale, transcriptionOptions: [], reportingOptions: [.volatileResults], attributeOptions: [.audioTimeRange])
        guard let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber]) else {
            throw SpeechError("Apple's English speech model isn't installed")
        }
        self.format = format
        rate = format.sampleRate
        analyzer = SpeechAnalyzer(modules: [transcriber], options: .init(priority: .userInitiated, modelRetention: .lingering))
        try await analyzer.prepareToAnalyze(in: format)
        let (stream, feed) = AsyncStream<AnalyzerInput>.makeStream()
        self.feed = feed
        try await analyzer.start(inputSequence: stream)
        results = Task { [weak self] in await self?.readResults() }
    }

    /// The engine's input format, at the start and after it changes.
    func attach(_ input: AVAudioFormat) throws {
        let converter = try Converter(from: input, to: format)
        lock.withLock { self.converter = converter }
    }

    func setListening(_ on: Bool) { lock.withLock { listening = on } }
    func setMuted(_ on: Bool) { lock.withLock { muted = on } }

    /// One buffer from the microphone, on the audio thread: to the analyzer, stamped with its
    /// place in the audio (silence while muted), and through the level detector.
    func heard(_ buffer: AVAudioPCMBuffer) {
        let level = Self.level(buffer)
        let playing = voicePlaying()
        let duration = Double(buffer.frameLength) / buffer.format.sampleRate
        lock.lock()
        defer { lock.unlock() }
        guard listening, !stopped, let converter, let converted = try? converter.convert(buffer) else { return }
        if muted { Self.silence(converted) }
        feed.yield(AnalyzerInput(buffer: converted, bufferStartTime: CMTime(value: frames, timescale: CMTimeScale(rate))))
        frames += Int64(converted.frameLength)
        let end = Double(frames) / rate
        for event in detector.feed(level: muted ? nil : level, duration: duration, end: end, voicePlaying: playing) {
            switch event {
            case .started: outbox.send("speech_started")
            case .stopped: outbox.send("speech_stopped")
            case .turn(let start, let end): finishTurn(start: start, end: end)
            }
        }
    }

    /// The final words of a turn as one text. A result can start with the punctuation that ends
    /// the one before it (". What do you think"), which goes onto that one, or at the start of
    /// the turn is dropped.
    static func joined(_ words: [String]) -> String {
        var text = ""
        for piece in words {
            let piece = piece.trimmingCharacters(in: .whitespaces)
            let lead = piece.prefix { ".,;:!?".contains($0) }
            let rest = piece.dropFirst(lead.count).trimmingCharacters(in: .whitespaces)
            if !text.isEmpty { text += lead }
            if !rest.isEmpty { text += (text.isEmpty ? "" : " ") + rest }
        }
        return text
    }

    /// Your turn ended: asks the analyzer for the final words through the end of your speech,
    /// waits for them, and sends them, "" if none came. Under `lock`.
    private func finishTurn(start: Double, end: Double) {
        let decided = Date.now
        let through = end + 0.2
        let previous = finishing
        finishing = Task { [self, analyzer, rate] in
            await previous?.value
            try? await analyzer.finalize(through: CMTime(seconds: through, preferredTimescale: CMTimeScale(rate)))
            // The final words come through the results a moment after (the trial: 0.15 s after
            // they're asked for, 0.25 s at most); no words, no results.
            let deadline = Date.now + 0.3
            while Date.now < deadline, lock.withLock({ finalizedThrough }) < through - 0.05 {
                try? await Task.sleep(for: .milliseconds(20))
            }
            let text = lock.withLock {
                // Words from before the turn (someone else, or the voice's echo) are dropped.
                let words = finals.filter { $0.start < through && $0.end > start - 0.3 }.map(\.text)
                finals.removeAll { $0.start < through }
                turnCount += 1
                return Self.joined(words)
            }
            outbox.send(
                "turn",
                ["text": text, "secs": Outbox.secs(end - start), "took": Outbox.secs(Date.now.timeIntervalSince(decided))])
        }
    }

    private func readResults() async {
        do {
            for try await result in transcriber.results {
                let finalized = result.resultsFinalizationTime
                lock.withLock {
                    if finalized.isNumeric { finalizedThrough = max(finalizedThrough, finalized.seconds) }
                    if result.isFinal {
                        let text = String(result.text.characters)
                        finals.append((text, result.range.start.seconds, CMTimeRangeGetEnd(result.range).seconds))
                    }
                }
            }
        } catch {
            if !lock.withLock({ stopped }) { failed("Speech recognition stopped: \(error.localizedDescription)") }
        }
    }

    func stop() {
        let first = lock.withLock {
            defer { stopped = true }
            return !stopped
        }
        guard first else { return }
        feed.finish()
        results?.cancel()
        lock.withLock { finishing?.cancel() }
        Task { [analyzer] in await analyzer.cancelAndFinishNow() }
    }

    /// The buffer's level, in dB below full scale, from its first channel.
    private static func level(_ buffer: AVAudioPCMBuffer) -> Double {
        guard let samples = buffer.floatChannelData?[0], buffer.frameLength > 0 else { return -120 }
        var sum: Float = 0
        for i in 0..<Int(buffer.frameLength) { sum += samples[i] * samples[i] }
        return 10 * log10(Double(max(sum / Float(buffer.frameLength), 1e-12)))
    }

    private static func silence(_ buffer: AVAudioPCMBuffer) {
        for part in UnsafeMutableAudioBufferListPointer(buffer.mutableAudioBufferList) {
            if let data = part.mData { memset(data, 0, Int(part.mDataByteSize)) }
        }
    }
}

/// One call's voice: each piece the hub asks for, made by the best English voice on the phone
/// (AVSpeechSynthesizer's `write`, one piece at a time) and played, one after another, through
/// the call's audio engine. It tells the hub when each piece starts and finishes playing, and
/// how long it is once it's all made. Pieces wait to be made until the engine runs.
private final class Speaking: NSObject, AVSpeechSynthesizerDelegate, @unchecked Sendable {  // its shared state is under `lock`
    let player = AVAudioPlayerNode()
    /// The player's format: the voice's rate, mono.
    let format: AVAudioFormat
    var outbox: Outbox?
    /// Called from any thread when a piece can't be made.
    var onFailed: (@Sendable (String) -> Void)?
    private let voice: AVSpeechSynthesisVoice?
    private let synthesizer = AVSpeechSynthesizer()
    /// Schedules the player's buffers, and stops it, in the order asked.
    private let scheduling = DispatchQueue(label: "phone-speech.player")

    /// A piece being made or played.
    private struct Piece {
        let number: Int
        var scheduled = 0
        var played = 0
        var made = false
        var started = false
    }

    /// The piece being made.
    private struct Making {
        let number: Int
        let utterance: AVSpeechUtterance
        let began: Date
        var converter: Converter?
        var frames: AVAudioFrameCount = 0
    }

    private let lock = NSLock()
    /// Counts cancels and stops of the engine, so what was asked or scheduled before one is
    /// ignored.
    private var generation = 0
    private var running = false
    private var stopped = false
    /// The pieces waiting to be made, in order.
    private var toMake: [(number: Int, text: String)] = []
    private var making: Making?
    /// The pieces made or being made that haven't finished playing, in order.
    private var queue: [Piece] = []
    private var lastPlayed: Date?

    override init() {
        voice = Self.bestVoice()
        let rate = (voice?.audioFileSettings[AVSampleRateKey] as? Double) ?? 22050
        format = AVAudioFormat(standardFormatWithSampleRate: rate, channels: 1)
            ?? AVAudioFormat(standardFormatWithSampleRate: 22050, channels: 1)!
        super.init()
        synthesizer.delegate = self
        synthesizer.usesApplicationAudioSession = true
    }

    var voiceName: String {
        guard let voice else { return "none" }
        let quality = switch voice.quality {
        case .premium: "premium"
        case .enhanced: "enhanced"
        default: "default"
        }
        return "\(voice.name), \(voice.language), \(quality)"
    }

    /// The voice is playing, or just stopped, so its echo may still be heard.
    var isPlaying: Bool {
        lock.withLock {
            queue.first?.started == true || lastPlayed.map { Date.now.timeIntervalSince($0) < 0.3 } == true
        }
    }

    /// The best English voice on the phone: premium, then enhanced, then the default quality;
    /// American English first among equals. Novelty voices and your Personal Voice are left out.
    static func bestVoice() -> AVSpeechSynthesisVoice? {
        let english = AVSpeechSynthesisVoice.speechVoices().filter {
            $0.language.hasPrefix("en") && !$0.voiceTraits.contains(.isNoveltyVoice)
                && !$0.voiceTraits.contains(.isPersonalVoice)
        }
        func rank(_ voice: AVSpeechSynthesisVoice) -> Int { voice.quality.rawValue * 2 + (voice.language == "en-US" ? 1 : 0) }
        return english.max { rank($0) < rank($1) } ?? AVSpeechSynthesisVoice(language: "en-US")
    }

    // On the main queue.

    func speak(piece: Int, text: String) {
        lock.withLock { toMake.append((piece, text)) }
        makeNext()
    }

    /// Stops at once and drops every piece not yet played (`speak_cancel`).
    func cancel() {
        lock.withLock {
            generation += 1
            toMake.removeAll()
            making = nil
            queue.removeAll()
        }
        synthesizer.stopSpeaking(at: .immediate)
        onPlayer { player in
            player.stop()
            player.play()
        }
    }

    /// The engine runs: play, and make the pieces that wait.
    func started() {
        lock.withLock { running = true }
        onPlayer { $0.play() }
        makeNext()
    }

    /// The engine is stopping (the audio session went, or the call ended): the pieces made so far
    /// count as played, no more are made until it runs again, and once this returns the player
    /// isn't touched, so it can be detached.
    func halted() {
        lock.withLock {
            running = false
            generation += 1
            making = nil
            for i in queue.indices {
                queue[i].played = queue[i].scheduled
                queue[i].made = true
            }
            advance()
        }
        synthesizer.stopSpeaking(at: .immediate)
        scheduling.sync {}
    }

    /// Runs `work` on the player, in order with its buffers, only while the engine runs: a
    /// player whose engine isn't running throws when it's told to play.
    private func onPlayer(_ work: @escaping @Sendable (AVAudioPlayerNode) -> Void) {
        scheduling.async { [self, player] in
            guard lock.withLock({ running }) else { return }
            work(player)
        }
    }

    func stop() {
        lock.withLock {
            stopped = true
            generation += 1
            toMake.removeAll()
            making = nil
            queue.removeAll()
        }
        synthesizer.stopSpeaking(at: .immediate)
        synthesizer.delegate = nil
    }

    private func makeNext() {
        let next: (number: Int, text: String, generation: Int, utterance: AVSpeechUtterance)? = lock.withLock {
            guard running, !stopped, making == nil, !toMake.isEmpty else { return nil }
            let next = toMake.removeFirst()
            let utterance = AVSpeechUtterance(string: next.text)
            utterance.voice = voice
            making = Making(number: next.number, utterance: utterance, began: .now)
            queue.append(Piece(number: next.number))
            return (next.number, next.text, generation, utterance)
        }
        guard let next else { return }
        let (piece, generation) = (next.number, next.generation)
        synthesizer.write(next.utterance) { [weak self] buffer in
            self?.made(buffer, piece: piece, generation: generation)
        }
    }

    // On the synthesizer's queue.

    private func made(_ buffer: AVAudioBuffer, piece: Int, generation: Int) {
        lock.lock()
        defer { lock.unlock() }
        guard generation == self.generation, var making, making.number == piece else { return }
        guard let pcm = buffer as? AVAudioPCMBuffer, pcm.frameLength > 0 else {
            return finishMaking()  // the empty buffer at the end
        }
        do {
            if making.converter == nil { making.converter = try Converter(from: pcm.format, to: format) }
            let converted = try making.converter!.convert(pcm)
            making.frames += converted.frameLength
            self.making = making
            schedule(converted, piece: piece)
        } catch {
            self.making = making
            onFailed?("The phone's voice failed: \(error.localizedDescription)")
        }
    }

    /// The synthesizer finished the piece. Its empty last buffer usually says so first; this
    /// is in case it doesn't, a moment later, so no buffer that's still coming is cut off.
    func speechSynthesizer(_ synthesizer: AVSpeechSynthesizer, didFinish utterance: AVSpeechUtterance) {
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) { [self] in
            lock.withLock {
                if making?.utterance === utterance { finishMaking() }
            }
        }
    }

    /// All of the piece being made is made: tells the hub how long it is, and makes the next.
    /// Under `lock`.
    private func finishMaking() {
        guard let done = making else { return }
        making = nil
        if let i = queue.firstIndex(where: { $0.number == done.number }) { queue[i].made = true }
        outbox?.send(
            "spoken",
            ["piece": done.number, "secs": Outbox.secs(Double(done.frames) / format.sampleRate),
             "took": Outbox.secs(Date.now.timeIntervalSince(done.began))])
        advance()
        DispatchQueue.main.async { [self] in makeNext() }
    }

    /// Under `lock`.
    private func schedule(_ buffer: AVAudioPCMBuffer, piece: Int) {
        guard let i = queue.firstIndex(where: { $0.number == piece }) else { return }
        queue[i].scheduled += 1
        let generation = generation
        scheduling.async { [self, player] in
            guard lock.withLock({ running && generation == self.generation }) else { return }
            player.scheduleBuffer(buffer, completionCallbackType: .dataPlayedBack) { [weak self] _ in
                self?.playedBack(piece: piece, generation: generation)
            }
        }
        advance()
    }

    // On the audio engine's thread.

    private func playedBack(piece: Int, generation: Int) {
        lock.withLock {
            guard generation == self.generation, let i = queue.firstIndex(where: { $0.number == piece })
            else { return }
            queue[i].played += 1
            advance()
        }
    }

    /// Tells the hub when the first piece starts playing, and when it has played to its end,
    /// then the next. Under `lock`.
    private func advance() {
        while let head = queue.first {
            if !head.started {
                guard head.scheduled > 0 || head.made else { return }
                queue[0].started = true
                outbox?.send("playing", ["piece": head.number])
            }
            guard queue[0].made, queue[0].played >= queue[0].scheduled else { return }
            outbox?.send("played", ["piece": head.number])
            queue.removeFirst()
            lastPlayed = .now
        }
    }
}

extension OnDeviceVoice {
    /// The voice the phone speaks with: the best English voice installed.
    static func bestVoice() -> AVSpeechSynthesisVoice? { Speaking.bestVoice() }
}

