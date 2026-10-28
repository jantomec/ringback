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

Status: design stage. See [docs/design.md](docs/design.md).

## License

[MIT](LICENSE)
