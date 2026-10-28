import Foundation
import Observation

/// One call as the phone saw it.
struct CallRecord: Codable, Identifiable {
    enum Outcome: String, Codable {
        case ringing, onCall, answered, declined, missed, busy, failed
    }

    let id: String  // the switchboard's call ID
    let project: String
    let ringAt: Date
    var answeredAt: Date?
    var endedAt: Date?
    var outcome: Outcome

    var duration: TimeInterval? {
        guard let answeredAt, let endedAt else { return nil }
        return endedAt.timeIntervalSince(answeredAt)
    }
}

/// What the settings screen shows. Changed only on the main queue (PushKit and CallKit both
/// deliver there).
@Observable
final class AppState {
    private static let callsKey = "recentCalls"
    private static let keep = 20

    var token: String?
    var registration = "Not registered yet"
    var registering = false
    /// Most recent first; kept across launches.
    private(set) var calls: [CallRecord] = {
        guard let data = UserDefaults.standard.data(forKey: callsKey) else { return [] }
        return (try? JSONDecoder().decode([CallRecord].self, from: data)) ?? []
    }()

    func add(_ call: CallRecord) {
        calls.insert(call, at: 0)
        calls = Array(calls.prefix(Self.keep))
        save()
    }

    func update(_ id: String, _ change: (inout CallRecord) -> Void) {
        guard let i = calls.firstIndex(where: { $0.id == id }) else { return }
        change(&calls[i])
        save()
    }

    private func save() {
        UserDefaults.standard.set(try? JSONEncoder().encode(calls), forKey: Self.callsKey)
    }
}
