import Foundation
import UserNotifications

/// Whether this phone can still reach the switchboard over Nebula, checked in the background.
/// Every 30 min the switchboard wakes the app with a background push, and the app calls it
/// back over Nebula, the way a call reaches the Mac. If it can't, Nebula is most likely off
/// and calls can't connect, so it warns you with a notification.
enum ConnectionCheck {
    private static let warningID = "connection"
    /// When the last warning was shown, in seconds since 1970; 0 or missing while there's
    /// none. The main screen shows a banner while there is one.
    static let lastWarnedKey = "lastConnectionWarning"
    static let title = "Claude can't call you"
    static let message = "Ringback can't reach your switchboard over Nebula. Check that Nebula is on."
    /// A warning you dismissed comes back after this long if the problem stays.
    private static let warnAgainAfter: TimeInterval = 3 * 3600

    static func requestPermission() async {
        _ = try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound])
    }

    /// Runs one check: tries to reach the switchboard a few times, within the half minute
    /// iOS gives a background push, and warns you if it can't. A network change can keep
    /// Nebula from getting through for a few seconds.
    static func run(token: String?) async -> Bool {
        for delay in [0.0, 3, 6] {
            try? await Task.sleep(for: .seconds(delay))
            if (try? await Switchboard.check(token: token)) != nil {
                clear()
                return true
            }
        }
        await warn()
        return false
    }

    /// Takes the warning away once the switchboard is reachable again.
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
