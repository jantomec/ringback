# Ask check

Checks the voice server's side of what a session asks you on a call: its permissions and
its questions (`/sessions/{id}/permissions` and `/sessions/{id}/questions` on the session
socket, in `voice/ringback_voice/sessions.py`). A voice server is started here with its own
socket, in a temporary folder, and phone-check's ports 18797 and 18798. So it runs beside
your own voice server, but not beside `tools/phone-check`. The session's requests go to that
socket as its mod sends them. A stand-in switchboard takes the calls they place, so nothing
rings. The phone's side is phone-check's: aiortc, offering while it rings, and your words
spoken by Kokoro.

It checks:
- an unknown id, another session's, or the wrong kind (a permission asked for as a
  question) gets 404; a permission without a tool, or no questions, gets 400;
- with no call, a permission places one with its subject (`"call": "placed"`), with an id
  of four lowercase letters. Two sent at once, as from parallel tool calls, share that call.
  A long poll for a verdict waits the time asked for while it rings. A question for the
  same session goes to that call, without placing another, and is gone once withdrawn;
- nobody answers that call. Once it's forgotten (`--unanswered` seconds), the permission's
  verdict is `none`, which ends a long poll before its 25 s;
- on a call you answer, a permission is `live` and the voice asks it. Your spoken "Yes, go
  ahead." comes back as `allow`, with your words. A question with two options is answered
  by your spoken "A new branch, please." with that option's label;
- a verdict a tool posts is final: a second gets 409. A withdrawn permission is gone, and
  one still waiting when you hang up gets `none`;
- the call's transcript has both requests and both answers.

Run from the repo root. It uses your Claude subscription, two Haiku sessions:

    uv run --project voice tools/ask-check/ask_check.py [--unanswered S] [--keep DIR]

- `--unanswered` is how long the voice server waits for a call to be answered, 10 s by
  default instead of the real 90 s (it sets `RINGBACK_UNANSWERED` for the voice server).
- `--keep DIR` keeps the voice's log and the call's transcript.

It prints the call's transcript, the voice's log and the checks. What the voice says when it
asks, and how it takes unclear answers, is for `tools/call-check` (`?` and `??` turns).
The mod's side, holding the tool call and showing its own prompt on `none`, isn't part of
it.

Result on 2026-10-29, Claude Code 2.1.312, one run: all 22 checks passed, in about 1 min
40 s after the models loaded. The unanswered call's two permissions got `none` 10.0 s after
it was placed. The voice asked both on the live call. The answers were given 6.9 s (the
yes) and 9.5 s (the choice) after the voice server recognised your words: Haiku called the
tool before speaking, its first words came 2.7 and 4.1 s after your turn, and the answer then
waited until its spoken sentence had been heard (3.9 and 5.1 s of speech). A permission
still waiting when the phone hung up got `none` 3 ms after the voice server noticed.
