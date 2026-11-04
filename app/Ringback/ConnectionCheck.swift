import Foundation
import UserNotifications

/// Whether this phone can still reach the hub, checked in the background. Every 30 min the
/// hub wakes the app with a background push, and the app calls it back, the way a call reaches
/// it. If it can't, the VPN it reaches the hub over (Nebula) is most likely off, or for a hub on
/// a public address the phone is offline, and calls can't connect, so it warns you with a
/// notification.
enum ConnectionCheck {
    private static let warningID = "connection"
    /// When the last warning was shown, in seconds since 1970; 0 or missing while there's
    /// none. The main screen shows a banner while there is one.
    static let lastWarnedKey = "lastConnectionWarning"
    static let title = "Claude can't call you"
    static var message: String { "Ringback can't reach your hub. " + hint }
    /// What to check when the hub can't be reached: the VPN for a hub on a private address,
    /// the phone's connection and the hub for one on a public address, as the hub's address
    /// was last looked up (`AudioLink.overVPN`).
    static var hint: String {
        overVPN ? "If you reach it over Nebula or another VPN, check that it's on." : "Check the phone's connection, and that the hub is running."
    }
    static let overVPNKey = "hubOverVPN"
    static var overVPN: Bool { UserDefaults.standard.object(forKey: overVPNKey) as? Bool ?? true }
    /// A warning you dismissed comes back after this long if the problem stays.
    private static let warnAgainAfter: TimeInterval = 3 * 3600

    static func requestPermission() async {
        _ = try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])
    }

    /// Runs one check: tries to reach the hub a few times, within the half minute
    /// iOS gives a background push, and warns you if it can't. A network change can keep
    /// Nebula from getting through for a few seconds.
    static func run(token: String?) async -> Bool {
        for delay in [0.0, 3, 6] {
            try? await Task.sleep(for: .seconds(delay))
            do {
                try await Switchboard.check(token: token)
            } catch is NotPaired {
                // Reached: the app's banner asks you to pair, so no warning that it can't be.
            } catch {
                continue
            }
            clear()
            return true
        }
        // Says what to check by where the hub is, if its address can be looked up now.
        if let url = Switchboard.url {
            _ = await AudioLink.overVPN(url)
        }
        await warn()
        return false
    }

    /// Takes the warning away once the hub is reachable again.
    static func clear() {
        UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: [warningID])
        UserDefaults.standard.removeObject(forKey: lastWarnedKey)
    }

    private static func warn() async {
        let defaults = UserDefaults.standard
        let last = defaults.double(forKey: lastWarnedKey)
        if last > 0, Date.now.timeIntervalSince1970 - last < warnAgainAfter { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = message
        content.sound = .default
        try? await UNUserNotificationCenter.current().add(
            UNNotificationRequest(identifier: warningID, content: content, trigger: nil))
        defaults.set(Date.now.timeIntervalSince1970, forKey: lastWarnedKey)
    }
}
