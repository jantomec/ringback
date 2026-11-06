#!/usr/bin/env python3
"""Everything measured, from the runs of run_model.py.

    python tally.py DATA_DIR RUNS_DIR OUT_DIR [--baseline parakeet-v2]

Writes OUT_DIR/results.json with every number, OUT_DIR/alignments/ with the word-by-word errors
of each model, set and recording, and OUT_DIR/errors.md with what each model got wrong. Those
two hold your words: keep OUT_DIR out of the repo. Scoring is tools/stt-eval's: each recording's
text is joined and compared with the whole reference, through Whisper's normalizer, which
deletes um, uh, hmm and mm. The second score here, "neutral", also deletes ah, er and eh, which
the first counts as wrong words, and treats spellings a reader wouldn't mistake as the same
(SAME).
"""

import argparse
import json
import math
import re
import statistics
import sys
from collections import Counter
from pathlib import Path

import jiwer
import numpy as np
from whisper_normalizer.english import EnglishTextNormalizer

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "stt-eval"))
from score import reference  # noqa: E402

SETS = ["clean", "opus", "call"]
FILLER = re.compile(r"(?<![\w'])(u+m+|u+h+m*|e+r+m+|e+r+|a+h+|e+h+|m{2,}|m+h+m+|h+m+)(?![\w'])", re.I)
MORE_FILLERS = re.compile(r"\b(u+m+|u+h+|ah+|er+|eh+|erm+|uhm+|mm+|hmm+)\b")

# The words of tools/stt-eval/sentences.txt a developer would call technical, normalized.
TECHNICAL = """database commit push branch test suite revert build configuration file documentation
latency milliseconds haiku model opus session webrtc connection relay notification function
parse config caller pull request merge ffmpeg homebrew json response id field switchboard
server retry logs error""".split()

# Spellings a reader wouldn't mistake, joined or split as the script has them. Strict scoring
# counts "web rtc" for "webrtc" as two errors.
SAME = [("web rtc", "webrtc"), ("parseconfig", "parse config"), ("home brew", "homebrew"), ("ff mpeg", "ffmpeg"),
        ("switch board", "switchboard"), ("sessionid", "session id"), ("always on", "alwayson"), ("alwayson", "always on")]  # fmt: skip

BOOTSTRAP = 10000
ANSWERS = {"yes": ["yes"], "no": ["no"], "yeah": ["yeah"], "nope": ["nope"], "okay": ["okay", "ok"],
           "yes go ahead": ["yes go ahead"], "its a yes": ["its a yes"]}  # fmt: skip

normalize = EnglishTextNormalizer()


def neutral(text):
    text = MORE_FILLERS.sub(" ", text)
    text = re.sub(r"\s+", " ", text)
    for a, b in SAME[:-1]:
        text = text.replace(a, b)
    return text.replace(*SAME[-1])


def plain(text):
    return re.sub(r"\s+", " ", re.sub(r"[^\w\s]", "", text.lower().replace("-", " "))).strip()


def load(runs, model, name):
    path = runs / model / f"{name}.json"
    return {r["id"]: r for r in json.loads(path.read_text())} if path.exists() else None


def said(row):
    return row["text"] or row.get("retry_text", "")


def recording_text(rows, clips, recording):
    parts = [(c["index"], said(rows[c["id"]])) for c in clips if c["recording"] == recording and c["id"] in rows]
    return " ".join(t for _, t in sorted(parts))


def align(ref, hyp):
    out = jiwer.process_words(ref, hyp)
    return out, out.substitutions + out.deletions + out.insertions, out.hits + out.substitutions + out.deletions


def word_errors(out):
    """Each error: (kind, reference words, what was written, index of the reference word)."""
    r, h = out.references[0], out.hypotheses[0]
    errors = []
    for c in out.alignments[0]:
        if c.type != "equal":
            errors.append((c.type, " ".join(r[c.ref_start_idx : c.ref_end_idx]), " ".join(h[c.hyp_start_idx : c.hyp_end_idx]), c.ref_start_idx))
    return errors


def sentence_spans(text):
    """The first word of each line of the reference, normalized on its own."""
    spans, at = [], 0
    for line in text.splitlines():
        if line.strip():
            spans.append(at)
            at += len(normalize(line).split())
    return spans, at


def per_sentence(errors, spans):
    counts = [0] * len(spans)
    for _, _, _, at in errors:
        i = max(sum(1 for s in spans if s <= at) - 1, 0)
        counts[i] += 1
    return counts


def bootstrap(a, b, words, rng):
    """95% range of (a's WER - b's) in points, resampling sentences of the two recordings."""
    a, b, words = np.array(a, float), np.array(b, float), np.array(words, float)
    picks = rng.integers(0, len(a), (BOOTSTRAP, len(a)))
    d = 100 * ((a - b)[picks].sum(1) / words[picks].sum(1))
    return [round(float(np.percentile(d, 2.5)), 2), round(float(np.percentile(d, 97.5)), 2)]


def fillers(rows):
    forms, ending, total, alone = Counter(), 0, 0, 0
    for r in rows:
        found = [m.group(1).lower() for m in FILLER.finditer(said(r))]
        forms.update(found)
        total += len(found)
        words = re.findall(r"[\w']+", said(r))
        ending += bool(words) and bool(FILLER.fullmatch(words[-1]))
        alone += len(found) if words and len(found) == len(words) else 0
    return {"count": total, "forms": dict(forms.most_common()), "utterances_ending_in_one": ending, "in_filler_only_stretches": alone}


def latency(rows):
    secs = sorted(r["secs"] for r in rows)
    return {"n": len(secs), "median_ms": round(1000 * statistics.median(secs)), "p90_ms": round(1000 * secs[int(len(secs) * 0.9)]),
            "worst_ms": round(1000 * secs[-1]), "total_audio_secs": round(sum(r["audio_secs"] for r in rows), 1)}  # fmt: skip


def short_answers(rows, clips, not_taken):
    by_clip = {}
    for c in clips:
        by_clip.setdefault(c["clip"], []).append(c)
    per_word, wrong = {}, Counter()
    first_empty = 0
    for parts in by_clip.values():
        c = parts[0]
        word = plain(c["word"])
        text = plain(" ".join(said(rows[p["id"]]) for p in parts if p["id"] in rows))
        first_empty += any(not rows[p["id"]]["text"] for p in parts if p["id"] in rows)
        k = per_word.setdefault(word, {"right": 0, "wrong": 0, "none": 0})
        if not text:
            k["none"] += 1
        elif text in ANSWERS[word]:
            k["right"] += 1
        else:
            k["wrong"] += 1
            wrong[f"{word} -> {text}"] += 1
    n = len(by_clip)
    right = sum(k["right"] for k in per_word.values())
    return {"clips": n, "not_taken_by_the_gate": len(not_taken), "right": right, "right_pct": round(100 * right / n, 1),
            "none": sum(k["none"] for k in per_word.values()), "wrong": sum(k["wrong"] for k in per_word.values()),
            "empty_at_first_try": first_empty, "per_word": per_word, "wrong_forms": dict(wrong.most_common())}  # fmt: skip


def own_short(data, runs, model, baseline):
    """The short things in your own recordings, each scored whole for every model:
    - the scripted sentences of up to 5 words, as stretches of a call (`call`). Aligning the
      baseline's text with the script says only which stretch holds which sentence, and those
      that share a stretch with something else are left out;
    - the free-speech lines of up to 3 words, as utterances through Opus (`opus`), and of them,
      the lines that are only a filler, which say whether a model writes it."""
    clips = json.loads((data / "clips" / "call.json").read_text())
    base, rows, through = load(runs, baseline, "call"), load(runs, model, "call"), load(runs, model, "opus")
    picked = []
    for rec in sorted({c["recording"] for c in clips if "free" not in c["recording"]}):
        ref_text = reference(data, rec)
        spans, total = sentence_spans(ref_text)
        ref_words = normalize(ref_text).split()
        words, owner = [], []
        for c in sorted((c for c in clips if c["recording"] == rec), key=lambda c: c["index"]):
            w = normalize(said(base[c["id"]])).split()
            words += w
            owner += [c["id"]] * len(w)
        covers = {}
        for ch in jiwer.process_words(" ".join(ref_words), " ".join(words)).alignments[0]:
            if ch.type in ("equal", "substitute"):
                for i in range(ch.ref_end_idx - ch.ref_start_idx):
                    covers.setdefault(owner[ch.hyp_start_idx + i], []).append(ch.ref_start_idx + i)
        edges = spans + [total]
        for x, y in zip(edges, edges[1:]):
            ids = [i for i, c in covers.items() if any(x <= n < y for n in c)]
            if y - x <= 5 and len(ids) == 1 and all(x <= n < y for n in covers[ids[0]]):
                picked.append({"id": ids[0], "ref": " ".join(ref_words[x:y]), "text": said(rows[ids[0]]), "kind": "scripted"})
    only_filler = {"lines": 0, "wrote_a_filler": 0, "wrote_nothing": 0, "wrote_other": 0}
    for rec in sorted({c["recording"] for c in clips if "free" in c["recording"]}):
        for i, line in enumerate(reference(data, rec).splitlines()):
            row = through[f"{rec}-{i:03}"]
            words = plain(MORE_FILLERS.sub(" ", normalize(line))).split()
            if not words:
                only_filler["lines"] += 1
                wrote = said(row)
                key = "wrote_a_filler" if FILLER.search(wrote) else "wrote_other" if plain(wrote) else "wrote_nothing"
                only_filler[key] += 1
            elif len(words) <= 3:
                picked.append({"id": row["id"], "ref": " ".join(words), "text": said(row), "kind": "free"})
    right, wrong, none = 0, [], 0
    for p in picked:
        text = plain(MORE_FILLERS.sub(" ", normalize(p["text"])))
        if not text:
            none += 1
        elif text == plain(p["ref"]):
            right += 1
        else:
            wrong.append([p["ref"], p["text"]])
    return {"stretches": len(picked), "right": right, "none": none, "wrong": len(wrong), "wrong_list": wrong, "picked": picked, "only_filler": only_filler}


def noise(rows, clips):
    kinds = {}
    invented = []
    for c in clips:
        k = kinds.setdefault(c["kind"], {"clips": 0, "with_words": 0, "words": 0})
        k["clips"] += 1
        words = re.findall(r"[\w']+", rows[c["id"]]["text"])
        if words:
            k["with_words"] += 1
            k["words"] += len(words)
            invented.append([c["id"], rows[c["id"]]["text"][:160]])
    return {"clips": sum(k["clips"] for k in kinds.values()), "with_words": sum(k["with_words"] for k in kinds.values()),
            "words": sum(k["words"] for k in kinds.values()), "kinds": kinds, "invented": invented}  # fmt: skip


def technical(all_errors, scripted):
    """How many times each model got each technical word wrong, of the times it was read."""
    missed = Counter()
    wrote = {}
    for _, errors in all_errors:
        for kind, ref, hyp, _ in errors:
            if kind == "insert":
                continue
            for w in ref.split():
                if w in TECHNICAL:
                    missed[w] += 1
                    wrote.setdefault(w, Counter())[hyp or "(nothing)"] += 1
    ref_counts = Counter(w for r in scripted for w in normalize(r).split() if w in TECHNICAL)
    return {"read": sum(ref_counts.values()), "missed": sum(missed.values()),
            "words": {w: {"missed": n, "of": ref_counts[w], "written": dict(wrote[w].most_common())} for w, n in missed.most_common()}}  # fmt: skip


def main():
    a = argparse.ArgumentParser()
    a.add_argument("data", type=Path)
    a.add_argument("runs", type=Path)
    a.add_argument("out", type=Path)
    a.add_argument("--baseline", default="parakeet-v2")
    args = a.parse_args()
    data, runs, out = args.data.expanduser(), args.runs.expanduser(), args.out.expanduser()
    (out / "alignments").mkdir(parents=True, exist_ok=True)

    clips = {s: json.loads((data / "clips" / f"{s}.json").read_text()) for s in SETS + ["short", "noise"]}
    not_taken = json.loads((data / "clips" / "short-not-taken.json").read_text())
    recordings = sorted({c["recording"] for c in clips["clean"]})
    models = sorted(p.name for p in runs.iterdir() if (p / "meta.json").exists() and not p.name.endswith("-ballast"))
    rng = np.random.default_rng(11)
    refs = {r: reference(data, r) for r in recordings}
    results, sentence_errors, errors_md = {}, {}, []

    for m in models:
        res = {"meta": json.loads((runs / m / "meta.json").read_text()), "wer": {}, "fillers": {}, "latency": {}, "technical": {}}
        ballast = runs / f"{m}-ballast" / "meta.json"
        if ballast.exists():
            res["ballast"] = json.loads(ballast.read_text())
            rows = load(runs, f"{m}-ballast", "opus")
            res["ballast"]["latency"] = latency(list(rows.values()))
            alone = load(runs, m, "opus")
            res["ballast"]["same_text_as_alone"] = sum(rows[k]["text"] == alone[k]["text"] for k in rows)
            res["ballast"]["clips"] = len(rows)
        errors_md.append(f"\n# {m}\n")
        for s in SETS:
            rows = load(runs, m, s)
            if rows is None:
                continue
            per = {}
            res["wer"][s] = per
            for kind, recs in (("scripted", [r for r in recordings if "free" not in r]), ("free", [r for r in recordings if "free" in r])):
                totals = Counter()
                all_err, neutral_err, sentence_counts, sentence_words = [], [], [], []
                for r in recs:
                    hyp_raw = recording_text(rows, clips[s], r)
                    ref_n, hyp_n = normalize(refs[r]), normalize(hyp_raw)
                    o, errs, words = align(ref_n, hyp_n)
                    o2, errs2, words2 = align(neutral(ref_n), neutral(hyp_n))
                    totals.update(errors=errs, words=words, sub=o.substitutions, dele=o.deletions, ins=o.insertions, errors2=errs2, words2=words2)
                    res["wer"].setdefault("by_recording", {}).setdefault(s, {})[r] = {"errors": errs, "words": words}
                    (out / "alignments" / f"{m}--{s}--{r}.txt").write_text(jiwer.visualize_alignment(o, show_measures=True))
                    we = word_errors(o)
                    all_err.append((r, we))
                    neutral_err.append((r, word_errors(o2)))
                    spans, total = sentence_spans(refs[r])
                    whole = len(ref_n.split())
                    if kind == "scripted":
                        assert total == whole, f"{r}: {total} words line by line, {whole} together"
                    else:  # the normalizer counts free speech differently line by line: near enough to resample lines
                        spans, total = [round(x * whole / total) for x in spans], whole
                    edges = spans + [total]
                    sentence_counts += per_sentence(we, spans)
                    sentence_words += [b - a for a, b in zip(edges, edges[1:])]
                p = totals["errors"] / totals["words"]
                entry = {"errors": totals["errors"], "words": totals["words"], "wer": round(100 * p, 2),
                         "range95": round(196 * math.sqrt(p * (1 - p) / totals["words"]), 2),
                         "sub": totals["sub"], "del": totals["dele"], "ins": totals["ins"],
                         "neutral_errors": totals["errors2"], "wer_neutral": round(100 * totals["errors2"] / totals["words2"], 2)}  # fmt: skip
                per[kind] = entry
                sentence_errors[(m, s, kind)] = (sentence_counts, sentence_words)
                if kind == "scripted":
                    res["technical"][s] = technical(neutral_err, [refs[r] for r in recs])
                if s == "opus":
                    errors_md.append(f"\n## {kind}, through Opus\n")
                    for r, we in all_err:
                        errors_md.append(f"\n{r}:\n")
                        errors_md += [f"- {k}: `{ref}` -> `{hyp}`\n" for k, ref, hyp, _ in we]
            res["fillers"][s] = {
                "free": fillers([rows[c["id"]] for c in clips[s] if "free" in c["recording"] and c["id"] in rows]),
                "scripted": fillers([rows[c["id"]] for c in clips[s] if "free" not in c["recording"] and c["id"] in rows]),
            }
            res["latency"][s] = latency(list(rows.values()))
        rows = load(runs, m, "short")
        if rows:
            res["short"] = short_answers(rows, clips["short"], not_taken)
            res["latency"]["short"] = latency(list(rows.values()))
            res["own_short"] = own_short(data, runs, m, args.baseline)
        rows = load(runs, m, "noise")
        if rows:
            res["noise"] = noise(rows, clips["noise"])
        speech = [r for s in ("opus", "call") for r in (load(runs, m, s) or {}).values()]
        if speech:
            res["latency"]["all_speech"] = latency(speech)
        results[m] = res

    for m in models:
        for s in SETS:
            for kind in ("scripted", "free"):
                if (m, s, kind) in sentence_errors and (args.baseline, s, kind) in sentence_errors and m != args.baseline:
                    mine, words = sentence_errors[(m, s, kind)]
                    base, _ = sentence_errors[(args.baseline, s, kind)]
                    results[m]["wer"][s][kind]["against_baseline_95"] = bootstrap(mine, base, words, rng)

    (out / "results.json").write_text(json.dumps(results, indent=1))
    (out / "errors.md").write_text("".join(errors_md))

    print(f"{'model':22}{'scripted clean':>18}{'scripted opus':>18}{'opus vs base':>17}{'call':>18}{'free opus':>11}{'fill':>6}{'short':>8}{'own':>7}{'noise':>7}{'med ms':>8}{'worst':>7}{'GPU MiB':>9}{'load s':>8}")
    for m in models:
        r = results[m]
        w = r["wer"]
        cell = lambda s: f"{w[s]['scripted']['wer']:.1f}±{w[s]['scripted']['range95']:.1f} ({w[s]['scripted']['errors']})" if s in w else "-"  # noqa: E731
        d = w.get("opus", {}).get("scripted", {}).get("against_baseline_95", "")
        lat = r["latency"].get("all_speech", {})
        print(f"{m:22}{cell('clean'):>18}{cell('opus'):>18}{str(d):>17}{cell('call'):>18}"
              f"{w.get('opus', {}).get('free', {}).get('wer', 0):>11.1f}{r['fillers'].get('opus', {}).get('free', {}).get('count', 0):>6}"
              f"{r.get('short', {}).get('right', 0):>5}/{r.get('short', {}).get('clips', 0):<3}"
              f"{r.get('own_short', {}).get('right', 0):>3}/{r.get('own_short', {}).get('stretches', 0):<3}"
              f"{r.get('noise', {}).get('with_words', 0):>4}/{r.get('noise', {}).get('clips', 0):<3}"
              f"{lat.get('median_ms', 0):>7}{lat.get('worst_ms', 0):>7}{r['meta']['peak_gpu_mib']:>9}{r['meta']['ready_secs']:>8.1f}")  # fmt: skip


if __name__ == "__main__":
    main()
