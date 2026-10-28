# ringback

Your Claude Code sessions ring you back. When a session finishes or needs a decision, it
calls your iPhone, tells you what it did, and you talk it through hands-free. You can
interrupt it like a person. Each session calls separately, and if you're on another call
it tries again a few minutes later.

Everything runs on your own Mac and phone:
- a switchboard and a voice pipeline on the Mac;
- an iPhone app that rings through CallKit;
- audio over Nebula.

The only running cost is Claude Haiku API usage.

Status: design stage. See [docs/design.md](docs/design.md).

## License

[MIT](LICENSE)
