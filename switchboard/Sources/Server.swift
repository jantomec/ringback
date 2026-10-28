import Foundation

/// The HTTP endpoints the phone talks to over Nebula: `/register` and `/events`. Requests are
/// small and rare, so they're handled one at a time. Each gets at most 5 s, so a stalled
/// connection can't hold the next request past the app's 10 s timeout.
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

/// Runs the endpoint a request asks for. The phone only checks for a 200.
private func answer(_ request: Request?) -> (String, String) {
    let endpoints: [Substring: ([String: Any]) throws -> Void] = ["/register": register, "/events": event]
    guard let request else { return ("400 Bad Request", #"{"ok": false}"#) }
    guard let endpoint = endpoints[request.path] else { return ("404 Not Found", #"{"ok": false}"#) }
    guard request.method == "POST" else { return ("405 Method Not Allowed", #"{"ok": false}"#) }
    guard let body = (try? JSONSerialization.jsonObject(with: request.body)) as? [String: Any] else {
        return ("400 Bad Request", #"{"ok": false}"#)
    }
    do {
        try endpoint(body)
        return ("200 OK", #"{"ok": true}"#)
    } catch is BadRequest {
        return ("400 Bad Request", #"{"ok": false}"#)
    } catch {
        log("\(request.path) failed: \(error.localizedDescription)")
        return ("500 Internal Server Error", #"{"ok": false}"#)
    }
}

private func register(_ body: [String: Any]) throws {
    guard let token = body["token"] as? String, !token.isEmpty else { throw BadRequest() }
    let environment = body["environment"] as? String ?? "sandbox"
    let device = body["device"] as? String ?? "a phone"
    try Devices.register(token: token, environment: environment, device: device)
    log("registered \(device) (\(environment)), token …\(token.suffix(8))")
}

private func event(_ body: [String: Any]) throws {
    try Events.append(body)
    let extra = ["on_another_call", "duration_s", "since_answer_s", "error", "note"]
        .compactMap { key in body[key].map { ", \(key) \($0)" } }.joined()
    let at = (body["at"] as? String).flatMap(parseISO8601).map { " (on the phone at \(timeOfDay($0)))" } ?? ""
    let call = (body["call_id"] as? String ?? "?").prefix(8)
    log("call \(call): \(body["event"].map { "\($0)" } ?? "?")\(extra)\(at)")
}

private func parseISO8601(_ text: String) -> Date? {
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
