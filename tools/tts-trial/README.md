# Text-to-speech trial

Which voice should a call use? Kokoro v1.0 `af_heart` was only ever compared with Pocket TTS and
Apple's standard voices. This measures candidates on the speech node, the same way for every model
and with Kokoro beside them, and makes a blind listening set that has been through what a call does
to audio. It can say what's fast enough, what fits and what stays steady; only the listening test
can say what sounds best.

Results are at the end. Audio stays out of the repo.

## What's measured

For every model, Kokoro included (`trial.py`, run through `gate.sh`):

- **Speed**, as a multiple of real time, and **first audio**, median and worst, warm, each sentence
  made on its own as on a call. For a model that streams, that's the time to its first chunk; for
  one that doesn't, the whole sentence, which is what a caller waits for.
- **Gap-free start**: how long playback must wait after the request never to run dry, worked out
  from when each chunk came. For a stream that keeps up, it's the same as first audio.
- **Memory**: peak GPU memory (the whole card's, less what it used before, so Vulkan counts too),
  peak host memory (the process's own `VmHWM`), the time to be ready, and whether it runs with 3 GB
  of the GPU already held (`ballast.py`, a stand-in for the speech-to-text model).
- **Mistakes** (`score.py`): each clip goes through Parakeet on the CPU, and the transcript is
  searched for the sentence's key items. This is a rough check. A miss may be Parakeet's own, and it
  can't hear a wrong stress or a wrong sound inside a word that still transcribes.
- **Voice stability** (`score.py`): over 24 sentences made separately, how alike they sound to a
  speaker-recognition model (CAM++, cosine similarity), and how far median pitch, loudness and pace
  move. Other Kokoro voices are the control: they show what a different speaker scores here, because
  the number means nothing without that.

The test text (`sentences.json`, from `make_sentences.py`) is written for this trial, as a Claude
Code session might report on a call: very short replies, yes/no and wh- questions, numbers, money,
percentages and versions, file names, paths, identifiers and acronyms, and a run of 24 ordinary
sentences. Nothing comes from anyone's calls.

Kokoro gets its phonemes from ringback's own front end, through the node's own Go code
(`kokoro_trial.go.txt`). The others read text, so each number and path sentence is made twice: as a
session writes it, and in a `spoken` form as the front end would read it ("voice server dot pie").
That shows what a model does alone, and what a rewriting stage in front of it would save.

## Running it

On the speech node, which needs an NVIDIA GPU and nothing else installed. Everything goes under one
folder (`~/ringback-models-test/tts` unless `TTS_ROOT` says otherwise); nothing needs root, and the
live node isn't touched, though its Kokoro and Parakeet model folders are read.

`gate.sh` holds the shared GPU lock for one run at a time, waits until no call is on, and kills the
run if a call starts (exit 75, in about a second). **The GPU serves live calls, so anything that
uses it goes through `gate.sh`.**

    rsync -a tools/tts-trial/ NODE:ringback-models-test/tts/harness/
    rsync -a speech/ NODE:ringback-models-test/tts/harness/speech/
    ssh NODE
    cd ~/ringback-models-test/tts
    sh harness/setup.sh                                 # runtimes, models, environments
    sh harness/screen.sh ming lfm3 qwen3 ...            # twelve sentences each, to see what's fast enough

Models with no built-in voice need a reference, made by the model itself, so no real person's voice
is cloned. There's no ear in the choice: `refs.py` takes a clip Parakeet hears correctly, with the
pitch nearest `af_heart`'s. Run `setup.sh` again afterwards: it saves Breeze's voice.

    sh harness/gate.sh python3 harness/trial.py voxcpm2-design --refs 8
    sh harness/gate.sh python3 harness/trial.py breeze-audiocpp --refs 8
    env/bin/python -I harness/refs.py voxcpm2-design --name voxcpm2
    env/bin/python -I harness/refs.py breeze-audiocpp --name breeze

Then the whole trial, the controls, the scores and the blind set:

    sh harness/run_all.sh kokoro ming breeze voxcpm2 lfm3 phonon   # 70 clips, again, and beside 3 GB
    for c in bella sarah nicole alloy; do                          # stability's control
        sh harness/gate.sh python3 harness/trial.py kokoro-$c --sentences run --label all
    done
    env/bin/python -I harness/score.py kokoro ming breeze voxcpm2 lfm3 phonon
    env/bin/python -I harness/stability.py --models kokoro ming breeze voxcpm2 lfm3 phonon
    env/bin/python -I harness/blind.py blind kokoro=all ming=all breeze=all voxcpm2=all lfm3=all phonon=all
    env/bin/python -I harness/check_blind.py blind
    env/bin/python -I harness/summary.py blind/key.json kokoro ming breeze voxcpm2 lfm3 phonon

On the node, these take about half an hour once nothing else holds the lock, and the folder ends up
at about 30 GB, 27 GB of it models. `screen.sh` and `run_all.sh` let go of the lock between runs.

Every number that a listener could hear goes by letter, so results don't give the test away:
`summary.py` prints speed and memory by name, and mistakes and stability by letter, and only
`key.json` joins them. Speed and memory say nothing about how a voice sounds.

## The blind set

`blind.py` takes each clip through what a call does to audio, then to a file a phone can play:
24 kHz mono, as the node hands audio to the hub; Opus as `hub/opus.go` sets it up (24 kbps, 20 ms
frames, VoIP, FEC at 10% expected loss, constrained VBR and complexity 9, libopus's defaults), through
ffmpeg's libopus; decoded at 48 kHz; AAC at 128 kbps. Before and after Opus each clip is set to the
same loudness (-23 LUFS, because at -20 a short word's peak went over full scale).

Sentence clips are matched one by one. The run of 24 sentences is matched as a whole, with one gain,
so drift in loudness from sentence to sentence stays audible, and goes through Opus as one stream, as
on a call. Silence at the ends of each sentence is evened out, so a model's padding is neither a tell
nor a difference, and sentences in the run are 350 ms apart. That levels the pacing between sentences,
which on a call would be each model's own.

Voices get letters at random. `key.json` holds the letters and nothing else does: `blind.py` prints
none of it, and files carry no tags and all the same time. `clips.json` lists each clip's voice,
sentence and file. `variant` is `text` as a session writes it, or `spoken` for the front end's
reading, with `read_as` saying what the model was given.

`check_opus.py` encodes a WAV with libopus directly, making the same calls as `hub/opus.go`, and with
`blind.py`'s ffmpeg arguments, and compares the packets. `check_blind.py` decodes every finished file
to check its format, tags and loudness, and has Parakeet confirm it holds the sentence it's listed as.

## Results

On 2026-11-06, on the speech node (one NVIDIA GPU with 12 GB), with the speech-to-text trial sharing
the GPU lock. The full runs of seven models and their controls took 17 minutes of the node's time.
No call came during a run. Nothing has been heard yet: which voice is best is the listening test's
question, and none of this answers it.

**Every model tried cleared 1.5× on this GPU**, which published numbers didn't lead anyone to expect.
Quantised weights through audio.cpp, or Breeze's own port, are what make it so. Each ran 70 clips
twice (54 sentences, with 16 of them also in their spoken form), warm, with speed repeating within 1%.

| Model | Runtime and weights | Speed | Slowest sentence | First audio, median / worst | First speech | GPU peak | Host peak | Ready | With 3 GB held |
|---|---|---|---|---|---|---|---|---|---|
| Kokoro v1.0 `af_heart` | sherpa-onnx, ringback's front end | 39.8× | 19.5× | 103 / 180 ms | 416 ms | 2348 MiB (723 on short text) | 1623 MiB | 1.9 s | 36.8×, 3985 MiB in all |
| Phonon 31M | ptts 0.6.0, CPU, q8, 2 threads | 15.1× | 14.1× | 21 / 39 ms | 130 ms | none | 357 MiB | 0.6 s | 15.2×, untouched |
| LFM3-Audio 2B | audio.cpp, Q8 | 8.54× | 8.15× | 22 / 24 ms | 162 ms | 2112 MiB | 1008 MiB | 1.7 s | 8.47×, 5369 MiB |
| VoxCPM2 2B | audio.cpp, Q8, reference | 3.61× | 3.46× | 68 / 71 ms | 505 ms | 3684 MiB | 1017 MiB | 1.0 s | 3.59×, 6943 MiB |
| Qwen3-TTS 1.7B CustomVoice | audio.cpp, Q8 | 3.56× | 2.97× | 26 / 28 ms | 106 ms | 2578 MiB | 1636 MiB | 1.0 s | 3.52×, 5795 MiB |
| Ming-3.0-tts-0.5B | audio.cpp, Q8 | 3.14× | 2.96× | 106 / 116 ms | 252 ms | 1602 MiB | 1223 MiB | 1.5 s | 3.18×, 4825 MiB |
| Breeze TTS 2 | Breeze-TTS-2.cpp 0.1.6, Vulkan, Q8, saved voice | 2.04× | 1.52× | 106 / 150 ms | 111 ms | 4406 MiB | 658 MiB | 3.7 s | 1.98×, 7518 MiB |

First speech is first audio plus the silence a model starts with, median over the sentences. **Kokoro
starts with about 0.3 s of its own silence**, and the node doesn't trim it, so a caller waits about
0.4 s after first audio is out; VoxCPM2 with a reference starts with about 0.44 s. Neither has been
tried on the phone.

Screened on 12 sentences only:

| Model | Speed | First audio, median / worst | GPU peak | What stopped it |
|---|---|---|---|---|
| Audio8-TTS-Realistic 0.6B (reference, 24 sentences) | 3.67× | 1142 / 1819 ms | 2705 MiB | It doesn't stream in audio.cpp, so you wait for the whole sentence, past the 700 ms limit |
| KugelAudio-0-Open, Q4_K | 2.16× | 363 / 394 ms | 6053 MiB | It made up speech on short text: "Okay." came back as "Hugh adds Denclimus to dabble Hensingham the tension" |
| Breeze through audio.cpp, Q8 | 1.80× | 58 / 87 ms | 4583 MiB | Beaten by its own port, which is faster and holds a fifth of the host memory (2441 MiB) |
| Breeze port, Q4_K | 2.54× | 56 / 59 ms | 3248 MiB | Nothing: it's the option with more room, if Breeze wins and the margin matters |

**The blind set's Opus is the hub's.** All 1235 packets of a 25 s check were byte-identical to what the
hub's calls to libopus make.

**Reference voices work.** A voice with no reference wanders from sentence to sentence, and one clip
of the model's own holds it: on the two models measured both ways, stability rose by 0.23 and 0.08.
Plain speaker similarity couldn't have shown it, because it hears the words more
than the speaker; see `stability.py`. All six voices in the blind set scored 0.74 to 0.79, with
steady Kokoro voices at 0.77 to 0.90 and a voice changing at every sentence at 0.54 (95th percentile
0.58). None looks like a changing voice, and differences this small mean nothing. It only catches a
change of speaker; slower drift is for the ear.

**Mistakes are a rough check.** Over 46 key items as a session writes them, the six voices missed 1 to
17; given the front end's reading as text, 0 to 3. So some models can't be used without a text
rewriting stage, and with one, what's left is mostly Parakeet.

Not checked: how any of this sounds, a whole call on the phone, packet loss, any model while speech
recognition is working at the same time (the 3 GB held is idle memory), Vulkan and CUDA both busy on
one GPU, and load times from a cold disk (the files were in the page cache). References were picked
by rule and Phonon's voice by its description, so a better voice may be among the others. Weights are
quantised and most runtimes are ports, so a voice may sound different through its maker's own code.
