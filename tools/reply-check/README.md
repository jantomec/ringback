# Reply check

Checks what the voice speaks of a reply around Haiku's tool calls, in the voice's reply
parser (`ClaudeCodeLLMService` in `voice/ringback_voice/claude_code.py`). It feeds the parser
scripted events from Haiku's stream, with no Haiku and no audio, so it takes a second or two.

It checks:
- a tool call before any words gets "Okay." at once, and the words after the tool follow;
- a tool call tried again before any words (as when the voice used a wrong id and was
  refused) gets one "Okay.", and the words after it are still spoken. Before the fix on
  2026-10-30 they weren't, and the answer that waited for them went 40 s late
  (`docs/design.md`, Stage 3 step 3);
- words, then a tool: what follows the tool isn't spoken;
- "Okay.", words, then another tool: what follows the second tool isn't spoken;
- Haiku's own "Okay" right after the "Okay." is dropped.

Run it from the repo:

    uv run --project voice tools/reply-check/reply_check.py

It prints a line per case and exits 1 if any fails.

Results: 5/5 on 2026-10-30. Without the fix, the second case fails (4/5): only "Okay." is
spoken.
