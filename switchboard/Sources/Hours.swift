import Foundation

/// A phone's work hours, set in the app: Claude calls only between `from` and `to`, in the
/// phone's time zone. A `from` later than `to` wraps past midnight (22:00–06:00), and equal
/// ones mean any time. They set the phone's hold switch at their edges (Hold).
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
        next(from, after: date)
    }

    /// When they next end after `date`.
    func end(after date: Date) -> Date {
        next(to, after: date)
    }

    /// When they last started or ended, at or before `date`, and whether it was a start; nil if
    /// they're any time (From and To equal), which has no edges.
    func lastEdge(atOrBefore date: Date) -> (at: Date, start: Bool)? {
        guard from != to else { return nil }
        let later = date.addingTimeInterval(1)
        let start = next(from, after: later, direction: .backward), end = next(to, after: later, direction: .backward)
        return start > end ? (start, true) : (end, false)
    }

    private func next(_ minutes: Int, after date: Date, direction: Calendar.SearchDirection = .forward) -> Date {
        let time = DateComponents(hour: minutes / 60, minute: minutes % 60, second: 0)
        return calendar.nextDate(after: date, matching: time, matchingPolicy: .nextTime, direction: direction) ?? date
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

/// A phone's hold switch (Stage 5 step 2): on hold, its calls wait; taking calls, they ring.
/// You set it in the app ("hold", and when, "hold_set", in Devices), and its work hours set it at
/// their edges: on hold when they end, taking calls when they start. An edge sets it, it doesn't
/// flip it, so one that finds it already so changes nothing, and between edges your setting
/// stands: taking calls from 21:00 lasts until the hours end the next day.
struct Hold {
    let on: Bool
    /// When it next changes by itself: when its hours next start (on hold) or end (taking
    /// calls); nil without hours, or with From and To equal.
    let until: Date?

    init(_ device: [String: String], at now: Date) {
        let hours = WorkHours(device)
        let set = device["hold_set"].flatMap(parseISO8601)
        var on = device["hold"] == "yes"
        if let edge = hours?.lastEdge(atOrBefore: now), set.map({ edge.at > $0 }) ?? true {
            on = !edge.start
        }
        self.on = on
        until = hours.flatMap { $0.from == $0.to ? nil : on ? $0.start(after: now) : $0.end(after: now) }
    }

    /// As the app gets it: `{"hold": Bool, "until": ISO 8601}`, `until` with the phone's offset.
    func answer(_ device: [String: String]) -> [String: Any] {
        var answer: [String: Any] = ["hold": on]
        if let until, let hours = WorkHours(device) { answer["until"] = hours.written(until) }
        return answer
    }
}

/// A call that waits: `until` is when the first phone comes off hold by its hours, written with
/// that phone's offset; nil when none will by itself, so it waits until you take calls.
struct Held {
    let until: String?
}

/// A call asked for at `now` is held while every registered phone is on hold; nil if it rings:
/// a phone takes calls, or none is registered.
func held(_ now: Date) -> Held? {
    let devices = Devices.load().values
    guard !devices.isEmpty else { return nil }
    var earliest: (at: Date, hours: WorkHours)?
    for device in devices {
        let hold = Hold(device, at: now)
        guard hold.on else { return nil }
        if let at = hold.until, let hours = WorkHours(device), earliest.map({ at < $0.at }) ?? true {
            earliest = (at, hours)
        }
    }
    return Held(until: earliest.map { $0.hours.written($0.at) })
}
