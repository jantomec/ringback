# ringback

Claude Code sessions call the user's iPhone when they're done, talk the result through and
take spoken replies. `docs/design.md` is the source of truth for requirements,
architecture, rejected alternatives, risks and the stage plan. Read it first.

Status: design only. Next step: **Stage 0 (Nebula test)**. It needs the phone's Nebula IP
from the user; the Mobile Nebula app shows it on the network's details screen.

## Environment

This repo is public. Machine-specific facts (hardware, Nebula addresses, accounts) are
kept in Claude's private memory for this project, never in the repo. Don't commit IP
addresses, hostnames, keys or certificates.

Requirements: a Mac with Xcode, a paid Apple Developer Program account, an iPhone, and
Nebula on both the Mac and the iPhone.

## Working rules

- Verify every stage on the real phone (locked, on mobile data, with headphones).
  Simulator runs and unit tests don't count for ringing, push or audio.
- Facts marked unverified in `docs/design.md` must be checked against current docs
  before building on them.
