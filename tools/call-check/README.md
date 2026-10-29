# Call check

Plays a scripted call through the voice's own pipeline (`call_task` in
`voice/ringback_voice/__main__.py`) on this Mac, without the phone. Your side is played in
real time from text, which another Kokoro voice speaks, or from your own recordings. The
voice's side is "played" on a clock. Use it to check a change to the conversation, and to
time it, before a call on the phone. Ringing, network and the phone's audio aren't part
of it.

Run from the repo root. It uses your Claude subscription, and `OUT_DIR` gets `call.wav`
with both sides of the call, so keep it outside the repo:

    uv run --project voice tools/call-check/call_check.py OUT_DIR TURN... [--report FILE] [--turn-wait S]
        [--sensitivity S] [--background-filter G] [--background SOUND [--background-db DB]]

- Each `TURN` is what you say: text, or a `CLIP`: a 16 kHz mono WAV file, optionally cut
  to a span in seconds (`speech.wav:12.5-19`), so stretches of your own recordings can be
  turns. In text, `[1.2]` is a pause of that many seconds.
- A turn starts 1 s after the voice stops talking. Write it as `@S:...` to start it S
  seconds after the voice starts its reply instead, which interrupts it. If the voice
  doesn't answer a turn within 15 s, the next one starts anyway, or the call ends.
- `--report` is what the voice reports, as for `ringback-voice serve`.
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

It prints the voice's log as the server would, then the timing measured on its own clock,
from where your speech starts and ends in each clip:
- when each of your turns started and ended, and whether the voice talked over you;
- when the voice started each reply, and how long after you stopped;
- how long it went on after you interrupted;
- `NOT YOU`: an interruption or words that Parakeet heard while none of your turns was
  playing (or within 1 s of one ending), so they came from the background.
