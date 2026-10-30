import Foundation

/// The HTTP endpoints the phone talks to over Nebula: `/register`, `/events`, `/check` and
/// `/waiting`; and `/call`, which the voice servers ask to ring the phone. Requests are small
/// and rare, so they're handled one at a time. Each gets at most 5 s, so a
/// stalled connection can't hold the next request past the app's 10 s timeout. Meanwhile it
/// asks the phones to check their connection (Check.swift).
func serve(listen address: String) -> Never {
    guard let colon = address.lastIndex(of: ":"), let port = UInt16(address[address.index(after: colon)...]) else {
        fail("listen must be HOST:PORT, not \(address)")
    }
    var addr = sockaddr_in()
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = port.bigEndian
    guard inet_pton(AF_INET, String(address[..<colon]), &addr.sin_addr) == 1 else {
        fail("listen must be an IPv4 address and port, not \(address)")
    }
    let server = socket(AF_INET, SOCK_STREAM, 0)
    var on: Int32 = 1
    setsockopt(server, SOL_SOCKET, SO_REUSEADDR, &on, socklen_t(MemoryLayout<Int32>.size))
    let bound = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            bind(server, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
        }
    }
    // Fails if Nebula isn't up yet; launchd then starts it again.
    guard bound == 0, listen(server, 16) == 0 else {
        fail("can't listen on \(address): \(String(cString: strerror(errno)))")
    }
    log("switchboard listening on \(address)")
    checkPeriodically()

    var limit = timeval(tv_sec: 5, tv_usec: 0)
    while true {
        let client = accept(server, nil, nil)
        guard client >= 0 else { continue }
        setsockopt(client, SOL_SOCKET, SO_RCVTIMEO, &limit, socklen_t(MemoryLayout<timeval>.size))
        setsockopt(client, SOL_SOCKET, SO_SNDTIMEO, &limit, socklen_t(MemoryLayout<timeval>.size))
        setsockopt(client, SOL_SOCKET, SO_NOSIGPIPE, &on, socklen_t(MemoryLayout<Int32>.size))
        let (status, body) = answer(readRequest(client))
        send(client, status: status, body: body)
        close(client)
    }
}

private struct Request {
    let method: Substring
    let path: Substring
    let body: Data
}

private struct BadRequest: Error {}
private struct Forbidden: Error {}

/// Runs the endpoint a request asks for, which answers its own JSON. The phone only checks for
/// a 200, apart from `/waiting`.
private func answer(_ request: Request?) -> (String, String) {
    let endpoints: [Substring: ([String: Any]) throws -> [String: Any]] = [
        "/register": register, "/events": event, "/check": checkIn, "/call": call, "/waiting": waiting,
    ]
    guard let request else { return ("400 Bad Request", #"{"ok": false}"#) }
    guard let endpoint = endpoints[request.path] else { return ("404 Not Found", #"{"ok": false}"#) }
    guard request.method == "POST" else { return ("405 Method Not Allowed", #"{"ok": false}"#) }
    guard let body = (try? JSONSerialization.jsonObject(with: request.body)) as? [String: Any] else {
        return ("400 Bad Request", #"{"ok": false}"#)
    }
    do {
        let answer = try JSONSerialization.data(withJSONObject: try endpoint(body), options: [.sortedKeys])
        return ("200 OK", String(decoding: answer, as: UTF8.self))
    } catch is BadRequest {
        return ("400 Bad Request", #"{"ok": false}"#)
    } catch is Forbidden {
        return ("403 Forbidden", #"{"ok": false}"#)
    } catch {
        log("\(request.path) failed: \(error.localizedDescription)")
        return ("500 Internal Server Error", #"{"ok": false}"#)
    }
}

private let ok: [String: Any] = ["ok": true]

/// A phone registers its push tokens, and its work hours if it has any: a registration
/// without them clears them, and ones that can't be read are a 400.
private func register(_ body: [String: Any]) throws -> [String: Any] {
    guard let token = body["token"] as? String, !token.isEmpty, let hours = WorkHours.fields(of: body) else {
        throw BadRequest()
    }
    let environment = body["environment"] as? String ?? "sandbox"
    let device = body["device"] as? String ?? "a phone"
    let notificationToken = (body["notification_token"] as? String).flatMap { $0.isEmpty ? nil : $0 }
    try Devices.register(
        token: token, environment: environment, device: device, notificationToken: notificationToken, hours: hours)
    let checks = notificationToken == nil ? "" : ", with connection checks"
    log("registered \(device) (\(environment)), token …\(token.suffix(8))\(checks)\(describe(hours).map { ", \($0)" } ?? "")")
    return ok
}

/// A voice server asks to ring the phone for one of its sessions. The call ID is that call's
/// secret: the voice server accepts only an offer that carries it, and only the push takes it
/// to the phone. The push goes out after the answer, so a slow APNs doesn't hold up requests.
///
/// Outside the work hours of every phone nothing rings: the answer says when the first of
/// them start (`held_until`), and the voice server asks again by then, with the same call ID.
/// Meanwhile the call waits, and the app shows it (Waiting). The voice server says whether the
/// call has a question (`question`). The first time a held call has a question, the phone gets
/// a notification, once for that call.
private func call(_ body: [String: Any]) throws -> [String: Any] {
    guard let project = body["project"] as? String, !project.isEmpty,
        let voice = body["voice"] as? String, voice.hasPrefix("http://") || voice.hasPrefix("https://"),
        let callID = body["call_id"] as? String, (16...128).contains(callID.count)
    else { throw BadRequest() }
    let subject = (body["subject"] as? String).flatMap { $0.isEmpty ? nil : $0 }
    log("call \(callID.prefix(8)): asked by \(voice) for \(project)\(subject.map { ": \($0)" } ?? "")")
    if let held = heldUntil(Date()) {
        let question = body["question"] as? Bool ?? false
        log("call \(callID.prefix(8)): held until \(held), outside the work hours of every phone\(question ? ", with a question" : "")")
        let notify = try Waiting.hold(
            callID: callID, project: project, subject: subject, voice: voice, heldUntil: held, question: question)
        if notify {
            Task.detached {
                for line in await notifyQuestion(callID: callID, project: project, subject: subject, until: parseISO8601(held)) {
                    log(line)
                }
            }
        }
        return ["ok": true, "held_until": held]
    }
    if try Waiting.remove(callID) { log("call \(callID.prefix(8)): no longer waiting, it rings") }
    Task.detached {
        let lines = await ring(project: project, subject: subject, voice: voice, callID: callID)
        if lines.isEmpty { log("call \(callID.prefix(8)): no phone registered") }
        lines.forEach { log($0) }
    }
    return ok
}

/// The phone reached the switchboard on a connection check. Without a known token it still
/// gets a 200: the check is whether it got through. Its work hours come along, so a new time
/// zone reaches the switchboard within the half hour between checks. Hours that can't be read
/// are left out rather than refused, for the same reason.
private func checkIn(_ body: [String: Any]) throws -> [String: Any] {
    guard let token = body["token"] as? String else { return ok }
    let sent = WorkHours.keys.contains { body[$0] != nil }
    let hours = sent ? WorkHours.fields(of: body) : nil
    if sent && hours == nil { log("check from …\(token.suffix(8)): work hours that can't be read, left out") }
    try Devices.checkedIn(token: token, hours: hours)
    return ok
}

/// The phone reports what happened to a call. A waiting call you called from the app ("called
/// now"), or whose question you sent to the Claude app ("claude app"), no longer waits.
private func event(_ body: [String: Any]) throws -> [String: Any] {
    try Events.append(body)
    let extra = ["on_another_call", "duration_s", "since_answer_s", "error", "note"]
        .compactMap { key in body[key].map { ", \(key) \($0)" } }.joined()
    let at = (body["at"] as? String).flatMap(parseISO8601).map { " (on the phone at \(timeOfDay($0)))" } ?? ""
    let call = (body["call_id"] as? String ?? "?").prefix(8)
    log("call \(call): \(body["event"].map { "\($0)" } ?? "?")\(extra)\(at)")
    let leaves = ["called now": "called from the app", "claude app": "its question went to the Claude app"]
    if let event = body["event"] as? String, let why = leaves[event], let callID = body["call_id"] as? String,
        try Waiting.remove(callID)
    {
        log("call \(call): no longer waiting, \(why)")
    }
    return ok
}

/// The calls that wait, for the app to show, soonest first: `{"calls": [...]}`, each with its
/// call ID, project, subject, voice server, when it rings (`held_until`), whether it has a
/// question (`question`).
/// Only a phone that registered gets them, by its push token: a call's ID lets whoever has it
/// take the call.
private func waiting(_ body: [String: Any]) throws -> [String: Any] {
    guard let token = body["token"] as? String, Devices.load()[token] != nil else { throw Forbidden() }
    return ["calls": try Waiting.list()]
}

/// A phone's work hours as the log and `devices` show them; nil if it has none.
func describe(_ hours: [String: String]) -> String? {
    guard let from = hours["hours_from"], let to = hours["hours_to"] else { return nil }
    return "work hours \(from)–\(to) \(hours["time_zone"] ?? "")"
}

func parseISO8601(_ text: String) -> Date? {
    let f = ISO8601DateFormatter()
    if let date = f.date(from: text) { return date }
    f.formatOptions.insert(.withFractionalSeconds)
    return f.date(from: text)
}

/// Reads one HTTP/1.1 request; nil if it's malformed, over 64 KB or too slow.
private func readRequest(_ fd: Int32) -> Request? {
    let maxSize = 65536
    var data = Data()
    var buffer = [UInt8](repeating: 0, count: 4096)
    func readMore() -> Bool {
        let n = read(fd, &buffer, buffer.count)
        if n > 0 { data.append(contentsOf: buffer[..<n]) }
        return n > 0 && data.count <= maxSize
    }

    let blankLine = Data("\r\n\r\n".utf8)
    var headerEnd = data.range(of: blankLine)
    while headerEnd == nil {
        guard readMore() else { return nil }
        headerEnd = data.range(of: blankLine)
    }
    let head = String(decoding: data[..<headerEnd!.lowerBound], as: UTF8.self).split(separator: "\r\n")
    let requestLine = head.first?.split(separator: " ") ?? []
    guard requestLine.count == 3 else { return nil }
    var length = 0
    for line in head.dropFirst() {
        let parts = line.split(separator: ":", maxSplits: 1)
        if parts.count == 2, parts[0].lowercased() == "content-length" {
            length = Int(parts[1].trimmingCharacters(in: .whitespaces)) ?? -1
        }
    }
    guard (0...maxSize).contains(length) else { return nil }
    let bodyStart = headerEnd!.upperBound
    while data.count - bodyStart < length {
        guard readMore() else { return nil }
    }
    return Request(method: requestLine[0], path: requestLine[1], body: Data(data[bodyStart..<bodyStart + length]))
}

private func send(_ fd: Int32, status: String, body: String) {
    let response = Array("""
        HTTP/1.1 \(status)\r
        Content-Type: application/json\r
        Content-Length: \(body.utf8.count)\r
        Connection: close\r
        \r
        \(body)
        """.utf8)
    var sent = 0
    while sent < response.count {
        let n = response[sent...].withUnsafeBytes { write(fd, $0.baseAddress, $0.count) }
        guard n > 0 else { return }
        sent += n
    }
}
