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
/// last asked about them ("refreshed"). The app lists them (`/waiting`), and can call one now.
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

    /// A call held until `heldUntil`, new or asked about again.
    static func hold(callID: String, project: String, subject: String?, voice: String, heldUntil: String) throws {
        var calls = fresh()
        var call = ["project": project, "voice": voice, "held_until": heldUntil]
        call["subject"] = subject
        call["refreshed"] = Date().ISO8601Format()
        calls[callID] = call
        try save(calls)
    }

    /// Takes a call off the list, as it rings or the app calls it now; false if it wasn't on it.
    @discardableResult
    static func remove(_ callID: String) throws -> Bool {
        var calls = load()
        guard calls.removeValue(forKey: callID) != nil else { return false }
        try save(calls)
        return true
    }

    /// The calls that wait, soonest first, each with its call ID; stale ones are dropped.
    static func list() throws -> [[String: String]] {
        let calls = fresh()
        if calls.count != load().count { try save(calls) }
        let soonest = calls.sorted { a, b in
            let (at, bt) = (date(a.value["held_until"]), date(b.value["held_until"]))
            return at != bt ? at < bt : a.key < b.key
        }
        return soonest.map { id, call in call.filter { $0.key != "refreshed" }.merging(["call_id": id]) { $1 } }
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
