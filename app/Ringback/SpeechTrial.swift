#if DEBUG
import AVFAudio
import CoreMedia
import Foundation
import Speech
import SwiftUI
import UIKit

/// A measured trial of Apple's on-device speech recognition (SpeechAnalyzer with
/// SpeechTranscriber), for hearing you on the phone instead of on a speech node ("Speech on the
/// phone" in docs/design.md; tools/speech-trial). In debug builds only. It transcribes the
/// recordings copied into the app's Documents/stt as tools/stt-eval's transcribe.py does, and
/// runs live during a trial call through CallKit, so with the phone locked too, logging what it
/// hears and when to Documents/trial.
@MainActor @Observable
final class SpeechTrial {
    static let shared = SpeechTrial()
    static let locale = Locale(identifier: "en-US")

    var model = "Not checked yet"
    /// The model's download, while it runs.
    var installing: Double?
    var recordings = "Copy recordings to Documents/stt first (tools/speech-trial)."
    var isTranscribing = false
    var live = LiveStats()
    var isLive = false
    private var run: LiveRun?

    struct LiveStats {
        var buffers = 0
        var locked = false
        var heard = ""  // the latest words, tentative or final
        var finals = 0
        /// From the end of your speech (the last loud audio) to the final words for it.
        var latencies: [Double] = []
        /// From when you started speaking to the first tentative words.
        var firstWords: [Double] = []
        var footprintMB = 0.0
        var battery = ""
        var file = ""
    }

    // MARK: The model

    func checkModel() async {
        guard SpeechTranscriber.isAvailable else {
            model = "SpeechTranscriber isn't available on this phone"
            return
        }
        guard let locale = await SpeechTranscriber.supportedLocale(equivalentTo: Self.locale) else {
            model = "No English among its locales"
            return
        }
        let status = await AssetInventory.status(forModules: [SpeechTranscriber(locale: locale, preset: .transcription)])
        model = "\(locale.identifier): \(status)"
    }

    func installModel() async {
        do {
            guard let locale = await SpeechTranscriber.supportedLocale(equivalentTo: Self.locale) else { return }
            let modules = [SpeechTranscriber(locale: locale, preset: .transcription)]
            if let request = try await AssetInventory.assetInstallationRequest(supporting: modules) {
                installing = 0
                let progress = Task {
                    while !Task.isCancelled {
                        installing = request.progress.fractionCompleted
                        try? await Task.sleep(for: .milliseconds(300))
                    }
                }
                try await request.downloadAndInstall()
                progress.cancel()
            }
            installing = nil
            await checkModel()
        } catch {
            installing = nil
            model = "Install failed: \(error.localizedDescription)"
        }
    }

    // MARK: Recordings

    /// Transcribes each utterance of each recording in Documents/stt/wav, cut where
    /// Documents/stt/segments says (tools/stt-eval's segment.py), each on its own as the call
    /// does, and writes Documents/stt/hyp/apple-speech/<recording>.json for score.py. `secs` is
    /// from the first audio given to the final words, the model loaded.
    func transcribeRecordings() async {
        let stt = URL.documentsDirectory.appending(path: "stt")
        let wavs = ((try? FileManager.default.contentsOfDirectory(at: stt.appending(path: "wav"), includingPropertiesForKeys: nil)) ?? [])
            .filter { $0.pathExtension == "wav" }.sorted { $0.lastPathComponent < $1.lastPathComponent }
        guard !wavs.isEmpty else {
            recordings = "No recordings in Documents/stt/wav."
            return
        }
        guard let locale = await SpeechTranscriber.supportedLocale(equivalentTo: Self.locale) else { return }
        isTranscribing = true
        defer { isTranscribing = false }
        let out = stt.appending(path: "hyp/apple-speech")
        try? FileManager.default.createDirectory(at: out, withIntermediateDirectories: true)
        var lines: [String] = []
        for wav in wavs {
            let name = wav.deletingPathExtension().lastPathComponent
            guard let data = try? Data(contentsOf: stt.appending(path: "segments/\(name).json")),
                let segments = try? JSONSerialization.jsonObject(with: data) as? [[Double]],
                let file = try? AVAudioFile(forReading: wav)
            else {
                lines.append("\(name): no segments, or the file can't be read")
                continue
            }
            var rows: [[String: Any]] = []
            for (i, segment) in segments.enumerated() where segment.count == 2 {
                recordings = "\(name): \(i + 1) of \(segments.count)"
                do {
                    let (text, secs) = try await transcribe(file, from: segment[0], to: segment[1], locale: locale)
                    rows.append(["start": segment[0], "end": segment[1], "text": text, "secs": (secs * 1000).rounded() / 1000])
                } catch {
                    rows.append(["start": segment[0], "end": segment[1], "text": "", "error": error.localizedDescription])
                }
            }
            let result: [String: Any] = [
                "model": "apple-speechtranscriber", "options": ["preset": "transcription", "locale": locale.identifier],
                "utterances": rows,
            ]
            try? JSONSerialization.data(withJSONObject: result, options: [.prettyPrinted, .sortedKeys])
                .write(to: out.appending(path: "\(name).json"))
            let secs = rows.compactMap { $0["secs"] as? Double }.sorted()
            lines.append("\(name): \(rows.count) utterances, median \(secs.isEmpty ? 0 : secs[secs.count / 2]) s")
        }
        recordings = lines.joined(separator: "\n")
    }

    private func transcribe(_ file: AVAudioFile, from start: Double, to end: Double, locale: Locale) async throws -> (String, Double) {
        let rate = file.processingFormat.sampleRate
        let count = AVAudioFrameCount(max(0, end - start) * rate)
        guard count > 0, let clip = AVAudioPCMBuffer(pcmFormat: file.processingFormat, frameCapacity: count) else { return ("", 0) }
        file.framePosition = AVAudioFramePosition(start * rate)
        try file.read(into: clip, frameCount: count)
        let transcriber = SpeechTranscriber(locale: locale, preset: .transcription)
        guard let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber]) else {
            throw TrialError("no audio format: is the model installed?")
        }
        let input = try Converter(from: clip.format, to: format).convert(clip, last: true)
        let analyzer = SpeechAnalyzer(modules: [transcriber], options: .init(priority: .userInitiated, modelRetention: .lingering))
        try await analyzer.prepareToAnalyze(in: format)
        let words = Task {
            var text = ""
            for try await result in transcriber.results where result.isFinal {
                text += String(result.text.characters)
            }
            return text
        }
        let began = ContinuousClock.now
        let (stream, feed) = AsyncStream<AnalyzerInput>.makeStream()
        feed.yield(AnalyzerInput(buffer: input))
        feed.finish()
        try await analyzer.start(inputSequence: stream)
        try await analyzer.finalizeAndFinishThroughEndOfInput()
        let text = try await words.value
        let took = ContinuousClock.now - began
        return (text.trimmingCharacters(in: .whitespacesAndNewlines), Double(took.components.attoseconds) / 1e18 + Double(took.components.seconds))
    }

    // MARK: Live, in a call

    /// The audio session as a call's (AudioLink): set before CallKit activates it.
    nonisolated static func prepareAudioSession() {
        try? AVAudioSession.sharedInstance().setCategory(.playAndRecord, mode: .voiceChat, options: [.allowBluetoothHFP])
    }

    /// CallKit activated the trial call's audio: hear and transcribe until it ends.
    func startLive() async {
        guard run == nil else { return }
        live = LiveStats()
        do {
            guard let locale = await SpeechTranscriber.supportedLocale(equivalentTo: Self.locale) else {
                throw TrialError("no English")
            }
            let run = try await LiveRun(locale: locale) { [weak self] change in
                Task { @MainActor in change(&self!.live) }
            }
            self.run = run
            isLive = true
        } catch {
            live.heard = "Couldn't start: \(error.localizedDescription)"
        }
    }

    func stopLive() async {
        guard let run else { return }
        self.run = nil
        await run.stop()
        isLive = false
    }
}

struct TrialError: LocalizedError {
    let errorDescription: String?
    init(_ text: String) { errorDescription = text }
}

/// Converts audio to the analyzer's format, keeping its state from one buffer to the next.
private final class Converter {
    private let converter: AVAudioConverter
    private let format: AVAudioFormat

    init(from: AVAudioFormat, to: AVAudioFormat) throws {
        guard let converter = AVAudioConverter(from: from, to: to) else { throw TrialError("can't convert \(from) to \(to)") }
        converter.primeMethod = .none
        converter.downmix = true
        self.converter = converter
        format = to
    }

    /// `last`: no more audio follows, so everything left comes out.
    func convert(_ buffer: AVAudioPCMBuffer, last: Bool = false) throws -> AVAudioPCMBuffer {
        let capacity = AVAudioFrameCount(Double(buffer.frameLength) * format.sampleRate / buffer.format.sampleRate) + 1024
        guard let out = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: capacity) else { throw TrialError("no buffer") }
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

/// One trial call's hearing: the microphone with the call's echo cancellation, through
/// SpeechTranscriber with tentative results and time ranges, and a simple level detector that
/// finds when you stop speaking (SpeechDetector reports none), finalizes the words through
/// there, and times how long they take. Everything goes to a log, one JSON line per event.
private final class LiveRun: @unchecked Sendable {  // its shared state is under `lock`
    private let engine = AVAudioEngine()
    private let analyzer: SpeechAnalyzer
    private let transcriber: SpeechTranscriber
    private let feed: AsyncStream<AnalyzerInput>.Continuation
    private let converter: Converter
    private let rate: Double
    private let began = Date()
    private let log: FileHandle
    private let logQueue = DispatchQueue(label: "speech-trial.log")
    private let update: (@escaping (inout SpeechTrial.LiveStats) -> Void) -> Void
    private var results: Task<Void, Never>?
    private var observers: [NSObjectProtocol] = []
    private var ticker: Timer?

    // The audio thread's own: frames given, and the level detector.
    private var frames: Int64 = 0
    private var buffers = 0
    private var noiseFloor = -50.0
    private var loudFor = 0.0
    private var quietFor = 0.0
    private var speaking = false
    private var lastLoud = (audio: 0.0, wall: Date())
    // Shared with the results: the end of speech waiting for its final words.
    private let lock = NSLock()
    private var pending: (audio: Double, wall: Date)?
    private var speechStart: Date?
    private var latencies: [Double] = []
    private var firstWords: [Double] = []
    private var maxFootprint = 0.0
    private let batteryAtStart: Float

    init(locale: Locale, update: @escaping (@escaping (inout SpeechTrial.LiveStats) -> Void) -> Void) async throws {
        self.update = update
        transcriber = SpeechTranscriber(
            locale: locale, transcriptionOptions: [], reportingOptions: [.volatileResults], attributeOptions: [.audioTimeRange])
        guard let format = await SpeechAnalyzer.bestAvailableAudioFormat(compatibleWith: [transcriber]) else {
            throw TrialError("no audio format: is the model installed?")
        }
        rate = format.sampleRate
        analyzer = SpeechAnalyzer(modules: [transcriber], options: .init(priority: .userInitiated, modelRetention: .lingering))
        let folder = URL.documentsDirectory.appending(path: "trial")
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let stamp = Date.now.formatted(.iso8601.year().month().day().time(includingFractionalSeconds: false))
            .replacingOccurrences(of: ":", with: "")
        let url = folder.appending(path: "live-\(stamp).jsonl")
        FileManager.default.createFile(atPath: url.path, contents: nil)
        log = try FileHandle(forWritingTo: url)
        batteryAtStart = await MainActor.run {
            UIDevice.current.isBatteryMonitoringEnabled = true
            return UIDevice.current.batteryLevel
        }

        let prepared = Date()
        try await analyzer.prepareToAnalyze(in: format)
        let (stream, feed) = AsyncStream<AnalyzerInput>.makeStream()
        self.feed = feed
        let input = engine.inputNode
        try input.setVoiceProcessingEnabled(true)
        let inputFormat = input.outputFormat(forBus: 0)
        converter = try Converter(from: inputFormat, to: format)
        write([
            "kind": "start", "locale": locale.identifier, "analyzer_format": "\(format)", "input_format": "\(inputFormat)",
            "prepare_s": Date().timeIntervalSince(prepared), "battery": batteryAtStart,
            "footprint_mb": Self.footprintMB(), "available_mb": Double(os_proc_available_memory()) / 1_048_576,
        ])
        update { $0.file = url.lastPathComponent }
        try await analyzer.start(inputSequence: stream)
        results = Task { [weak self] in await self?.readResults() }
        input.installTap(onBus: 0, bufferSize: 1024, format: inputFormat) { [weak self] buffer, _ in self?.heard(buffer) }
        try engine.start()
        let center = NotificationCenter.default
        observers = [
            center.addObserver(forName: UIApplication.protectedDataWillBecomeUnavailableNotification, object: nil, queue: nil) { [weak self] _ in
                self?.write(["kind": "locked"])
                self?.update { $0.locked = true }
            },
            center.addObserver(forName: UIApplication.protectedDataDidBecomeAvailableNotification, object: nil, queue: nil) { [weak self] _ in
                self?.write(["kind": "unlocked"])
                self?.update { $0.locked = false }
            },
        ]
        // Every 5 s: how many buffers came, and memory; every minute, the battery.
        var ticks = 0
        let timer = Timer(timeInterval: 5, repeats: true) { [weak self] _ in
            guard let self else { return }
            ticks += 1
            let footprint = Self.footprintMB()
            self.lock.withLock { self.maxFootprint = max(self.maxFootprint, footprint) }
            // The timer runs on the main run loop.
            let (background, battery) = MainActor.assumeIsolated {
                (UIApplication.shared.applicationState == .background, UIDevice.current.batteryLevel)
            }
            var line: [String: Any] = [
                "kind": "tick", "buffers": self.buffers, "audio_s": Double(self.frames) / self.rate,
                "footprint_mb": footprint, "available_mb": Double(os_proc_available_memory()) / 1_048_576,
                "background": background,
            ]
            if ticks % 12 == 0 { line["battery"] = battery }
            self.write(line)
            let buffers = self.buffers
            self.update {
                $0.buffers = buffers
                $0.footprintMB = footprint
                $0.battery = "\(Int(self.batteryAtStart * 100))% → \(Int(battery * 100))%"
            }
        }
        RunLoop.main.add(timer, forMode: .common)
        ticker = timer
    }

    /// One buffer from the microphone, on the audio thread: to the analyzer, stamped with its
    /// place in the audio, and through the level detector.
    private func heard(_ buffer: AVAudioPCMBuffer) {
        let now = Date()
        let duration = Double(buffer.frameLength) / buffer.format.sampleRate
        guard let converted = try? converter.convert(buffer) else { return }
        feed.yield(AnalyzerInput(buffer: converted, bufferStartTime: CMTime(value: frames, timescale: CMTimeScale(rate))))
        frames += Int64(converted.frameLength)
        buffers += 1
        let end = Double(frames) / rate

        let level = Self.level(buffer)
        noiseFloor = min(level, noiseFloor + 0.05)  // follows the quiet down at once, up slowly
        if level > max(noiseFloor + 15, -55) {
            loudFor += duration
            quietFor = 0
            lastLoud = (end, now)
            if !speaking && loudFor >= 0.12 {
                speaking = true
                let start = now.addingTimeInterval(-loudFor)
                lock.withLock { speechStart = start }
                write(["kind": "speech_start", "audio": end - loudFor, "level": level, "floor": noiseFloor])
            }
        } else {
            quietFor += duration
            loudFor = 0
            if speaking && quietFor >= 0.6 {
                speaking = false
                let stop = lastLoud
                lock.withLock { pending = stop }
                write(["kind": "speech_end", "audio": stop.audio, "quiet_s": quietFor])
                Task { [analyzer] in
                    try? await analyzer.finalize(through: CMTime(seconds: stop.audio + 0.2, preferredTimescale: CMTimeScale(rate)))
                }
            }
        }
    }

    private func readResults() async {
        do {
            for try await result in transcriber.results {
                let now = Date()
                let text = String(result.text.characters)
                let range = (result.range.start.seconds, CMTimeRangeGetEnd(result.range).seconds)
                var line: [String: Any] = [
                    "kind": result.isFinal ? "final" : "volatile", "text": text, "start": range.0, "end": range.1,
                ]
                lock.withLock {
                    if !result.isFinal, let start = speechStart {
                        firstWords.append(now.timeIntervalSince(start))
                        line["first_words_s"] = now.timeIntervalSince(start)
                        speechStart = nil
                    }
                    if result.isFinal, let end = pending, range.1 >= end.audio - 0.25 {
                        latencies.append(now.timeIntervalSince(end.wall))
                        line["latency_s"] = now.timeIntervalSince(end.wall)
                        pending = nil
                    }
                }
                write(line)
                let (latencies, firstWords) = lock.withLock { (self.latencies, self.firstWords) }
                update {
                    $0.heard = text
                    if result.isFinal { $0.finals += 1 }
                    $0.latencies = latencies
                    $0.firstWords = firstWords
                }
            }
        } catch {
            write(["kind": "error", "text": error.localizedDescription])
            update { $0.heard = "Error: \(error.localizedDescription)" }
        }
    }

    func stop() async {
        ticker?.invalidate()
        observers.forEach(NotificationCenter.default.removeObserver)
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        feed.finish()
        try? await analyzer.finalizeAndFinishThroughEndOfInput()
        await results?.value
        let (latencies, firstWords, footprint) = lock.withLock { (self.latencies, self.firstWords, self.maxFootprint) }
        let battery = await MainActor.run { UIDevice.current.batteryLevel }
        write([
            "kind": "stop", "seconds": Date().timeIntervalSince(began), "buffers": buffers, "latencies": latencies,
            "first_words": firstWords, "max_footprint_mb": footprint, "battery_start": batteryAtStart,
            "battery_end": battery,
        ])
        try? logQueue.sync { try log.close() }
    }

    private func write(_ fields: [String: Any]) {
        var line = fields
        line["t"] = (Date().timeIntervalSince(began) * 1000).rounded() / 1000
        guard let data = try? JSONSerialization.data(withJSONObject: line, options: [.sortedKeys]) else { return }
        logQueue.async { [log] in try? log.write(contentsOf: data + Data("\n".utf8)) }
    }

    /// The buffer's level, in dB below full scale, from its first channel.
    private static func level(_ buffer: AVAudioPCMBuffer) -> Double {
        guard let samples = buffer.floatChannelData?[0], buffer.frameLength > 0 else { return -120 }
        var sum: Float = 0
        for i in 0..<Int(buffer.frameLength) { sum += samples[i] * samples[i] }
        return 10 * log10(Double(max(sum / Float(buffer.frameLength), 1e-12)))
    }

    /// The app's memory as iOS counts it against its limit (the model runs outside it).
    static func footprintMB() -> Double {
        var info = task_vm_info_data_t()
        var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<natural_t>.size)
        let result = withUnsafeMutablePointer(to: &info) {
            $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count) }
        }
        return result == KERN_SUCCESS ? Double(info.phys_footprint) / 1_048_576 : -1
    }
}

/// The trial's screen, from Recents in debug builds.
struct SpeechTrialView: View {
    let calls: CallService
    @State private var trial = SpeechTrial.shared

    var body: some View {
        List {
            Section("Model") {
                Text(trial.model)
                if let progress = trial.installing {
                    ProgressView(value: progress)
                } else {
                    Button("Install English") { Task { await trial.installModel() } }
                    Button("Check Again") { Task { await trial.checkModel() } }
                }
            }
            Section("Recordings") {
                Text(trial.recordings).font(.footnote.monospaced())
                Button(trial.isTranscribing ? "Transcribing…" : "Transcribe Recordings") {
                    Task { await trial.transcribeRecordings() }
                }
                .disabled(trial.isTranscribing)
            }
            Section {
                if trial.isLive {
                    let live = trial.live
                    LabeledContent("Buffers", value: "\(live.buffers)\(live.locked ? " · locked" : "")")
                    LabeledContent("Final results", value: "\(live.finals)")
                    LabeledContent("Final words after you stop", value: Self.summary(live.latencies))
                    LabeledContent("First words after you start", value: Self.summary(live.firstWords))
                    LabeledContent("App's memory", value: String(format: "%.0f MB", live.footprintMB))
                    LabeledContent("Battery", value: live.battery)
                    Text(live.heard).font(.footnote)
                    Button("End Trial Call", role: .destructive) { calls.endTrial() }
                } else {
                    Button("Start Trial Call") { calls.startTrial() }
                    if !trial.live.file.isEmpty {
                        Text("Last log: Documents/trial/\(trial.live.file)").font(.footnote)
                    }
                }
            } header: {
                Text("Live, in a call")
            } footer: {
                Text("Start it, lock the phone, and talk for a few minutes, pausing after each sentence. Then end the call.")
            }
        }
        .navigationTitle("Speech Trial")
        .task { await trial.checkModel() }
    }

    private static func summary(_ values: [Double]) -> String {
        guard !values.isEmpty else { return "–" }
        let sorted = values.sorted()
        return String(format: "%.2f s median, %.2f s max (%d)", sorted[sorted.count / 2], sorted.last!, sorted.count)
    }
}
#endif
