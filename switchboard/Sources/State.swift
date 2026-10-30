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
