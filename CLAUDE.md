# ringback

Claude Code sessions call the user's iPhone when they're done, talk the result through and
take spoken replies. `docs/design.md` is the source of truth for requirements,
architecture, rejected alternatives, risks and the stage plan. Read it first.

Status (2026-10-28, `e42b14e`): Stages 0 (Nebula) and 1 (ring + audio) are done, with
results in `docs/design.md`. Next is **Stage 2 (conversation)**. Resume by replacing the
test pipeline in `voice/ringback_voice/__main__.py`, which greets you and plays your words
back, with Parakeet v2 → Haiku → Kokoro `af_heart`:
- Haiku runs in a Claude Code session that stays open for the call;
- Kokoro speaks in the growing pieces described under "Voice pipeline" in the design;
- new code: Pipecat services for Parakeet (speech-to-text), for Kokoro through mlx-audio
  (Pipecat's own Kokoro service is too slow) and around the Claude Code process.

Code: `app/` (iPhone app, generated with XcodeGen), `switchboard/` (push and call
events; one Swift binary that needs nothing but macOS), `voice/` (the calling Mac's audio
and voice), `tools/` (Nebula, speech-to-text and speech-speed checks). How to set it all
up and place a test call: `docs/setup.md`.

## Checks

Run before every commit; together they take a few seconds:

```sh
(cd app && xcodegen generate && xcodebuild -project Ringback.xcodeproj -scheme Ringback \
  -destination 'generic/platform=iOS' -derivedDataPath build -allowProvisioningUpdates build)  # 0 Swift warnings
(cd switchboard && swift build -c release --arch arm64 --arch x86_64 && ./smoke-test.sh)  # 1 expected warning: x86_64 deprecated
(cd voice && uv run python -m compileall -q ringback_voice && uv run python -c 'import ringback_voice.__main__')
```

They prove the code builds, not that calls work. Ringing, push and audio count only when
verified on the phone (see working rules), with the user answering a real call.

## Environment

This repo is public and holds only what someone else needs to install ringback. The
user's personal setup and info (hardware, machines, Nebula addresses, accounts, location)
stay in Claude's private memory for this project. Don't commit IP addresses, hostnames,
keys or certificates.

Requirements: a Mac with Xcode, a paid Apple Developer Program account, an iPhone, and
Nebula on both the Mac and the iPhone, with a lighthouse that acts as a relay, and Claude Code
on a Claude subscription.

## Working rules

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
