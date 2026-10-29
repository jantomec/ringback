# Setup

How to run ringback as built so far (Stage 2 in progress: the call rings, and the voice
talks about a report you give it and answers you). Replace the example addresses with your
own.

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
- **Optional, for `tools/nebula_check.py`:** Mobile Nebula drops all inbound traffic by
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

Open the app, allow the microphone, and enter the switchboard's Nebula address, e.g.
`10.0.0.1` (port 8787 is the default). The top row turns green: "Ready for calls".

**Interruption Sensitivity** sets how easily your speech stops Claude, and
**Ignore Background Noise** (on by default) keeps quieter voices and sounds than yours,
such as people nearby, from stopping it. Changes apply from the next call. During your first call, turn on Voice Isolation
in Control Center (Mic Mode), so background noise doesn't stop it.

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
events at `/events`. Those are kept in `~/.local/state/ringback/` (`devices.json` and
`events.jsonl`).

## 4. The voice server (every Mac that runs sessions)

```sh
cd voice && uv run ringback-voice serve --listen 10.0.0.1:8788 --report report.txt   # this Mac's Nebula address
```

It needs a Mac with Apple silicon, and Claude Code signed in to your Claude subscription:
the voice runs Haiku through `claude`. The first start downloads the speech models
(Parakeet v2 and Kokoro) from Hugging Face. After that, loading them takes a few seconds.

Until sessions call (Stage 3), the voice talks about `report.txt`: a few plain sentences
on what a session did and what needs you. It's read again for every call, so you can
change it between calls. Without `--report`, the voice says there's nothing to report.

To try a change without the phone, `tools/call-check` plays a scripted call through the
same pipeline.

## 5. A test call

```sh
~/.local/bin/ringback-switchboard ring --project myproject --subject "Test call" \
  --voice http://10.0.0.1:8788   # on the always-on Mac
```

The phone rings as "Claude from myproject". The switchboard logs ringing, answered, the
time to connected audio, and how the call ended. `ring` without `--voice` rings without
audio.
