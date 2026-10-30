# Env check

Checks what a mod sees of its session's environment, for telling whether Remote Control is on
(Stage 3 step 7 in `docs/design.md`). No mod API says so. Claude Code's docs (env-vars,
hooks) say that from 2.1.199 `CLAUDE_CODE_BRIDGE_SESSION_ID` is set in the Bash tool's and
hooks' commands while the session has an active Remote Control connection, and removed when it
ends. Whether a mod's own `$.env`, or a command it runs with `$.process.run`, sees it isn't
documented. The ringback mod tries both.

A small mod (`mod/`) writes, when the session starts and each time a turn ends, the variables
its `$.env.get` reads (`CLAUDE_CODE_BRIDGE_SESSION_ID`, and others the docs say Claude Code
gives the Bash tool and hooks, such as `CLAUDE_EFFORT`) and the `CLAUDE` variables that
`printenv` run with `$.process.run` gets, to the file `$ENV_CHECK_OUT` names. `run.sh` runs
one `claude -p` session (Haiku) with it, which also runs `printenv` with the Bash tool, and
prints both. It uses your Claude subscription, one short turn:

    tools/env-check/run.sh [--inherited] [OUT_DIR]

By default it first removes the `CLAUDE` variables its own shell has, as when you start Claude
Code from an ordinary terminal; `--inherited` keeps them, as when Claude Code starts inside
another session's Bash tool. `OUT_DIR` (a temporary folder by default) gets the session's
folder, `mod.jsonl` and `bash.txt`; `mod.jsonl` can hold session IDs, so keep it outside the
repo.

To check Remote Control itself, which `claude -p` can't switch on, load the mod in an
interactive session from an ordinary terminal and turn Remote Control on:

    ENV_CHECK_OUT=/tmp/env-check.jsonl claude --plugin-dir tools/env-check/mod

then `/remote-control` (`/rc`), send any prompt, and read the line the end of its turn wrote:
whether `env` or `printenv` has `CLAUDE_CODE_BRIDGE_SESSION_ID`.

Result on 2026-10-30, Claude Code 2.1.314, without the inherited variables: the Bash tool saw
`CLAUDECODE`, `CLAUDE_CODE_SESSION_ID`, `CLAUDE_EFFORT`, `CLAUDE_PID` and six others; the mod's
`$.env` had only `CLAUDE_CODE_ENTRYPOINT`, and `printenv` run by the mod only that and the two
`CLAUDE_CODE_MESSAGING_*`. So what Claude Code gives the Bash tool and hooks, it gives only
them, not its own environment, which the mod and its commands see. `CLAUDE_EFFORT` is
documented as `CLAUDE_CODE_BRIDGE_SESSION_ID` is ("exposed to hook commands and Bash"), so the
mod most likely doesn't see Remote Control either; not checked with Remote Control on.

With `--inherited`, run from inside a Claude Code session that had Remote Control on, the mod
saw that session's `CLAUDE_CODE_BRIDGE_SESSION_ID` (and `CLAUDE_EFFORT` and the rest) in both
its `$.env` and `printenv`, though the `claude -p` session had no Remote Control: Claude Code
leaves an inherited value alone. A session started inside another's Bash tool would seem
to have Remote Control.

So ringback's mod doesn't use the variable. Claude Code's undocumented file for each
session, `~/.claude/sessions/<process>.json`, has a `bridgeSessionId` while Remote Control
is on (seen 2026-10-30), but the developer chose not to rely on it: `/ringback` turns Remote
Control on itself, and the rest takes it as on (`docs/design.md`, Stage 3 step 7).
