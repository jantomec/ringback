# App screens

Screenshots the iPhone app in a simulator, with sample calls, in light and dark, to review
how it looks (Stage 5 step 1 in `docs/design.md`: the app as the Phone app, with Recents,
Sessions and its settings in the Settings app). A simulator is fine for this: it's SwiftUI's
drawing, not ringing, push or audio.

The project is the app's own sources (`app/Ringback`, `app/Settings.bundle`), built for a
simulator without signing, and a UI test. The app shows the samples only in a debug build,
when launched with `-RingbackSeed calls`, `problem` or `setup` (`app/Ringback/Seed.swift`);
then it doesn't reach a switchboard and asks for no permissions. The test saves, for each
appearance:
- `recents`: waiting calls (one with a question, one without), and calls answered,
  missed, declined, called back, called from Sessions, one that didn't connect, one on
  another call, from today, yesterday, this week and earlier;
- `recents-swipe`: the waiting call with a question swiped open to its "Claude App" action;
- `call-details`, `call-details-outgoing`, `waiting-details`: a call's details from its ⓘ;
- `sessions`, cards with two sessions in one project and both kinds of waiting call,
  `session-menu`, a card held, and `session-details`, from its Details;
- `banner`: Recents while the switchboard can't be reached, with Try Again;
- `setup`: a phone that isn't set up, then `settings` and `settings-more`, Ringback's page in
  the Settings app, from the banner's Open Settings.

Needs Xcode, XcodeGen and an iOS simulator. Run from the repo root (about 3 min):

    tools/app-screens/run.sh ["iPhone 17 Pro"] [OUT_DIR]

The first argument is the simulator's name, as `xcrun simctl list devices available` lists
it. The project is generated in `OUT_DIR` (a temporary folder by default), outside the repo,
with the build, the logs and the screenshots (`light-recents.png`, `dark-recents.png`…). It
boots the simulator, sets its appearance for each run, and leaves it light. It prints one line
per screen, saying whether what the test looked for was shown. It reuses the WebRTC package
from `app/build` if the app was built there.

Result on 2026-10-30, Xcode 27.1, iPhone 17 Pro on iOS 26.5, three runs (a light and a dark
each, 2.5–3 min): every screen was shown in the last two; in the first, Ringback's page in
Settings wasn't reached (below) and the simulator's own notification covered Recents' title.
In the simulator, the banner's Open Settings opened the Settings app on its first page, not on
Ringback's, so the test goes on through Apps → Ringback; on the phone it should open
Ringback's page (not checked). A simulator that has just
booted shows a notification of its own over the top for a few seconds, so the test waits 8 s
before the first screenshot.

Result on 2026-10-30, after Contacts became Sessions, with cards: one run, 2 min 20 s, every
screen shown in light and dark, the cards, a card held with its Call and Details, and the
session's details from it included (Open Settings again opened Settings' first page).
