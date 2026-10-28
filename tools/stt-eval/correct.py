#!/usr/bin/env python3
"""Let Haiku repair each model's transcripts from context, via Claude Code.

    python3 tools/stt-eval/correct.py DATA_DIR [MODEL ...]

For each model's transcript of each recording, sends all utterances as numbered
lines to `claude -p` with PROMPT and writes the repaired lines to
DATA_DIR/hyp/<model>+haiku/, which score.py then scores like any other model.
Lines Haiku can't recover are kept and marked [UNCLEAR] (the scorer drops the
marker). Runs claude from an empty directory so no project CLAUDE.md is loaded.
"""

import json
import re
import subprocess
import sys
import tempfile
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

HAIKU = "claude-haiku-5-5"
PROMPT = """\
You receive transcripts produced by automatic speech recognition of a developer talking
on the phone to a Claude Code session that works on a software project. The developer is
not a native English speaker. Recognition errors are common: words replaced by
similar-sounding words (often technical terms, commands and tool names), missing or extra
short words, and filler sounds. The session may have asked the developer to approve or
deny an action.

Rewrite each line as what the developer most likely said. Change only words you believe
were misrecognized and keep the developer's own wording and grammar otherwise. If a line
is too garbled to recover, repeat it unchanged and append [UNCLEAR].

Answer with exactly one line per input line, in the same order, each starting with its
number and a colon, and nothing else."""


def repair(lines, cwd):
    numbered = "\n".join(f"{i + 1}: {l}" for i, l in enumerate(lines))
    for attempt in range(2):
        r = subprocess.run(
            ["claude", "-p", "--model", HAIKU, "--tools", "", "--no-session-persistence",
             "--output-format", "json", "--system-prompt", PROMPT, numbered],
            capture_output=True, text=True, cwd=cwd,
        )
        text = json.loads(r.stdout)["result"]
        out = {}
        for m in re.finditer(r"^\s*(\d+):\s?(.*)$", text, re.M):
            out[int(m.group(1))] = m.group(2).strip()
        if sorted(out) == list(range(1, len(lines) + 1)):
            return [out[i + 1] for i in range(len(lines))]
    raise RuntimeError(f"Haiku returned {len(out)} lines for {len(lines)}")


def job(data, model, path, cwd):
    d = json.loads(path.read_text())
    t = time.perf_counter()
    fixed = repair([u["text"] for u in d["utterances"]], cwd)
    for u, f in zip(d["utterances"], fixed):
        u["asr"], u["text"] = u["text"], f
    d["corrected_by"] = HAIKU
    out = data / "hyp" / f"{model}+haiku"
    out.mkdir(exist_ok=True)
    (out / path.name).write_text(json.dumps(d, indent=1))
    unclear = sum("[UNCLEAR]" in f for f in fixed)
    changed = sum(u["asr"] != u["text"] for u in d["utterances"])
    return f"{model} {path.stem}: {changed}/{len(fixed)} lines changed, {unclear} unclear, {time.perf_counter() - t:.1f} s"


def main():
    data = Path(sys.argv[1]).expanduser()
    models = sys.argv[2:] or sorted(p.name for p in (data / "hyp").iterdir() if p.is_dir() and "+" not in p.name)
    with tempfile.TemporaryDirectory() as cwd, ThreadPoolExecutor(4) as pool:
        jobs = [pool.submit(job, data, m, p, cwd) for m in models for p in sorted((data / "hyp" / m).glob("*.json"))]
        for j in jobs:
            print(j.result(), flush=True)


if __name__ == "__main__":
    main()
