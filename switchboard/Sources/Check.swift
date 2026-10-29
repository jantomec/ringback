import Foundation

/// How often `serve` asks each phone to check that it can still reach the switchboard over
/// Nebula. iOS delivers an app at most a few background pushes an hour.
let checkEvery: TimeInterval = 30 * 60

/// Asks every phone that gave a notification token to check, with a background push. The app
/// wakes for a moment and calls /check over Nebula, the way a call reaches the Mac. If it
/// can't, it warns you itself, so nothing here needs to hear back. Returns one line per phone.
func askPhonesToCheck(config: [String: String]) async -> [String] {
    var lines: [String] = []
    var clients: [String: APNs] = [:]
    for device in Devices.load().values.sorted(by: { ($0["device"] ?? "") < ($1["device"] ?? "") }) {
        guard let token = device["notification_token"] else { continue }
        let name = device["device"] ?? "a phone"
        let environment = device["environment"] ?? "sandbox"
        do {
            if clients[environment] == nil {
                clients[environment] = try APNs(environment: environment, config: config)
            }
            let payload: [String: Any] = ["aps": ["content-available": 1], "check": "nebula"]
            let (status, reason) = try await clients[environment]!.push(
                to: token, environment: environment, kind: .background, payload: payload)
            lines.append("check → \(name): APNs \(status) \(reason)".trimmingCharacters(in: .whitespaces))
        } catch {
            lines.append("check → \(name): \(error)")
        }
    }
    return lines
}

/// Runs the check every `checkEvery` for as long as the switchboard serves. Logs only what
/// went wrong; `devices` shows when each phone last checked in.
func checkPeriodically() {
    Task.detached {
        while true {
            try? await Task.sleep(for: .seconds(checkEvery))
            for line in await askPhonesToCheck(config: loadConfig()) where !line.hasSuffix("APNs 200") {
                log(line)
            }
        }
    }
}
