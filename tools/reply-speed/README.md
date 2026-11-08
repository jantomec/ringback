# Reply speed

Times how quickly the voice's model starts answering in a Claude Code session that stays open
for the call, the way the voice runs it (`claude -p` with stream-json in and out, no tools, no
settings files, the voice's instructions from `agent/texts.go`). Each turn is timed from sending your words to the
first text of the reply. Your words then still have to be transcribed before, and the
first sentence spoken after.

Run from the repo root; it uses your Claude subscription. `ROUNDS` (default 3) is the
number of fresh sessions per setting, each with the six turns in `TURNS`, and the settings in
`SETTINGS`, one per model, can be named:

    python3 tools/reply-speed/claude_code.py ROUNDS [haiku,sonnet,opus]
    python3 tools/reply-speed/claude_code.py stop [MODEL]

`stop` asks for a long reply, stops it once its first text has come, and times the next turn.

Each setting prints the first turn (after waiting as long as a phone rings)
and the later turns: median, then the range.

Result on 2026-10-28, Claude Code 2.1.312, three rounds: later turns had a median of about
1.2 s under every setting (0.9–3.2 s). The first turn ranged from 1.0 to 3.2 s. Neither
`--effort low` nor turning thinking off made a clear difference, and no setting produced any
thinking, so the voice uses the defaults.

Result on 2026-11-06, Claude Code 2.1.319, the voice's instructions, three rounds per model,
15 later turns each. A turn with no text is printed, and left out of the times:

| Model | First turn: median (range) | Later turns: median (range) |
|---|---|---|
| Haiku | 1.19 s (0.84–1.32) | 1.16 s (0.93–2.59) |
| Sonnet | 3.33 s (1.92–7.68) | 1.98 s (0.84–6.35) |
| Opus | 0.56 s (0.50–0.57) | 0.91 s (0.50–2.31) |

One Sonnet turn in an earlier run of this check had no text, and none in this one. The voice's
tools weren't there, so a real call's times can differ. `stop`, tried once on Sonnet: the reply
ended 0.01 s after the stop (`control_request`, `interrupt`), and the session's next turn
answered 2.60 s later.

Result on 2026-11-06, the same three rounds on Opus, also timing the rest of each later turn: first text at a
median of 1.03 s, the first sentence complete at 1.31 s, the whole reply at 1.66 s (slowest 4.68 s, for 138
words). So waiting for the whole reply costs about 0.35 s more than waiting for its first sentence.
