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

Status: early. Ringing a locked iPhone and two-way audio over Nebula work (Stage 1); the
conversation comes next. See [docs/design.md](docs/design.md).

- `app/`: the iPhone app (generate the Xcode project with `xcodegen` in `app/`);
- `switchboard/`: rings the phone and logs what happened to each call;
- `voice/`: the audio side of a call, on the Mac whose session is calling;
- `tools/`: checks for Nebula, and for choosing speech-to-text and speech.

## License

[MIT](LICENSE)
