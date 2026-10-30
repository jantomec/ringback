import XCTest

/// Screenshots the app with sample calls (the app's debug-only Seed), in the simulator's
/// current appearance: Recents, a waiting call swiped open, a call's and a waiting call's
/// details, Contacts and a contact's details, the connection banner, the banner of a phone
/// that isn't set up, and Ringback's page in the Settings app. Saves each as
/// `<look>-<name>.png` in APP_SCREENS_OUT (run.sh sets it, and APP_SCREENS_LOOK), and prints
/// one line per screen ("APP-SCREENS …") saying whether what it looked for was there.
final class AppScreensUITests: XCTestCase {
    private var out: URL!
    private var look = "light"

    override func setUp() {
        continueAfterFailure = true
        let env = ProcessInfo.processInfo.environment
        out = URL(fileURLWithPath: env["APP_SCREENS_OUT"] ?? NSTemporaryDirectory())
        look = env["APP_SCREENS_LOOK"] ?? "light"
    }

    func testScreens() throws {
        let app = launch("calls")
        note("recents", app.navigationBars["Recents"].waitForExistence(timeout: 10))
        sleep(8)  // a simulator just booted shows a notification of its own over the top for a while
        shot("recents")

        // The waiting call with a question, swiped open to its Claude App action.
        let question = app.buttons.matching(NSPredicate(format: "label CONTAINS 'Question'")).firstMatch
        note("waiting call with a question", question.waitForExistence(timeout: 5))
        question.swipeLeft()
        note("Claude App action", app.buttons["Claude App"].waitForExistence(timeout: 5))
        sleep(1)  // the swipe's animation
        shot("recents-swipe")
        question.swipeRight()
        sleep(1)

        // Details: the rows' ⓘ, in order: the two waiting calls, then the calls.
        let details = app.buttons.matching(identifier: "Details")
        details.element(boundBy: 3).tap()  // the second call: an answered one
        note("call details", app.staticTexts["Incoming Call"].waitForExistence(timeout: 5))
        shot("call-details")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        details.element(boundBy: 2).tap()  // the first call: one made from Contacts
        note("outgoing call details", app.staticTexts["Called from Contacts"].waitForExistence(timeout: 5))
        shot("call-details-outgoing")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        details.element(boundBy: 0).tap()  // the waiting call with a question
        note("waiting call details", app.staticTexts["Rings"].waitForExistence(timeout: 5))
        shot("waiting-details")
        app.navigationBars.buttons.element(boundBy: 0).tap()

        app.tabBars.buttons["Contacts"].tap()
        note("contacts", app.navigationBars["Contacts"].waitForExistence(timeout: 5))
        shot("contacts")
        app.buttons.matching(identifier: "Details").element(boundBy: 2).tap()  // ringback, with a briefing
        note("contact details", app.staticTexts["Latest Briefing"].waitForExistence(timeout: 5)
            || app.staticTexts["LATEST BRIEFING"].exists)
        shot("contact-details")
        app.terminate()

        let problem = launch("problem")
        note("connection banner", problem.buttons["Try Again"].waitForExistence(timeout: 10))
        shot("banner")
        problem.terminate()

        let setup = launch("setup")
        let open = setup.buttons["Open Settings"]
        note("set-up banner", open.waitForExistence(timeout: 10))
        shot("setup")
        open.tap()
        let settings = XCUIApplication(bundleIdentifier: "com.apple.Preferences")
        if !settings.staticTexts["Work Hours"].waitForExistence(timeout: 5) {
            // In the simulator it can open on Settings' first page instead: Apps, then Ringback.
            print("APP-SCREENS \(look): Open Settings opened Settings' first page, not Ringback's")
            reach(settings.staticTexts["Apps"], in: settings)
            reach(settings.staticTexts["Ringback"], in: settings)
        }
        note("Ringback's page in Settings", settings.staticTexts["Work Hours"].waitForExistence(timeout: 10))
        sleep(1)
        shot("settings")
        settings.swipeUp()
        sleep(1)
        shot("settings-more")
        settings.terminate()
    }

    private func launch(_ seed: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-RingbackSeed", seed]
        app.launch()
        return app
    }

    /// Scrolls until the element can be tapped, then taps it.
    private func reach(_ element: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<15 where !(element.exists && element.isHittable) {
            app.swipeUp()
        }
        if element.exists { element.tap() }
    }

    private func shot(_ name: String) {
        let file = out.appending(path: "\(look)-\(name).png")
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: file)
    }

    private func note(_ what: String, _ found: Bool) {
        print("APP-SCREENS \(look): \(what): \(found ? "shown" : "NOT FOUND")")
    }
}
