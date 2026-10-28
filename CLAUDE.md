# ringback

Claude Code sessions call the user's iPhone when they're done, talk the result through and
take spoken replies. `docs/design.md` is the source of truth for requirements,
architecture, rejected alternatives, risks and the stage plan. Read it first.

Status: Stage 0 (Nebula) is done; its results are in `docs/design.md`. Next step:
**Stage 1 (ring + audio)**, the first code.

## Environment

This repo is public and holds only what someone else needs to install ringback. The
user's personal setup and info (hardware, machines, Nebula addresses, accounts, location)
stay in Claude's private memory for this project. Don't commit IP addresses, hostnames,
keys or certificates.

Requirements: a Mac with Xcode, a paid Apple Developer Program account, an iPhone, and
Nebula on both the Mac and the iPhone, with a lighthouse that acts as a relay.

## Working rules

- Verify every stage on the real phone (locked, on mobile data, with headphones).
  Simulator runs and unit tests don't count for ringing, push or audio.
- Facts marked unverified in `docs/design.md` must be checked against current docs
  before building on them.
