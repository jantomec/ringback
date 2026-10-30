import SwiftUI
import UIKit

@main
struct RingbackApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        WindowGroup {
            SettingsView(state: delegate.state, push: delegate.push, calls: delegate.calls)
        }
    }
}

final class AppDelegate: NSObject, UIApplicationDelegate {
    let state = AppState()
    lazy var calls = CallService(state: state)
    lazy var push = PushService(calls: calls, state: state)

    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        // A VoIP push relaunches the app in the background, so PushKit must be set up at launch.
        _ = push
        application.registerForRemoteNotifications()
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        state.notificationToken = deviceToken.map { String(format: "%02x", $0) }.joined()
        Task { await push.connect() }
    }

    /// The switchboard's background push for a connection check, which also fetches the calls
    /// that wait.
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
