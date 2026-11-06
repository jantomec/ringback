# Speech-to-text trial

Would a larger speech-to-text model beat Parakeet TDT 0.6B v2 on a call? This runs each model on
the speech node's GPU, one stretch at a time, on your own voice as a call really hears it, and
measures what `tools/stt-eval` can't: the effect of Opus, of how a call cuts speech into
stretches, fillers kept, short answers, words invented on noise, speed, memory and load time.

Every model gets the same five sets of clips, made once by `prepare.py`:
- **clean:** the utterances of `tools/stt-eval/segment.py`, as they are.
- **opus:** the same, cut from the recording after it has been through the call's Opus: libopus
  in VoIP mode at 24 kbps, wideband, 20 ms frames, FEC on with 10% expected loss
  (`hub/opus.go`), decoded straight to 16 kHz. No packet is dropped. `clips/opus-stats.json` says
  what it really did (on these recordings: 23.4 kbps, FEC in 70 to 81% of packets).
- **call:** the recording after Opus, cut as the speech node cuts a call. `gate.py` is a port of
  `speech/internal/hearing`: Silero through the volume gate at the app's defaults, a stretch ending
  at a 0.2 s pause, the second of audio kept before it and 0.5 s of silence after it, and a stretch
  written down as nothing tried once more from just before it began. This is 189 stretches of up
  to 7.1 s, where `segment.py` makes 99 of up to 17.2 s. The port gives the same numbers as
  `tools/short-answers` (`prepare.py --check-gate`).
- **short:** the 336 clips of `tools/short-answers` after Opus, cut the same way. They're Kokoro's
  voices, not yours, and the 205 the gate takes for speech are scored.
- **noise:** 43 clips of silence, noise and the room tone between your utterances, after Opus. The
  gate lets none of them through, so this shows what a model does if something gets past it.

`tally.py` scores them as `tools/stt-eval/score.py` does, through Whisper's normalizer (which
deletes um and uh), with the 95% range of each share of errors. It adds a paired range against the
baseline, by resampling sentences, which is what can say whether two models differ. The "neutral"
score also ignores ah, er and eh and spellings a reader wouldn't mistake ("web RTC"). Fillers are
counted raw. Its output holds your words: keep it out of this repository.

The GPU serves live calls. `gpu.sh` waits until no call is on, takes the lock for one model run,
and stops the run at once if a call starts, then runs it again when the call has ended.

## Running it

`DATA_DIR` is the private folder of `tools/stt-eval`, `NODE` the speech node's ssh host, and
everything on the node goes in one folder, which changes nothing else there:

    uv run --project tools tools/stt-trial/short_clips.py CLIPS_DIR    # on the Mac, Kokoro's clips
    ssh NODE mkdir -p ringback-models-test/stt/harness
    rsync -a tools/stt-trial tools/stt-eval NODE:ringback-models-test/stt/harness/
    ssh NODE ringback-models-test/stt/harness/stt-trial/node_setup.sh  # about 29 GB, no GPU
    rsync -a DATA_DIR/wav DATA_DIR/segments DATA_DIR/ref NODE:ringback-models-test/stt/data/
    rsync -a CLIPS_DIR/ NODE:ringback-models-test/stt/data/short-answers/

Then on the node, from `harness/stt-trial`, after `. ../../env.sh`:

    $STT/envs/harness/bin/python prepare.py $STT/data $STT/models/silero/silero_vad.onnx
    ./gpu.sh $STT/envs/sherpa/bin/python run_model.py parakeet-v2 $STT/data $STT/runs/parakeet-v2
    ./gpu.sh $STT/envs/sherpa/bin/python run_model.py parakeet-v2 $STT/data $STT/runs/parakeet-v2-ballast \
        --sets opus --ballast 3500
    $STT/envs/harness/bin/python tally.py $STT/data $STT/runs $STT/results

`--ballast` holds that much GPU memory in another process for the whole run, to show that a model
fits beside a text-to-speech model. The run's folder must end in `-ballast`. Each model in
`MODELS` in `backends.py` runs in one environment:

| Model | Environment | What runs it |
|---|---|---|
| `parakeet-v2`, `parakeet-v2-int8`, `parakeet-ultra-int8`, `cohere-int8` | `sherpa` | `sherpa_run.c`: sherpa-onnx v2.0.1 with the node's settings |
| `parakeet-v2-nemo`, `canary-qwen`, `canary-qwen-float32` | `nemo` | NeMo 3.0.0 |
| `qwen3-1.7b` | `qwen3` | `qwen-asr` 0.0.6, bfloat16 |
| `granite-5.0-2b`, `granite-5.0-2b-unpinned` | `granite` | Transformers 5.22.1, bfloat16 |

## Results

On 2026-11-06, on the speech node (one NVIDIA GPU with 12 GB), with one non-native speaker's three
recordings: 464 scripted words, read twice with two microphones, and 519 of free speech. The
models are in the order I'd rank them for a call. Parakeet v2 is the baseline; Cohere and Parakeet
Ultra ran as int8, the only sherpa-onnx packages there are, which on the GPU makes Parakeet v2 about
three times slower than the fp16 it runs as on the node. Granite used the card's prompt that pins the
language. Sixteen runs took 21 minutes of GPU in all, 10 s to 3.5 min each, and none was stopped by
a call.

**Scripted words, the only neutral reference.** Errors of 464, with the range of the difference
from Parakeet v2 in points:

| Model | Clean | Opus | Cut as a call | Call against v2 |
|---|---|---|---|---|
| Parakeet v2 | 40 (8.6%) | 37 (8.0%) | 41 (8.8%) | |
| Qwen3-ASR 1.7B | 37 (8.0%) | 32 (6.9%) | 40 (8.6%) | -3.0 to +2.0 |
| Granite Speech 5.0 2B | 32 (6.9%) | 35 (7.5%) | 47 (10.1%) | -2.0 to +4.0 |
| Cohere Transcribe | 24 (5.2%) | 28 (6.0%) | 40 (8.6%) | -2.4 to +2.6 |
| Canary-Qwen 2.5B | 36 (7.8%) | 41 (8.8%) | 51 (11.0%) | -1.1 to +3.9 |
| Parakeet Ultra | 51 (11.0%) | 44 (9.5%) | 52 (11.2%) | -0.6 to +3.8 |

Each share is good to about ±2.5 points. Cut as a call, no model differs from Parakeet v2 by more
than these recordings can show. NVIDIA's own runtime for Parakeet v2 gives 38, 37 and 40, so the
node's sherpa-onnx is faithful to it; the 31 errors of the earlier Mac run (MLX) aren't what a call
gets.

**Free speech.** The reference was corrected from Whisper's text, which favours models that write
tidy text like Whisper's, so read this as a comparison among the rest, and with care. Errors of 519:

| Model | Opus | Cut as a call | Call against v2 |
|---|---|---|---|
| Parakeet v2 | 27 (5.2%) | 44 (8.5%) | |
| Qwen3-ASR 1.7B | 27 (5.2%) | 34 (6.5%) | -3.0 to +0.7 |
| Granite Speech 5.0 2B | 22 (4.2%) | 35 (6.7%) | -2.5 to +1.0 |
| Cohere Transcribe | 22 (4.2%) | 52 (10.0%) | -0.3 to +3.3 |
| Canary-Qwen 2.5B | 23 (4.4%) | 55 (10.6%) | -0.2 to +4.8 |
| Parakeet Ultra | 33 (6.4%) | 58 (11.2%) | +0.2 to +5.2 |

How a call cuts speech costs every model more than the choice between most of them. A stretch that
ends at a 0.2 s pause leaves a fragment with no context around it: the same audio, split at 0.6 s
pauses, gives Parakeet v2 27 errors, and Cohere and Canary-Qwen less than half their errors cut as a
call. The 0.6 s split isn't a whole turn, so what decoding whole turns would gain isn't measured.
Part of Cohere's and Ultra's fall may be int8 itself: Parakeet v2 in int8 makes 54 errors here
against its fp16's 44, so their places are the least settled.

**Fillers.** Raw count in the 51 utterances of free speech. Opus takes them from Qwen3: it writes 47
on clean audio and 10 after Opus, so its filler cue doesn't exist on a call. The reason wasn't
looked into. Cut as a call, most of what Qwen3, Granite and Cohere write are stretches that are
only a filler; "inside" is what's left, the fillers within speech.

| Model | Clean | Opus | Cut as a call | Of those, inside | Stretches ending in one |
|---|---|---|---|---|---|
| Parakeet v2 | 57 | 55 | 45 | 34 | 18 |
| Qwen3-ASR 1.7B | 47 | 10 | 19 | 7 | 13 |
| Granite Speech 5.0 2B | 6 | 6 | 13 | 1 | 11 |
| Cohere Transcribe | 8 | 9 | 15 | 4 | 12 |
| Canary-Qwen 2.5B | 35 | 23 | 39 | 27 | 18 |
| Parakeet Ultra | 59 | 58 | 55 | 43 | 22 |

Nobody has listened and counted the true number. All six write a filler on all six lines that are
only a filler.

**Short answers and noise.** "Own" is 15 short things in your recordings, the scripted sentences of
up to 5 words and the free-speech lines of up to 3, scored whole. The gate passes none of the noise
clips, so the last column is a model's habit, not something a call runs into:

| Model | Kokoro's right, of 205 | Own right, of 15 | Noise clips with words, of 43 | Words on them |
|---|---|---|---|---|
| Parakeet v2 | 182 | 10 | 23 | 25 |
| Qwen3-ASR 1.7B | 176 | 12 | 43 | 49 |
| Granite Speech 5.0 2B | 182 | 12 | 4 | 7 |
| Cohere Transcribe | 174 | 11 | 25 | 40 |
| Canary-Qwen 2.5B | 178 | 10 | 36 | 39 |
| Parakeet Ultra | 181 | 8 | 2 | 3 |

Parakeet v2 came back empty on 30 of the 205 at first and the second try rescued all 30; no other
model needed it. Every model loses "Nope." and "Okay." more than the rest. Without the language
pinned, Granite wrote "Ja" for "Yeah." on 31 of 32 and invented 241 words on noise, some of them
parliamentary Spanish and French. sherpa-onnx has no language setting for Parakeet Ultra, which
writes foreign words for short answers.

**Technical words.** Times wrong of the 88 technical words in the script (`TECHNICAL` in
`tally.py`), after Opus and cut as a call, not counting spellings a reader wouldn't mistake:

| Model | Opus | Cut as a call | Wrong most |
|---|---|---|---|
| Parakeet v2 | 9 | 8 | caller, revert, suite, haiku, ffmpeg |
| Qwen3-ASR 1.7B | 7 | 7 | revert, caller, commit, haiku, build |
| Granite Speech 5.0 2B | 5 | 10 | caller, logs, revert, suite |
| Cohere Transcribe | 5 | 8 | caller, revert, suite, commit |
| Canary-Qwen 2.5B | 8 | 8 | caller, revert, logs, pull, commit |
| Parakeet Ultra | 10 | 15 | caller, revert, ffmpeg, suite, json |

"caller" and "revert" defeat all of them. This isn't a test of technical vocabulary: the script has
few such words, read twice.

**Speed and memory.** Warm, one stretch at a time. "Ready" is from the process starting to the
first words, on the first run and a later one. "Beside 3.5 GB" is the whole GPU's peak, of 12,288
MiB, with that much held by another process; every model fitted, with the same text on all 99
clips and the median within about 6% of its own.

| Model | Call: median, worst ms | Worst on any speech, ms | GPU, MiB | Host, MiB | Ready, s | Beside 3.5 GB |
|---|---|---|---|---|---|---|
| Parakeet v2 | 67, 75 | 87 | 2,300 | 2,270 | 1.7, 2.5 | 6,340 |
| Qwen3-ASR 1.7B | 133, 330 | 623 | 4,668 | 5,255 | 14.7, 11.0 | 8,708 |
| Granite Speech 5.0 2B | 120, 294 | 587 | 4,682 | 5,378 | 5.5, 7.7 | 8,722 |
| Cohere Transcribe | 316, 624 | 1,043 | 2,714 | 3,333 | 5.9, 6.7 | 6,752 |
| Canary-Qwen 2.5B | 119, 301 | 658 | 5,176 | 15,957 | 26.9, 26.2 | 9,214 |
| Parakeet Ultra | 186, 387 | 871 | 1,204 | 2,035 | 2.5, 3.3 | 5,252 |

Canary-Qwen in float32 scores the same (41 and 52 errors) at 10,156 MiB, so its half precision
costs nothing. Parakeet v2 on NeMo takes 32 ms a stretch but 5,044 MiB.

**So:** nothing here earns a switch. Parakeet v2 ties the best on neutral speech cut as a call, is
the fastest, loads in about 2 s against 3 to 27, and keeps its fillers through Opus, which Qwen3,
Granite and Cohere don't. Qwen3-ASR 1.7B and Granite Speech 5.0 2B are the two to look at again if a better test
favours them.

Not checked: real call audio (these are Voice Memos through Opus, without Bluetooth's narrowband
path, packet loss, or the phone's own encoder settings), any speaker but one, a technical
vocabulary, keyword lists, a whole turn decoded at once, the torch models on a route the Go node
could use, or two models computing at once. Cohere and Parakeet Ultra weren't timed in fp16.
