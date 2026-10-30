# ringback

Claude Code sessions call the user's iPhone when they're done, talk the result through and
take spoken replies. `docs/design.md` is the source of truth for requirements,
architecture, rejected alternatives, risks and the stage plan. Read it first.

Status (2026-10-30): Stages 0 (Nebula) and 1 (ring + audio) are done. **Stage 3 (one
session) is in progress**, as five steps under Stage 3 in `docs/design.md`. Steps 1 and 2
are built, checked offline (`tools/session-check`, `tools/call-check --session`) and
worked on the phone (three calls, including the session calling again after you hung up);
still untried: the app reaching the Mac through a stale Nebula tunnel (the first test
call's audio failed on one). Built after that and tried on the phone (two calls; offline
with `tools/phone-check`): Haiku starts when the call is placed and thinks of its opening
while the phone rings, the app connects the audio while it rings, only over Nebula, and
tells the Mac when you answer, which sends nothing until then; the voice started 0.2–0.4 s
after the Mac heard you answer, and sounded clean. The session is told how each call went
in a note that starts no turn. Step 3 is built, tried offline (`tools/session-check
--asks`, `tools/ask-check`, `tools/call-check`) and on the phone (two calls): a permission
prompt shows in the terminal and goes to the call at once, whichever answers first
decides; the session's questions (AskUserQuestion) go to the call instead of the terminal.
Step 4 (a better model on the line: Opus resumes the call's session) is built and tried
offline (`tools/call-check`, `tools/handover-check`), not yet on the phone. Also built
and tried offline only (`tools/ask-check`, `tools/phone-check --back`): work hours, set in
the app, outside which the switchboard holds a call and the calling Mac asks again when they
start; and step 5, calling back a call you didn't answer from the app's call history, where
what the missed call asked waits for the call back while the session's mod holds it (15 min).
How it works:
the ringback mod (`mod/`) adds `/ringback`; while it's on, each finished turn has the
session write a call briefing, and the voice server places the call through the
switchboard's `/call`. On the call, the voice passes what you say to the session with its
one tool, held until you've heard it announced; the session's answer comes back to the
call, or the session calls again if you've hung up; the transcript goes to the project's
`.ringback/calls/`. Mods replaced the planned hooks and MCP channel, and a tool replaced
tags in Haiku's reply (rejected alternatives). Stage 2 (conversation) works: Parakeet v2 →
Haiku → Kokoro `af_heart` on the phone, interruptions included (`voice/ringback_voice/`,
results under Stage 2 in `docs/design.md`). Its last test is left open: two interruption
settings are built and checked offline, not yet on the phone: the sensitivity, and a
background filter ("Ignore Background Noise"), a rough level gate that ignores speech
quieter than yours. They still need a phone test with gym-like noise and with someone
talking nearby. Tuning the filter waits until testers report a problem. VoxLock (a model
keeping only your voice, from a sample recorded in the app) is parked on the branch
`voxlock`; see rejected alternatives in `docs/design.md`.

The app warns you when calls can't reach the phone because Nebula is off: the switchboard
wakes it every 30 min to check (the connection check in `docs/design.md`).

Left until testers report them as real problems, because the calls so far went fine:
- very short words can go unheard: a recorded "Great." offline, and once "wait" on the
  phone while the voice talked (Silero needs 0.2 s of speech to start a turn);
- a stop where no words are recognised should make the voice carry on (built, untested).

Try a change offline with `tools/call-check` before calling the phone.

Code: `app/` (iPhone app, generated with XcodeGen), `switchboard/` (push and call events;
one Swift binary that needs nothing but macOS), `voice/` (the calling Mac's audio and
voice), `mod/` (the Claude Code mod in each session, TypeScript), `tools/` (checks for
Nebula, speech-to-text, speech speed, reply speed, briefings, scripted calls
(`call-check`), sessions' calls and asks (`session-check`), the phone's side of calls
(`phone-check`) what sessions ask the voice server (`ask-check`) and Opus taking a call
over (`handover-check`)). How to set it all up and place a test call: `docs/setup.md`.

## Checks

Run before every commit; together they take a few seconds:

```sh
(cd app && xcodegen generate && xcodebuild -project Ringback.xcodeproj -scheme Ringback \
  -destination 'generic/platform=iOS' -derivedDataPath build -allowProvisioningUpdates build)  # 0 Swift warnings
(cd switchboard && swift build -c release --arch arm64 --arch x86_64 && ./smoke-test.sh)  # 1 expected warning: x86_64 deprecated
(cd voice && uv run python -m compileall -q ringback_voice && uv run python -c 'import ringback_voice.__main__')
claude plugin validate mod && claude plugin test mod
```

They prove the code builds, not that calls work. Ringing, push and audio count only when
verified on the phone (see working rules), with the user answering a real call.

## Environment

This repo is public and holds only what someone else needs to install ringback. The
user's personal setup and info (hardware, machines, Nebula addresses, accounts, location)
stay in Claude's private memory for this project. Don't commit IP addresses, hostnames,
keys or certificates.

Requirements: a Mac with Apple silicon and Xcode, a paid Apple Developer Program account, an iPhone, and
Nebula on both the Mac and the iPhone, with a lighthouse that acts as a relay, and Claude Code
on a Claude subscription that includes Opus (for a better model on the line).

## Working rules

- Build a rough version, say what's untested, and move on: tuning and edge cases wait
  until testers report a real problem (the list under Status).
- Verify every stage on the real phone (locked, on mobile data, with headphones).
  Simulator runs and unit tests don't count for ringing, push or audio.
- Facts marked unverified in `docs/design.md` must be checked against current docs
  before building on them.
- A script that measures or checks something for a decision (a benchmark, an evaluation,
  a network check) goes in `tools/<name>/` from the start, with a README that says how to
  rerun it, and the design cites it next to its results. Look at `tools/` before writing
  one. The scratchpad is only for throwaway output such as audio, logs and test state.
- Before every commit, scan the full content of every staged file and the commit message
  for personal details. The list of strings to look for is in Claude's private memory,
  because the list itself is personal.
- Git: work on a short branch, merge it fast-forward into `main`, delete the branch, and
  push when the user asks.
- Installing on the phone over the network needs it unlocked and on the same Wi-Fi as the
  Mac. Ask the user for that before installing, and for the test setup before a test call.
