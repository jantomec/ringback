import Foundation

/// Phones that registered for calls, keyed by VoIP push token: `devices.json` in the state
/// folder. A phone that also gave a notification token gets connection checks, and
/// "checked_in" is when it last reached the switchboard on one. A phone with work hours has
/// them here too (WorkHours.keys).
enum Devices {
    static let path = stateDir + "/devices.json"

    static func load() -> [String: [String: String]] {
        guard let data = FileManager.default.contents(atPath: path) else { return [:] }
        return (try? JSONSerialization.jsonObject(with: data)) as? [String: [String: String]] ?? [:]
    }

    /// `hours` are the phone's work hours (WorkHours.fields); empty clears them.
    static func register(
        token: String, environment: String, device: String, notificationToken: String?, hours: [String: String]
    ) throws {
        var devices = load()
        var entry = devices[token] ?? [:]
        entry["environment"] = environment
        entry["device"] = device
        entry["registered"] = Date().ISO8601Format()
        entry["notification_token"] = notificationToken ?? entry["notification_token"]
        for key in WorkHours.keys { entry[key] = hours[key] }
        devices[token] = entry
        try save(devices)
    }

    /// A connection check can bring the phone's work hours too, so a new time zone reaches
    /// the switchboard when you travel; nil leaves them as they were.
    static func checkedIn(token: String, hours: [String: String]?) throws {
        var devices = load()
        guard var entry = devices[token] else { return }
        entry["checked_in"] = Date().ISO8601Format()
        if let hours {
            for key in WorkHours.keys { entry[key] = hours[key] }
        }
        devices[token] = entry
        try save(devices)
    }

    static func forget(_ token: String) throws {
        var devices = load()
        if devices.removeValue(forKey: token) != nil { try save(devices) }
    }

    private static func save(_ devices: [String: [String: String]]) throws {
        try FileManager.default.createDirectory(atPath: stateDir, withIntermediateDirectories: true)
        let data = try JSONSerialization.data(withJSONObject: devices, options: [.prettyPrinted, .sortedKeys])
        try data.write(to: URL(fileURLWithPath: path), options: .atomic)
    }
}

/// The calls that wait: held outside the work hours of every phone, until they ring. Kept in
/// `waiting.json` in the state folder, keyed by call ID, so they survive a restart, with their
/// project, subject, voice server, when they ring ("held_until") and when their voice server
/// last asked about them ("refreshed"); "question" and "remote_control" ("yes" or missing) as
/// the voice server last said, and when the phone was told of its question ("notified"). The
/// app lists them (`/waiting`), and can call one now, or send its question to the Claude app.
/// A voice server asks again about each held call every 10 min, which refreshes it; one not
/// refreshed for 25 min is dropped, so a call from a Mac that's gone doesn't wait forever.
enum Waiting {
    static let path = stateDir + "/waiting.json"
    /// Seconds after its last refresh that a call is dropped; RINGBACK_WAITING_STALE sets
    /// fewer, for the smoke test.
    static let stale = ProcessInfo.processInfo.environment["RINGBACK_WAITING_STALE"].flatMap(Double.init) ?? 25 * 60

    static func load() -> [String: [String: String]] {
        guard let data = FileManager.default.contents(atPath: path) else { return [:] }
        return (try? JSONSerialization.jsonObject(with: data)) as? [String: [String: String]] ?? [:]
    }

    /// A call held until `heldUntil`, new or asked about again: whether it has a question, and
    /// whether Remote Control is on in its session, as its voice server last said. True the
    /// first time it has a question, when the phone is to be told: "notified" keeps when, so a
    /// restart or asking again doesn't tell it twice.
    @discardableResult
    static func hold(
        callID: String, project: String, subject: String?, voice: String, heldUntil: String, question: Bool = false,
        remoteControl: Bool = false, session: String? = nil
    ) throws -> Bool {
        var calls = fresh()
        let notified = calls[callID]?["notified"]
        var call = ["project": project, "voice": voice, "held_until": heldUntil]
        call["subject"] = subject
        call["refreshed"] = Date().ISO8601Format()
        call["question"] = question ? "yes" : nil
        call["remote_control"] = remoteControl ? "yes" : nil
        call["session_id"] = session
        let notify = question && notified == nil
        call["notified"] = notify ? Date().ISO8601Format() : notified
        calls[callID] = call
        try save(calls)
        return notify
    }

    /// Takes a call off the list, as it rings, the app calls it now or sends its question to
    /// the Claude app; false if it wasn't on it.
    @discardableResult
    static func remove(_ callID: String) throws -> Bool {
        var calls = load()
        guard calls.removeValue(forKey: callID) != nil else { return false }
        try save(calls)
        return true
    }

    /// The calls that wait, soonest first, each with its call ID, and whether it has a question
    /// and Remote Control is on in its session; stale ones are dropped.
    static func list() throws -> [[String: Any]] {
        let calls = fresh()
        if calls.count != load().count { try save(calls) }
        let soonest = calls.sorted { a, b in
            let (at, bt) = (date(a.value["held_until"]), date(b.value["held_until"]))
            return at != bt ? at < bt : a.key < b.key
        }
        let kept = ["project", "subject", "voice", "held_until", "session_id"]
        return soonest.map { id, call in
            var listed: [String: Any] = call.filter { kept.contains($0.key) }
            listed["call_id"] = id
            listed["question"] = call["question"] != nil
            listed["remote_control"] = call["remote_control"] != nil
            return listed
        }
    }

    /// The calls refreshed within `stale` seconds; the log says which are dropped.
    private static func fresh() -> [String: [String: String]] {
        let now = Date()
        return load().filter { id, call in
            let refreshed = call["refreshed"].flatMap(parseISO8601) ?? .distantPast
            guard now.timeIntervalSince(refreshed) > stale else { return true }
            log("call \(id.prefix(8)): no longer waiting, its voice server hasn't asked about it since \(timeOfDay(refreshed))")
            return false
        }
    }

    private static func date(_ text: String?) -> Date {
        text.flatMap(parseISO8601) ?? .distantFuture
    }

    private static func save(_ calls: [String: [String: String]]) throws {
        try FileManager.default.createDirectory(atPath: stateDir, withIntermediateDirectories: true)
        let data = try JSONSerialization.data(withJSONObject: calls, options: [.prettyPrinted, .sortedKeys])
        try data.write(to: URL(fileURLWithPath: path), options: .atomic)
    }
}

/// The sessions that listen on each Mac, for the app's Contacts: `contacts.json` in the state
/// folder, keyed by voice server, with when it last said ("refreshed") and its sessions, each
/// with its session ID, project, and when it can, since when it listens and its latest
/// briefing's subject and time. A voice server says every 30 s; one not heard from for 90 s is
/// dropped, as when its Mac sleeps or the voice server stops.
enum Contacts {
    static let path = stateDir + "/contacts.json"
    /// Seconds after a voice server last said that its sessions are dropped;
    /// RINGBACK_CONTACTS_STALE sets fewer, for the smoke test.
    static let stale = ProcessInfo.processInfo.environment["RINGBACK_CONTACTS_STALE"].flatMap(Double.init) ?? 90

    static func load() -> [String: [String: Any]] {
        guard let data = FileManager.default.contents(atPath: path) else { return [:] }
        return (try? JSONSerialization.jsonObject(with: data)) as? [String: [String: Any]] ?? [:]
    }

    /// A voice server's sessions now, replacing those it gave before; true if they changed.
    @discardableResult
    static func update(voice: String, sessions: [[String: String]]) throws -> Bool {
        var all = fresh()
        let before = all[voice]?["sessions"] as? [[String: String]]
        all[voice] = ["refreshed": Date().ISO8601Format(), "sessions": sessions]
        try save(all)
        return before != sessions
    }

    /// Every fresh voice server's sessions, by project, then since when they listen, each with
    /// its voice server; stale ones are dropped.
    static func list() throws -> [[String: String]] {
        let all = fresh()
        if all.count != load().count { try save(all) }
        let listed = all.flatMap { voice, entry in
            (entry["sessions"] as? [[String: String]] ?? []).map { $0.merging(["voice": voice]) { $1 } }
        }
        return listed.sorted { a, b in
            let key = { (s: [String: String]) in [s["project"]?.lowercased() ?? "", s["since"] ?? "", s["session_id"] ?? ""] }
            return key(a).lexicographicallyPrecedes(key(b))
        }
    }

    /// The voice servers heard from within `stale` seconds; the log says which are dropped.
    private static func fresh() -> [String: [String: Any]] {
        let now = Date()
        return load().filter { voice, entry in
            let refreshed = (entry["refreshed"] as? String).flatMap(parseISO8601) ?? .distantPast
            guard now.timeIntervalSince(refreshed) > stale else { return true }
            log("sessions listening on \(voice): dropped, not heard from since \(timeOfDay(refreshed))")
            return false
        }
    }

    private static func save(_ all: [String: [String: Any]]) throws {
        try FileManager.default.createDirectory(atPath: stateDir, withIntermediateDirectories: true)
        let data = try JSONSerialization.data(withJSONObject: all, options: [.prettyPrinted, .sortedKeys])
        try data.write(to: URL(fileURLWithPath: path), options: .atomic)
    }
}

/// What the phone reported about each call, one JSON object per line: `events.jsonl` in the
/// state folder. Each gets the switchboard's local time as "time".
enum Events {
    static let path = stateDir + "/events.jsonl"

    private static let localTime: DateFormatter = {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyy-MM-dd'T'HH:mm:ss"
        return f
    }()

    static func append(_ event: [String: Any]) throws {
        var record = event
        record["time"] = record["time"] ?? localTime.string(from: Date())
        var line = try JSONSerialization.data(withJSONObject: record, options: [.sortedKeys, .withoutEscapingSlashes])
        line.append(0x0A)
        try FileManager.default.createDirectory(atPath: stateDir, withIntermediateDirectories: true)
        if !FileManager.default.fileExists(atPath: path) {
            FileManager.default.createFile(atPath: path, contents: nil)
        }
        let file = try FileHandle(forWritingTo: URL(fileURLWithPath: path))
        defer { try? file.close() }
        try file.seekToEnd()
        try file.write(contentsOf: line)
    }
}
