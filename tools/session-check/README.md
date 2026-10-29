# Session check

Plays a session's calls on this Mac, up to where the phone would ring. A Claude Code
session (`claude -p`) loads the ringback mod from `mod/`, runs `/ringback`, then does a
little work. When that turn ends, the mod has the session write its call briefing and
gives it to a voice server started here, which asks a switchboard started here to ring.
That switchboard knows only a made-up phone and has no APNs key, so nothing rings. Then a
message is left for the session, as the voice leaves what you say on a call: the session
answers it, and since no call is live, it writes a new briefing and calls again. Neither
call is answered, so once each one's time is up the voice server tells the session so, and
the mod adds that to the conversation as a note, which starts no turn. Then the session is
asked at the prompt, as you would when you're back, what it was told about its last call,
and it should quote the note. Use it to
check a change to the mod, the voice server's session side or the switchboard's `/call`
before a call on the phone. The phone, the voice's conversation and Nebula aren't part of
it; `tools/call-check --session` plays the voice's side.

Stop your own voice server first: both use the same socket. Build the switchboard first
(`cd switchboard && swift build -c release --arch arm64 --arch x86_64`). Run from the
repo root; it uses your Claude subscription:

    uv run --project voice tools/session-check/session_check.py [--model MODEL] [--unanswered S] [--keep DIR] [WORK]

- `WORK` is the prompt the session works on before it calls (by default, one sentence).
- `--model` is the session's model, Haiku by default to keep it quick.
- `--unanswered S` is how long the voice server waits for each call to be answered, 10 s
  by default instead of the real 90 s (it sets `RINGBACK_UNANSWERED` for the voice server).
- `--keep DIR` keeps both servers' logs and everything the session printed
  (`session.jsonl`).

It prints the session's turns, the briefing among them, both servers' logs and these
checks:
- an offer for a call the voice server didn't place is refused;
- the session placed a call, and how long that took from its first prompt;
- the call names this voice server and the project, and its subject has 3–8 words;
- the voice server placed that call, and the push went out with the call's ID;
- the message was left, the session answered it, and it called again with a new call ID,
  and how long that took from leaving the message;
- each call's outcome, that it wasn't answered, was shown in the session (the mod's dim
  line) within S + 30 s;
- those outcomes started no turn and no call in the next 10 s;
- asked at the prompt what it was told about its last call, the session quoted that it
  "wasn't answered within S s". Only the note says how long the call waited, so the
  session can't get that from a guess.

Result on 2026-10-29, Claude Code 2.1.312, Haiku as the session's model (three runs): the
call was placed 6 s after the session's first prompt each time, with a 5-word subject and a
briefing of 132–161 words in plain spoken sentences: what happened, that nothing needed a
decision, and what the check didn't cover. All checks passed. (One run first showed a
failure that was the check's own: it didn't match call IDs that contain a `-`.)

Result on 2026-10-29 with the message and the second call (one run): all 12 checks passed.
The first call was placed 6 s after the session's first prompt, and the second 5 s after
the message was left; its briefing mentioned the message and the answer.

Result on 2026-10-29 with the calls' outcomes (four runs, the third with `--unanswered 3`):
all 14 checks passed each time, in 38–86 s (the voice's models took 32 s to load in the
first run, 5–9 s after). Each outcome was queued when its call's time was up, 10.0 s (or
3.0 s) after it was placed, and shown in the session. In the 3 s run, the first outcome
came while the session was answering the message and writing its next briefing, and was
added only when that turn ended. Asked what it was told, the session quoted both outcomes
word for word each time, and one later briefing said "Earlier updates may not have reached
you, so here is the full picture." Before the mod said who wrote the note, Haiku read it
as part of the prompt and said it hadn't been told anything (outside this check, one
session). In the first three runs, whose briefing prompt still asked what the session
didn't check, 6 of 9 briefings said the call or the phone wasn't confirmed; in the last,
with the prompt the mod uses now, none of 3 did (see `tools/briefing-check`).
