<p align="center">
  <img src="docs/images/icon.png" width="112" alt="Ringback's icon: a terminal prompt with the arcs of a ringing phone">
</p>

<h1 align="center">Ringback</h1>

<p align="center">
  <b>Your Claude Code sessions call you back.</b><br>
  When a session finishes or needs you, your iPhone rings. Talk it through, hands-free,<br>
  and it gets back to work.
</p>

<p align="center">
  <a href="#how-it-works">How it works</a> ·
  <a href="#get-started">Get started</a> ·
  <a href="docs/setup.md">Setup guide</a> ·
  <a href="docs/design.md">Design</a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/iPhone-iOS_26+-1f2328?style=flat-square" alt="iPhone on iOS 26 or later">
  <img src="https://img.shields.io/badge/sessions-macOS-1f2328?style=flat-square" alt="Claude Code sessions on a Mac">
  <img src="https://img.shields.io/badge/speech-Linux_+_NVIDIA,_or_the_iPhone-1f2328?style=flat-square" alt="Speech on a Linux machine with an NVIDIA GPU, or on the iPhone itself">
  <img src="https://img.shields.io/badge/license-MIT-1f2328?style=flat-square" alt="MIT license">
</p>

<p align="center">
  <img src="docs/images/hero.svg" width="820" alt="A Claude Code session finishes in a terminal, and an iPhone rings with the call">
</p>

Leave a long task running and walk away. When it's done, the session calls your phone, a
voice tells you what happened, and you answer the way you'd answer a colleague: *"Push it and
open a pull request."* Your words reach the session, it carries on, and if there's more to
decide, it calls again.

Ringback runs on your own machines and phone, over a private network of yours such as
[Nebula](https://github.com/slackhq/nebula). The voice is Claude Haiku through Claude Code, on
your Claude subscription, and the speech models run on your own GPU, or on the iPhone itself:
no API key, and no server of ours in between.

## What a call is like

- **It rings like a phone call.** Locked phone, headphones in, through CallKit. The voice is
  ready the moment you answer.
- **You can interrupt.** Start talking and the voice stops, the way a person would. It answers
  you in about two seconds.
- **You say what's next.** "Run the tests again", "what did you change in the parser?". It
  passes instructions on, answers from the briefing, and tells you when the session replies.
- **You approve by voice.** A permission prompt or a question from the session comes to the
  call. A clear yes or no, or one of its options, answers it in the terminal.
- **It keeps your hours.** Outside your work hours, or while you've paused calls, calls wait in
  the app and ring when you're back. A question can go to the Claude app instead.
- **You can call back.** Every session with ringback on is one tap away, and so are your recent
  calls. Ask for a better model and Opus takes over the call.

## The app

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/screens-dark.png">
    <img src="docs/images/screens-light.png" width="820" alt="Three iPhone screens: Recents with two calls waiting while calls are on hold, Sessions with a card for each session, and a waiting question swiped to send it to the Claude app">
  </picture>
</p>

<p align="center"><sub>Recents, with calls on hold &nbsp;·&nbsp; Sessions, a tap to call each one &nbsp;·&nbsp; a waiting question, sent to the Claude app</sub></p>

Built like the Phone app, with one switch of its own: the pill beside the tabs holds every call
until you let them through, and breathes while it does. It's in Control Center too, and the
app's icon counts the calls that wait. When something calls need is down (your hub, the speech
node, Apple's push), a banner at the top says what, in plain words, and goes once it's back.

## How it works

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/how-it-works-dark.png">
    <img src="docs/images/how-it-works-light.png" width="820" alt="Claude Code sends a briefing to the agent on the same Mac, which asks the hub to ring; the hub pushes to the iPhone and carries your voice; the speech node beside the hub hears you and speaks; your words go between the hub and the agent, whose Haiku answers, and what you said goes back to the session">
  </picture>
</p>

A small **mod** in each Claude Code session writes a spoken briefing when a turn ends, and hands
it to the **agent** on that Mac. The agent asks the **hub**, a small always-on service, to ring
your **iPhone**. When you answer, your voice goes to the hub, which passes it to the **speech
node**, a machine with a GPU: Silero hears when you speak, Smart Turn when you've finished, and
Parakeet what you said. Your words go to the agent, where Claude Haiku answers through Claude Code,
on the session's own Mac, and Kokoro, back on the speech node, speaks the answer. What you ask for
goes back to the session, and its answer comes back to the call. Without a speech node, the
iPhone can hear and speak the call itself, with Apple's speech, and only text goes to the hub.

The Macs and the speech node only ever dial out to the hub, and the phone reaches only the hub.
Between calls the speech node lets go of the GPU and its memory; it loads its models while your
phone rings.

How each part works and why, and what's been tried on the phone so far, are in
[docs/design.md](docs/design.md).

## Get started

You need:

- an always-on Mac for the hub, Intel or Apple silicon (it needs nothing installed but your
  VPN);
- for the speech node, a Linux machine with an NVIDIA GPU: about 3 GB of GPU memory and 3 GB
  of RAM during calls, next to nothing between them, and 4 GB on disk. It's optional: without one,
  the iPhone hears and speaks calls itself, and gets more of your words wrong;
- Claude Code on each Mac that runs sessions, on a Claude subscription that includes Opus;
- a Mac with Xcode, and Go, CMake, XcodeGen and pkg-config from Homebrew, to build the app, the
  hub and the agent; Go and a copy of this repository on each Mac that runs sessions;
- an iPhone on iOS 26 or later, and a paid Apple Developer account, for VoIP pushes;
- a private network all of them reach, the phone too on mobile data: Nebula, with a lighthouse
  that relays, is the one it's tested on.

[docs/setup.md](docs/setup.md) takes you through it in nine steps. Once everything is set up, a
session calls you like this:

```sh
claude --plugin-dir path/to/ringback/mod
> /ringback
```

## Status

Ringback is a working prototype. Everything here is built and passes its offline checks; a
tick means it has also been tried on a real phone, locked, with the current code.

- [x] A locked iPhone rings, and you talk with the voice, interrupting it, and what you ask to
      pass on goes once you've heard it announced (a call placed through the agent by hand)
- [x] A call waits while no speech node is linked, and rings once one links
- [x] The hub's portal, from the phone
- [ ] A session calling when it finishes, and its answer coming back to the call
- [ ] Permission prompts and questions, answered on the call
- [ ] Work hours, the hold switch, its breathing light and the Lock Screen widget
- [ ] Calling back, calling a waiting call now, and the Sessions tab
- [ ] Opus on the line
- [ ] Weak signal: 3G, and the Network Link Conditioner's Very Bad Network
- [ ] Speech on the iPhone itself, without a speech node (On-device Voice)
- [ ] Calls and asks that survive a restart of the agent or the hub

Planned: several sessions calling in turn, with retries, and a public hub that needs no
private network ("Next" in [docs/design.md](docs/design.md)).

<details>
<summary><b>Repository and development</b></summary>
<br>

| Folder | What's in it |
|---|---|
| `app/` | The iPhone app (SwiftUI), with its Control Center and Lock Screen extension |
| `mod/` | The Claude Code mod (TypeScript) that adds `/ringback` |
| `hub/` | The hub (Go): push, waiting calls, and the call's audio and timing |
| `speech/` | The speech node (Go, sherpa-onnx on CUDA): hearing and speaking |
| `agent/` | The agent (Go): each Mac's sessions, and the call's Haiku and Opus through Claude Code |
| `tools/` | The checks and measurements behind the design, each with a README |
| `docs/` | The design, the protocol between the parts, the setup guide, and the images here |

Before every commit, these show that everything builds and passes its offline checks, in about
a minute and a half. They need `app/Config/Local.xcconfig` (step 7 of the setup) and
Homebrew's `opus` (`brew install opus`), which a plain `go build` of the hub links:

```sh
(cd app && xcodegen generate && xcodebuild -project Ringback.xcodeproj -scheme Ringback \
  -destination 'generic/platform=iOS' -derivedDataPath build -allowProvisioningUpdates build)
claude plugin validate mod && claude plugin test mod
(cd hub && go vet ./... && go test ./... && go build -o build/ringback-hub . && ./smoke-test.sh)
(cd agent && go vet ./... && go test ./...)
uv run --project tools tools/hub-check/hub_check.py > /dev/null
```

They prove the code builds, not that calls work: ringing, push and audio count only on a real
phone. Before calling the phone, `tools/line-check` places a whole call through the real hub,
agent and speech node, with a stand-in phone, and the speech node checks itself with
`speech/check.sh`.

</details>

## License

[MIT](LICENSE)
