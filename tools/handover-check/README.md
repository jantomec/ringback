# Hand-over check

Checks what a better model on the line rests on: the voice's own Claude Code session
(`voice/ringback_voice/haiku.py`), handed over from Haiku to Opus without a call, and
what's left of it on disk afterwards. The whole call, with speech, is checked with
`tools/call-check`.

Each run starts Haiku as a call for a session starts it: the voice's instructions with a
briefing that holds a code name, its tools, and a known `--session-id`, so Claude Code keeps
the session on disk. It plays the opening and two turns: one Haiku should pass to the
session with its tool, whose result holds a receipt word, and one that only says
something. Then it hands over as the voice does (`Haiku.hand_over`): Haiku's process is
closed, and `claude -p --resume ID --model opus` starts with the same flags and tools.
Opus gets the voice's hand-over note and is asked what only the briefing, the
conversation and the tool's result tell it. Last, the session is closed as at the end of a
call, which deletes the folder Claude Code kept for it.

Run from the repo root; it uses your Claude subscription:

    uv run --project voice tools/handover-check/handover_check.py [--runs N] [--bare-resume]

- `--runs` is the number of runs (1 by default).
- `--bare-resume` resumes without `--system-prompt`, to see whether the session's own record
  of its system prompt is enough. The voice gives it again anyway.

For each turn it prints the model, when the first words came, the tokens as `claude -p`
reports them (input, read from the prompt cache, written to it, output), the cost it
reports for the turn, and the reply. Then what Claude Code kept on disk while Haiku ran and
at the end of the call, whether Opus knew the code name, the message, the receipt word and
your favourite fruit in the same session, and what's left after closing. On the subscription the cost isn't
charged; it shows how much of your usage a turn takes, relative to others.

Each run's briefing ends with a build number of its own, so its system prompt is new to the
prompt cache, as a real call's is.

Result on 2026-10-30, Claude Code 2.1.312, three runs, and eight more of earlier versions of
this check (the same briefing in each run; in four, a receipt number instead of a word):
- **Resume works with `-p` and stream-json in and out.** In all eleven, Opus knew the code
  name (system prompt), the message and the receipt (Haiku's tool call and its result) and
  the fruit (a plain turn), in the same session ID. Once it said the receipt number as
  "forty-four seventeen", as the voice's instructions say to speak numbers, hence the word.
- **The system prompt carries over by itself.** With `--bare-resume` (three runs) Opus still
  knew the code name: Claude Code records the system prompt on the first request and
  reuses it on resume (`--system-prompt-snapshot`, on by default).
- **On disk:** only `~/.claude/projects/<working directory>/<session ID>.jsonl`, already
  while Haiku runs, in a folder named for the call's own temporary directory (its real path,
  `/private/var/...`, with every character but letters and digits made `-`). Once, in a raw
  `claude -p` run before this check, the folder also held an empty `memory/` folder. The
  voice deletes the whole folder, and after closing nothing named for the session was left
  under `~/.claude`. A built-in plugin's store
  (`~/.claude/plugins/store/cc-plugin-agents-md_builtin-*.json`) holds the last session's ID
  in a queued usage row; it's one row, replaced by the next session, so it doesn't pile up.
- **Handing over** took 0.57 s (closing Haiku and starting Opus); Opus's first words came
  1.1–1.2 s after its note was sent (1.1–1.8 s in all eleven).
- **Prompt caching:** with a briefing of its own, Opus's first turn read 1,125 tokens from
  the cache (the part every call shares), wrote about 2,800, and cost $0.025 as reported,
  against $0.0025–0.0027 for each later Opus turn and $0.0001–0.0005 for each Haiku turn. So
  the hand-over costs about ten later Opus turns' worth, once. When Opus had nothing of the
  voice's prompt cached (the first run of the day), it wrote 3,781 tokens, for $0.033. When
  it had the very same system prompt cached from a run minutes before, $0.012–0.014.
