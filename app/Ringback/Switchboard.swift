import Foundation
import UIKit

/// Talks to the hub, over Nebula or another VPN: registers this phone's push tokens, reports
/// what happened to each call, answers connection checks, and fetches the calls that wait and
/// the sessions you can call.
enum Switchboard {
    /// Its address, as you typed it in the Settings app ("10.0.0.1"); before that, the app
    /// kept the whole URL here, which reads the same.
    static let urlKey = "switchboardURL"
    static let defaultPort = 8787

    static var url: URL? {
        UserDefaults.standard.string(forKey: urlKey).flatMap(normalize)
    }

    /// The address as you'd type it: no http://, and no port when it's the default.
    static var displayHost: String? {
        guard let url, let host = url.host() else { return nil }
        guard let port = url.port, port != defaultPort else { return host }
        return "\(host):\(port)"
    }

    /// Turns what you type ("10.0.0.1", "10.0.0.1:9000" or a full URL) into the hub's
    /// URL, adding http:// and the default port. Nil if there's no host.
    static func normalize(_ input: String) -> URL? {
        let text = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, var parts = URLComponents(string: text.contains("://") ? text : "http://\(text)"),
            let host = parts.host, !host.isEmpty
        else { return nil }
        parts.port = parts.port ?? defaultPort
        parts.path = ""
        return parts.url
    }

    /// Waits between attempts to report an event. After the phone changes network, Nebula can
    /// take a while to reach the hub again (16 s in one test), so a report keeps trying for
    /// about a minute.
    private static let retryDelays: [Double] = [0, 2, 4, 8, 15, 30]

    /// The notification token is for connection checks (ConnectionCheck), once iOS has
    /// given it. `hours` are the work hours as WorkHours.fields gives them. `onDevice` is On-device
    /// voice: the hub then rings calls while no speech computer is linked, and the phone talks
    /// them through itself (PhoneSpeech).
    static func register(token: String, notificationToken: String?, hours: [String: String], onDevice: Bool) async throws {
        var body: [String: Any] = hours.merging(["token": token, "environment": environment, "device": await UIDevice.current.name]) { $1 }
        body["notification_token"] = notificationToken
        body["on_device"] = onDevice
        try await post("register", body)
    }

    /// Reaches the hub on a connection check. The VoIP token, if it has arrived yet,
    /// tells it which phone checked in. The work hours come along, so a new time zone reaches
    /// it within the half hour between checks when you travel.
    static func check(token: String?) async throws {
        var body = WorkHours.fields
        body["token"] = token
        try await post("check", body, timeout: 5)
    }

    /// The calls that wait to ring, held while your phone holds calls, soonest first, with
    /// whether each has a question and why it waits; the hold switch; and what the hub says
    /// about what calls need (each nil if the answer leaves it out). The hub gives them only to a
    /// phone that registered, by its VoIP token: a call's ID lets whoever has it take the call.
    static func waiting(token: String) async throws -> (calls: [WaitingCall], hold: HoldState?, status: HubStatus?) {
        let data = try await post("waiting", ["token": token], timeout: 5)
        guard let answer = try JSONSerialization.jsonObject(with: data) as? [String: Any],
            let calls = answer["calls"] as? [[String: Any]]
        else { throw URLError(.cannotParseResponse) }
        let iso = ISO8601DateFormatter()
        let waiting = calls.compactMap { call -> WaitingCall? in
            guard let id = call["call_id"] as? String, let project = call["project"] as? String,
                let voice = (call["voice"] as? String).flatMap(URL.init(string:))
            else { return nil }
            return WaitingCall(
                id: id, project: project, subject: call["subject"] as? String, voice: voice,
                heldUntil: (call["held_until"] as? String).flatMap(iso.date(from:)),
                question: call["question"] as? Bool ?? false, session: call["session_id"] as? String,
                reason: call["reason"] as? String)
        }
        let hold = (answer["hold"] as? [String: Any]).flatMap { hold in
            (hold["hold"] as? Bool).map { HoldState(on: $0, until: (hold["until"] as? String).flatMap(iso.date(from:))) }
        }
        let status = (answer["status"] as? [String: Any]).map { status in
            let speech = status["speech"] as? [String: Any]
            let push = status["push"] as? [String: Any]
            return HubStatus(
                speechOK: speech?["ok"] as? Bool ?? true, waitingForSpeech: speech?["waiting"] as? Int ?? 0,
                pushProblem: push?["ok"] as? Bool == false ? push?["problem"] as? String ?? "apple" : nil)
        }
        return (waiting, hold, status)
    }

    /// The sessions you can call, with ringback on, on every Mac, by project, each with where
    /// its call's audio goes (the hub). As for the waiting calls, only a phone that registered
    /// gets them: a session's ID lets whoever has it call the session.
    static func contacts(token: String) async throws -> [Contact] {
        let data = try await post("contacts", ["token": token], timeout: 5)
        guard let listed = (try JSONSerialization.jsonObject(with: data) as? [String: Any])?["contacts"] as? [[String: Any]]
        else { throw URLError(.cannotParseResponse) }
        let iso = ISO8601DateFormatter()
        return listed.compactMap { contact in
            guard let session = contact["session_id"] as? String, let project = contact["project"] as? String,
                let voice = (contact["voice"] as? String).flatMap(URL.init(string:))
            else { return nil }
            let subject = (contact["subject"] as? String).flatMap { $0.isEmpty ? nil : $0 }
            return Contact(
                session: session, project: project, voice: voice, subject: subject,
                briefedAt: (contact["briefed_at"] as? String).flatMap(iso.date(from:)),
                since: (contact["since"] as? String).flatMap(iso.date(from:)))
        }
    }

    /// Reports a call event, stamped with when it happened here, and retries until the
    /// hub confirms it. Asks iOS for background time so a report from a locked phone
    /// can finish.
    static func report(callID: String, event: String, extra: [String: String] = [:], at: Date = .now) {
        var body = extra
        body["call_id"] = callID
        body["event"] = event
        body["at"] = at.ISO8601Format()
        Task { @MainActor in
            let task = UIApplication.shared.beginBackgroundTask(withName: "report \(event)")
            defer { UIApplication.shared.endBackgroundTask(task) }
            for delay in retryDelays {
                try? await Task.sleep(for: .seconds(delay))
                if (try? await post("events", body)) != nil { return }
            }
        }
    }

    /// Development builds get their pushes from the APNs sandbox.
    private static var environment: String {
        #if DEBUG
        "sandbox"
        #else
        "production"
        #endif
    }

    @discardableResult
    private static func post(_ path: String, _ body: [String: Any], timeout: TimeInterval = 10) async throws -> Data {
        guard let base = url else { throw URLError(.badURL) }
        var request = URLRequest(url: base.appending(path: path), timeoutInterval: timeout)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (data, response) = try await URLSession.shared.data(for: request)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard status == 200 else { throw URLError(.badServerResponse) }
        return data
    }
}

/// The hours Claude may call you, set in the Settings app. Outside them the hub doesn't
/// ring the phone: a call waits until they start. From later than To wraps past midnight, and
/// equal ones mean any time. Kept as "HH:MM", in 15-minute steps (the Settings app has lists,
/// not time pickers), so they stay the same hours when you travel; the hub reads them
/// in the phone's time zone.
enum WorkHours {
    static let onKey = "workHours"
    static let fromKey = "workHoursFrom"
    static let toKey = "workHoursTo"
    static let defaultFrom = "08:00"
    static let defaultTo = "20:00"

    /// What the hub gets with /register and /check: empty hours while they're off,
    /// which means any time.
    static var fields: [String: String] {
        let defaults = UserDefaults.standard
        let on = defaults.bool(forKey: onKey)
        return [
            "hours_from": on ? defaults.string(forKey: fromKey) ?? defaultFrom : "",
            "hours_to": on ? defaults.string(forKey: toKey) ?? defaultTo : "",
            "time_zone": TimeZone.current.identifier,
        ]
    }

    /// "HH:MM" on the nearest 15-minute step, at most 23:45; nil if it isn't a time.
    static func quarter(_ time: String) -> String? {
        let parts = time.split(separator: ":").compactMap { Int($0) }
        guard parts.count == 2, (0..<24).contains(parts[0]), (0..<60).contains(parts[1]) else { return nil }
        let minutes = min((parts[0] * 60 + parts[1] + 7) / 15 * 15, 23 * 60 + 45)
        return String(format: "%02d:%02d", minutes / 60, minutes % 60)
    }
}
