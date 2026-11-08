#!/usr/bin/env python3
"""Finds where a sentence ends in overlapping audio, splices the pieces and scores what came out.

    env/bin/python -I harness/context_join.py MODEL...

Two ways to find the end of a sentence in a pass that's said more than is wanted:
  pause   the pause nearest where the text says it should be: the share of the characters so far of
          the speech's length. It costs nothing, and needs no second model
  words   Parakeet's word times: the end of the sentence's last word, then the pause there
Each is judged two ways: against where Parakeet heard the gap, and by effect: cut there, transcribe
both sides, and see whether the words either side of the cut are the right ones.

Pieces are spliced in the middle of the pause, with a 10 ms crossfade, keeping the pause the model
made. Every join is then measured: level and any click at the join, and the steps in loudness and
pitch across it, beside the same steps at sentence ends inside one pass, which are the natural
ones. Sentence starts are scored on all methods (see `starts`), and against the same sentence made
alone, as the nearest numbers to "weird intonation at the beginnings". They're only a stand-in for
the ear: every model's sentences begin above their median pitch, as English does.

Writes ROOT/runs/context/MODEL/results.json, and METHOD.wav and METHOD-8.wav, the whole 24 and the
first 8 sentences, for the clips.
"""
import difflib
import json
import pathlib
import statistics
import sys
import time

import numpy as np
import pyloudnorm
import soundfile
import soxr

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import score  # noqa: E402
import stability  # noqa: E402

ROOT = score.ROOT
RUN = [s["text"] for s in json.loads((score.HERE / "sentences.json").read_text()) if s["kind"] == "run"]
RATE = 16000
MIN_PAUSE = 0.08
FADE = 0.01


def load(path):
    x, rate = soundfile.read(path, dtype="float64")
    return x, rate


def to16(x, rate):
    return (soxr.resample(x, rate, RATE) if rate != RATE else x).astype("float32")


def recognise(rec, y):
    """Words with their times in y: (normalised word, start, end). The padding comes back out."""
    lead = 0.25
    s = rec.create_stream()
    s.accept_waveform(RATE, np.concatenate([np.zeros(int(RATE * lead), "float32"), y, np.zeros(RATE, "float32")]))
    rec.decode_stream(s)
    r = s.result
    words = []
    for token, start, length in zip(r.tokens, r.timestamps, r.durations):
        # A token's length runs up to the next token, pause or not, so a word ends one frame into
        # its last letters. Punctuation has its own token, and says nothing about the word.
        start, end = max(start - lead, 0.0), max(start - lead + min(length, 0.08), 0.0)
        if not score.norm(token):
            continue
        if token.startswith(" ") or not words:
            words.append([token.strip(), start, end])
        else:
            words[-1][0] += token
            words[-1][2] = max(words[-1][2], end)
    out = [(score.norm(w), a, b) for w, a, b in words]
    return [w for w in out if w[0]], r.text


def expected_words(sentences):
    return [w for s in sentences for w in score.norm(s).split()]


def word_map(sentences, words):
    """Which heard word each expected word is, or None."""
    want = expected_words(sentences)
    match = difflib.SequenceMatcher(a=want, b=[w[0] for w in words], autojunk=False)
    index = [None] * len(want)
    for a, b, n in match.get_matching_blocks():
        for i in range(n):
            index[a + i] = b + i
    return index


def sentence_ends(sentences):
    """The index in expected_words of each sentence's last word."""
    ends, n = [], 0
    for s in sentences:
        n += len(score.norm(s).split())
        ends.append(n - 1)
    return ends


def gap_truth(sentences, k, words):
    """(end of sentence k's last word, start of the next's first word), as Parakeet heard them."""
    index = word_map(sentences, words)
    last = sentence_ends(sentences)[k]
    a = index[last]
    b = index[last + 1] if last + 1 < len(index) else None
    if a is None or b is None:
        return None
    return words[a][2], words[b][1]


def envelope(x, rate):
    n = rate // 100
    frames = x[: len(x) // n * n].reshape(-1, n)
    return np.sqrt((frames ** 2).mean(axis=1))


def pauses(x, rate):
    """Quiet stretches: (start, end) in seconds, quiet being 30 dB under the loud frames."""
    rms = envelope(x, rate)
    if not len(rms) or rms.max() == 0:
        return []
    quiet = rms < np.percentile(rms[rms > 0], 95) * 10 ** (-30 / 20)
    found, start = [], None
    for i, q in enumerate(list(quiet) + [False]):
        if q and start is None:
            start = i
        elif not q and start is not None:
            if (i - start) / 100 >= MIN_PAUSE:
                found.append((start / 100, i / 100))
            start = None
    return found


def speech_span(x, rate):
    a, b = score.active(x, rate)
    return a / rate, b / rate


def find_by_pause(x, rate, sentences, k):
    """The inner pause nearest where the text says sentence k ends, longer ones winning a tie."""
    t0, t1 = speech_span(x, rate)
    chars = np.cumsum([len(s) for s in sentences])
    want = t0 + (t1 - t0) * chars[k] / chars[-1]
    inner = [p for p in pauses(x, rate) if p[0] > t0 and p[1] < t1]
    if not inner:
        return None, want
    best = min(inner, key=lambda p: abs((p[0] + p[1]) / 2 - want) - 0.5 * (p[1] - p[0]))
    return best, want


def find_by_words(x, rate, sentences, k, words):
    truth = gap_truth(sentences, k, words)
    if truth is None:
        return None
    a, b = truth
    near = [p for p in pauses(x, rate) if p[1] >= a - 0.1 and p[0] <= b + 0.1]
    if near:
        return max(near, key=lambda p: min(p[1], b) - max(p[0], a))
    return (a, max(a, b))


def right_cut(rec, x, rate, sentences, k, cut):
    """Does the cut leave the right words each side? Judges the last word before it and first after."""
    i = int(cut * rate)
    head, _ = recognise(rec, to16(x[:i], rate))
    tail, _ = recognise(rec, to16(x[i:], rate))
    if not head or not tail:
        return False
    return (head[-1][0] == expected_words(sentences[: k + 1])[-1]
            and tail[0][0] == expected_words(sentences[k + 1:])[0])


def locate_all(rec, model):
    """Every cut the overlapping methods need, by both ways, and against each other."""
    rows = []
    for method in ("ctx1", "ctx2", "window", "overlap"):
        d = ROOT / "runs/context" / model / method
        passes = json.loads((d / "passes.json").read_text())
        for p in passes:
            if not p["context"]:
                continue
            x, rate = load(d / f"{p['name']}.wav")
            sentences = [RUN[i] for i in p["said"]]
            k = p["context"] - 1
            t = time.time()
            words, _ = recognise(rec, to16(x, rate))
            cost = time.time() - t
            truth = gap_truth(sentences, k, words)
            by_pause, want = find_by_pause(x, rate, sentences, k)
            by_words = find_by_words(x, rate, sentences, k, words)
            row = {"method": method, "pass": p["name"], "said": p["said"], "k": k, "seconds": len(x) / rate,
                   "chars_before": sum(len(s) for s in sentences[: k + 1]), "truth": truth, "want": want,
                   "pause": by_pause, "words": by_words, "asr_s": cost}
            for name, found in (("pause", by_pause), ("words", by_words)):
                if found is None:
                    row[name + "_ok"] = False
                    row[name + "_in_gap"] = False
                    continue
                cut = (found[0] + found[1]) / 2
                row[name + "_in_gap"] = bool(truth and truth[0] - 0.05 <= cut <= truth[1] + 0.05)
                row[name + "_ok"] = right_cut(rec, x, rate, sentences, k, cut)
            rows.append(row)
    return rows


def locate_short(rec, model):
    """A very short sentence first: is there a pause to cut at, and does the cut work?"""
    d = ROOT / "runs/context" / model / "short"
    rows = []
    for p in json.loads((d / "passes.json").read_text()):
        x, rate = load(d / f"{p['name']}.wav")
        sentences = [p["first"], p["rest"]]
        words, text = recognise(rec, to16(x, rate))
        truth = gap_truth(sentences, 0, words)
        by_pause, want = find_by_pause(x, rate, sentences, 0)
        by_words = find_by_words(x, rate, sentences, 0, words)
        row = {"first": p["first"], "heard": text, "seconds": len(x) / rate, "truth": truth, "want": want,
               "pause": by_pause, "words": by_words,
               "gap_s": (truth[1] - truth[0]) if truth else None}
        for name, found in (("pause", by_pause), ("words", by_words)):
            row[name + "_ok"] = bool(found) and right_cut(rec, x, rate, sentences, 0, (found[0] + found[1]) / 2)
        rows.append(row)
    return rows


def splice(a, b, rate):
    """Joins b onto a with a short crossfade. Both are cut at silence, so it should never show."""
    n = int(rate * FADE)
    if len(a) < n or len(b) < n:
        return np.concatenate([a, b]), len(a)
    t = np.linspace(0, np.pi / 2, n)
    mid = a[-n:] * np.cos(t) + b[:n] * np.sin(t)
    return np.concatenate([a[:-n], mid, b[n:]]), len(a) - n // 2


def pause_at(found, a, b):
    """The length of the longest pause between a word's end and the next word's start."""
    near = [p for p in found if p[1] >= a - 0.1 and p[0] <= b + 0.1]
    return max((p[1] - p[0] for p in near), default=0.0)


def assemble(rec, model, method):
    """The sentences as they'd play: each pass after its context, the join in the middle of the pause."""
    d = ROOT / "runs/context" / model / method
    passes = json.loads((d / "passes.json").read_text())
    audio, joins, rate = None, [], None
    for p in passes:
        x, rate = load(d / f"{p['name']}.wav")
        if audio is None:
            audio = x
            continue
        if not p["context"]:
            joins.append(len(audio))
            audio = np.concatenate([audio, x])
            continue
        sentences = [RUN[i] for i in p["said"]]
        k = p["context"] - 1
        words, _ = recognise(rec, to16(x, rate))
        found = find_by_words(x, rate, sentences, k, words) or find_by_pause(x, rate, sentences, k)[0]
        if found is None:
            continue
        # The pause the model made here, half from each side.
        half = (found[1] - found[0]) / 2
        cut = (found[0] + found[1]) / 2
        _, end = speech_span(audio, rate)
        kept = audio[: int((end + half) * rate)]
        audio, at = splice(kept, x[int(cut * rate):], rate)
        joins.append(at)
    return audio, rate, joins


def assemble_separate(model):
    audio, joins, rate = [], [], None
    total = 0
    for i in range(len(RUN)):
        x, rate = load(ROOT / "runs" / model / "all" / f"run-{i + 1:02d}.wav")
        audio.append(x)
        total += len(x)
        joins.append(total)
    return np.concatenate(audio), rate, joins[:-1]


def sentence_times(rec, x, rate):
    """Start and end of each of the 24 sentences in x, as Parakeet heard them."""
    y = to16(x, rate)
    words, text = recognise(rec, y)
    index = word_map(RUN, words)
    want = expected_words(RUN)
    ends = [-1] + sentence_ends(RUN)
    spans = []
    for s in range(len(RUN)):
        inside = [index[i] for i in range(ends[s] + 1, ends[s + 1] + 1) if index[i] is not None]
        spans.append((words[inside[0]][1], words[inside[-1]][2]) if inside else None)
    errors, n = score.wer(" ".join(RUN), text)
    return spans, y, {"wrong_words": errors, "words": n, "missing_sentences": sum(s is None for s in spans),
                      "expected": len(want)}


def starts(y, spans):
    """How each sentence begins. offset: its first 0.3 s, in semitones from its own median pitch.
    rise: from its first 0.2 s to the next 0.3 s. attack: its first 0.15 s against its loud frames, in
    dB. All are voiced frames only, and None where there's too little to go on."""
    out = []
    for span in spans:
        row = {"offset": None, "rise": None, "attack": None}
        out.append(row)
        if span is None:
            continue
        a, b = int(span[0] * RATE), int(span[1] * RATE)
        part = y[a:b]
        n = int(RATE * 0.15)
        if len(part) > 2 * n:
            frames = np.sqrt(np.mean(part[: len(part) // 160 * 160].reshape(-1, 160) ** 2, axis=1))
            row["attack"] = float(20 * np.log10(np.sqrt((part[:n] ** 2).mean()) / (np.percentile(frames, 95) + 1e-9) + 1e-9))
        track = score.pitch_track(part)
        if len(track) < 10:
            continue
        f = np.array([v for _, v in track])
        t = np.array([u for u, _ in track]) - track[0][0]
        row["offset"] = float(12 * np.log2(np.median(f[t <= 0.3]) / np.median(f)))
        early, later = f[t <= 0.2], f[(t > 0.2) & (t <= 0.5)]
        if len(early) and len(later):
            row["rise"] = float(12 * np.log2(np.median(later) / np.median(early)))
    return out


def spread(begin, key):
    v = [b[key] for b in begin if b[key] is not None]
    return {"median": float(np.median(v)), "sd": float(np.std(v))}


def against(begin, base, key):
    """How far each sentence's start moved from the same sentence made alone, median, in semitones or dB."""
    v = [abs(a[key] - b[key]) for a, b in zip(begin, base) if a[key] is not None and b[key] is not None]
    return float(np.median(v)) if v else None


def loud(x, rate):
    if len(x) < rate * 0.45:
        return None
    return pyloudnorm.Meter(rate).integrated_loudness(x)


def step(x, rate, a_end, b_start):
    """Loudness and pitch of the 1.5 s of speech before a point, against the 1.5 s after."""
    before, after = x[max(int((a_end - 1.5) * rate), 0): int(a_end * rate)], x[int(b_start * rate): int((b_start + 1.5) * rate)]
    la, lb = loud(before, rate), loud(after, rate)
    pa, pb = score.pitch(to16(before, rate)), score.pitch(to16(after, rate))
    return (lb - la if la is not None and lb is not None else None,
            float(12 * np.log2(pb / pa)) if pa > 0 and pb > 0 else None)


def join_marks(x, rate, joins):
    """Level at each join, and whether the waveform jumps there, against the speech around it."""
    d = np.abs(np.diff(x))
    typical = np.percentile(d[d > 0], 99.9)
    speech = np.percentile(envelope(x, rate), 95)
    out = []
    for j in joins:
        r = int(rate * 0.01)
        near = x[max(j - r, 0): j + r]
        jump = d[max(j - rate // 500, 0): j + rate // 500].max()
        out.append({"level_db": float(20 * np.log10(np.sqrt((near ** 2).mean()) / speech + 1e-9)),
                    "click_db": float(20 * np.log10(jump / typical + 1e-9))})
    return out


def judge(rec, model, name, x, rate, joins, reference, base=None):
    soundfile.write(ROOT / "runs/context" / model / f"{name}.wav", x, rate, subtype="PCM_16")
    spans, y, words = sentence_times(rec, x, rate)
    begin = starts(y, spans)
    boundaries = [(spans[i][1], spans[i + 1][0]) for i in range(len(spans) - 1) if spans[i] and spans[i + 1]]
    quiet = pauses(x, rate)
    gaps = [pause_at(quiet, a, b) for a, b in boundaries]
    points = [(j / rate, b) for j in joins for b in boundaries if b[0] - 0.4 <= j / rate <= b[1] + 0.4]
    marked = {b for _, b in points}
    steps = {"join": [step(x, rate, *b) for b in marked], "natural": [step(x, rate, *b) for b in boundaries if b not in marked]}
    summary = {k: {"loudness_lu": median_abs(v, 0), "pitch_st": median_abs(v, 1), "n": len(v)} for k, v in steps.items()}
    # Boundary by boundary, for comparing a method with the same boundaries made separately.
    each = {}
    for i in range(len(spans) - 1):
        if spans[i] and spans[i + 1]:
            each[i] = step(x, rate, spans[i][1], spans[i + 1][0])
    marks = join_marks(x, rate, joins)
    n = len(x) / rate
    middle = [e for e in stability_one(y, spans, reference)]
    return {
        "seconds": n, **words, "joins": len(joins),
        "starts": begin, "offset": spread(begin, "offset"), "rise": spread(begin, "rise"), "attack": spread(begin, "attack"),
        "moved": {k: against(begin, base, k) for k in ("offset", "rise", "attack")} if base else None,
        "gap_s_median": float(np.median(gaps)), "gap_s_max": float(max(gaps)), "gap_s_p90": float(np.percentile(gaps, 90)),
        "steps": summary, "boundary_steps": each,
        "join_level_db_max": max((m["level_db"] for m in marks), default=None),
        "joins_in_speech": sum(m["level_db"] > -30 for m in marks),
        "click_db_max": max((m["click_db"] for m in marks), default=None),
        "steadiness": middle[0] if middle else None,
        "pitch_iqr_st": pitch_iqr(y, spans),
        "first8_s": spans[7][1] + 0.3 if spans[7] else None,
    }


def median_abs(values, i):
    v = [abs(t[i]) for t in values if t[i] is not None]
    return float(np.median(v)) if v else None


def pitch_iqr(y, spans):
    p = [score.pitch(y[int(s[0] * RATE): int(s[1] * RATE)]) for s in spans if s]
    st = 12 * np.log2(np.array(p) / np.nanmedian(p))
    return float(np.subtract(*np.nanpercentile(st, [75, 25])))


def stability_one(y, spans, reference):
    if reference is None or any(s is None for s in spans):
        return []
    extractor, ref = reference
    e = []
    for s in spans:
        stream = extractor.create_stream()
        # At least a second: a span Parakeet only partly heard can be too short to embed.
        stream.accept_waveform(RATE, y[int(s[0] * RATE): int(max(s[1], s[0] + 1) * RATE)])
        stream.input_finished()
        v = np.array(extractor.compute(stream))
        e.append(v / np.linalg.norm(v))
    return [stability.steadiness(np.array(e), ref)[0]]


def main():
    models = sys.argv[1:]
    rec = score.recogniser()
    import sherpa_onnx
    extractor = sherpa_onnx.SpeakerEmbeddingExtractor(sherpa_onnx.SpeakerEmbeddingExtractorConfig(
        model=str(ROOT / "models/speaker" / stability.EMBEDDINGS["titanet"]), num_threads=8, provider="cpu"))
    ids = [f"run-{i + 1:02d}" for i in range(len(RUN))]
    reference = (extractor, stability.embed(extractor, stability.REFERENCE, ids))
    for model in models:
        out = {}
        keep = ROOT / "runs/context" / model / "cuts.json"
        if model in ("ming", "phonon"):
            if keep.exists():
                out.update(json.loads(keep.read_text()))
            else:
                out["cuts"] = locate_all(rec, model)
                out["short"] = locate_short(rec, model)
                keep.write_text(json.dumps(out, indent=0))
        out["methods"] = {}
        x, rate, joins = assemble_separate(model)
        out["methods"]["separate"] = judge(rec, model, "separate", x, rate, joins, reference)
        base = out["methods"]["separate"]["starts"]
        names = ["whole", "call"]
        if model in ("ming", "phonon"):
            names += ["ctx1", "ctx2", "window"]
        if model == "ming":
            names += ["native1", "native2", "nativegrp", "nativespk", "anchor", "anchorgrp"]
        for name in names:
            x, rate, joins = assemble(rec, model, name)
            out["methods"][name] = judge(rec, model, name, x, rate, joins, reference, base)
            print(model, name, {k: (round(v, 2) if isinstance(v, float) else v) for k, v in out["methods"][name].items()
                                if k in ("seconds", "wrong_words", "gap_s_median", "click_db_max", "steadiness", "pitch_iqr_st", "offset", "rise", "attack", "moved")}, flush=True)
        (ROOT / "runs/context" / model / "results.json").write_text(json.dumps(out, indent=0, default=str))


if __name__ == "__main__":
    main()
