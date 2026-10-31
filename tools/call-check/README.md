# Call check

Plays a scripted call through the voice's own pipeline (`call_task` in
`voice/ringback_voice/__main__.py`) on this Mac, without the phone. Your side is played in
real time from text, which another Kokoro voice speaks, or from your own recordings. The
voice's side is "played" on a clock. Use it to check a change to the conversation, and to
time it, before a call on the phone. Ringing, network and the phone's audio aren't part
of it.

Run from the repo root. It uses your Claude subscription, and `OUT_DIR` gets `call.wav`
with both sides of the call, so keep it outside the repo:

    uv run --project voice tools/call-check/call_check.py OUT_DIR TURN... [--report FILE] [--session]
        [--turn-wait S] [--sensitivity S] [--background-filter G] [--background SOUND [--background-db DB]]

- Each `TURN` is what you say: text, or a `CLIP`: a 16 kHz mono WAV file, optionally cut
  to a span in seconds (`speech.wav:12.5-19`), so stretches of your own recordings can be
  turns. In text, `[1.2]` is a pause of that many seconds.
- A turn starts 1 s after the voice stops talking. Write it as `@S:...` to start it S
  seconds after the voice starts its reply instead, which interrupts it. If the voice
  doesn't answer a turn within 15 s, the next one starts anyway, or the call ends.
- A `TURN` written `...` is you saying nothing: the next turn waits until the voice has
  spoken again, as after asking for a better model, when Opus speaks first.
- `--report` is what the voice reports, as for `ringback-voice serve`.
- `--session` makes it a call for a session, with `--report` as the session's briefing:
  the voice can pass messages to the session, which are listed with the timings, and the
  call's transcript goes to `OUT_DIR/.ringback/calls/`. A `TURN` written `!TEXT` is the
  session's answer arriving as news, instead of your speech; it's due when a spoken turn
  would be. So is what the session asks you, as its mod sends it to the voice server:
  - `?TOOL: INPUT // CONTEXT` asks your permission for a tool call, such as
    `?Bash: rm -rf build // I'm cleaning up before the release.` (`TOOL: ` and
    ` // CONTEXT` can be left out; the tool is then Bash);
  - `??QUESTION | LABEL: DESCRIPTION | LABEL: DESCRIPTION` asks you a question with its
    options (`??*` if you can choose several).

  As the first `TURN`, either makes the call one placed to ask it: its briefing asks it,
  instead of `--report`'s. Each answer the voice gives is listed with the timings: when
  Haiku gave it, held until you've heard it said, and when it was given to the session, or
  dropped because you cut the voice off first. The answers the session got close the list.
- `--turn-wait` overrides how long the voice waits for the end of your turn when Pipecat's
  end-of-turn model thinks you haven't finished (`TURN_WAIT` in the voice).
- `--sensitivity` is the app's interruption sensitivity, from 0 to 1 (0.5 by default), and
  `--background-filter` the app's background filter, from 0 (off) to 1 (0.5 by default).
- `--background SOUND` plays under the whole call, looped: a `CLIP` (such as gym noise), or
  text that a third Kokoro voice speaks (someone else talking). `--background-db` turns it
  up or down; the levels of both sides while there's sound (pauses left out) are printed
  first.
- Audio connects 2.5 s after the call starts, as on the phone.

For example, interrupting the voice's opening, then two normal turns:

    uv run --project voice tools/call-check/call_check.py OUT_DIR \
      "@3:Sorry, which Mac is that?" "How much memory did it use before?" "Thanks, that's all."

For a session, answering the briefing's question, then the session's news, then wrapping
up:

    uv run --project voice tools/call-check/call_check.py OUT_DIR --session --report briefing.txt \
      "Yes, push it and open the pull request." "!Pushed it and opened pull request 42." "Thanks, that's all."

For a session asking permission mid-call, then you saying yes:

    uv run --project voice tools/call-check/call_check.py OUT_DIR --session --report briefing.txt \
      "Okay, sounds good." "?Bash: git push --force origin fix-parser // I rebased it onto main." \
      "Yes, go ahead." "Thanks, that's all."

For a better model on the line, waiting for Opus to speak first, then asking what only the
earlier conversation tells:

    uv run --project voice tools/call-check/call_check.py OUT_DIR --session --report briefing.txt \
      "How long do the tests take now?" "Can I get a better model on the line?" "..." \
      "What did I ask you before?" "Thanks, that's all."

Whether the voice does work itself or passes it on (2026-10-31), with the briefings in
`briefings/`: a call back with no briefing, then a task, cutting off the voice's reply; and
a briefing's questions, then a suggestion only the session can make:

    uv run --project voice tools/call-check/call_check.py OUT_DIR --session \
      --report tools/call-check/briefings/called-back-no-briefing.txt \
      "Write a short poem." "@0.3:Let it be about Halloween." \
      "No, no, no, just write it, I'll read it later." "Thanks, that's all."
    uv run --project voice tools/call-check/call_check.py OUT_DIR --session \
      --report tools/call-check/briefings/parser-fix.txt \
      "How many tests were there, and how long did they take?" "What was the crash about again?" \
      "Okay. Also, what's a good name for the pull request?" "Thanks, that's all."

Result: before the voice was told it does no work itself, the first wrote and read Halloween
poems, twice, and passed nothing on (one run, 63 s). After, three runs of 47–54 s: each passed
"write a short poem about Halloween" to the session once (the first message was dropped, as
your interruption came before you'd heard it announced), and none mentioned the interruption;
to "just write it", twice "I can't write it myself", once a further message. The second, two
runs of 82–88 s: the briefing's questions answered from it, the name asked of the session.

It prints the voice's log as the server would, then the timing measured on its own clock,
from where your speech starts and ends in each clip:
- when each of your turns started and ended, and whether the voice talked over you;
- when the voice started each reply, and how long after you stopped;
- how long it went on after you interrupted;
- when Haiku put Opus on, when you'd heard it say so, and when Opus took over (or that it
  didn't, because you cut the voice off first);
- `NOT YOU`: an interruption or words that Parakeet heard while none of your turns was
  playing (or within 1 s of one ending), so they came from the background.

Last, it names any session folder that Claude Code kept for the call's voice and that's
left in `~/.claude/projects`: there should be none, because the voice deletes it when the
call ends.
