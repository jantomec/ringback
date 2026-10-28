import CryptoKit
import Foundation

/// VoIP pushes through APNs, with token-based (JWT) authentication. URLSession speaks the
/// HTTP/2 that APNs requires.
struct APNs {
    static let hosts = [
        "sandbox": "https://api.sandbox.push.apple.com",
        "production": "https://api.push.apple.com",
    ]

    let topic: String
    /// One provider token per run. Apple refuses new tokens more often than every 20 minutes
    /// on a connection, and wants a fresh one at least every 60.
    let bearer: String

    init(teamID: String, keyID: String, keyFile: String, bundleID: String) throws {
        topic = "\(bundleID).voip"
        let key = try P256.Signing.PrivateKey(pemRepresentation: String(contentsOfFile: expand(keyFile), encoding: .utf8))
        let header = base64url(try JSONSerialization.data(withJSONObject: ["alg": "ES256", "kid": keyID]))
        let claims = base64url(try JSONSerialization.data(withJSONObject: ["iss": teamID, "iat": Int(Date().timeIntervalSince1970)]))
        let signature = try key.signature(for: Data("\(header).\(claims)".utf8))
        bearer = "\(header).\(claims).\(base64url(signature.rawRepresentation))"
    }

    /// Sends one VoIP push; returns APNs' status and its reason if it refused.
    func push(to deviceToken: String, environment: String, payload: [String: String]) async throws -> (Int, String) {
        guard let host = Self.hosts[environment], let url = URL(string: "\(host)/3/device/\(deviceToken)") else {
            throw URLError(.badURL)
        }
        var request = URLRequest(url: url, timeoutInterval: 10)
        request.httpMethod = "POST"
        request.setValue("bearer \(bearer)", forHTTPHeaderField: "authorization")
        request.setValue(topic, forHTTPHeaderField: "apns-topic")
        request.setValue("voip", forHTTPHeaderField: "apns-push-type")
        request.setValue("10", forHTTPHeaderField: "apns-priority")
        request.setValue("0", forHTTPHeaderField: "apns-expiration")
        request.httpBody = try JSONSerialization.data(withJSONObject: payload)
        let (data, response) = try await URLSession.shared.data(for: request)
        let reason = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["reason"] as? String
        return ((response as? HTTPURLResponse)?.statusCode ?? 0, reason ?? "")
    }
}

private func base64url(_ data: Data) -> String {
    data.base64EncodedString()
        .replacingOccurrences(of: "+", with: "-")
        .replacingOccurrences(of: "/", with: "_")
        .replacingOccurrences(of: "=", with: "")
}

/// Rings every registered phone once.
func ring(project: String, subject: String?, voice: String?) async {
    let config = loadConfig()
    let known = Devices.load()
    if known.isEmpty { fail("No phone registered yet: open the app and enter the switchboard address.") }
    var clients: [String: APNs] = [:]
    for (token, device) in known.sorted(by: { $0.key < $1.key }) {
        let name = device["device"] ?? "a phone"
        let environment = device["environment"] ?? "sandbox"
        if clients[environment] == nil {
            guard let team = config["apns.team_id"], let bundle = config["apns.bundle_id"],
                let keyID = config["apns.\(environment).key_id"], let keyFile = config["apns.\(environment).key_file"]
            else {
                print("skipping \(name): no [apns.\(environment)] key in the config")
                continue
            }
            do {
                clients[environment] = try APNs(teamID: team, keyID: keyID, keyFile: keyFile, bundleID: bundle)
            } catch {
                fail("can't sign with the APNs key \(keyFile): \(error)")
            }
        }
        let callID = UUID().uuidString.lowercased()
        var payload = ["call_id": callID, "project": project]
        payload["subject"] = subject
        payload["voice"] = voice
        do {
            let (status, reason) = try await clients[environment]!.push(to: token, environment: environment, payload: payload)
            print("call \(callID.prefix(8)) → \(name): APNs \(status) \(reason)".trimmingCharacters(in: .whitespaces))
            if reason == "BadDeviceToken" || reason == "Unregistered" {
                try? Devices.forget(token)
                print("  token no longer valid; forgotten — reopen the app to register again")
            }
        } catch {
            print("call \(callID.prefix(8)) → \(name): \(error.localizedDescription)")
        }
    }
}
