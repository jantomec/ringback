# ringback

Claude Code sessions call the user's iPhone when they're done, talk the result through and
take spoken replies. `docs/design.md` is the source of truth: how it works, why, what's
turned down, the limits, what's tried on the phone (its "Status") and what's next. Read it
first. `docs/setup.md` installs it and places a test call; `docs/protocol.md` is how the
parts talk to each other.

## The parts

- `app/`: the iPhone app (SwiftUI, generated with XcodeGen), with its Control Center control
  and Lock Screen widget (`app/Controls`); its settings are in the Settings app
  (`app/Settings.bundle`).
- `hub/` (Go): one always-on Mac. Push (APNs), work hours, the hold and the calls that wait,
  the sessions you can call, each call's audio (WebRTC, Opus) and timing, the portal.
- `speech/` (Go, sherpa-onnx on CUDA): the optional speech node on a Linux machine with an
  NVIDIA GPU. Hears (Silero, Smart Turn v3, Parakeet v2) and speaks (Kokoro `af_heart`).
  Without one, the phone's On-device Voice hears and speaks, and only text crosses.
- `agent/` (Go): each Mac with sessions. The mods' socket, each call's state, and the call's
  Haiku (Opus on request) as `claude -p` on the user's subscription.
- `mod/` (TypeScript): the Claude Code mod in each session. `/ringback`, the call briefing,
  and putting permissions and questions to the call.
- `tools/`: checks and measurements, each with a README. The Python ones run in the tools'
  environment (`uv run --project tools ...`); the stand-in phone the call checks use is
  `tools/stand-in-phone`.

## Status

All of the above is built. On the phone with the current code, so far: a call placed by hand
through the agent (rang, connected while ringing, talked through with an interruption, a
message passed on once heard), a call waiting for a missing speech node and ringing when it
linked, and the portal from the phone. Not yet tried on the phone: a real session calling
through the mod, held calls and work hours, the hold set from the pill or Control Center,
call backs, Sessions, asks, Opus, weak signal, the interruption settings in real noise,
On-device Voice in a real call, and restarts. The full list is under "Status" in
`docs/design.md`; known problems left until testers report them are under "Limits and risks".

Before calling the phone, try a change offline: `tools/hub-check` for the hub's side of a
call, `tools/line-check` for a whole call through the real hub, agent and speech node with a
stand-in phone.

## Checks

Run before every commit; together they take about a minute and a half (89 s on 2026-11-03):

```sh
(cd app && xcodegen generate && xcodebuild -project Ringback.xcodeproj -scheme Ringback \
  -destination 'generic/platform=iOS' -derivedDataPath build -allowProvisioningUpdates build)  # 0 Swift warnings
claude plugin validate mod && claude plugin test mod
(cd hub && go vet ./... && go test ./... && go build -o build/ringback-hub . && ./smoke-test.sh)
(cd agent && go vet ./... && go test ./...)
uv run --project tools tools/hub-check/hub_check.py > /dev/null  # the hub's call with stand-ins
```

They prove the code builds, not that calls work. Ringing, push and audio count only when
verified on the phone (see working rules), with the user answering a real call.

## Environment

This repo is public and holds only what someone else needs to install ringback. The
user's personal setup and info (hardware, machines, Nebula addresses, accounts, location)
stay in Claude's private memory for this project. Don't commit IP addresses, hostnames,
keys or certificates.

What it needs to run is under "You need" in `docs/setup.md`.

## Working rules

- Build a rough version, say what's untested, and move on: tuning and edge cases wait
  until testers report a real problem ("Limits and risks" in `docs/design.md`).
- Verify every change to calls on the real phone (locked, on mobile data, with headphones).
  Simulator runs and unit tests don't count for ringing, push or audio.
- Facts marked unverified or "not checked" in `docs/design.md` must be checked against
  current docs before building on them.
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
