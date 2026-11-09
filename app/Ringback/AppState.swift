import Foundation
import Observation
import UserNotifications

/// One call as the phone saw it.
struct CallRecord: Codable, Identifiable {
    enum Outcome: String, Codable {
        case ringing, onCall, answered, declined, missed, failed
        /// Only in history from the first version, which ended calls itself during another call.
        case busy
    }

    let id: String  // the call's ID, from the push; a call back's own, made on the phone
    let project: String
    /// A few words on what the call is about, written on the calling Mac.
    var subject: String?
    /// When it rang, or when you called back.
    let ringAt: Date
    var answeredAt: Date?
    var endedAt: Date?
    var outcome: Outcome
    /// It rang as call waiting during another call (phone, WhatsApp, FaceTime…).
    var onAnotherCall = false
    /// Where the call's audio goes, as the push named it (the hub), which a call back goes to.
    /// A call rung by hand (`ringback-hub ring`) has none, so it can't be called back.
    var voice: URL?
    /// On a call you made by calling one back: the ID of the call it calls back, which its
    /// offer carries.
    var callsBack: String?
    /// You called it from the waiting calls, while it was held outside your work hours. It
    /// keeps the call's own ID.
    var calledNow = false
    /// You called its session from Sessions. Its ID is the phone's own, which its offer carries.
    var contact = false
    /// The session that called, or that you called: its ID, from the push, the waiting calls or
    /// Sessions. A call rung by hand has none.
    var session: String?

    var callerName: String { project }

    /// A call you made: a call back, a waiting call you called now, or a call from Sessions.
    var isOutgoing: Bool { callsBack != nil || calledNow || contact }

    /// Whether it's red in the history: declined, missed, couldn't ring, or a call you made
    /// that didn't connect.
    var isMissed: Bool { [.declined, .missed, .busy, .failed].contains(outcome) }

    var duration: TimeInterval? {
        guard let answeredAt, let endedAt else { return nil }
        return endedAt.timeIntervalSince(answeredAt)
    }
}

/// A call that waits to ring: held while your phone holds calls (outside your work hours, or
/// paused). The hub keeps them, and the app fetches them; tapping one calls it now. One
/// with a question can send it to the Claude app instead.
struct WaitingCall: Identifiable, Equatable {
    let id: String  // the call's ID, which its offer carries
    let project: String
    let subject: String?
    /// Where its audio goes: the hub, which passes it to the agent whose session placed it.
    let voice: URL
    /// When it rings, once your work hours start; nil if it waits until you take calls.
    let heldUntil: Date?
    /// The session asks you a question on it, and waits for the answer.
    var question = false
    /// The session that placed it, if the hub says.
    var session: String?
    /// Why it waits, when it isn't your work hours or the hold switch: "no speech", the
    /// speech computer isn't linked to the hub, so it can't be talked through; "in turn",
    /// another call rings or is on, or waits before it, as one rings at a time, or, with
    /// heldUntil, the last call you were on ended less than a minute ago; "missed", you
    /// didn't answer it, and it rings again at heldUntil; "call back", you didn't answer its
    /// last try, and it waits until you call its session, as every call does meanwhile.
    var reason: String?

    var callerName: String { project }
    var waitsForSpeech: Bool { reason == "no speech" }

    /// When it rings, in a few words after "Rings" (in a sentence) or as a value: nil after its
    /// last try, as it doesn't ring again.
    func rings(sentence: Bool) -> String? {
        switch reason {
        case "no speech": return sentence ? "when the speech computer is back" : "When the speech computer is back"
        case "in turn":
            if let heldUntil {
                return sentence ? heldUntil.ringsLabel : heldUntil.formatted(date: .omitted, time: .shortened)
            }
            return sentence ? "after the call before it" : "After the call before it"
        case "call back": return nil
        case "missed":
            let at = heldUntil?.formatted(date: .omitted, time: .shortened) ?? ""
            return sentence ? "again \(heldUntil?.ringsLabel ?? "soon")" : "Again at \(at)"
        default:
            if let heldUntil {
                return sentence ? heldUntil.ringsLabel : heldUntil.formatted(date: .omitted, time: .shortened)
            }
            return sentence ? "when you take calls" : "When you take calls"
        }
    }
}

/// What the hub says about what calls need, with the waiting calls; the banners show what's
/// wrong (Banners).
struct HubStatus: Equatable {
    /// Calls can be talked through: a speech computer is linked to the hub, or no call needs one.
    var speechOK = true
    /// Calls waiting for the speech computer.
    var waitingForSpeech = 0
    /// Why the last push to this phone failed: "key" (Apple refused the hub's key, or it has
    /// none), "phone" (Apple no longer knows this phone), "network" (the hub couldn't reach
    /// Apple) or "apple" (anything else); nil if it got through.
    var pushProblem: String?
}

/// A session you can call: one with ringback on, on any of your Macs, as the hub lists
/// them for the Sessions tab. Its Mac's agent takes the call, through the hub.
struct Contact: Identifiable, Equatable {
    var id: String { session }
    let session: String
    let project: String
    let voice: URL
    /// What its latest briefing was about, and when the session wrote it, if it has one.
    let subject: String?
    let briefedAt: Date?
    /// Since when ringback has been on in it, as its agent saw.
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

/// Whether calls can reach this phone: it has a push token and the hub has it. Not paired: the
/// hub answered, but hasn't paired with this phone.
enum Connection: Equatable {
    case notSetUp
    case waitingForToken
    case connecting
    case connected(at: Date)
    case notPaired
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
    private static let lastConnectedKey = "lastConnected"
    /// The count last put on the app's icon.
    private static let badgeKey = "badgeCount"
    private static let seenShownKey = "seenShown"

    var token: String?
    /// For background pushes that start a connection check; nil until iOS gives it.
    var notificationToken: String?
    var connection: Connection = .notSetUp
    /// A pairing link's one-time code, until the hub has taken it (PushService.connect).
    var pairingCode: String?
    /// The hub has to pair this phone: it has no key yet, or the hub said its key isn't one it
    /// knows. Only pairing clears it, so its banner stays while the hub can't be reached.
    var needsPairing = Shared.key == nil
    /// How pairing went, shown once: paired, or why not.
    /// The phone has just paired, for the sheet that says so.
    var justPaired = false
    /// The app's own settings show (SettingsSheet).
    var showsSettings = false
    /// Why a pairing link didn't pair, for an alert.
    var pairingProblem: String?
    var activeCall: ActiveCall? {
        // A new call shows its call screen.
        didSet { if activeCall?.uuid != oldValue?.uuid { callMinimised = false } }
    }
    /// You've minimised the call screen to use the app during the call: a green pill at the top
    /// brings it back (CallOnTop).
    var callMinimised = false
    /// What you opened while the call screen showed: it opens once the call screen is gone.
    var pendingShown: ShownItem?
    /// When you last opened something shown, so coming back to the app to see it doesn't bring
    /// the call screen up over it.
    private(set) var shownOpenedAt = Date.distantPast
    /// Why a call you made ended by itself, a call back or a waiting call you called, shown
    /// once the in-call screen is gone; or why a waiting call's question didn't go to the
    /// Claude app, the hold switch couldn't be set, or what a session showed you can't open.
    var callBackProblem: String?
    /// The calls that wait to ring, soonest first, as the hub last gave them.
    var waiting: [WaitingCall] = []
    /// The hold switch, as the hub last gave it with the waiting calls; nil until then.
    var hold: HoldState?
    /// What the hub last said about what calls need, with the waiting calls; nil until then,
    /// or if its answer had none.
    var status: HubStatus?
    /// The sessions you can call, by project, as the hub last gave them.
    var contacts: [Contact] = []
    /// What sessions showed you, newest first, as the hub last gave them; and the one open.
    var shown: [ShownItem] = []
    var openShown: ShownItem?
    /// Which of them you've opened, kept on the phone while the hub keeps them.
    private(set) var seenShown = Set(UserDefaults.standard.stringArray(forKey: seenShownKey) ?? [])
    /// Microphone access is off, so Claude can't hear you on calls.
    var microphoneOff = false
    /// Why On-device voice can't be used: Apple's English speech model couldn't be installed
    /// (OnDeviceVoice); nil while it's off or ready.
    var onDeviceProblem: String?
    /// Counts successful checks you started yourself; the haptic tap plays on each.
    var confirmations = 0
    /// The last time the hub confirmed this phone, kept across launches.
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

    /// Fetches the calls that wait from the hub, and the hold switch. If it can't be
    /// reached, both stay as they were.
    @MainActor
    func refreshWaiting() async {
        guard Switchboard.url != nil, let token else { return }
        let found: (calls: [WaitingCall], hold: HoldState?, status: HubStatus?)
        do {
            found = try await Switchboard.waiting(token: token)
        } catch is NotPaired {
            connection = .notPaired
            needsPairing = true
            return
        } catch {
            return
        }
        if found.calls != waiting { waiting = found.calls }
        if let switched = found.hold, switched != hold { hold = HoldSwitch.remember(switched) }
        if found.status != status { status = found.status }
        // The app's icon counts the waiting calls, and the Lock Screen widget says how many. Only
        // when the count changes: setting it to 0 can clear the notifications delivered, such
        // as what sessions showed you, and the waiting calls are fetched every 3 s.
        HoldSwitch.remember(waiting: waiting.count)
        if UserDefaults.standard.object(forKey: Self.badgeKey) as? Int != waiting.count {
            try? await UNUserNotificationCenter.current().setBadgeCount(waiting.count)
            UserDefaults.standard.set(waiting.count, forKey: Self.badgeKey)
        }
    }

    /// Fetches the sessions you can call from the hub. If it can't be reached, the list stays
    /// as it was.
    @MainActor
    func refreshContacts() async {
        guard Switchboard.url != nil, let token else { return }
        if let found = try? await Switchboard.contacts(token: token), found != contacts { contacts = found }
    }

    /// Fetches what sessions showed you from the hub. If it can't be reached, the list stays as
    /// it was.
    @MainActor
    func refreshShown() async {
        guard Switchboard.url != nil else { return }
        guard let found = try? await ShownFiles.list() else { return }
        if found != shown { shown = found }
        // Forget having opened what the hub no longer keeps.
        let kept = seenShown.intersection(found.map(\.id))
        if kept != seenShown { remember(seen: kept) }
        ShownView.clearOld()
    }

    /// Opens what a session showed you, and remembers you have. With the call screen up, it
    /// minimises first, and the item opens once it's gone.
    func open(_ item: ShownItem) {
        shownOpenedAt = .now
        if activeCall != nil && !callMinimised {
            pendingShown = item
            callMinimised = true
        } else {
            openShown = item
        }
        if !seenShown.contains(item.id) { remember(seen: seenShown.union([item.id])) }
    }

    /// The call screen has gone: what you opened meanwhile opens now.
    func callScreenGone() {
        guard let item = pendingShown else { return }
        pendingShown = nil
        openShown = item
    }

    /// You came back to the app from elsewhere during a call, as from iOS's green pill, which
    /// the app can't tell from its icon: the call screen comes back, unless you came to see
    /// something shown.
    func cameBack() {
        guard activeCall != nil, Date.now.timeIntervalSince(shownOpenedAt) > 2 else { return }
        callMinimised = false
    }

    func hasSeen(_ item: ShownItem) -> Bool { seenShown.contains(item.id) }

    /// Counts it as seen without opening it, as a link opened from its notification or its menu.
    func markSeen(_ item: ShownItem) { markSeen(id: item.id) }

    func markSeen(id: String) {
        if !seenShown.contains(id) { remember(seen: seenShown.union([id])) }
    }

    private func remember(seen: Set<String>) {
        seenShown = seen
        UserDefaults.standard.set(Array(seen), forKey: Self.seenShownKey)
    }

    /// Opens what a session showed you, as its notification names it; says so if the hub no
    /// longer keeps it.
    @MainActor
    func openShown(id: String) async {
        if !shown.contains(where: { $0.id == id }) { await refreshShown() }
        if let item = shown.first(where: { $0.id == id }) {
            open(item)
        } else {
            callBackProblem = String(localized: "That's no longer kept: your hub keeps what sessions show you for a week.")
        }
    }

    /// The session a call in the history is from, if it's in Sessions now: by its ID, or for a
    /// call without one, the only session of that project at that address.
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
        guard let data = try? Data(contentsOf: historyFile) else { return [] }
        return (try? JSONDecoder().decode([CallRecord].self, from: data)) ?? []
    }
}
