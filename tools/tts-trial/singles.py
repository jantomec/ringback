import json, re, difflib, sys
from collections import Counter
sys.path.insert(0, ".")
from skips import norm, NUM
for m in sys.argv[1:]:
    r = json.load(open(f"runs/context/{m}/lost.json"))
    seen = Counter(); shown = set()
    for p in r:
        a, b = norm(p["text"]).split(), norm(p["heard"]).split()
        for tag, i1, i2, j1, j2 in difflib.SequenceMatcher(None, a, b, autojunk=False).get_opcodes():
            if tag in ("delete", "replace"):
                gone = [w for w in a[i1:i2] if w not in NUM and not re.match(r"^\d", w)]
                if 0 < len(gone) < 3:
                    key = " ".join(a[i1:i2]) + " -> " + " ".join(b[j1:j2])
                    seen[key] += 1
                    if key not in shown and len(shown) < 14:
                        shown.add(key); print("   ", m, p["name"], " ".join(a[max(0, i1 - 3):i1]), "[", key, "]", " ".join(a[i2:i2 + 2]))
    print(m, "single-word differences, not numbers:", sum(seen.values()), "in", len(r), "takes;", seen.most_common(8))
