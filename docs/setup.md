# Setup

How to run ringback: a session with `/ringback` on calls your iPhone when it finishes, you
talk it through, and what you say reaches the session. Four parts run on your machines, and
the app on your phone:

| Part | Runs on | Does |
|---|---|---|
| **hub** | one always-on Mac, Intel or Apple silicon | rings the phone (APNs), keeps the calls that wait, and carries each call's audio |
| **speech node** (optional) | a Linux machine with an NVIDIA GPU | hears you (Silero, Smart Turn, Parakeet) and speaks (Kokoro), for the hub; without one, the phone can hear and speak calls itself |
| **agent** | each Mac that runs Claude Code sessions | the sessions' side: their briefings and questions, and the call's Haiku and Opus through Claude Code |
| **mod** | each Claude Code session | adds `/ringback`, and hands the agent the session's briefings |
| **app** | your iPhone | rings like a phone call, and lists your calls and sessions |

The speech node and the agents dial out to the hub, and the phone reaches only the hub. The
steps below set them up in that order. Commands run from the repository's folder, unless they
say where else. Replace the example addresses (`10.0.0.1` for the hub) with your own.

## You need

- **A Mac with Xcode** that can build for your phone's iOS version (Xcode 27 for iOS 27),
  with **XcodeGen**, **Go**, **CMake** and **pkg-config** (`brew install xcodegen go cmake
  pkgconf`), to build the app, the hub and the agent; and for the checks in step 9, **uv**
  and **opus** (`brew install uv opus`): a plain `go build` of the hub links Homebrew's
  libopus, while `hub/build.sh` builds its own. The checks in `tools/` need an Apple silicon
  Mac: some of their Python packages (MLX for Kokoro, and `cryptography`) come built for it
  only.
- **A paid Apple Developer Program account.** VoIP pushes need it.
- **An iPhone** on iOS 26 or later.
- **An always-on Mac** for the hub, Intel or Apple silicon, on macOS 13 or later. It needs
  nothing installed but your VPN. Or a Linux machine (step 2), as a server would be: x86-64
  or ARM, built there.
- **A Linux machine with an NVIDIA GPU** for the speech node, if you want one (step 4):
  x86-64, a GPU from the Turing generation (2018) or later, and a driver of the 580 series or
  later, for CUDA 13 (it's tried on 610). A call takes about 3 GB of its GPU memory and 3 GB
  of RAM; between calls it takes 16 MB and no GPU memory. Its install takes 4 GB of disk. While
  it runs, it also holds its downloads (2.7 GB) and their unpacked files until each part is in
  place, so keep about 10 GB free (the peak isn't measured).
- **On each Mac that runs sessions:** a copy of this repository, which the mod loads from, and
  **Go**, to build the agent (step 5).
- **Claude Code** on each Mac that runs sessions, version 2.1.287 or later, signed in to a
  Claude subscription that includes Opus: the voice is Haiku through `claude`, and Opus when
  you ask for a better model on the line.
- **A private network** that the phone, the hub, the speech node and the Macs all reach, the
  phone also on mobile data. Ringback is tested on **Nebula**
  ([github.com/slackhq/nebula](https://github.com/slackhq/nebula); Mobile Nebula on the
  phone):
  - the lighthouse is a relay (`relay.am_relay: true`), and each machine lists it in
    `relay.relays`: on mobile data, the phone reaches the hub only through that relay (see
    "The network" in `docs/design.md`);
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

**On Linux** instead, build it on that machine, with Go, a C compiler, CMake, pkg-config and
curl (`sudo apt install build-essential cmake pkg-config curl` on Debian or Ubuntu, with Go from
go.dev): `hub/build.sh` makes `hub/build/ringback-hub`, which needs nothing but the system's C
library. Copy it to `~/.local/bin/ringback-hub`, and the config and APNs key to
`~/.config/ringback/` as above. Its `install` makes a systemd user service, which starts at
boot (it turns on lingering, as the speech node's does), and its log is `journalctl --user -u
ringback-hub`. Tried on one x86-64 machine with its checks, not as anyone's hub yet.

On the always-on Mac, fill in `~/.config/ringback/hub.toml`:
- `listen` is the hub's address on your private network, such as its Nebula address, and its
  port, which it needs: 8787 is the app's default. It listens only there. The call's audio
  uses the same address, on UDP ports it picks.
- `[tokens]` are optional: shared secrets the speech nodes and the agents can link with
  instead of pairing (steps 4 and 5), each a long random string, such as
  `openssl rand -hex 32` prints. Leave them out to have each pair.
- `[apns]` is your team ID and the app's bundle ID, and `[apns.sandbox]` the key: its key ID
  and the path of its `.p8` file.

**HTTPS (optional).** On a private network plain HTTP is enough, since the VPN encrypts it. To
have the hub serve HTTPS and WSS instead, as a hub on a public address will need (not
supported yet: "The network" in `design.md`), give it a certificate under `[tls]`: `cert`,
the chain, and `key`, as PEM files, as an ACME client such as lego, acme.sh or certbot writes
them for a domain name of yours (a DNS challenge works for a hub that only your private
network reaches), and set `url` to the hub's `https://` address by that name. It reads them
again when they change, so a renewal needs no restart, and it logs a week ahead of their
running out. The speech node's `hub` is then `wss://NAME:PORT/speech`, and the agent's `hub`
and the phone's Address are the hub's URL; pairing the phone from the portal sets it. A
certificate a CA of your own signed works too: the agent and the speech node trust it with
`ca`, and the phone needs the CA's profile installed and turned on under Settings, General,
About, Certificate Trust Settings. Checked offline only (`hub/smoke-test.sh`), not with the
phone.

Then install it as a service, a launchd agent: it starts now, at login, and again whenever it
stops (at most every 30 s: while the network is down it can't listen on its address, and
launchd tries again):

```sh
~/.local/bin/ringback-hub install
```

`install` refuses while the config lacks `listen`, has a token that's short or a `[tls]` it can't read,
or while something else listens on the address, such as a hub started by hand. Its log is
`~/Library/Logs/ringback-hub.log`. After copying a new binary over the old one, run `install`
again to restart it; `ringback-hub uninstall` stops it and takes the launchd agent away. To
try it in a terminal instead, `ringback-hub serve` runs it until you stop it. On a Mac with
FileVault and no automatic login, nothing starts after a reboot until someone logs in, not
even Nebula.

What it keeps is in `~/.local/state/ringback/`: the phones (`devices.json`), the calls that
wait (`waiting.json`), the sessions you can call (`contacts.json`) and every call's events
(`events.jsonl`). Every 30 min it wakes each phone with a background push, and the app checks
in over the network; if it can't, the phone warns you. Once the phone has paired (step 7),
`ringback-hub check` does that now, `ringback-hub devices` shows when each phone last checked
in, and `ringback-hub ring --project myproject` rings every paired phone without audio, to
try the push. `ringback-hub pair` prints a one-time link that pairs a phone (step 7).

## 3. The hub's portal

The hub serves a few pages, for setting up and for the rare times something's wrong, at
`http://10.0.0.1:8787/portal/` (its `listen` address). Signing in takes a user name and
password, then a passcode from an authenticator app (6 digits, a new one every 30 s), such as
the Passwords app on the iPhone. Make the account on the always-on Mac, in its own terminal:

```sh
~/.local/bin/ringback-hub portal-user yourname
```

It asks for a password twice (at least 10 characters), without showing it, and prints an
`otpauth://` link and a setup key: add either to your authenticator app, then type the passcode
the app shows. It keeps the account only once a passcode matches. Running it again replaces the
account and signs everyone out. The account is kept in `portal-user.json` in the state folder,
readable only by you: the password hashed (PBKDF2-SHA256, 600,000 iterations) and the
passcodes' secret. Until there's an account, the portal shows only how to make one.
- A sign-in lasts until it's gone unused for 12 h (the pages' own refreshes don't count) or the
  hub restarts.
- After five failed sign-ins from an address, the portal refuses that address for a minute;
  the log shows each failure.
- Each passcode works once.
- Like the rest of the hub, the portal is plain HTTP inside your private network, so anything
  on that network can reach it, which is why it asks for a passcode too.

**Status** starts with a map of the
system: your Macs, the hub, the speech nodes, Apple push and the phones, and the links between
them, each working, a problem, gone or not set up. A part you haven't set up yet is drawn
dashed, with a link to its place on Setup; a Mac that's asleep is drawn faint, which is
normal; a speech node that's gone is a problem. Above the map, a sentence says what a call
would do now: which phone it rings, and what hears and speaks it (a speech node, the phone's
own voice, or nothing, so calls wait); a call ringing or live lights its path. Below it:
- the calls, live and waiting (and why);
- the sessions listening, each with the Mac it runs on;
- each paired phone: when it registered and last checked in, the last push to it, and what's
  set in the app (work hours, On-device voice), as the hub last heard them; a phone that
  registered but hasn't paired is named above the map, with how to pair it;
- the speech nodes and Macs: since when they're linked, whether a node's models are loaded (it
  loads them while a call rings and lets them go a minute after), and those that are gone.

**Calls** lists the calls waiting, then the last 50 the phone reported, by day, with their
events; **Log** the hub's last 500 lines, newest first. Status, Calls and Log refresh
themselves every 5 s. **Setup** shows which parts are set up, and what a new speech node or Mac
needs: the hub's address for its link, its token from `hub.toml` (hidden until you show it,
and a button to copy the config) and the commands of steps 4 and 5; and how the phone and
Apple push are set up. It doesn't refresh itself, so its tokens are fetched once. **Pair this
phone** (`/portal/pair`), opened on the iPhone, pairs it (step 7); signed out, it signs you in
first and comes back.

The portal changes only what the hub keeps in its state. The **hold** is set on Status, and
every page's bar shows it: hold calls and nothing rings, not through a speech node and not with
the phone's own voice; they wait until you take calls. It's the app's hold switch, set on every
paired phone at once, so the app shows it too; and as in the app, your work hours set it at their
edges, so with work hours a hold lasts until they next start, and the page says so. A phone
that registers while you've held every other (in the app or here) starts on hold too. A speech
node or Mac that's gone for good can be **forgotten** on Status: the hub remembers the ones it
has seen in `seen.json` in the state folder, with whether each is linked, to tell one that's
gone from one never set up; one linked when the hub stopped counts as gone since the hub
started again.
Tokens and addresses stay in `hub.toml`, which the portal never changes.

## 4. The speech node (the Linux machine with the GPU)

This step is optional. Without a speech node, turn on **On-device Voice** in the app's
settings (step 7): the phone then hears and speaks calls itself, with Apple's speech
recognition and voices, and only text goes between it and the hub. It gets more of your words
wrong than the speech node (about 10% against 6% on the recordings in `tools/stt-eval`; see
`tools/speech-trial`), and it's built but not yet tried on a real call. With neither, calls
wait until there's one, then ring. A speech node, once linked, is used before the phone's own
voice.

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
needs a C compiler (gcc; `sudo apt install build-essential` on Debian or Ubuntu). Then pair
it with the hub: on the portal's Setup page, under Speech node, tap Make a pairing code (or run
`ringback-hub pair node` on the hub's machine), and run the command it gives on the node,
within 10 minutes:

```sh
~/ringback-speech/bin/ringback-speech pair http://10.0.0.1:8787 CODE
```

It writes the hub's address and the node's own key into `~/ringback-speech/speech.toml`, and
pairs by the machine's host name unless `name` is set there. (Instead of pairing, `hub` can be
`ws://10.0.0.1:8787/speech` with `token` set to `[tokens] speech` in the hub's `hub.toml`.)
Then install it as a service, a systemd user unit: it starts now, at boot, and again whenever
it stops:

```sh
~/ringback-speech/bin/ringback-speech install
journalctl --user -u ringback-speech -f      # its log
```

`install` refuses while `speech.toml` lacks the hub, or the key or token, or while a node
started by hand runs. It also turns on lingering for you (`loginctl enable-linger`), so the node runs
from boot, not only while you're logged in; where that needs an administrator, it says so.
After building a new binary, run `install` again to restart it; `ringback-speech uninstall`
stops it and takes the unit away. To try it in a terminal instead:
`. ~/ringback-speech/env.sh && ~/ringback-speech/bin/ringback-speech serve`.

It links to the hub ("linked to the hub" in its log, and "speech … connected" in the hub's),
and holds no models until a call comes: the hub tells it when a call rings, and it loads them
while the phone rings, in about 2 s. A minute after the call, it lets go of the GPU and its
memory again.

`speech/check.sh` checks the node against a stand-in hub (about 2.5 min): its hearing, its
speaking, and its memory before, during and after a call.

## 5. The agent (every Mac that runs sessions)

```sh
mkdir -p ~/.local/bin
(cd agent && go build -o ~/.local/bin/ringback-agent .)
```

Pair it with the hub: on the portal's Setup page, under Mac, tap Make a pairing code (or run
`ringback-hub pair mac` on the hub's machine), and run the command it gives on this Mac, within
10 minutes:

```sh
~/.local/bin/ringback-agent pair http://10.0.0.1:8787 CODE
```

It writes the hub's address and this Mac's own key into `~/.config/ringback/agent.toml`,
making it if there's none (`agent/agent.example.toml` has the other settings). Instead of
pairing, `hub` can be the hub's address and port, as in its `listen`, with `token` set to
`[tokens] agent` in the hub's `hub.toml`. Then install it as a service, a launchd agent, from a
terminal where `claude` runs: it starts now, at login, and again whenever it stops.

```sh
~/.local/bin/ringback-agent install
```

`install` refuses while the config lacks the hub, or the key or token, while `claude` isn't on your
`PATH` (the service gets its folder on its own `PATH`), or while an agent started by hand runs.
Its log is `~/Library/Logs/ringback-agent.log`. After copying a new binary over the old one,
run `install` again to restart it; `ringback-agent uninstall` stops it and takes the launchd
agent away. To try it in a terminal instead, `ringback-agent serve` runs it until you stop it.

Sessions on this Mac reach the agent through a socket only you can open,
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

**To show you 3D models** (optional; images, PDFs and clips need nothing more), the agent needs
a renderer beside it or on its `PATH` (`render/README.md`). On a Mac, Apple's fallback builds
with Xcode alone; OpenUSD's renderer, which opens glTF and GLB too and renders better, builds
against OpenUSD from conda-forge (with pixi), or on Linux the system's:

```sh
render/build.sh apple && ln -sf "$PWD/render/build/ringback-render-apple" ~/.local/bin/
render/build.sh       && ln -sf "$PWD/render/build/ringback-render" ~/.local/bin/   # or OpenUSD's
~/.local/bin/ringback-agent install   # restarts it, so it finds the renderer
```

Without either, a session that shows you a model is told this Mac can't render 3D models, and
what it lacks. The agent remembers where each model it showed is, for its live view, in
`shown-models.json` beside its socket.

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
- The app has a notification service extension, which fetches what a session shows you, and
  the Communication Notifications capability, so it shows as a message from the session.
  Automatic signing adds both to your App IDs the first time, with Xcode signed in.

Open the app and allow the microphone and notifications. Its settings are in the Settings
app, under **Settings → Apps → Ringback** (the app's banner, "Set up Ringback", has a button
that opens it): enter the hub's address under **Hub**, e.g. `10.0.0.1` (port 8787 is the
default), so the app can reach the hub, then go back to the app.

Then pair the phone with the hub: the hub rings, and lists calls and sessions for, only a
phone that proves it's yours. The app's banner, "Pair this iPhone", has a **Pair** button,
which opens the hub's pairing page (`/portal/pair`) in Safari: sign in to the portal, as in
step 3, and tap **Pair this phone**. Safari opens Ringback with a one-time link, the app
trades it for the phone's own key, kept in its Keychain, and says it's paired; the banner
goes. The link works once, for 10 minutes. Without a browser at hand, `ringback-hub pair` on
the hub's machine prints such a link, to open on the phone (send it to yourself). A phone
pairs once; registering again, or with a new push token from Apple, carries its key.

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
- **On-device Voice** (off by default): when no speech node is linked, the phone hears and
  speaks calls itself (step 4). It speaks with the best English voice on the phone; better
  ones can be downloaded in Accessibility settings. It reaches the hub when the app next
  opens.

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
the speech node isn't linked (the app calls it the speech computer; unless On-device Voice
is on, calls then wait instead of ringing, and ring once it's back), or the microphone is off.
Notifications are for a warning when the phone can't reach the hub, for a waiting call's
question, and for a call waiting for the speech node.

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

These need uv (`brew install uv`); they run in the tools' Python environment
(`tools/pyproject.toml`), which uv sets up the first time (about 1.3 GB).
- **A whole call:** a hub and an agent started on this Mac, your speech node started over ssh
  and linked to them, and a stand-in phone, which answers, says something and hangs up; it
  checks that you're heard and answered (about 45 s, one Haiku session). `--listen` is this
  Mac's address that the node reaches, with a free port, and `--node` the node's ssh host:

      uv run --project tools tools/line-check/line_check.py --listen 10.0.0.2:18840 --node speech-host

- **The hub's side of calls,** with stand-ins for everything around it (about 10 s):

      (cd hub && go build -o build/ringback-hub .) && uv run --project tools tools/hub-check/hub_check.py

- **The speech node,** on the node (step 4): `speech/check.sh` (about 2.5 min).
- **A model's live view,** through a hub, an agent and the renderer started on this Mac, with
  a stand-in phone that decodes its video (about 20 s; build the three first, as its README
  says):

      uv run --project tools tools/view-check/view_check.py

## 10. A public hub (prepared, not tried)

Everything above runs on a private network, such as Nebula. The hub has what it would need on
an address the internet reaches, with no VPN, but none of it has been tried on one yet (Status
in `design.md`), and the portal still signs in with a password and a passcode, not a passkey.
What it would take:
- **A machine the internet reaches,** always on: a small server, or a machine of yours with a
  port forward. On Linux, build and install the hub as step 2 says. As a user's service it
  can't listen on a port below 1024, such as 443: keep 8787, the app's default.
- **A domain name** whose DNS points at it, and a **certificate** for that name, from an ACME
  client (lego, acme.sh, certbot) that renews it on its own; the hub reads the files under
  `[tls]` again when they change (step 2).
- In `hub.toml`: `listen` on its address, or `0.0.0.0:8787` for every one it has; `url`, the
  hub's `https://` address by that name; `[tls]`; `udp_ports`, a range the calls' audio uses,
  one port each (such as `50000-50199`); and `public_ip` if the machine doesn't have its public
  address on an interface, as on clouds that put their servers behind NAT. Leave `[tokens]`
  out, so each Mac and speech node pairs (steps 4 and 5).
- A firewall that lets in the hub's TCP port and that UDP range.
- On the phone, the hub's URL as its Address, or pair it from the portal, which sets it.

Requests the hub refuses (a key, token, code or call ID that isn't right) are counted by
address: 30 within a minute shut that address out for a minute (429).

For a hub on a public address, the app offers the call's audio every one of its addresses
(Wi-Fi, mobile data), rather than only its VPN's, so a VPN that's on, such as a work one,
doesn't stand in the way; a hub's name counts as public if it looks up to a public address.
For such a hub, the app's messages when it can't reach it say to check the phone's connection
and the hub, not the VPN, and the portal doesn't say the phone needs one.
