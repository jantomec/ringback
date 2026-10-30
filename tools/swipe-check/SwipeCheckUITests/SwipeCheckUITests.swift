import XCTest

/// Swipes each row open and taps its action. Prints one line per row ("SWIPE-CHECK …"): whether
/// the action showed, whether XCUITest reads it as enabled, and what ran after the tap. With
/// SWIPE_CHECK_OUT set (run.sh sets it), it saves a screenshot of each row swiped open there.
final class SwipeCheckUITests: XCTestCase {
    func testDisabledSwipeAction() throws {
        let out = ProcessInfo.processInfo.environment["SWIPE_CHECK_OUT"]
        for name in ["Disabled row", "Enabled row", "Greyed row"] {
            let app = XCUIApplication()
            app.launch()
            let row = app.buttons[name]
            XCTAssertTrue(row.waitForExistence(timeout: 10), "\(name) isn't shown")
            row.swipeLeft()
            let action = app.buttons["Claude App"]
            let shown = action.waitForExistence(timeout: 5)
            sleep(1)  // the swipe's animation
            if let out {
                let file = URL(fileURLWithPath: out).appending(path: "\(name.replacingOccurrences(of: " ", with: "-")).png")
                try? XCUIScreen.main.screenshot().pngRepresentation.write(to: file)
            }
            let enabled = shown && action.isEnabled
            if shown { action.tap() }
            sleep(1)
            let ran = app.staticTexts["ran"].label
            print("SWIPE-CHECK \(name): shown \(shown), isEnabled \(enabled), after the tap \"\(ran)\"")
            app.terminate()
        }
    }
}
