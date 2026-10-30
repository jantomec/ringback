# Swipe check

Checks what `.disabled` does to a SwiftUI swipe action (`.swipeActions` on a List row), for
the app's "Claude App" action on a waiting call with a question, which works only while
Remote Control is on in that call's session (Stage 3 step 7 in `docs/design.md`). Apple's
docs don't say whether a disabled swipe action greys out, or whether its tap is blocked.

A small app in a simulator shows three rows like the app's waiting calls, in a Form, each a
Button with a trailing swipe action "Claude App" (`allowsFullSwipe: false`, as in the app):
one enabled, one `.disabled(true)`, and one enabled but tinted grey. A UI test swipes each
open, saves a screenshot, reads whether XCUITest sees the action as enabled, taps it, and
reads which action ran. A simulator is fine for this: it's SwiftUI's drawing and touch
handling, not ringing, push or audio.

Needs Xcode, XcodeGen and an iOS simulator. Run from the repo root (about 1 min each):

    tools/swipe-check/run.sh ["iPhone 17,OS=26.5"] [OUT_DIR]

The first argument is the simulator, as `xcodebuild -destination` names it after
`platform=iOS Simulator,name=`; `xcrun simctl list devices available` lists them. The project
is generated in `OUT_DIR` (a temporary folder by default), outside the repo, with the build,
the log and the screenshots. It prints one line per row.

Result on 2026-10-30, Xcode 27.1, on iOS 26.5 (iPhone 17) and iOS 27.1 (iPhone Duo), the same
on both:

    Disabled row: shown true, isEnabled true, after the tap "ran: nothing"
    Enabled row: shown true, isEnabled true, after the tap "ran: action of Enabled row"
    Greyed row: shown true, isEnabled true, after the tap "ran: action of Greyed row"

So `.disabled` blocks the tap but doesn't grey the action out: in the screenshots it looks
exactly like the enabled one, in its full tint, and XCUITest reads it as enabled too. A
button that looks ready and does nothing is worse than either, so the app doesn't use
`.disabled` there: while Remote Control was off, the action was tinted grey, and its tap only
explained, in an alert. Retry if a new iOS greys disabled swipe actions. Since then ringback
turns Remote Control on itself, so the app no longer tells: the action is always available.
