# Reply speed

Times how quickly Haiku starts answering in a Claude Code session that stays open for the
call, the way the voice runs it (`claude -p` with stream-json in and out, no tools, no
settings files, its own system prompt). Each turn is timed from sending your words to the
first text of the reply. Your words then still have to be transcribed before, and the
first sentence spoken after.

Run from the repo root; it uses your Claude subscription. `ROUNDS` (default 3) is the
number of fresh sessions per setting, each with the six turns in `TURNS`:

    python3 tools/reply-speed/claude_code.py ROUNDS

Each setting in `SETTINGS` prints the first turn (after waiting as long as a phone rings)
and the later turns: median, then the range.

Result on 2026-10-28, Claude Code 2.1.312, three rounds: later turns had a median of about
1.2 s under every setting (0.9–3.2 s). The first turn ranged from 1.0 to 3.2 s. Neither
`--effort low` nor turning thinking off made a clear difference, and no setting produced any
thinking, so the voice uses the defaults.
