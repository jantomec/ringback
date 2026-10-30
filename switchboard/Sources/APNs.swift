import CryptoKit
import Foundation

/// Pushes through APNs, with token-based (JWT) authentication. URLSession speaks the HTTP/2
/// that APNs requires.
struct APNs {
    static let hosts = [
        "sandbox": "https://api.sandbox.push.apple.com",
        "production": "https://api.push.apple.com",
    ]

    enum Kind {
        /// Rings the phone, through PushKit. Goes to the VoIP token.
        case voip
        /// Wakes the app for a moment without showing anything. Goes to the notification token.
        case background
        /// An ordinary notification, as any app's: shown, and sounding, as your Focus allows.
        /// Goes to the notification token.
        case alert
    }

    let bundleID: String
    /// One provider token per client. Apple refuses new tokens more often than every 20
    /// minutes on a connection, and wants a fresh one at least every 60.
    let bearer: String

    init(teamID: String, keyID: String, keyFile: String, bundleID: String) throws {
        self.bundleID = bundleID
        let key = try P256.Signing.PrivateKey(pemRepresentation: String(contentsOfFile: expand(keyFile), encoding: .utf8))
        let header = base64url(try JSONSerialization.data(withJSONObject: ["alg": "ES256", "kid": keyID]))
        let claims = base64url(try JSONSerialization.data(withJSONObject: ["iss": teamID, "iat": Int(Date().timeIntervalSince1970)]))
        let signature = try key.signature(for: Data("\(header).\(claims)".utf8))
        bearer = "\(header).\(claims).\(base64url(signature.rawRepresentation))"
    }

    /// The client for an environment, from the config.
    init(environment: String, config: [String: String]) throws {
        guard let team = config["apns.team_id"], let bundle = config["apns.bundle_id"],
            let keyID = config["apns.\(environment).key_id"], let keyFile = config["apns.\(environment).key_file"]
        else { throw APNsSetupError.noKey(environment) }
        do {
            try self.init(teamID: team, keyID: keyID, keyFile: keyFile, bundleID: bundle)
        } catch {
            throw APNsSetupError.badKey(keyFile, error)
        }
    }

    /// Sends one push; returns APNs' status and its reason if it refused. It expires at once,
    /// so a phone that's offline doesn't get it later, unless it's kept until `expiresAt`.
    func push(
        to deviceToken: String, environment: String, kind: Kind = .voip, expiresAt: Date? = nil, payload: [String: Any]
    ) async throws -> (Int, String) {
        guard let host = Self.hosts[environment], let url = URL(string: "\(host)/3/device/\(deviceToken)") else {
            throw URLError(.badURL)
        }
        var request = URLRequest(url: url, timeoutInterval: 10)
        request.httpMethod = "POST"
        request.setValue("bearer \(bearer)", forHTTPHeaderField: "authorization")
        request.setValue(kind == .voip ? "\(bundleID).voip" : bundleID, forHTTPHeaderField: "apns-topic")
        let type =
            switch kind {
            case .voip: "voip"
            case .background: "background"
            case .alert: "alert"
            }
        request.setValue(type, forHTTPHeaderField: "apns-push-type")
        request.setValue(kind == .background ? "5" : "10", forHTTPHeaderField: "apns-priority")  // background must be 5
        let expiration = expiresAt.map { String(Int($0.timeIntervalSince1970)) } ?? "0"
        request.setValue(expiration, forHTTPHeaderField: "apns-expiration")
        request.httpBody = try JSONSerialization.data(withJSONObject: payload)
        let (data, response) = try await URLSession.shared.data(for: request)
        let reason = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["reason"] as? String
        return ((response as? HTTPURLResponse)?.statusCode ?? 0, reason ?? "")
    }
}

/// Why the switchboard can't push to an environment.
enum APNsSetupError: Error, CustomStringConvertible {
    case noKey(String)
    case badKey(String, Error)

    var description: String {
        switch self {
        case .noKey(let environment): "no [apns.\(environment)] key in the config"
        case .badKey(let file, let error): "can't sign with the APNs key \(file): \(error)"
        }
    }
}

private func base64url(_ data: Data) -> String {
    data.base64EncodedString()
        .replacingOccurrences(of: "+", with: "-")
        .replacingOccurrences(of: "/", with: "_")
        .replacingOccurrences(of: "=", with: "")
}

/// Rings every registered phone once, as call `callID`, or a new call, from `session` if a
/// voice server named it. Returns one line per phone.
func ring(project: String, subject: String?, voice: String?, callID: String? = nil, session: String? = nil) async -> [String] {
    let config = loadConfig()
    let callID = callID ?? UUID().uuidString.lowercased()
    var lines: [String] = []
    var clients: [String: APNs] = [:]
    for (token, device) in Devices.load().sorted(by: { $0.key < $1.key }) {
        let name = device["device"] ?? "a phone"
        let environment = device["environment"] ?? "sandbox"
        var payload = ["call_id": callID, "project": project]
        payload["subject"] = subject
        payload["voice"] = voice
        payload["session_id"] = session
        do {
            if clients[environment] == nil {
                clients[environment] = try APNs(environment: environment, config: config)
            }
            let (status, reason) = try await clients[environment]!.push(to: token, environment: environment, payload: payload)
            lines.append("call \(callID.prefix(8)) → \(name): APNs \(status) \(reason)".trimmingCharacters(in: .whitespaces))
            if reason == "BadDeviceToken" || reason == "Unregistered" {
                try? Devices.forget(token)
                lines.append("  token no longer valid; forgotten — reopen the app to register again")
            }
        } catch {
            lines.append("call \(callID.prefix(8)) → \(name): \((error as? APNsSetupError)?.description ?? error.localizedDescription)")
        }
    }
    return lines
}

/// Tells every phone that a call held outside your work hours has a question, with one
/// ordinary notification to its notification token: "<project> has a question", and the call's
/// subject. Its interruption level is the default (active), so your Focus decides whether it
/// shows and sounds, as for any app's. Tapping it opens the app, which lists the call. APNs
/// keeps it until the call would ring anyway (`until`), for a phone that's off meanwhile.
/// Returns one line per phone.
func notifyQuestion(callID: String, project: String, subject: String?, until: Date?) async -> [String] {
    let config = loadConfig()
    var lines: [String] = []
    var clients: [String: APNs] = [:]
    let alert = ["title": "\(project) has a question", "body": subject ?? "It waits for your work hours."]
    let payload: [String: Any] = ["aps": ["alert": alert, "sound": "default", "interruption-level": "active"]]
    for device in Devices.load().values.sorted(by: { ($0["device"] ?? "") < ($1["device"] ?? "") }) {
        let line = "call \(callID.prefix(8)): question → \(device["device"] ?? "a phone")"
        guard let token = device["notification_token"] else {
            lines.append("\(line): no notification token")
            continue
        }
        let environment = device["environment"] ?? "sandbox"
        do {
            if clients[environment] == nil {
                clients[environment] = try APNs(environment: environment, config: config)
            }
            let (status, reason) = try await clients[environment]!.push(
                to: token, environment: environment, kind: .alert, expiresAt: until, payload: payload)
            lines.append("\(line): APNs \(status) \(reason)".trimmingCharacters(in: .whitespaces))
        } catch {
            lines.append("\(line): \((error as? APNsSetupError)?.description ?? error.localizedDescription)")
        }
    }
    return lines
}
