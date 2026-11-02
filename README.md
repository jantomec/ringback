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
  <img src="https://img.shields.io/badge/Mac-Apple_silicon-1f2328?style=flat-square" alt="Mac with Apple silicon">
  <img src="https://img.shields.io/badge/license-MIT-1f2328?style=flat-square" alt="MIT license">
</p>

<p align="center">
  <img src="docs/images/hero.svg" width="820" alt="A Claude Code session finishes in a terminal, and an iPhone rings with the call">
</p>

Leave a long task running and walk away. When it's done, the session calls your phone, a
voice tells you what happened, and you answer the way you'd answer a colleague: *"Push it and
open a pull request."* Your words reach the session, it carries on, and if there's more to
decide, it calls again.

Ringback runs on your own Macs and phone, over your own [Nebula](https://github.com/slackhq/nebula)
network. The voice is Claude Haiku through Claude Code, on your Claude subscription: no API key,
and no server of ours in between.

## What a call is like

- **It rings like a phone call.** Locked phone, headphones in, through CallKit. The voice is
  ready the moment you answer.
- **You can interrupt.** Start talking and the voice stops, the way a person would. It answers
  you in a second or two.
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
app's icon counts the calls that wait.

## How it works

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/how-it-works-dark.png">
    <img src="docs/images/how-it-works-light.png" width="820" alt="Claude Code sends a briefing to the voice server on the same Mac, which asks the switchboard to ring; the switchboard pushes to the iPhone; your voice goes straight between the iPhone and the voice server over Nebula, and what you said goes back to the session">
  </picture>
</p>

A small **mod** in each Claude Code session writes a spoken briefing when a turn ends. The
**voice server** on that Mac asks the **switchboard**, a tiny always-on service, to ring your
**iPhone**. When you answer, the audio goes straight between your phone and the Mac over Nebula:
Silero hears when you speak, Parakeet recognises it, Haiku answers, and Kokoro speaks. What you
ask for goes back to the session, and its answer comes back to the call.

The reasoning behind each part, and what every test on the phone showed, are in
[docs/design.md](docs/design.md).

## Get started

You need:

- a Mac with Apple silicon and Xcode, for each Mac that runs sessions;
- an always-on Mac for the switchboard (it needs nothing installed);
- an iPhone on iOS 26 or later, and a paid Apple Developer account, for VoIP pushes;
- Nebula on the Macs and the phone, with a lighthouse that relays;
- Claude Code on a Claude subscription that includes Opus.

[docs/setup.md](docs/setup.md) takes you through it in six steps. Once everything is set up, a
session calls you like this:

```sh
claude --plugin-dir path/to/ringback/mod
> /ringback
```

## Status

Ringback is a working prototype, built in stages and tested on real calls.

- [x] A locked iPhone rings, and you talk with the voice
- [x] Sessions call when they finish, and what you say reaches them
- [x] Permission prompts and questions, answered on the call
- [x] Opus on the line, missed calls called back, work hours
- [x] Weak signal: 3G, and the Network Link Conditioner's Very Bad Network
- [x] Recents and Sessions: call any session, or a recent call back
- [ ] The hold switch, its breathing light, and the Lock Screen widget: built, partly tried on the phone
- [ ] Calls that survive a restart of the voice server: built, tried offline
- [ ] Several sessions calling in turn, with retries: planned

<details>
<summary><b>Repository and development</b></summary>
<br>

| Folder | What's in it |
|---|---|
| `app/` | The iPhone app (SwiftUI), with its Control Center and Lock Screen extension |
| `mod/` | The Claude Code mod (TypeScript) that adds `/ringback` |
| `voice/` | The voice server (Python): calls, speech, and the link to each session |
| `switchboard/` | The switchboard (Swift, no dependencies) and its smoke test |
| `tools/` | The checks and measurements behind the design, each with a README |
| `docs/` | The design, the setup guide, and the images here |

Before every commit, these show that everything builds and passes its offline checks, in about
a minute:

```sh
(cd app && xcodegen generate && xcodebuild -project Ringback.xcodeproj -scheme Ringback \
  -destination 'generic/platform=iOS' -derivedDataPath build -allowProvisioningUpdates build)
(cd switchboard && swift build -c release --arch arm64 --arch x86_64 && ./smoke-test.sh)
(cd voice && uv run python -m compileall -q ringback_voice && uv run python -c 'import ringback_voice.__main__')
claude plugin validate mod && claude plugin test mod
```

They prove the code builds, not that calls work: ringing, push and audio count only on a real
phone. To try a change to the conversation first, play a scripted call through the voice with
`tools/call-check`.

</details>

## License

[MIT](LICENSE)
