#!/bin/sh
# Runs one `claude -p` session (Haiku) with the env-check mod and compares what the Bash tool
# sees of the session's environment with what the mod sees. See README.md.
#
#   tools/env-check/run.sh [--inherited] [OUT_DIR]
set -eu
here=$(cd "$(dirname "$0")" && pwd)
inherited=no
if [ "${1:-}" = --inherited ]; then
    inherited=yes
    shift
fi
out=${1:-$(mktemp -d)}
mkdir -p "$out/work"
rm -f "$out/mod.jsonl"
if [ $inherited = no ]; then
    # As from an ordinary terminal: without what this shell inherited from a Claude Code session.
    for name in $(env | grep '^CLAUDE' | cut -d= -f1); do unset "$name"; done
fi
cd "$out/work"
ENV_CHECK_OUT="$out/mod.jsonl" claude -p \
    "Run this with the Bash tool: printenv | grep '^CLAUDE' | cut -d= -f1 | sort. Reply with its output only." \
    --plugin-dir "$here/mod" --model haiku --allowedTools=Bash > "$out/bash.txt" 2>&1
echo "the Bash tool sees: $(grep '^CLAUDE' "$out/bash.txt" | tr '\n' ' ')"
echo "the mod sees:"
cat "$out/mod.jsonl"
