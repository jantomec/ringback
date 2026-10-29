# Briefing check

Shows what a session writes about the call itself in its call briefing, for the mod at one
or more git revisions. The session is told afterwards how each call went, so the briefing
shouldn't say whether the call will reach you ("I can't confirm this call reached you").

For each revision, it takes `mod/` as it is there, points its socket at a path that doesn't
exist, so no call can be placed, and runs `--runs` sessions (`claude -p`, 4 at a time):
`/ringback`, then the session check's work, "Say in one sentence that the session check
works." When that turn ends, the mod asks for the briefing. The session's work is about
ringback itself, as in `tools/session-check`, which is where briefings said the call might
not reach you.

Run from the repo root; it uses your Claude subscription. `.` is the working tree (the
default):

    python3 tools/briefing-check/briefing_check.py [--runs N] [--model MODEL] [REV ...]

It prints each briefing's subject and its sentences that mention a call or the phone, for
you to judge: saying that ringback will call is fine, saying the call may not reach you
isn't.

Result on 2026-10-29, Claude Code 2.1.312, Haiku as the session's model. The briefings that
said the call or the phone wasn't confirmed:
- the prompt that asked "what you didn't check" (64009b0): 6 of 8;
- that prompt with "in your work" added, and a sentence saying to leave out the call itself
  because the session is told afterwards how it went: 2 of 4. One of those repeated the
  instruction ("you will hear how it went afterward");
- the same with "what you didn't check" left in and a sentence forbidding any word about
  the calls, "not even as something you can't confirm": 2 of 10;
- "what you didn't check" dropped, with the leave-out sentence (what the mod uses): 1 of 14.
  Its other briefings mentioned the call only to say ringback will call.

The middle two were scratch variants, not commits. Dropping the question means the briefing
no longer asks for what the session didn't check in its work, which the voice used to
answer "did you run the tests?". Whether briefings for real work miss that wasn't tested:
the check's work is a single sentence.
