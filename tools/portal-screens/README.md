# Portal screens

Screenshots the hub's portal (`hub/portal/`, "Status and the hub's portal" under Stages in
`docs/design.md`) in every state its status page's map shows, to review how it looks: light
and dark, on a desk (1440 px wide) and on a phone (390 px), and the Calls, Log, Setup and
sign-in pages. Headless Chrome is fine for this: it's the page's drawing, not ringing, push or
audio.

Each state is a throwaway hub (`hub/build/ringback-hub`, which it builds first) on localhost,
with its own HOME under the output folder: its config with tokens, and an account made with
`ringback-hub portal-user` as you'd answer it, the password twice and then the passcode for
the setup key it prints. On its links, stand-in Macs (agents with sessions, which accept every
offer) and speech nodes (which say their models are loaded when a call comes); a fake phone
registered over `/register` and `/check`; a live call made as from the app's Sessions tab with
`tools/phone-check`'s stand-in phone, so no push goes out; calls reported over `/events`. It
signs in through the pages in Chrome (its DevTools protocol), with a fresh passcode. Nothing
reaches Apple: the APNs key is one Apple has never seen and nothing pushes with it, or a file
that isn't a key.

The states:
- `working`: two Macs, a speech node with its models loaded, the phone, and a live call, its
  path lit; also Calls (one opened), Log and Setup (the token shown), a tablet's width, and
  the page refreshing itself as the node goes offline;
- `no-node-voice-off` and `no-node-voice-on`: no speech node ever, so calls wait (one waits
  for speech), or the phone speaks them itself;
- `node-offline`: a node that linked and went, a problem, with Forget;
- `mac-asleep`: a Mac that linked and went, asleep, quietly;
- `push-failing`: an APNs key that can't be read, and a call whose push failed on it;
- `on-hold`: held through Status's own button, then two calls placed that wait; the app's
  `/hold` answer is checked to say so; also Calls on hold, listing them;
- `on-hold-hours`: held by the phone's work hours, which start in two hours (in UTC), so the
  hold says it ends then;
- `ringing`: a call whose offer came while the phone rang, not answered yet. No push goes out
  for it, so Apple push still shows "Not tried yet", which a real ringing call wouldn't;
- `no-phone`: a Mac and a node, but no phone and no APNs key;
- `fresh`: nothing set up; first the page for making the account, then signing in on a phone,
  a wrong password first;
- `busy`: four Macs (one asleep, one with a long name), two nodes, two phones (one speaking
  calls itself), a long project and subject, and a live call, which the hub can't place on
  either phone.

It prints one line per check: signed in; the words each state's status page must show, on a
desk and on a phone; no page wider than its window; on a phone, the tabs' dock at the bottom of
the screen; the refresh; the tokens hidden. Whole-page captures on a phone leave the dock out,
which Chrome would draw where the window's bottom was when the capture began;
`status-…-phone-…-screen.png` shows the first screen with it.

Needs Google Chrome, Go, and the voice project's Python (for `aiohttp` and the stand-in
phone). Run from the repo root (about 2.5 min):

    uv run --project voice tools/portal-screens/portal_screens.py [OUT_DIR] [--only working,on-hold]

`OUT_DIR` is a new temporary folder by default; the throwaway hubs' state and logs are in its
`state/`. The screenshots are named `status-<state>-<desktop|phone>-<light|dark>.png`,
`<page>-<state>-…` and `login-…`, `code-…`, `noaccount-…`.

Results on 2026-11-02 (macOS, Chrome, one run each):
- First round: 43 checks passed, 85 screenshots, 143 s. A run before found the dock pinned to
  the top bar (its backdrop filter made the bar the dock's containing block), which the dock's
  check now catches.
- Second round, after the reviews, with `on-hold-hours` and `ringing`: 51 checks passed, 97
  screenshots, 162 s.

In each, every state's status page was looked at, in at least one of light and dark and one of
desk and phone, and each other page at least once; not every screenshot.
