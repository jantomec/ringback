import Foundation
import Observation

/// One call as the phone saw it.
struct CallRecord: Codable, Identifiable {
    enum Outcome: String, Codable {
        case ringing, onCall, answered, declined, missed, failed
        /// Only in history from the first version, which ended calls itself during another call.
        case busy
    }

    let id: String  // the switchboard's call ID
    let project: String
    /// A few words on what the call is about, written on the calling Mac.
    var subject: String?
    let ringAt: Date
    var answeredAt: Date?
    var endedAt: Date?
    var outcome: Outcome
    /// It rang as call waiting during another call (phone, WhatsApp, FaceTime…).
    var onAnotherCall: Bool?

    var callerName: String { "Claude from \(project)" }

    var duration: TimeInterval? {
        guard let answeredAt, let endedAt else { return nil }
        return endedAt.timeIntervalSince(answeredAt)
    }
}

/// What the settings screen shows. Changed only on the main queue (PushKit and CallKit both
/// deliver there).
@Observable
final class AppState {
    /// The whole call history, kept on the phone. (Moving it to iCloud comes later.)
    private static let historyFile = URL.applicationSupportDirectory.appending(path: "calls.json")
    /// Where the first version kept its last 20 calls; imported once.
    private static let oldDefaultsKey = "recentCalls"

    var token: String?
    var registration = "Not registered yet"
    var registering = false
    /// Most recent first.
    private(set) var calls: [CallRecord] = AppState.loadHistory()

    func add(_ call: CallRecord) {
        calls.insert(call, at: 0)
        save()
    }

    func update(_ id: String, _ change: (inout CallRecord) -> Void) {
        guard let i = calls.firstIndex(where: { $0.id == id }) else { return }
        change(&calls[i])
        save()
    }

    private func save() {
        try? FileManager.default.createDirectory(at: .applicationSupportDirectory, withIntermediateDirectories: true)
        try? JSONEncoder().encode(calls).write(to: Self.historyFile, options: .atomic)
    }

    private static func loadHistory() -> [CallRecord] {
        if let data = try? Data(contentsOf: historyFile) {
            return (try? JSONDecoder().decode([CallRecord].self, from: data)) ?? []
        }
        guard let old = UserDefaults.standard.data(forKey: oldDefaultsKey) else { return [] }
        let calls = (try? JSONDecoder().decode([CallRecord].self, from: old)) ?? []
        try? FileManager.default.createDirectory(at: .applicationSupportDirectory, withIntermediateDirectories: true)
        if (try? JSONEncoder().encode(calls).write(to: historyFile, options: .atomic)) != nil {
            UserDefaults.standard.removeObject(forKey: oldDefaultsKey)
        }
        return calls
    }
}
