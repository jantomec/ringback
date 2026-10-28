import Foundation
import UIKit

/// Talks to the switchboard over Nebula: registers this phone's VoIP push token and reports
/// what happened to each call.
enum Switchboard {
    static let urlKey = "switchboardURL"

    /// Waits between attempts to report an event. After the phone changes network, Nebula can
    /// take a while to reach the Mac again (16 s in one test), so a report keeps trying for
    /// about a minute.
    private static let retryDelays: [Double] = [0, 2, 4, 8, 15, 30]

    static func register(token: String) async throws {
        try await post("register", [
            "token": token,
            "environment": environment,
            "device": await UIDevice.current.name,
        ])
    }

    /// Reports a call event, stamped with when it happened here, and retries until the
    /// switchboard confirms it. Asks iOS for background time so a report from a locked phone
    /// can finish.
    static func report(callID: String, event: String, extra: [String: String] = [:]) {
        var body = extra
        body["call_id"] = callID
        body["event"] = event
        body["at"] = Date.now.ISO8601Format()
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

    private static func post(_ path: String, _ body: [String: String]) async throws {
        guard let text = UserDefaults.standard.string(forKey: urlKey), let base = URL(string: text) else {
            throw URLError(.badURL)
        }
        var request = URLRequest(url: base.appending(path: path), timeoutInterval: 10)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (_, response) = try await URLSession.shared.data(for: request)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard status == 200 else { throw URLError(.badServerResponse) }
    }
}
