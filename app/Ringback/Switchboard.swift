import Foundation
import UIKit

/// Talks to the switchboard over Nebula: registers this phone's push tokens, reports what
/// happened to each call, and answers connection checks.
enum Switchboard {
    static let urlKey = "switchboardURL"
    static let defaultPort = 8787

    static var url: URL? {
        UserDefaults.standard.string(forKey: urlKey).flatMap { URL(string: $0) }
    }

    /// The address as you'd type it: no http://, and no port when it's the default.
    static var displayHost: String? {
        guard let url, let host = url.host() else { return nil }
        guard let port = url.port, port != defaultPort else { return host }
        return "\(host):\(port)"
    }

    /// Turns what you type ("10.0.0.1", "10.0.0.1:9000" or a full URL) into the switchboard's
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
    /// take a while to reach the Mac again (16 s in one test), so a report keeps trying for
    /// about a minute.
    private static let retryDelays: [Double] = [0, 2, 4, 8, 15, 30]

    /// The notification token is for connection checks (ConnectionCheck), once iOS has
    /// given it.
    static func register(token: String, notificationToken: String?) async throws {
        var body = ["token": token, "environment": environment, "device": await UIDevice.current.name]
        body["notification_token"] = notificationToken
        try await post("register", body)
    }

    /// Reaches the switchboard on a connection check. The VoIP token, if it has arrived yet,
    /// tells it which phone checked in.
    static func check(token: String?) async throws {
        try await post("check", token.map { ["token": $0] } ?? [:], timeout: 5)
    }

    /// Reports a call event, stamped with when it happened here, and retries until the
    /// switchboard confirms it. Asks iOS for background time so a report from a locked phone
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

    private static func post(_ path: String, _ body: [String: String], timeout: TimeInterval = 10) async throws {
        guard let base = url else { throw URLError(.badURL) }
        var request = URLRequest(url: base.appending(path: path), timeoutInterval: timeout)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (_, response) = try await URLSession.shared.data(for: request)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard status == 200 else { throw URLError(.badServerResponse) }
    }
}
