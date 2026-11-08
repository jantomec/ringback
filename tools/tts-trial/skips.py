import json, re, difflib, sys
def norm(s):
    s = s.lower().replace("’", "'")
    s = re.sub(r"(?<=\d),(?=\d{3}\b)", "", s)
    s = re.sub(r"[^a-z0-9.:' ]+", " ", s)
    s = re.sub(r"(?<!\d)[.:]|[.:](?!\d)", " ", s)
    return " ".join(s.split())
NUM = set("zero one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty thirty forty fifty sixty seventy eighty ninety hundred thousand million quarter".split())
for m in sys.argv[1:]:
    r = json.load(open(f"runs/context/{m}/lost.json"))
    print(m)
    bylen = {}
    for p in r:
        a, b = norm(p["text"]).split(), norm(p["heard"]).split()
        runs = []
        for tag, i1, i2, j1, j2 in difflib.SequenceMatcher(None, a, b, autojunk=False).get_opcodes():
            if tag in ("delete", "replace"):
                gone = [w for w in a[i1:i2] if w not in NUM and not re.match(r"^\d", w)]
                if len(gone) >= 3 and (tag == "delete" or (j2 - j1) < (i2 - i1) / 2):
                    runs.append((" ".join(a[max(0, i1 - 3):i1]), "[" + " ".join(a[i1:i2]) + "]", " ".join(b[j1:j2])))
        k = bylen.setdefault(p["size"], [0, 0, 0])
        k[1] += 1
        if runs:
            k[0] += 1
            k[2] += sum(len(x[1].split()) for x in runs)
            for x in runs:
                print("   ", p["name"], x[0], "|", x[1][:110], "| heard:", x[2][:40])
    print("  takes with a skipped run of 3+ words, of takes, words in them:", {k: v for k, v in sorted(bylen.items())})
