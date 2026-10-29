# Session check

Plays a session's call on this Mac, up to where the phone would ring. A Claude Code
session (`claude -p`) loads the ringback mod from `mod/`, runs `/ringback`, then does a
little work. When that turn ends, the mod has the session write its call briefing and
gives it to a voice server started here, which asks a switchboard started here to ring.
That switchboard knows only a made-up phone and has no APNs key, so nothing rings. Use it
to check a change to the mod, the voice server's session side or the switchboard's `/call`
before a call on the phone. The phone, the voice's conversation and Nebula aren't part of
it.

Stop your own voice server first: both use the same socket. Build the switchboard first
(`cd switchboard && swift build -c release --arch arm64 --arch x86_64`). Run from the
repo root; it uses your Claude subscription:

    uv run --project voice tools/session-check/session_check.py [--model MODEL] [--keep DIR] [WORK]

- `WORK` is the prompt the session works on before it calls (by default, one sentence).
- `--model` is the session's model, Haiku by default to keep it quick.
- `--keep DIR` keeps both servers' logs.

It prints the session's turns, the briefing among them, both servers' logs and these
checks:
- an offer for a call the voice server didn't place is refused;
- the session placed a call, and how long that took from its first prompt;
- the call names this voice server and the project, and its subject has 3–8 words;
- the voice server placed that call, and the push went out with the call's ID.

Result on 2026-10-29, Claude Code 2.1.312, Haiku as the session's model (three runs): the
call was placed 6 s after the session's first prompt each time, with a 5-word subject and a
briefing of 132–161 words in plain spoken sentences: what happened, that nothing needed a
decision, and what the check didn't cover. All checks passed. (One run first showed a
failure that was the check's own: it didn't match call IDs that contain a `-`.)
