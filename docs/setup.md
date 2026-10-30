# Setup

How to run ringback as built so far (Stage 3 in progress: a session with `/ringback` on
calls you when it finishes, what you tell the voice reaches the session, and you can
answer its permission prompts and questions on the call). Replace the example addresses with your own.

## You need

- **Macs with Apple silicon and Xcode** that can build for your phone's iOS version
  (Xcode 27 for iOS 27), **XcodeGen** and **uv** (`brew install xcodegen uv`), for the
  Macs that run sessions.
  The always-on Mac needs none of these: the switchboard is one binary, built elsewhere.
- **A paid Apple Developer Program account.** VoIP pushes need it.
- **An iPhone** on iOS 26 or later.
- **Nebula** on the Macs and the iPhone (Mobile Nebula):
  - the lighthouse is a relay (`relay.am_relay: true`);
  - each Mac lists the lighthouse in `relay.relays`;
  - on mobile data, the phone can only reach the Macs through that relay (see
    `docs/design.md`, Stage 0).
- **Optional, for `tools/nebula-check`:** Mobile Nebula drops all inbound traffic by
  default, so pinging the phone needs an inbound ICMP rule for the Mac.

## 1. APNs key

On developer.apple.com, go to Certificates, Identifiers & Profiles → Keys and create a key
with Apple Push Notifications service. Choose the **Sandbox** environment: builds you
install from Xcode receive their pushes from the sandbox. Download the `.p8` file (Apple
lets you do this only once) into `~/.config/ringback/`.

## 2. The iPhone app

```sh
cp app/Config/Local.xcconfig.example app/Config/Local.xcconfig   # your team ID and bundle ID
cd app && xcodegen generate
xcodebuild -project Ringback.xcodeproj -scheme Ringback -destination 'platform=iOS,id=<UDID>' \
  -derivedDataPath build -allowProvisioningUpdates build
xcrun devicectl device install app --device <UDID> build/Build/Products/Debug-iphoneos/Ringback.app
```

- `xcrun devicectl list devices` shows the UDID.
- Installing over the network needs the phone **unlocked and on the same Wi-Fi** as the
  Mac. On mobile data it shows as unavailable; a cable always works.
- If Xcode's sign-in to your Apple account has expired, signing falls back to a profile
  without push, and the build fails on the push entitlement. Sign in again in Xcode →
  Settings → Accounts.

Open the app, allow the microphone and notifications, and enter the switchboard's Nebula
address, e.g. `10.0.0.1` (port 8787 is the default). The top row turns green: "Ready for
calls". Notifications are for one warning: when the phone can't reach the switchboard over
Nebula, so calls can't connect.

**Interruption Sensitivity** sets how easily your speech stops Claude, and **Ignore
Background Noise** (on by default) keeps quieter voices and sounds than yours, such as
people nearby, from stopping it. Changes apply from the next call. During your first call,
turn on Voice Isolation in Control Center (Mic Mode), so background noise doesn't stop it.

## 3. The switchboard (one always-on Mac)

Build it on a Mac with Xcode. The binary runs on Intel and Apple silicon Macs with macOS
13 or later and needs nothing else installed:

```sh
cd switchboard && swift build -c release --arch arm64 --arch x86_64
./smoke-test.sh   # optional; --apns also checks your APNs key, without ringing anything
scp "$(swift build -c release --arch arm64 --arch x86_64 --show-bin-path)/ringback-switchboard" always-on-mac:.local/bin/
```

On the always-on Mac, copy `switchboard/switchboard.example.toml` to
`~/.config/ringback/switchboard.toml`, fill it in, put the APNs key next to it, and start
it:

```sh
~/.local/bin/ringback-switchboard serve
```

It listens only on its Nebula address. Phones register at `/register` and report call
events at `/events`, and voice servers ask it to ring at `/call`. Those are kept in `~/.local/state/ringback/` (`devices.json` and
`events.jsonl`).

Every 30 min it wakes each phone with a background push, and the app checks in at
`/check` over Nebula; if it can't, the phone warns you. `ringback-switchboard check` does
that now, and `ringback-switchboard devices` shows when each phone last checked in.

To update it, copy the new binary over the old one, stop `serve` and start it again.
Work hours (a setting in the app) need the voice servers updated too: one from before them
takes a call the switchboard holds for one that rang, so the call is lost and its session
hears it wasn't answered.

## 4. The voice server (every Mac that runs sessions)

```sh
cd voice && uv run ringback-voice serve --listen 10.0.0.2:8788 --switchboard 10.0.0.1:8787
```

`--listen` is this Mac's Nebula address, where the phone sends a call's audio, and
`--switchboard` the switchboard's. Sessions on this Mac reach the voice server through
a socket only you can open, `~/.local/state/ringback/voice.sock`.

It needs a Mac with Apple silicon, and Claude Code signed in to your Claude subscription:
the voice runs Haiku through `claude`, and Opus when you ask for a better model on the
line, so the plan must include Opus. The first start downloads the speech models
(Parakeet v2 and Kokoro) from Hugging Face. After that, loading them takes a few seconds.

The voice server answers only calls it placed itself, for a session. To try the voice on
a call rung by hand (step 6), add `--report report.txt`: a few plain sentences on what a
session did and what needs you, which the voice talks about instead. It's read again for
every call, so you can change it between calls.

To try a change without the phone, `tools/call-check` plays a scripted call through the
same pipeline, and `tools/phone-check` plays the phone's side of whole calls and times the
silence after you answer. `--socket PATH` moves the session socket, for running a second
voice server beside your own.

The app and the voice server change together: since the app connects while the phone
rings, a voice server without `/answered` stays silent on its calls. Update both.

## 5. The ringback mod (every Mac that runs sessions)

The mod is how a session calls you. It needs Claude Code 2.1.287 or later. To load it in
every session, add it to `~/.claude/settings.json`:

```json
{ "env": { "CLAUDE_CODE_PLUGIN_DIRS": "/path/to/ringback/mod" } }
```

or load it in one session with `claude --plugin-dir /path/to/ringback/mod`.

It does nothing until you run `/ringback` in a session. The dim line under the prompt
then ends with "☎ ringback on". Each time the session finishes, it writes a short
briefing, and the voice server rings your phone with it. What you ask the voice to pass on
reaches the session, and its answer comes back to the call, or a new call if you've hung
up. Each call's transcript is kept in the project's `.ringback/calls/`, which git ignores.
Run `/ringback` again when you're back.

When the session needs your permission, the prompt shows in the terminal and the voice
asks you on a call; whichever you answer first decides. Questions the session asks you
with options go to the call instead of the terminal, and show there only if the call
brings no answer. In auto mode, what auto mode approves by itself never rings.

`tools/session-check` tries a session's call without the phone.

## 6. A test call

From a session: run `/ringback`, then give the session something to do. Or ring by hand,
with the voice server started with `--report`:

```sh
~/.local/bin/ringback-switchboard ring --project myproject --subject "Test call" \
  --voice http://10.0.0.2:8788   # on the always-on Mac
```

The phone rings as "Claude from myproject". The switchboard logs ringing, answered, the
time to connected audio, and how the call ended. `ring` without `--voice` rings without
audio.
