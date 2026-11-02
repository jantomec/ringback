# App screens

Screenshots the iPhone app in a simulator, with sample calls, in light and dark, to review
how it looks (Stage 5 step 1 in `docs/design.md`: the app as the Phone app, with Recents,
Sessions and its settings in the Settings app). A simulator is fine for this: it's SwiftUI's
drawing, not ringing, push or audio.

The project is the app's own sources (`app/Ringback`, `app/Settings.bundle`), built for a
simulator without signing, and a UI test. The app shows the samples only in a debug build,
when launched with `-RingbackSeed calls`, `problem`, `speech` or `setup` (`app/Ringback/Seed.swift`);
then it doesn't reach a switchboard and asks for no permissions. The test saves, for each
appearance:
- `recents`: waiting calls (one with a question, one without) under Waiting, and under
  Calls, calls answered, missed, declined, called back, called from Sessions, one that
  didn't connect, one on another call, from today, yesterday, this week and earlier; and the
  hold switch beside the tabs, on hold until tomorrow's work hours;
- `recents-swipe`: the waiting call with a question swiped open to its "Claude App" action;
- `call-details`, `call-details-outgoing`, `waiting-details`: a call's details from its ⓘ;
- `sessions`, cards with two sessions in one project and both kinds of waiting call,
  `session-menu`, a card held, and `session-details`, from its Details;
- `banner`: Recents while the switchboard can't be reached, with Try Again, and the hold
  switch taking calls until 19:00;
- `speech-banner`: no speech computer linked, a call waiting for it, and the microphone off:
  the speech banner with "1 More Problem", then `speech-banner-all` with both;
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

2026-10-31, with Recents' Waiting and Calls headers (one run, 2 min 26 s): every screen was
shown, the headers in both appearances, and the Claude App swipe under Waiting.

2026-11-02, with the hold switch (one run, 2 min 23 s): every screen was shown; the bar was on
hold in `recents` and taking calls in `banner`, in both appearances. The Control Center
control can't be shown here: it's for the phone. Then with the switch as a pill beside the
tabs, which are pills too, instead of the system tab bar (one run, 2 min 18 s): every screen
was shown, the test now finding the Sessions tab among the buttons; the pill was on hold
with its badge in `recents` and `sessions`, and taking calls in `banner`.

Result on 2026-10-30, after Contacts became Sessions, with cards: one run, 2 min 20 s, every
screen shown in light and dark, the cards, a card held with its Call and Details, and the
session's details from it included (Open Settings again opened Settings' first page).

## The breathing light

`breath_check.py` measures the hold's breathing light, after `run.sh` has installed the
screenshot build in a booted simulator: it launches the app on hold, in dark mode, records the
screen, and reads the blue channel just above the pill, mid-screen and in the far corner of
every frame. It prints each one's range and period, and how far the background lags the pill,
their correlation and its share of the pill's swing (the wash should beat with the pill, at the
same period and phase, faintly). Needs the voice project's Python:

    uv run --project voice tools/app-screens/breath_check.py [SECONDS] [--keep VIDEO]

Result on 2026-11-02 (iPhone 17 Pro simulator, 12 s, 20 s in all): above the pill, blue 0–76,
period 4.47 s; mid-screen 2–31, 4.50 s, 0.00 s behind, correlation 0.99, 37% of the swing; the
far corner 13–24, 4.57 s, 0.00 s behind, correlation 0.99, 14%. The set period is 4.6 s; 12 s
holds only about 2.6 breaths.
