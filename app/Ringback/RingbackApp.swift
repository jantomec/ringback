import SwiftUI
import UIKit

@main
struct RingbackApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        WindowGroup {
            SettingsView(state: delegate.state, push: delegate.push)
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
        return true
    }
}
