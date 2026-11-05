import SwiftUI
import UIKit
import UserNotifications

@main
struct RingbackApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        WindowGroup {
            RootView(state: delegate.state, push: delegate.push, calls: delegate.calls)
        }
    }
}

final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    let state = AppState()
    lazy var calls = CallService(state: state)
    lazy var push = PushService(calls: calls, state: state)

    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        // The settings' defaults, before anything reads them (AppSettings).
        AppSettings.register()
        #if DEBUG
        if Seed.isOn { Seed.fill(state) }
        #endif
        // A VoIP push relaunches the app in the background, so PushKit must be set up at launch.
        _ = push
        UNUserNotificationCenter.current().delegate = self
        application.registerForRemoteNotifications()
        return true
    }

    /// What a session shows you shows while the app is open too, and joins its list; other
    /// notifications don't, since the app says the same.
    func userNotificationCenter(
        _ center: UNUserNotificationCenter, willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        guard notification.request.content.userInfo["shown"] != nil else { return [] }
        await state.refreshShown()
        return [.banner, .list, .sound]
    }

    /// Tapping what a session showed you opens it.
    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        guard let id = response.notification.request.content.userInfo["shown"] as? String else { return }
        await state.openShown(id: id)
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        state.notificationToken = deviceToken.map { String(format: "%02x", $0) }.joined()
        Task { await push.connect() }
    }

    /// The hub's background push for a connection check, which also fetches the calls
    /// that wait. Work hours changed in the Settings app go with it, at the latest every 30 min.
    func application(
        _ application: UIApplication, didReceiveRemoteNotification userInfo: [AnyHashable: Any],
        fetchCompletionHandler completion: @escaping (UIBackgroundFetchResult) -> Void
    ) {
        guard userInfo["check"] != nil else { return completion(.noData) }
        let token = state.token
        Task { @MainActor [state] in
            let reached = await ConnectionCheck.run(token: token)
            if reached { await state.refreshWaiting() }
            completion(reached ? .newData : .failed)
        }
    }

    func applicationWillTerminate(_ application: UIApplication) {
        calls.appWillTerminate()
    }
}
