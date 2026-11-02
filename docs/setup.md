# Setup

How to run ringback: a session with `/ringback` on calls your iPhone when it finishes, you
talk it through, and what you say reaches the session. Four parts run on your machines, and
the app on your phone:

| Part | Runs on | Does |
|---|---|---|
| **hub** | one always-on Mac, Intel or Apple silicon | rings the phone (APNs), keeps the calls that wait, and carries each call's audio |
| **speech node** | a Linux machine with an NVIDIA GPU | hears you (Silero, Smart Turn, Parakeet) and speaks (Kokoro), for the hub |
| **agent** | each Mac that runs Claude Code sessions | the sessions' side: their briefings and questions, and the call's Haiku and Opus through Claude Code |
| **mod** | each Claude Code session | adds `/ringback`, and hands the agent the session's briefings |
| **app** | your iPhone | rings like a phone call, and lists your calls and sessions |

The speech node and the agents dial out to the hub, and the phone reaches only the hub. The
steps below set them up in that order. Commands run from the repository's folder, unless they
say where else. Replace the example addresses (`10.0.0.1` for the hub) with your own.

## You need

- **A Mac with Xcode** that can build for your phone's iOS version (Xcode 27 for iOS 27),
  with **XcodeGen**, **Go**, **CMake** and **pkg-config** (`brew install xcodegen go cmake
  pkgconf`), to build the app, the hub and the agent; and **uv** (`brew install uv`) for the
  checks in step 9.
- **A paid Apple Developer Program account.** VoIP pushes need it.
- **An iPhone** on iOS 26 or later.
- **An always-on Mac** for the hub, Intel or Apple silicon, on macOS 13 or later. It needs
  nothing installed.
- **A Linux machine with an NVIDIA GPU** for the speech node: x86-64, a GPU from the Turing
  generation (2018) or later, and a driver of the 580 series or later, for CUDA 13 (it's
  tried on 610). A call takes 3–3.5 GB of its GPU memory and 3 GB of RAM; between calls it
  takes 16 MB and no GPU memory. Its install takes 4 GB of disk. While it runs, it also holds
  its downloads (2.7 GB) and their unpacked files until each part is in place, so keep about
  10 GB free (the peak isn't measured).
- **Claude Code** on each Mac that runs sessions, version 2.1.287 or later, signed in to a
  Claude subscription that includes Opus: the voice is Haiku through `claude`, and Opus when
  you ask for a better model on the line.
- **A private network** that the phone, the hub, the speech node and the Macs all reach, the
  phone also on mobile data. Ringback is tested on **Nebula** (Mobile Nebula on the phone):
  - the lighthouse is a relay (`relay.am_relay: true`), and each machine lists it in
    `relay.relays`: on mobile data, the phone reaches the hub only through that relay (see
    `docs/design.md`, Stage 0);
  - the hub's Nebula firewall lets in the phone, on the hub's port (8787 in the examples, and
    the app's default) and on any UDP port, for the call's audio, and the speech node and the
    Macs on its port.

## 1. APNs key

On developer.apple.com, go to Certificates, Identifiers & Profiles → Keys and create a key
with Apple Push Notifications service. Choose the **Sandbox** environment: builds you
install from Xcode receive their pushes from the sandbox. Download the `.p8` file (Apple
lets you do this only once). It goes to the hub, in step 2.

## 2. The hub (the always-on Mac)

Build it on the Mac with Xcode. `build.sh` builds libopus from its release, checked against a
pinned SHA-256, and links it in, so the hub is one file for Intel and Apple silicon that needs
nothing else (the first build takes about a minute):

```sh
(cd hub && ./build.sh)
ssh always-on-mac mkdir -p .local/bin .config/ringback
scp hub/build/ringback-hub-universal always-on-mac:.local/bin/ringback-hub
scp hub/hub.example.toml always-on-mac:.config/ringback/hub.toml
scp AuthKey_XYZ9876543.p8 always-on-mac:.config/ringback/   # your APNs key, from step 1
```

On the always-on Mac, fill in `~/.config/ringback/hub.toml`:
- `listen` is the hub's address on your private network, such as its Nebula address, and its
  port, which it needs: 8787 is the app's default. It listens only there. The call's audio uses the same address, on UDP ports it picks.
- `[tokens]` are what the speech node and the agents link with: make each a long random
  string, such as `openssl rand -hex 32` prints.
- `[apns]` is your team ID, the app's bundle ID, and the key.

Then start it:

```sh
~/.local/bin/ringback-hub serve
```

What it keeps is in `~/.local/state/ringback/`: the phones (`devices.json`), the calls that
wait (`waiting.json`), the sessions you can call (`contacts.json`) and every call's events
(`events.jsonl`). Every 30 min it wakes each phone with a background push, and the app checks
in over the network; if it can't, the phone warns you. Once the app has registered (step 7),
`ringback-hub check` does that now, `ringback-hub devices` shows when each phone last checked
in, and `ringback-hub ring --project myproject` rings every phone without audio, to try the
push.

To start it at login, and again whenever it stops, run it as a launchd agent:

```sh
cat > ~/Library/LaunchAgents/ringback.hub.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>ringback.hub</string>
  <key>ProgramArguments</key>
  <array>
    <string>$HOME/.local/bin/ringback-hub</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>30</integer>
  <key>StandardOutPath</key>
  <string>$HOME/Library/Logs/ringback-hub.log</string>
  <key>StandardErrorPath</key>
  <string>$HOME/Library/Logs/ringback-hub.log</string>
</dict>
</plist>
EOF
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/ringback.hub.plist
```

`KeepAlive` starts it again whenever it stops, at most every 30 s (`ThrottleInterval`): while
the network is down, it can't listen on its address, and launchd tries again. Its log is
`~/Library/Logs/ringback-hub.log`. To restart it, as after an update:
`launchctl kickstart -k gui/$(id -u)/ringback.hub`. To stop it until you load it again:
`launchctl bootout gui/$(id -u)/ringback.hub`. On a Mac with FileVault and no automatic
login, nothing starts after a reboot until someone logs in, not even Nebula.

## 3. The hub's portal

The hub serves a few pages, for setting up and for the rare times something's wrong, at
`http://10.0.0.1:8787/portal/` (its `listen` address). They show:
- the speech nodes linked, since when, and whether their models are loaded (a node loads them
  while a call rings and lets them go a minute after);
- the Macs linked, with their sessions;
- the calls: live, waiting (and why), and the last 50 the phone reported, with their events;
- each phone: when it registered and last checked in, the last push to it, and what's set on
  the phone (work hours, the hold switch, On-device voice), as the hub last heard them;
- the hub's last 500 log lines.

They refresh themselves every 5 s. **Setup** shows what a new speech node or Mac needs: the
hub's address for its link, its token from `hub.toml` (hidden until you show it, and a button
to copy the config) and the commands of steps 4 and 5. The portal changes only what the hub
keeps in its state: you can **pause** a speech node, which stays linked, but the hub neither
warms it nor gives it calls, so calls wait unless another node is linked (`nodes.json` in the
state folder). Tokens and addresses stay in `hub.toml`, which the portal never changes.

Signing in takes a user name and password, then a passcode from an authenticator app (6 digits,
a new one every 30 s), such as the Passwords app on the iPhone. Make the account on the
always-on Mac:

```sh
~/.local/bin/ringback-hub portal-user yourname
```

It asks for a password twice (at least 10 characters), without showing it, and prints an
`otpauth://` link and a setup key: add either to your authenticator app, then type the passcode
the app shows. It keeps the account only once a passcode matches. Running it again replaces the
account and signs everyone out. The account is kept in `portal-user.json` in the
state folder, readable only by you: the password hashed (PBKDF2-SHA256, 600,000 iterations)
and the passcodes' secret. Until there's an account, the portal shows only how to make one.
- A sign-in lasts until it's gone unused for 12 h (the pages' own refreshes don't count) or the
  hub restarts.
- After five failed sign-ins from an address, the portal refuses that address for a minute;
  the log shows each failure.
- Each passcode works once.
- Like the rest of the hub, the portal is plain HTTP inside Nebula, so anything in your mesh
  can reach it, which is why it asks for a passcode too. Passkeys (Face ID instead of the
  passcode) need the portal on HTTPS under a domain name, so they come later.

## 4. The speech node (the Linux machine with the GPU)

With a copy of this repository on that machine:

```sh
speech/install.sh   # ~/ringback-speech: sherpa-onnx, the CUDA libraries, the models
speech/build.sh     # builds ~/ringback-speech/bin/ringback-speech
```

`install.sh` first checks the machine (x86-64 Linux, the GPU and its driver), then downloads
everything the node needs into one folder, `~/ringback-speech`, and nothing outside it: 2.7 GB,
each archive checked against a pinned SHA-256, which took 69 s on a fast connection
(`speech/README.md` lists what's where). Running it again keeps what's right and fetches only
what's missing; a run you stop keeps what it fetched. It needs curl or wget, tar, bzip2, gzip,
unzip and sha256sum, and installs Go into the folder too if the machine has none. `build.sh`
needs a C compiler (gcc; `sudo apt install build-essential` on Debian or Ubuntu). Then, in
`~/ringback-speech/speech.toml`, set the hub's address and its speech token:

```toml
hub = "ws://10.0.0.1:8787/speech"
token = "…"   # [tokens] speech in the hub's hub.toml
```

and start it:

```sh
. ~/ringback-speech/env.sh && ~/ringback-speech/bin/ringback-speech serve
```

It links to the hub ("linked to the hub" in its log, and "speech … connected" in the hub's),
and holds no models until a call comes: the hub tells it when a call rings, and it loads them
while the phone rings, in about 2 s. A minute after the call, it lets go of the GPU and its
memory again.

To run it as a service, from boot and again if it fails, use the systemd user unit in
`speech/`:

```sh
mkdir -p ~/.config/systemd/user
cp speech/ringback-speech.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now ringback-speech
loginctl enable-linger                       # so it runs from boot, not only while you're logged in
journalctl --user -u ringback-speech -f      # its log
```

`speech/check.sh` checks the node against a stand-in hub (about 2.5 min): its hearing, its
speaking, and its memory before, during and after a call.

## 5. The agent (every Mac that runs sessions)

```sh
mkdir -p ~/.local/bin ~/.config/ringback
(cd agent && go build -o ~/.local/bin/ringback-agent .)
cp agent/agent.example.toml ~/.config/ringback/agent.toml   # the hub's address and its agent token
~/.local/bin/ringback-agent serve
```

Sessions on this Mac reach it through a socket only you can open,
`~/.local/state/ringback/voice.sock`. It needs `claude` on its `PATH`: on a call, it runs Haiku
as `claude -p`, on this Mac, and Opus when you ask for a better model. It links to the hub
("linked to the hub" in its log, and "agent … connected" in the hub's) and tells it which
sessions listen, for the app's Sessions tab.

It remembers the calls it placed for a day after they end, so you can call them back from the
app, and what it keeps for sessions survives a restart: it's written to `voice-state.json`
beside its socket on every change, and read at start. That's the calls held outside your
work hours, the calls to call back, what sessions asked you and what waits for their mods. A
call you're on when it restarts ends, and its session is told so. (Tried offline, not yet on
the phone.)

To start it at login, and again whenever it stops, run it as a launchd agent. Write the
agent's file with `claude` on your `PATH`, as it finds it there:

```sh
cat > ~/Library/LaunchAgents/ringback.agent.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>ringback.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>$HOME/.local/bin/ringback-agent</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>$(dirname "$(which claude)"):/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>30</integer>
  <key>StandardOutPath</key>
  <string>$HOME/Library/Logs/ringback-agent.log</string>
  <key>StandardErrorPath</key>
  <string>$HOME/Library/Logs/ringback-agent.log</string>
</dict>
</plist>
EOF
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/ringback.agent.plist
```

Stop any agent you started by hand first: both would use the same socket. Its log is
`~/Library/Logs/ringback-agent.log`; `launchctl kickstart -k gui/$(id -u)/ringback.agent`
restarts it, and `launchctl bootout gui/$(id -u)/ringback.agent` stops it.

## 6. The ringback mod (every Mac that runs sessions)

The mod is how a session calls you. To load it in every session, add it to
`~/.claude/settings.json`:

```json
{ "env": { "CLAUDE_CODE_PLUGIN_DIRS": "/path/to/ringback/mod" } }
```

or load it in one session with `claude --plugin-dir /path/to/ringback/mod`.

It does nothing until you run `/ringback` in a session. The dim line under the prompt
then ends with "☎ ringback on". Each time the session finishes, it writes a short
briefing, and the agent rings your phone with it. What you ask the voice to pass on
reaches the session, and its answer comes back to the call, or a new call if you've hung
up. Each call's transcript is kept in the project's `.ringback/calls/`, which git ignores.
Run `/ringback` again when you're back. Until then it stays on, also when the mod reloads
and when you resume the session after Claude Code restarts (`claude --resume`), within 30
days of the session's last start.

While the agent is down, as while it restarts, the session doesn't give up: it offers its
briefing again every 30 s for up to 30 min, and the dim line under the prompt ends with
"☎ waiting for the ringback agent…". A permission or question it's asking you is asked again
every 5 s, within its 15 min.

When the session needs your permission, the prompt shows in the terminal and the voice
asks you on a call; whichever you answer first decides. Questions the session asks you
with options go to the call instead of the terminal, and show there only if the call
brings no answer. In auto mode, what auto mode approves by itself never rings. If you don't
answer the call, both wait for you to call it back from the app, or for the session's next
call, for up to 15 min: a question then shows in the terminal only after those 15 min, or
once you run `/ringback` to switch calling off. Outside your work hours they wait for the
call, which rings when your hours start, and the 15 min count from then; a question can go
to the Claude app from the phone instead. `/ringback` turns on Remote Control for that
(`/remote-control`); if it's on already, Claude Code shows its Remote Control menu, and Enter
or Esc leaves it on. Remote Control needs a claude.ai subscription.

## 7. The iPhone app

```sh
cp app/Config/Local.xcconfig.example app/Config/Local.xcconfig   # your team ID and bundle ID
(cd app && xcodegen generate && xcodebuild -project Ringback.xcodeproj -scheme Ringback \
  -destination 'platform=iOS,id=<UDID>' -derivedDataPath build -allowProvisioningUpdates build)
xcrun devicectl device install app --device <UDID> app/build/Build/Products/Debug-iphoneos/Ringback.app
```

- `xcrun devicectl list devices` shows the UDID.
- The phone needs **Developer Mode** on (Settings → Privacy & Security → Developer Mode,
  which restarts it), and to trust this Mac: connect it by cable the first time and tap Trust.
- Installing over the network needs the phone **unlocked and on the same Wi-Fi** as the
  Mac. On mobile data it shows as unavailable; a cable always works.
- If Xcode's sign-in to your Apple account has expired, signing falls back to a profile
  without push, and the build fails on the push entitlement. Sign in again in Xcode →
  Settings → Accounts.

Open the app and allow the microphone and notifications. Its settings are in the Settings
app, under **Settings → Apps → Ringback** (the app's banner, "Set up Ringback", has a button
that opens it): enter the hub's address under **Hub**, e.g. `10.0.0.1` (port 8787 is the
default), then go back to the app. The banner goes once the hub has the phone.

The same page has the rest:
- **Work Hours**, off by default, with **From** and **To** in 15-minute steps: Claude calls
  only between them. They reach the hub when the app next runs, or at the latest with its
  next connection check, within half an hour; open the app after changing them, so they
  apply from the next call.
- **Interruption Sensitivity** sets how easily your speech stops Claude: less to the left,
  more to the right.
- **Ignore Background Noise** (on by default) keeps quieter voices and sounds than yours,
  such as people nearby, from stopping it; its slider is gentle to the left and strong to the
  right. These two apply from the next call.

During your first call, turn on Voice Isolation in Control Center (Mic Mode), so background
noise doesn't stop it.

The app has two tabs, as the Phone app has. **Recents** lists every call: tap one to call
its session at once, answered or not, and its ⓘ for its details. Calls that wait for your
work hours show at the top in orange, under Waiting, with when they ring; tap one to call
it now, without waiting. One whose session asks you a question says so, and the phone gets
one notification for it. Swipe it left for **Claude App** to send the question to the
session's own dialog instead, which Remote Control shows in the Claude app (ringback turns
Remote Control on in the session); the call then never rings.

When something calls need is down, a banner at the top of Recents says what, most serious
first, with the others a tap away: the hub can't be reached, Apple refused the hub's push,
the speech node isn't linked (the app calls it the speech computer; calls then wait instead of
ringing, and ring once it's back), or the microphone is off. Notifications are for a warning when the phone can't reach the hub,
for a waiting call's question, and for a call waiting for the speech node.

The pill beside the tabs is the **hold switch**. Press pause and every call waits, listed under
Waiting; press play and they ring. With work hours on, it goes on hold when they end and
takes calls when they start; in between, what you press stands. The same switch can go in
Control Center: open Control Center, tap +, then Add a Control, and choose Ringback's Hold
Calls. You can also put it on the Lock Screen or the Action button. On hold, the pill breathes,
and the app's icon shows how many calls wait. To see the hold on the Lock Screen, touch and
hold the Lock Screen, tap Customize, then the widgets, and add Ringback's Hold.

**Sessions** shows your sessions with ringback on, on every Mac, by project, a card each,
and keeps up while it shows: tap one to call it, and the voice picks up with what's new since
the session last reported. Hold one for its details.

## 8. A test call

From a session: run `/ringback`, then give the session something to do. Or place one by hand
through the agent, as a session's mod does, with a few sentences for the voice to talk about:

```sh
curl --unix-socket ~/.local/state/ringback/voice.sock http://voice/calls \
  -d '{"session_id": "test-call", "project": "myproject", "subject": "Test call",
       "briefing": "This is a test call through ringback. Nothing needs you."}'
```

The phone rings as "myproject". The hub's log shows the call ringing, answered, the time
until its audio connected, what you said and what the voice said, and how it ended; the
agent's log shows Haiku's side.

## 9. Checking without the phone

These need uv (`brew install uv`); they run in the earlier stack's Python environment, which
uv sets up the first time (about 1.7 GB).
- **A whole call:** a hub and an agent started on this Mac, your speech node started over ssh
  and linked to them, and a stand-in phone, which answers, says something and hangs up; it
  checks that you're heard and answered (about 45 s, one Haiku session). `--listen` is this
  Mac's address that the node reaches, with a free port, and `--node` the node's ssh host:

      uv run --project voice tools/line-check/line_check.py --listen 10.0.0.2:18840 --node speech-host

- **The hub's side of calls,** with stand-ins for everything around it (about 10 s):

      (cd hub && go build -o build/ringback-hub .) && uv run --project voice tools/hub-check/hub_check.py

- **The speech node,** on the node (step 4): `speech/check.sh` (about 2.5 min).

## Coming from the earlier stack

Before the hub, a Python voice server on each Mac did the speech and the sessions' side, and a
Swift switchboard rang the phone. To move:
- **The hub replaces the switchboard,** with its state as it is. Stop the switchboard first:
  both listen on the same address. The hub reads `~/.config/ringback/switchboard.toml` while
  there's no `hub.toml`; add `[tokens]` to it.
- **The agent replaces each Mac's voice server,** on the same socket, and reads the voice
  server's `voice-state.json`. Stop the voice server first (`launchctl bootout
  gui/$(id -u)/ringback.voice` if it runs as a launchd agent).
- **The mod and the app stay as they are.** An app from before calls the hub's address
  "Switchboard" in its settings, and shows no banner for the speech node.
