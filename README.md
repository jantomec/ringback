# ringback

Your Claude Code sessions ring you back. When a session finishes or needs a decision, it
calls your iPhone, tells you what it did, and you talk it through hands-free. You can
interrupt it like a person. Each session calls separately, and if you're on another call
it tries again a few minutes later.

Everything runs on your own Macs and phone:
- a switchboard on an always-on Mac, and a voice pipeline on each Mac that runs sessions;
- an iPhone app that rings through CallKit;
- audio over Nebula.

The voice runs Claude Haiku through Claude Code on your Claude subscription, so it needs no
API key.

Status: early. A locked iPhone rings, and the voice talks about a report you give it,
answers you and stops when you interrupt. Sessions are learning to call (Stage 3, in
progress): with `/ringback` on, a session writes a briefing when it finishes and places a
call, so far tried only without the phone, and your replies don't reach it yet. See
[docs/design.md](docs/design.md).

- `app/`: the iPhone app (generate the Xcode project with `xcodegen` in `app/`);
- `mod/`: the Claude Code mod that adds `/ringback` to your sessions;
- `switchboard/`: rings the phone and logs what happened to each call;
- `voice/`: the voice of a call, on the Mac whose session is calling;
- `tools/`: checks for Nebula, for choosing speech-to-text and speech, for how fast Haiku
  replies, and for playing a scripted call or a session's call without the phone.

## License

[MIT](LICENSE)
