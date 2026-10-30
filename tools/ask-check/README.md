# Ask check

Checks the voice server's side of what a session asks you on a call: its permissions and
its questions (`/sessions/{id}/permissions` and `/sessions/{id}/questions` on the session
socket, in `voice/ringback_voice/sessions.py`); and its calls held outside your work hours. A voice server is started here with its own
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
- nobody answers that call. Once it's forgotten (`--unanswered` seconds), both
  permissions are still `pending`, and a long poll waits on through it to its end: they
  wait for the next call with the session you answer. One is then withdrawn, and is gone;
- the next call you answer gets the other as a note once the voice has opened, the voice
  asks it, and your spoken "Yes, go ahead." comes back as `allow`;
- on that call, a permission is `live` and the voice asks it. Your spoken yes comes back
  as `allow`, with your words. A question with two options is answered by your spoken "A
  new branch, please." with that option's label;
- a verdict a tool posts is final: a second gets 409. A withdrawn permission is gone, and
  one still waiting when you hang up gets `none`;
- the call's transcript has the requests and the answers.

A call back (`tools/phone-check --back`) answering a permission asked on a call you didn't
answer is checked there.

Then calls held outside your work hours: the stand-in switchboard answers `held_until`, as
the switchboard does outside every phone's hours. It checks:
- a permission with no call places a held one (`"call": "held"`, with the `held_until` the
  switchboard gave), which starts no Haiku and keeps the Mac awake (a `caffeinate` child
  of the voice server); a long poll answers `"held": true`; an offer for it gets 403;
- a session's new briefing (`/calls`) goes to the held call, without asking the
  switchboard for another, and a question goes to it too. Withdrawn, as the mod does
  outside your work hours, the question is asked in the terminal: its long poll gets 404,
  and the voice server logs it;
- held for an hour, it's asked again after `--reask` seconds, with the same call ID and
  the new subject; held for 2.5 s, at the time the switchboard gave; after a 503,
  `--reask` seconds later (a minute, in real use); then it rings and Haiku starts;
- answered, its transcript has the new briefing, and the permission the old briefing
  asked, and the question, went to the voice as notes, the question as one that waits in
  the terminal, and the voice said so (it mentions the terminal or the Claude app);
- another session's call, held 3 s, rings and isn't answered: it's forgotten
  `--unanswered` seconds after it rang, not after it was placed, and then no Haiku and no
  sleep assertion are left.

Run from the repo root. It uses your Claude subscription, four Haiku sessions:

    uv run --project voice tools/ask-check/ask_check.py [--unanswered S] [--reask S] [--keep DIR]

- `--unanswered` is how long the voice server waits for a call to be answered, 10 s by
  default instead of the real 90 s (it sets `RINGBACK_UNANSWERED` for the voice server).
- `--reask` is the longest a held call waits before the switchboard is asked again, 4 s by
  default instead of the real 10 min (`RINGBACK_REASK`).
- `--keep DIR` keeps the voice's log and the calls' transcripts.

It prints the calls' transcripts, the voice's log and the checks. What the voice says when it
asks, and how it takes unclear answers, is for `tools/call-check` (`?` and `??` turns).
The mod's side, holding the tool call, waiting on through `pending` and showing its own
prompt on `none`, isn't part of it.

Result on 2026-10-29, Claude Code 2.1.312, one run: all 22 checks passed, in about 1 min
40 s after the models loaded. The unanswered call's two permissions got `none` 10.0 s after
it was placed. The voice asked both on the live call. The answers were given 6.9 s (the
yes) and 9.5 s (the choice) after the voice server recognised your words: Haiku called the
tool before speaking, its first words came 2.7 and 4.1 s after your turn, and the answer then
waited until its spoken sentence had been heard (3.9 and 5.1 s of speech). A permission
still waiting when the phone hung up got `none` 3 ms after the voice server noticed.

Result on 2026-10-30, Claude Code 2.1.314, one run with held calls: all 41 checks passed
(the 22 above and 19 for held calls), in 3 min 5 s with the models loading. The call held
for an hour was asked again 4.01 s after it was placed, with the new subject; then 0.00 s
after the time the switchboard gave; then 4.00 s after the 503. Haiku started 0.06 s after
the switchboard took the call. Answered, the voice opened on the new briefing, then put
both the permission the old briefing asked and the question to you. The other call rang
2.59 s after it was placed and was forgotten 10.06 s after it rang ("wasn't answered within
10 s"). 0.64 s after that outcome arrived, no Haiku ran and the sleep assertion had ended.

Result on 2026-10-30 with what a call you don't answer asked kept pending (Stage 3 step 5),
two runs. Before merging with work hours, 23 of 24 checks passed: once Haiku answered the
question with its text as the id, then again with the right one after saying "Okay.", and
the answer waited 40 s for a sentence that was never spoken (fixed since; Stage 3 step 3 in
`docs/design.md`, and `tools/reply-check`). After merging, all 43 passed, in 2 min 48 s with the models
loading. Both permissions of the unanswered call were still `pending` once it was
forgotten, and a 14 s long poll waited to its end. The next call you answered asked the
one left after its opening, and "Yes, go ahead." allowed it.

Result on 2026-10-30, after the "Okay." fix: all 43 passed. After questions outside your work
hours started going to the terminal (Stage 3 step 7), all 45 passed, about 3 min with the
models loading: answered, the held call's voice asked the permission, then said "Separately,
a question about where to push the parser fix is waiting in the terminal."
