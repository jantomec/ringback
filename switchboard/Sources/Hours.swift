import Foundation

/// A phone's work hours, set in the app: Claude calls only between `from` and `to`, in the
/// phone's time zone. A `from` later than `to` wraps past midnight (22:00–06:00), and equal
/// ones mean any time. Outside every phone's hours, `/call` holds a call until they start.
struct WorkHours {
    /// How Devices keeps them for each phone, as the app sends them: "HH:MM" and a time zone
    /// identifier such as "America/Chicago".
    static let keys = ["hours_from", "hours_to", "time_zone"]

    let from: Int  // minutes after midnight
    let to: Int
    let calendar: Calendar  // in the phone's time zone

    /// A phone's hours as Devices keeps them; nil if it has none.
    init?(_ fields: [String: String]) {
        guard let from = fields["hours_from"].flatMap(minutes), let to = fields["hours_to"].flatMap(minutes),
            let zone = fields["time_zone"].flatMap(TimeZone.init(identifier:))
        else { return nil }
        self.from = from
        self.to = to
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = zone
        self.calendar = calendar
    }

    /// The hours in a phone's request, checked, as Devices keeps them: empty when it sent
    /// none (both times missing or empty), nil when a time isn't "HH:MM" or the time zone
    /// isn't one this Mac knows.
    static func fields(of body: [String: Any]) -> [String: String]? {
        let from = body["hours_from"] as? String ?? "", to = body["hours_to"] as? String ?? ""
        if from.isEmpty && to.isEmpty { return [:] }
        let fields = ["hours_from": from, "hours_to": to, "time_zone": body["time_zone"] as? String ?? ""]
        return WorkHours(fields) == nil ? nil : fields
    }

    func contain(_ date: Date) -> Bool {
        guard from != to else { return true }
        let parts = calendar.dateComponents([.hour, .minute], from: date)
        let now = (parts.hour ?? 0) * 60 + (parts.minute ?? 0)
        return from < to ? from <= now && now < to : now >= from || now < to
    }

    /// When they next start after `date`.
    func start(after date: Date) -> Date {
        let time = DateComponents(hour: from / 60, minute: from % 60, second: 0)
        return calendar.nextDate(after: date, matching: time, matchingPolicy: .nextTime) ?? date
    }

    /// `date` in ISO 8601, with the phone's offset from UTC: "2026-10-31T08:00:00-05:00".
    func written(_ date: Date) -> String {
        let f = ISO8601DateFormatter()
        f.timeZone = calendar.timeZone
        return f.string(from: date)
    }
}

/// Minutes after midnight of "HH:MM", or nil.
private func minutes(_ text: String) -> Int? {
    let parts = text.split(separator: ":", omittingEmptySubsequences: false)
    guard parts.count == 2, parts.allSatisfy({ $0.count == 2 && $0.allSatisfy { ("0"..."9").contains($0) } }),
        let hour = Int(parts[0]), let minute = Int(parts[1]), (0...23).contains(hour), (0...59).contains(minute)
    else { return nil }
    return hour * 60 + minute
}

/// When a call asked for at `now` may ring, if every registered phone is outside its work
/// hours: the earliest start of any phone's hours, written with that phone's offset. Nil if
/// a phone is inside its hours, or has none, or no phone is registered.
func heldUntil(_ now: Date) -> String? {
    let devices = Devices.load().values
    guard !devices.isEmpty else { return nil }
    var earliest: (start: Date, hours: WorkHours)?
    for device in devices {
        guard let hours = WorkHours(device), !hours.contain(now) else { return nil }
        let start = hours.start(after: now)
        if earliest.map({ start < $0.start }) ?? true { earliest = (start, hours) }
    }
    return earliest.map { $0.hours.written($0.start) }
}
