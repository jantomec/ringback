import Foundation
import Observation

/// One call as the phone saw it.
struct CallRecord: Codable, Identifiable {
    enum Outcome: String, Codable {
        case ringing, onCall, answered, declined, missed, failed
        /// Only in history from the first version, which ended calls itself during another call.
        case busy
    }

    let id: String  // the switchboard's call ID; a call back's own, made on the phone
    let project: String
    /// A few words on what the call is about, written on the calling Mac.
    var subject: String?
    /// When it rang, or when you called back.
    let ringAt: Date
    var answeredAt: Date?
    var endedAt: Date?
    var outcome: Outcome
    /// It rang as call waiting during another call (phone, WhatsApp, FaceTime…).
    var onAnotherCall: Bool?
    /// The voice server the push named, which a call back goes to. Calls from before calling
    /// back have none, so they can't be called back.
    var voice: URL?
    /// On a call you made by calling one back: the ID of the call it calls back, which its
    /// offer carries.
    var callsBack: String?
    /// You called it from the waiting calls, while it was held outside your work hours. It
    /// keeps the call's own ID.
    var calledNow: Bool?
    /// You called its session from Sessions. Its ID is the phone's own, which its offer carries.
    var contact: Bool?
    /// The session that called, or that you called: its ID, from the push, the waiting calls or
    /// Sessions. Calls from a switchboard or voice server before the Sessions tab have none.
    var session: String?

    var callerName: String { project }

    /// A call you made: a call back, a waiting call you called now, or a call from Sessions.
    var isOutgoing: Bool { callsBack != nil || calledNow == true || contact == true }

    /// Whether it's red in the history: declined, missed, couldn't ring, or a call you made
    /// that didn't connect.
    var isMissed: Bool { [.declined, .missed, .busy, .failed].contains(outcome) }

    var duration: TimeInterval? {
        guard let answeredAt, let endedAt else { return nil }
        return endedAt.timeIntervalSince(answeredAt)
    }
}

/// A call that waits to ring: held outside your work hours. The switchboard keeps them, and
/// the app fetches them; tapping one calls it now. One with a question can send it to the
/// Claude app instead.
struct WaitingCall: Identifiable, Equatable {
    let id: String  // the call's ID, which its offer carries
    let project: String
    let subject: String?
    /// The voice server of the Mac whose session placed it.
    let voice: URL
    /// When it rings, once your work hours start.
    let heldUntil: Date
    /// The session asks you a question on it, and waits for the answer.
    var question = false
    /// The session that placed it, if the switchboard says.
    var session: String?

    var callerName: String { project }
}

/// A session you can call: one with ringback on, on any of your Macs, as the switchboard lists
/// them for the Sessions tab. Its voice server takes the call.
struct Contact: Identifiable, Equatable {
    var id: String { session }
    let session: String
    let project: String
    let voice: URL
    /// What its latest briefing was about, and when the session wrote it, if it has one.
    let subject: String?
    let briefedAt: Date?
    /// Since when ringback has been on in it, as its voice server saw.
    let since: Date?
}

/// The call you're on, while it lasts; the in-call screen shows it.
struct ActiveCall: Equatable {
    enum Audio: Equatable {
        case none, connecting, connected
        case failed(String)
    }

    let uuid: UUID
    let id: String
    let callerName: String
    let subject: String?
    let answeredAt: Date
    var audio: Audio
    var muted = false
}

/// Whether calls can reach this phone: it has a push token and the switchboard has it.
enum Connection: Equatable {
    case notSetUp
    case waitingForToken
    case connecting
    case connected(at: Date)
    case failed(reason: String)

    var failed: Bool {
        if case .failed = self { true } else { false }
    }
}

/// What the app's screens show. Changed only on the main queue (PushKit and CallKit both
/// deliver there).
@Observable
final class AppState {
    /// The whole call history, kept on the phone. (Moving it to iCloud comes later.)
    private static let historyFile = URL.applicationSupportDirectory.appending(path: "calls.json")
    /// Where the first version kept its last 20 calls; imported once.
    private static let oldDefaultsKey = "recentCalls"
    private static let lastConnectedKey = "lastConnected"

    var token: String?
    /// For background pushes that start a connection check; nil until iOS gives it.
    var notificationToken: String?
    var connection: Connection = .notSetUp
    var activeCall: ActiveCall?
    /// Why a call you made ended by itself, a call back or a waiting call you called, shown
    /// once the in-call screen is gone; or why a waiting call's question didn't go to the
    /// Claude app.
    var callBackProblem: String?
    /// The calls that wait to ring, soonest first, as the switchboard last gave them.
    var waiting: [WaitingCall] = []
    /// The sessions you can call, by project, as the switchboard last gave them.
    var contacts: [Contact] = []
    /// Microphone access is off, so Claude can't hear you on calls.
    var microphoneOff = false
    /// Counts successful checks you started yourself; the haptic tap plays on each.
    var confirmations = 0
    /// The last time the switchboard confirmed this phone, kept across launches.
    var lastConnected: Date? = UserDefaults.standard.object(forKey: lastConnectedKey) as? Date {
        didSet { UserDefaults.standard.set(lastConnected, forKey: Self.lastConnectedKey) }
    }
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

    /// Fetches the calls that wait from the switchboard. If it can't be reached, the list stays
    /// as it was.
    @MainActor
    func refreshWaiting() async {
        guard Switchboard.url != nil, let token else { return }
        if let calls = try? await Switchboard.waiting(token: token), calls != waiting { waiting = calls }
    }

    /// Fetches the sessions you can call from the switchboard. If it can't be reached, or is
    /// from before the Sessions tab, the list stays as it was.
    @MainActor
    func refreshContacts() async {
        guard Switchboard.url != nil, let token else { return }
        if let found = try? await Switchboard.contacts(token: token), found != contacts { contacts = found }
    }

    /// The session a call in the history is from, if it's in Sessions now: by its ID, or for a
    /// call from before the Sessions tab, the only session of that project on that Mac.
    func contact(for call: CallRecord) -> Contact? {
        if let session = call.session { return contacts.first { $0.session == session } }
        let same = contacts.filter { $0.project == call.project && (call.voice == nil || $0.voice == call.voice) }
        return same.count == 1 ? same[0] : nil
    }

    #if DEBUG
    /// Sample calls for screenshots (Seed), kept in memory only.
    func seed(_ calls: [CallRecord]) {
        self.calls = calls
    }
    #endif

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
