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

## Does context fix sentence starts?

The user picked Ming from the blind test, but heard "weird intonations at the beginnings of each
sentence", and suspected it came from making each sentence separately. Their idea: one worker says
sentence 1 alone for a fast start, while another says 1 to X, whose sentence 1 is cut away, so it
shapes what follows; a third says 1 to Y, and so on. This tries that and cheaper forms of it, for Ming
and Phonon with Kokoro as the reference. The same 24 sentences are made each way:

| Method | What it does |
|---|---|
| `separate` | each sentence alone |
| `whole` | one text, in the runtime's own chunks |
| `call` | as a call does: sentence 1 alone, then the rest as one piece |
| `overlap` | the user's idea, as far as a pass can hold |
| `ctx1`, `ctx2` | each sentence with the one or two before it said first, then cut away |
| `window` | six sentences a pass, the first two cut away |
| `native1`, `native2`, `nativegrp` | Ming's own prompt: the last audio and its words, nothing regenerated |
| `anchor`, `anchorgrp` | the same with one fixed clip as the prompt throughout, as Ming's own long-text code does |
| `ming-speaker` | one fixed speaker embedding, and no audio prompt |
| `ming-zero` | the empty speaker row Ming's README calls for |
| `ming-zero-short` | the same, with text cut at about 130 characters |

On the speech node, after the rest of the trial:

    sh harness/gate.sh python3 harness/context_make.py kokoro      # also phonon, ming
    env/bin/python -I harness/context_join.py kokoro phonon ming   # find the cuts, splice, score
    python3 harness/context_make.py ming --save-prompt             # the clip for ming-anchored, ming-speaker
    sh harness/gate.sh python3 harness/context_make.py ming-anchored --only drops   # also ming-speaker
    env/bin/python -I harness/context_drops.py ming ming-anchored ming-speaker ming-zero ming-zero-short   # who loses words
    sh harness/overlap_all.sh                                      # workers, Parakeet, replays
    python3 harness/schedule.py ming                               # X and Y, from those numbers
    env/bin/python -I harness/context_clips.py context-clips
    env/bin/python -I harness/context_report.py

All of it took 1 h 20 min on 2026-11-06. No call came during a run.

**Ming was losing words because the request left out a setting.** Ming's README calls every built-in
voice with `use_zero_spk_emb=True`, an empty speaker row, and `trial.py`'s `ming` didn't send audio.cpp's
`zero_speaker_embedding`. Its own Python also cuts text at about 24 words by default, where audio.cpp cuts
at 300 characters. All 60 passes below are the same text and seeds, straight from the server's stream, so
none of our cutting is in the path:

| Request | Passes that lost words | Words lost, of 2472 | Speed over the 60 passes, warm-up gaps included |
|---|---|---|---|
| `ming`: no speaker row, 300 characters | 30 of 60 | 108 | 2.76× |
| `ming-zero`: empty row, 300 characters | 2 of 60 | 9 | 2.76× |
| `ming-zero-short`: empty row, `text_chunk_size` 130 | 0 of 60 | 0 | 3.08× |
| `ming-speaker`: a real speaker embedding, 300 characters | 0 of 60 | 0 | 2.75× |

A sentence alone never lost a word, in any of them. The loss wasn't Parakeet (words were absent from short
windows heard on their own, and Qwen3-ASR heard the same gaps), the Q8 weights (full precision lost as many,
28 of 60) or the sampling settings (guidance 1 to 5, a lower noise scale). The voice runs on smoothly through
each gap: Ming generates frame by frame and chooses when to stop, so nothing makes it say every word.

A stored embedding of 856 bytes also works (`speaker_embedding_file`; 0 of 12 three-sentence passes), with
speed unchanged at 3.16×, and moves the voice down, from 200 Hz to 184 on a call's pieces. The 490 MiB of GPU
memory it adds in the table below is from loading the clip's encoders, and wasn't measured with the file.
This was on audio.cpp v0.9.4, and Ming's own Python hasn't been tried.

| Ming, words heard wrong of 305 | Built-in voice | With the speaker embedding |
|---|---|---|
| Each sentence alone | 0 | 0 |
| One text | 7 | 0 |
| As a call does | 20 | 0 |
| Overlaps cut away (`ctx1`, `ctx2`, `window`) | 11, 4, 12 | not tried |

**The overlap can't reach the end of a reply.** On Ming, a pass shares 512 positions between its prompt,
its text and the audio it makes, so asking for more loses the end: 8 sentences came back as 20.9 of
27 s, and 12 as 15 s. From sentence 1, X can't pass about 6 ordinary sentences, or about 20 s. On
Phonon, a pass is at most 50 tokens, about 2 ordinary sentences, past which it starts again knowing
nothing of what came before. Neither has anything like the whole reply in view.

**More workers don't help, on the GPU.** It runs one at a time, so workers only divide it:

| At once | Ming, each | First audio | Worst margin past the cut | Phonon, each |
|---|---|---|---|---|
| 1 | 2.43× | 117 ms | | 16.1× |
| 2 | 1.23× | 242 ms | +0.46 s | 14.2× |
| 3 | 0.81× | 378 ms | **-0.31 s** | 12.1× |

The margin is how long before sentence 1 ran out the first audio past the cut came, over first
sentences from "Okay." to one of 5.3 s. Worked out from these numbers, for Ming (`schedule.py`): two
workers can do replies of up to 5 sentences with no gap, X being the whole reply. Three never help:
they run dry by 0.16-0.57 s, always, and cost 25-45% more GPU time. From 8 sentences up there's no X and
Y at all. One worker, going straight on from one piece to the next, never ran dry at any length, with
the same first audio and 20-50% less GPU time than two. For Phonon, everything's so fast that it
doesn't matter, and a pass holds so little that the idea doesn't apply.

Played for real, with the text arriving over 1 or 2 s (`overlap.py replay`): 3, 8 and 24 sentences, one
worker, today's hand-over one second before a piece ends, no dry time at all, in 13 runs, one of
them with Parakeet transcribing all the time.

**Parakeet barely touches Ming.** Pieces 2.64×, sentences 3.19×, first audio 110 ms alone. With Parakeet
transcribing a stretch of 5.75 s once a second, far more than a call does: 2.57×, 3.11×, 112 ms. Doing
nothing else, without a break: 1.54×, 1.67×, 208 ms, 262 ms at worst, and Parakeet's 23 ms became 57 ms.

**Ming's prompt is real, and unsafe as a chain.** It takes earlier words and audio directly: `reference_text`
with `voice_ref` through audio.cpp's C API (`audiocpp_request_set_voice_audio`), `prompt_text` with
`prompt_latent` in its own Python. Nothing is regenerated, and a new prompt adds 30-140 ms to first audio. Chained, each
piece prompted by the last, it loses its place in the text, skipping and repeating sentences (28 to 61
words wrong) and making things up, and the voice drifts (pitch from 190 to 154 Hz, stability 0.60,
where a changing voice scores 0.54). One fixed clip as the prompt works: 0 wrong words of 305, even at
four sentences a piece, and stability 0.83. Phonon has no such way: every chunk starts again from its
voice, and its released models can't encode audio.

**One take, streamed, is the way.** `raw_text` puts a whole reply in one pass, and audio.cpp streams it from
about 0.11 s, however long the text. Ming's own Python (`ming_own.py`, commit a39b806) gives the same results as
audio.cpp, so these are Ming's behaviour, not the port's:

| On the 60 passes, empty row unless said | Passes that lost words | Words lost, of 2472 |
|---|---|---|
| audio.cpp, one piece | 3 | 10 |
| Ming's Python, one piece | 2 | 9 |
| Ming's Python, its default cuts at 24 words | 3 | 3 |
| Ming's Python, one piece, no row | 21 | 69 |

Every loss with the row was in a pass of 4 or 6 sentences. On the 102 replies of 9 real calls
(`context_make.py --only replies`, from a private `replies.json` on the node), two seeds each: all fit one take, the
longest 18.1 s; first audio 0.113 s, worst 0.126 s; 2.79× real time. One reply lost a clause in both seeds, and
one, "Mm-hmm.", wasn't heard at all. The scorer's other flags are mostly Parakeet writing "$7" for "seven dollars",
read by hand. "1000 kg" came out as "kilobit", so units still need spelling out. Guidance 3 was no better.

A take stops at about 21 s of speech through audio.cpp (512 positions). Ming's Python with `--cache 2048` said all 8
sentences in 26 s with no loss, and fell apart past that: 36 of 498 words lost at 12 sentences, 254 of 915 at 24,
so the backbone's 32768 positions don't help. Going on from a take, with its last sentence as the prompt
(`--only takes`, one try), skipped the second take's first sentence, so that isn't a way past the limit.

**Cutting a take off.** The voice cuts a take off by closing its request (`docs/design.md`,
"Speaking a reply"). In `cut_loop.py qwen3-hadley 30`, 30 cuts at random points from 0.3 to 6 s
into a long take, the server never crashed or hung, and the next reply's first audio came in
0.344 s (median; 0.355 s worst), against 0.065 s from an idle server, as the cut take stops
within a chunk or two. Once before that, in a first try, the server printed "double free or
corruption" after a cut and hung, so the node restarts it when a take goes 5 s with no audio.

**Why long takes fail, and what doesn't help.** `ming_own.py` records how each pass ended. With `--cache 2048`:

| Sentences | Positions: prompt + speech, of 2048 | Audio | How it ended |
|---|---|---|---|
| 8 | 254 + 328 | 26.2 s | Ming stopped, all words said |
| 12 | 328 + 412 | 32.9 s | Ming stopped, words skipped |
| 24 | 490 + 593 | 47.4 s | Ming stopped, words skipped |

So 512 positions is what ends a take at about 21 s, and it's not what ends one past 26 s: Ming stops itself with
965 positions spare. Why isn't known, and its paper gives no length.

Five texts that had lost words, six seeds each, 30 passes, chosen to fail, so these aren't typical rates:

| Ming's own Python | Passes that lost words | Words lost, of 2040 |
|---|---|---|
| Built-in voice, empty row, one piece | 7 | 28 |
| The same through its text path (`--prep`) | 7 | 28 |
| With `language_id="English"` | 13 | 50 |
| A real speaker embedding (`--speaker`) | 11 | 41 |
| Its cloning call, on a 6 s clip (`--clone`) | 11 | 33 |
| Its default cuts at 24 words | 6 | 6, the one word below |

No setting beat the built-in voice in one piece. Only the default cuts kept whole clauses, and they leave joins.
Their one loss, in all 6 tries of one text, was the word "because": Ming's splitter joins that fragment as
"I didn't change it,because", with no space. Counts here are Parakeet's alone, without the second ear.

**How long a batch can be.** Passes that lost words, by length, over the 60 passes through audio.cpp and through
Ming's Python and the 204 of real replies: none of 260 at up to 50 words, 7 of 64 above that. Numbers Parakeet wrote
as digits are left out, as in the table above. At 50 words a batch, 89% of the real replies have no join, and the
rest one. `--only batches` makes the first eight sentences as three takes, with a join as long as a pause inside a
take and 20 ms fades (`ming-batches.m4a`): no words lost, by either ear. The limit is what these texts showed, not
a guarantee.

**Qwen3 in one take** (`context_make.py MODEL --only long --label long --seeds 42,7,1895`). `long_texts.json` holds
four technical replies of about 300 words, written for this: each is cut at whole sentences near 50, 100, 150,
200 and 300 words, and every length made with three seeds, 60 takes per voice, each one request. `qwen3-aiden`
is the CustomVoice preset; `qwen3-hadley` is the Base model cloning Phonon's Hadley from a 6.7 s clip
(`refs/hadley.wav`). audio.cpp cuts Qwen3's text only past 8192 characters, so each take is one pass.

Counting runs of 3 or more words missing (`skips.py`, with `singles.py` for the rest), so numbers Parakeet writes as digits
and contractions don't count, at 50 to 200 words no take of 48 lost a phrase, for either voice. At 300 words one
of 12 did for each: Aiden stopped about 130 words early, and Hadley left out one sentence. Kokoro, each sentence
made alone, on the same texts: none, and the same few single-word differences ("I'd drop" heard as "I dropped"),
which are Parakeet's. Qwen3 had a few more of those, such as "I've finished" heard as "I finished".

| | Aiden | Hadley |
|---|---|---|
| First audio, median / worst | 0.015 / 0.087 s | 0.065 / 0.093 s |
| Slowest 10 s of any take | 2.66× | 3.02× |
| Whole run | 2.84× | 3.25× |
| Seconds a word, 50 / 300 words | 0.325 / 0.330 | 0.285 / 0.252 |
| GPU peak, whole card | 3.1 GB | 2.8 GB |

First audio and speed hold to the longest take, 108 s. Hadley speaks fast and speeds up as a take grows: 12% more
words a second at 300 words than at 50. Nothing here was heard, run beside Parakeet working, or on the phone.

**What a join is.** Measured in `ming-zero`'s and `ming-zero-short`'s passes, against sentence ends inside a piece:
0.45 s of pause against 0.38 s, and audio.cpp's `trim_silence` cuts the tail with no fade, up to 48 dB in 10 ms
against 15. Each piece also starts with no memory of the last. Which of these the ear catches wasn't tested.

**What context does to a sentence start.** Each sentence's opening is unchanged, as far as pitch shows:
its rise in the first half second moved 0.2 st (`whole`), and was lower in 13 of 24 sentences, a coin
flip. What does change is the join from one sentence to the next. On Ming the pitch step across a
sentence end falls from 3.0 st when made separately to 1.4-2.4 st with context, and is smaller at
15-19 of 23 ends. With the embedding the step is 2.5 st made separately. These are stand-ins for the
ear, and don't say what the user heard. The listening clips do.

| Ming, on sentences alone | Built-in voice | One clip as prompt | Embedding only |
|---|---|---|---|
| Losing words in longer passes, of 60 | 30 | 3 | 0 |
| Stability | 0.77 | 0.82 | 0.81 |
| Pitch range between sentences | 7.6 st | 3.5 st | 4.7 st |
| Loudness range | 4.8 LU | 2.5 LU | 4.0 LU |
| Raw keys missed, of 46 | 17 | 10 | 10 |
| Speed, first audio | 3.14×, 106 ms | 3.11×, 115 ms | 3.16×, 106 ms |
| Silence at the start | 0.14 s | 0.28 s | 0.14 s |
| GPU peak | 1602 MiB | not measured | 2092 MiB from a clip |

**Finding the cut.** Of 54 cuts, the pause nearest where the text says a sentence ends was right 53 times
on Phonon and 45 on Ming; Parakeet's word times, 54 and 50, at a cost of about 0.3 s of CPU a pass. Some of
Ming's misses are its own lost words, at the cut. On a very short first sentence, in 18 passes, 14 by pause
on both, and 16-17 by word times: "Okay." and "Done." were right every time, as they're followed by a
real pause, and what fails is a comma inside, as in "Got it, thanks." ("Got it. Dance."). Cut in the middle of
a pause, joins are clean: no click (-53 dB or lower, against speech), and only 1-2 of 23 joins on Ming
fell in speech, where it had no pause to cut at.

**Kokoro makes every sentence on its own anyway.** `Chunks` keeps each sentence of five words or more as
a chunk, so a larger piece changes nothing: 0.80 s between sentences whether it's made whole or
separately. The design says larger pieces keep the pauses natural, which isn't so on Kokoro.

**A second ear.** `second_ear.py` has Qwen3-ASR hear all 22 clips, under `tools/stt-trial/gpu.sh`, in the
`qwen3` environment and with its `backends.py`. It heard the same gaps Parakeet did in `ming-call` and
`ming-whole`, 7 and 3 words, and nothing else missing in any clip. Its one other difference is "passed" for
"pass" in `ming-speaker-call`, where Parakeet heard "pass". It also finds each sentence's start for the
listening page and hears each piece to prove the cut: 175 of 176 pieces began with their sentence's first two
words, and the other was "I finish" for "I finished" at the top of a clip, so all 176 are right.

Not checked: any of this by ear, on a phone, or on Ming's own Python. Why the empty row matters is inferred,
not shown: without it the model gets a prompt shape its README never uses. 60 passes of one text isn't every text.
A stored embedding with the audio prompt can't be tried, as the file replaces the prompt. No pass here
was made with the text still arriving.
