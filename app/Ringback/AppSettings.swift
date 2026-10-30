import Foundation

/// The app's settings are in the Settings app (Settings → Apps → Ringback), from
/// Settings.bundle: the switchboard's address, work hours, interruption sensitivity and the
/// background filter. They're kept under the keys the app used when it had a settings screen
/// of its own, so values carry over, and the app reads them when it needs them. The Settings
/// app shows a bundle's defaults but doesn't store them, so the app registers the same ones at
/// launch.
enum AppSettings {
    private static let migratedKey = "settingsMigrated"

    static func register() {
        UserDefaults.standard.register(defaults: [
            WorkHours.onKey: false,
            WorkHours.fromKey: WorkHours.defaultFrom,
            WorkHours.toKey: WorkHours.defaultTo,
            AudioLink.sensitivityKey: AudioLink.defaultSensitivity,
            AudioLink.backgroundFilterOnKey: true,
            AudioLink.backgroundFilterKey: AudioLink.defaultBackgroundFilter,
        ])
        migrate()
    }

    /// Once, what the app's own settings screen kept, as the Settings app shows it: work hours
    /// on its 15-minute steps (the app's time pickers took any minute), and the switchboard's
    /// address as you'd type it, rather than as a URL.
    private static func migrate() {
        let defaults = UserDefaults.standard
        guard defaults.integer(forKey: migratedKey) < 1 else { return }
        for key in [WorkHours.fromKey, WorkHours.toKey] {
            if let time = defaults.string(forKey: key), let step = WorkHours.quarter(time), step != time {
                defaults.set(step, forKey: key)
            }
        }
        if defaults.string(forKey: Switchboard.urlKey)?.contains("://") == true, let host = Switchboard.displayHost {
            defaults.set(host, forKey: Switchboard.urlKey)
        }
        defaults.set(1, forKey: migratedKey)
    }
}
