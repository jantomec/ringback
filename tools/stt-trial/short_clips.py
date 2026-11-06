#!/usr/bin/env python3
"""Make tools/short-answers' clips with its own code and keep them, for prepare.py. They're
Kokoro's voices, on Apple silicon, in the tools' environment:

    uv run --project tools tools/stt-trial/short_clips.py OUT_DIR
"""

import json
import sys
from pathlib import Path

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "short-answers"))
from short_answers import make_clips  # noqa: E402

out = Path(sys.argv[1]).expanduser()
out.mkdir(parents=True, exist_ok=True)
clips = make_clips(out)
(out / "clips.json").write_text(json.dumps(clips, indent=0))
print(f"{len(clips)} clips in {out}")
