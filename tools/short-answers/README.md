# Short answers

Which short answers the speech node takes for speech and writes down, as a call hears them:
"Yes.", "No.", "Yeah.", "Nope.", "Okay.", and two longer ones ("Yes, go ahead.", "It's a
yes."). Made for the answers to permissions, after a "yes" on a call went unheard twice: once
not taken for speech, and once taken but written down as nothing.

Each answer is spoken here by four Kokoro voices (not `af_heart`, the call's own voice),
trimmed to the word, and set to a loudness as the node measures it: its loudest 400 ms block
(ITU-R BS.1770), at -25, -30, -35, -40, -45 and -50 LUFS. Each goes after 1.5 s of room noise,
with 0.5 s after it, in a quiet room (noise at -75 LUFS, below BS.1770's -70, so the node's
volume is 0 there) and a noisy one (-55 LUFS). The speech node hears each file as a call of its
own (`ringback-speech listen`: Silero with the background filter, Smart Turn, Parakeet, in
20 ms frames), with the background filter at its default (0.5: speech must reach -40 LUFS)
and off (-50 LUFS). It prints, for each answer and voice, what became of it at each level: its
words, a turn with no words, a stretch not taken for speech, or nothing; then the totals, and
what Parakeet wrote.

Run from the repo root, with the node set up as in `speech/README.md` (it runs beside a node
that's running, with its own process and GPU memory for about a minute):

    uv run --project tools tools/short-answers/short_answers.py --node HOST \
        [--node-dir DIR] [--bin bin|bin-next] [--keep DIR]

- `--node` is the node's ssh host, `--node-dir` its install directory (`~/ringback-speech`
  by default), and `--bin` the folder in it with the binary to try (`bin` by default; a build
  in `bin-next` to compare).
- `--keep DIR` keeps the clips and the node's answers (`listen-0.5.jsonl`, `listen-0.0.jsonl`).

The clips are synthetic: the timing of the filter and of Silero is the same for any voice, but
Parakeet's words for your own voice may differ.

## Results

On 2026-11-03, on the speech node (one NVIDIA GPU), about 1.5 min a run.

Before (the node as it was: the filter's volume as Pipecat measures it), at -25 to -35 LUFS, 168
answers heard with each setting:
- **The filter at its default:** 134 heard with words, 21 not taken for speech, 13 turns with
  no words. In the quiet room, 13 of the 28 at -35 LUFS weren't taken, every one of them shorter
  than 0.7 s, and 5 of the 28 at -30. At -40 LUFS, the filter's own level, none of 56 were taken.
  The filter compared its gate with the loudness of the last 400 ms, silence before the word
  included, smoothed again from one 32 ms window to the next, so a short word's volume reached
  the gate only as the word ended: too late for the 0.2 s of speech the sensitivity needs.
- **The filter off:** 156 heard, 12 turns with no words.
- **Parakeet:** "Yes." in two of the voices came back as no words at most levels, with the filter
  off too: 31 of 64 "Yes." were written down right. Given the word alone, Parakeet wrote it
  every time (6 of 6); with the second of audio a call keeps before you start speaking in front
  of it, it wrote nothing in 7 of 9 (variants tried with `ringback-speech hear`).

After (`speech/internal/hearing`: the filter measures the loudness of the speech Silero hears,
over as much of it as has come, up to 400 ms; and a stretch that comes back with no words is
written down once more from 0.1 s before it began):
- **The filter at its default:** 167 heard, none not taken, 1 turn with no words. At -40 LUFS,
  45 of 56 were taken; at -45 and -50, none of 112: the filter still keeps out speech quieter
  than its level.
- **The filter off:** 165 heard, 3 turns with no words; at -50 LUFS, 41 of 56 were taken.
- **Parakeet:** 60 of 64 "Yes." written down right. What it still gets wrong: "No." as "Noble."
  (13 times in 336) or "No, but", "Okay." as "Locay.", "Locate." or "Local.", "Yeah." as "Yeah,
  and"; the voice has to read them in context.
