# ringback-speech

Ringback's speech node ("The parts" in `docs/design.md`): it dials out to the hub over a
WebSocket (`docs/protocol.md`, "The speech node") and does all the hearing and speaking for
calls, natively on a Linux machine with an NVIDIA GPU (its config can name the CPU instead,
`provider = "cpu"`, which `install.sh` doesn't set up and which hasn't been tried):

- **Hearing**, per stream: Silero VAD v5, and Smart Turn v3.2 for the log, through ONNX Runtime's C API,
  Parakeet TDT 0.6B v2 through sherpa-onnx. It decides "speech started", "speech stopped"
  and "turn" as Pipecat 1.14.0 does (the voice framework whose hearing these settings were
  tuned with): the same thresholds from the app's sensitivity and
  background filter (`internal/hearing/params.go`), the same volume gate (BS.1770 loudness of
  the last 400 ms), the same state machine and model resets, and Smart Turn on the same Whisper
  features, whose score only goes in the log. A turn is every pause of about 0.7 s, and whether
  you've finished is for the call's model to say. At each stop, Parakeet writes down your whole
  turn so far, everything since the voice last started speaking, with 1 s before it and 0.5 s of
  silence after: on your recordings it got 20 words of 519 wrong that way, and 44 stretch by
  stretch, as Pipecat's segmented STT cuts them (`tools/stt-trial`). A turn over a minute keeps
  what its last pause heard, and only what came since is written down again.
- **Speaking**: Qwen3-TTS 1.7B Base through audio.cpp's server, in the voice of a clip of
  Phonon 31M's Hadley (`voices/`): a whole reply in one take, streamed as it's made, its first
  audio about 0.08 s after the hub asks (`check.sh`). With `voice = "kokoro"` in `speech.toml`:
  Kokoro v1.0 `af_heart` through sherpa-onnx, a chunk (about a sentence) at a time.
- One GPU worker for Parakeet and Kokoro: your words go before speech, and Kokoro's speech goes a
  chunk at a time, so your words wait at most for one chunk. Qwen3 runs in a process of its own
  (below), so your words are transcribed while it speaks.

## Two processes, so the GPU is free between calls

The models take about 2.8 GB of memory and 2.9 GB of the GPU's (with Qwen3, its server
another 2.6 GB of memory, and the GPU peaked at 4.9 GB in all in `check.sh`), and letting them go inside
one process wouldn't free that: CUDA, cuDNN and ONNX Runtime keep their memory, and the
CUDA context, until their process exits. So the node is two processes of the same binary:

- **`ringback-speech serve`, the link process**, keeps the WebSocket to the hub (hello,
  token, pings, reconnecting) and never touches the models or the GPU. When a call is coming
  (`warm`, which the hub sends while the phone rings) or a stream needs one (`hear`,
  `speak`), it starts a worker and relays every message of the hub's to it, and the worker's
  to the hub. The hub's messages wait for the worker in a queue with no limit, in order,
  audio included, so the hub's link is never held up while the models load (about 2.2 s with
  Kokoro, 4.6–5.2 s with Qwen3).
- **`ringback-speech work`, the worker**, loads the models and hears and speaks, as above.
  It talks to the link process on its stdin and stdout: frames of one byte of kind (text,
  binary, or control between the two), 4 of length and the message (`cmd/ringback-speech/pipe.go`).
  Its log goes to stderr, which is the node's log.
- **audio.cpp's server**, with Qwen3, which the worker starts as it loads and talks to over
  HTTP on 127.0.0.1, streaming each take as server-sent events (`internal/audiocpp`). It dies
  with the worker (a parent-death signal). If it dies, or a take goes 5 s with no audio, at
  its start or partway (it hung once after a take was cut off), the worker starts it again and
  the reply goes on from the sentence it was in, after the voice says the server crashed
  (`takes.go`).
- Once the worker has had no stream, no speech to make and no message from the hub for the
  idle time (`idle` in `speech.toml`, 60 s), it asks the link process to let it go, which
  does if nothing more is on its way to it, and the worker exits. The next `warm` or `hear`
  starts a new one. If a worker dies with streams open, the link process closes its link to
  the hub and links again, so the hub ends those calls; if the link drops, the worker is
  stopped, since the hub has ended its calls.

## Words sherpa-onnx would drop

sherpa-onnx 2.x reads text only through Kokoro's lexicon and silently drops every word that
isn't in it ("42", "voice_server.py", "onnx"); 1.x fell back to espeak-ng for those (checked
in its source, v1.13.8, `kokoro-multi-lang-lexicon.cc`), 2.x no longer does. So the node
turns text into phonemes itself (`internal/speaking`) and gives sherpa-onnx the phonemes:
the same lexicon, numbers read out ("142" one hundred forty two, "2026" twenty twenty six,
"3.14", "1st", "42%", "$5", "12:30", "2.0.1", "500ms" five hundred milliseconds),
identifiers split ("voice_server.py" voice server dot pie, "camelCase", "macOS"), short
acronyms spelled ("GPU"), word forms by misaki's rules ("milliseconds"), "the", "a" and "to"
by context, and any other word through espeak-ng, mapped to Kokoro's phonemes as misaki
(Kokoro's own front end) does. Without espeak-ng such words are split into known words or spelled.

## Install

On the speech node, Linux on x86-64 with an NVIDIA GPU of compute capability 7.5 (Turing) or
later and its driver, 580 or later (CUDA 13):

```sh
speech/install.sh                   # into ~/ringback-speech, or the folder given
speech/build.sh                     # into its bin/
~/ringback-speech/bin/ringback-speech pair HUB CODE   # a code from the hub's portal (Setup)
~/ringback-speech/bin/ringback-speech install   # a service: now, at boot, and whenever it stops
```

`install.sh` needs only curl (or wget), tar, bzip2, gzip, unzip and sha256sum: no pip, Python
or Docker (a wheel is a zip, fetched from PyPI by its URL). It checks each download against a
SHA-256 pinned in it, and each part of the folder against a SHA-256 of its files, pinned from
the first install, which was made by hand, so every file is the same as there. Run again, it
hashes what's there and fetches only what's missing or different; a part that differs is
replaced, and nothing outside the folder is touched. One run at a time (`.installing`); a run
that fails or is stopped keeps what it fetched in `download/` for the next. It stops on the
wrong machine (not x86-64 Linux, no NVIDIA driver, a driver older than 580, a GPU older than
Turing) and warns of a driver older than CUDA 13.3's (610.43.02), whose libraries these are:
CUDA 13's minor version compatibility should cover it, but that's untried. sherpa-onnx's model
releases are tags whose files can be uploaded again (Kokoro's was, 2026-10-26); then
`install.sh` stops at its SHA-256 and the pin needs a look.

Measured on the speech node (2026-11-02, 2.7 GB to download): the first run took 69 s, the
parts side by side (Parakeet, 1.1 GB of bzip2, the longest; every other part was in place
within 32 s); a second run took 0.9 s and fetched nothing. The folder is 3.8 GB, every file
(16,341) the same SHA-256 as the hand-made install's; `build.sh` then took 4 s and added
125 MB (`bin/` and Go's caches), and `check.sh` passed all 45 checks with it.

Everything lives in that one folder, nothing outside it:

| Path | Size | What |
|---|---|---|
| `sherpa-onnx/` | 307 MB | sherpa-onnx v2.0.1 prebuilt, the CUDA 13 / cuDNN 9 build (`lib/`, `include/` and five of its tools in `bin/`); its ONNX Runtime is 1.28.2 |
| `lib/` | 1.7 GB | the NVIDIA runtime libraries it needs (cudart, cuBLAS, cuRAND, NVRTC, cuDNN 9), unpacked from their PyPI wheels (CUDA 13.3); not cuFFT or nvJitLink, which nothing loads |
| `onnxruntime/` | 1 MB | the headers of ONNX Runtime 1.28.2 (`include/`) and its licence, from its release `onnxruntime-linux-x64-1.28.2.tgz` |
| `models/silero-vad-v5.onnx` | 2 MB | Silero VAD, the file Pipecat 1.14.0 ships, from its wheel (snakers4/silero-vad's `silero_vad.onnx`) |
| `models/smart-turn-v3.2-cpu.onnx` | 8 MB | Smart Turn v3.2, the file Pipecat 1.14.0 ships, from its wheel (Hugging Face `pipecat-ai/smart-turn-v3`) |
| `models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16/` | 1.2 GB | Parakeet, from sherpa-onnx's asr-models release (fp16 for a GPU; on a CPU the int8 one is faster, which `install.sh` doesn't fetch) |
| `models/kokoro-multi-lang-v1_0/` | 384 MB | Kokoro, from sherpa-onnx's tts-models release |
| `models/qwen3-tts-12hz-1.7b-base-q8_0.gguf` | 2.6 GB | Qwen3-TTS 12 Hz 1.7B Base, audio.cpp's Q8 build, from Hugging Face `audio-cpp/audio.cpp-gguf` |
| `audiocpp/` | 1.8 GB | audio.cpp v0.9.4's `audiocpp_server`, its `model_specs/` and licence, and in `lib/` the CUDA 12.8 libraries its release ships with NCCL from NVIDIA's wheel, kept apart from the node's CUDA 13 ones |
| `voices/` | 324 KB | `hadley.wav`, the clip Qwen3 speaks like (Phonon 31M's Hadley, Apache 2.0, made for ringback), and `hadley.txt`, its words, from this repository |
| `espeak-ng/` | 3 MB | `lib/libespeak-ng.so.1` and `espeak-ng-data/` (only `en_dict`, `phon*`, `intonations`, `lang/`, `voices/`), from the `espeakng-loader` 0.2.4 wheel, as misaki uses; optional |
| `go/` | 283 MB | Go 1.27.3, if there's no Go on PATH as recent as `go.mod` asks; `build.sh` puts Go's caches in `gopath/` and `gocache/` (read-only: `chmod -R u+w gopath` before deleting it) |
| `env.sh` | | sets `LD_LIBRARY_PATH` to `sherpa-onnx/lib` and `lib`: source it before running |
| `speech.toml` | | the settings, copied from `speech.example.toml` if there are none: `pair` writes `hub` and `key` (or set `hub` and `token`) |
| `bin/` | 22 MB | `ringback-speech` and `fakehub`, from `build.sh` (which can build into another folder, such as `bin-next/`, to try a build beside the one that runs) |

## As a service

`ringback-speech install` makes the node a systemd user service: it writes
`~/.config/systemd/user/ringback-speech.service`, which runs `bin/ringback-speech serve` in the
install directory with `env.sh`'s environment and starts it again whenever it stops, enables
and starts it, and turns on lingering, so it runs from boot rather than only while you're
logged in. The stop signal goes to `serve` alone, which stops its worker. Its log is the
journal's (`journalctl --user -u ringback-speech -f`). `install` refuses while `speech.toml`
lacks the hub, or the key or token, or while a node started by hand runs; run it again after a new
build to restart the node; `ringback-speech uninstall` stops it and removes the unit.

On the speech node (2026-11-03), installed over a node that a unit made with `systemd-run`
was running: the unit was enabled and active, the node linked to the hub, and killed with
SIGKILL it was running again and linked 5 s later. Starting at boot isn't tried.

## Build, run, check

```sh
speech/build.sh                     # on the speech node, into bin/
. ~/ringback-speech/env.sh
~/ringback-speech/bin/ringback-speech serve    # in a terminal, with no service running
~/ringback-speech/bin/ringback-speech phonemes "All 142 tests pass."   # what Kokoro would say
~/ringback-speech/bin/ringback-speech say "All 142 tests pass." out.wav  # in the voice speech.toml sets
~/ringback-speech/bin/ringback-speech listen --gate 0.5 yes.wav   # heard as a call hears it
speech/check.sh                     # the node against fakehub, about 5 min
# A build in bin-next/, beside a node that runs, with its own port and settings (a copy of
# speech.toml in the install directory):
speech/build.sh ~/ringback-speech bin-next
PORT=8811 BIN=bin-next CONFIG=speech.next.toml speech/check.sh
```

`check.sh` starts the node and `fakehub`, a stand-in hub. It sends `warm` and at once a
turn (its audio all at once) and a piece of speech, all before the models are loaded,
checking that the link answers a ping meanwhile and that the turn's words and the speech come
back once they are. Then it asks for speech (checking the audio's length against `spoken`
and how soon the first audio comes), cancels speech, and sends speech in 20 ms frames in real
time (the voice's own pieces, so its numbers and file names must come back as words; a
recording; the recording 20 dB quieter with the background filter off and at its strongest;
a turn that stops short; one with a short pause; two turns with a longer one; two streams at once), checking the
events and words and timing the turns, and prints the memory of the link process and the
worker. With the idle time at 5 s (`IDLE`), it checks that the worker exits that long after
the last message and the GPU no longer lists it, that `hear` starts a new one that hears,
and that one started by `warm` alone exits too; then it kills a worker during a call (the
node must close its link and link again) and drops the link while a worker loads (the worker
must exit). The node's log ends with each worker's start, load time and exit.

Something said that started no turn is in the node's log: each stretch of 0.1 s or more that
Silero took for speech while you weren't speaking, which the background filter kept out
(quieter than its volume) or that was shorter than the sensitivity needs, as "not taken for
speech", with its length, loudness and why. It changes nothing about what's heard. On
2026-11-03, `check.sh` passed all 45 checks with it, and the recording 20 dB quieter, with the
filter at its strongest (-30 LUFS), logged three: 1.1–2.5 s each, loudest -38 to -43 LUFS,
all of it kept out by the filter. No stream that was heard logged one.

The pure Go parts have tests that run anywhere (`go test ./internal/hearing
./internal/speaking`); with `RINGBACK_KOKORO_DIR` set to Kokoro's folder they check that
every word of sentences sherpa-onnx dropped words from gets sounds.

Two things in hearing differ from Pipecat on purpose, after short answers on calls went
unheard (`tools/short-answers`, with its results): the background filter compares its gate
with the loudness of the speech Silero hears, over as much of it as has come, up to 400 ms,
rather than of the last 400 ms smoothed from window to window; and a turn that Parakeet writes
down as nothing is written down once more from 0.1 s before it began, without the second kept
before it. `listen` hears WAV files as a call does, each as a call of its own, and prints what
it heard as JSON lines; it takes each turn as answered, so a file's turns don't run together.

`internal/hearing/parity_test.go` (build tag `parity`, on the speech node) compares the
voice-activity states, frame by frame, and Smart Turn's verdict with Pipecat's own, from
reference values made with Pipecat 1.14.0 (`RINGBACK_HEARING_REF`; the script that makes them
isn't in the repo).

## Measured (2026-11-02, the speech node: one NVIDIA GPU, CUDA)

From `check.sh` (all 22 checks passed, 2.5 min, before the node was two processes) and the
parity test:

- **Same decisions as Pipecat:** the voice-activity state of every 20 ms frame matched
  Pipecat's on 924 frames of a recording at two levels, with three settings (default,
  background filter strongest, most sensitive); Smart Turn 0.92559 against Pipecat's
  0.92559; the Whisper features are identical; the volume is within 0.0005 (0.05 dB).
- **Words outside the lexicon are spoken:** Parakeet, hearing the voice's own audio back,
  got "voice server.py at line 42", "one hundred forty two tests", "38 seconds", "2.1GB of
  VRAM", "500 milliseconds is 0.5 seconds"; through sherpa-onnx's own front end the first of
  those sentences came out as "I fixed the bug in. At line, it was an off-by-one error."
- **Speak → first audio:** median 118 ms, at most 164 ms, for pieces of one sentence
  (1.3–7.1 s of audio, made 35–45 times faster than real time).
- **End of speech → `turn`:** `speech_stopped` comes 180–260 ms after (it needs 0.2 s of pause),
  then Parakeet takes 75–90 ms, its queue included, beside Smart Turn (about 50 ms on one CPU
  thread), and the turn wait ends the turn about 0.7 s after the speech (680 ms on the recording,
  2026-11-06). Before the turn wait became every turn's, Smart Turn ended most turns at 318 ms.
- **Memory**, all models loaded: 2.84 GB resident (2.42 GB of it allocated outside Go, by
  CUDA, cuDNN and ONNX Runtime; Go's own is 34 MB), 2.95 GB of GPU memory.

With the two processes, from three runs of `check.sh` (the last: all 45 checks passed, 2.5
min; speak → first audio median 118 ms, end of speech → `turn` median 301 ms, as before):

- **The link process alone:** 16 MB resident (3 MB its own, 13 MB of the libraries it
  maps), 21 MB after relaying calls; `nvidia-smi` never lists it.
- **`warm` → models loaded:** 2.19–2.30 s (13 workers, in those runs and a test of stopping
  `serve`). Meanwhile the link answers at once:
  a ping sent after `warm`, `hear`, 10.5 s of audio and `speak` came back 2 ms after `warm`.
  Once loaded, the worker answers what waited: the first event 2.24 s after `warm`, the
  piece's first audio 2.34 s, the turn 2.59 s.
- **The worker, models loaded:** 2.78 GB resident (2.37 GB its own, by CUDA, cuDNN and ONNX
  Runtime), 2946 MiB of GPU memory.
- **After the idle time, both are free:** with the idle time at 5 s the worker exited 5.7–5.8 s
  after the last message (it looks every second); `nvidia-smi` stopped listing it 0.1–0.2 s
  after it exited (as soon as it could be asked), and the GPU's memory in use, by every
  process, went from 3963 MiB before `warm` to 6914 MiB with the worker and back to 3963 MiB.
- **When something dies:** a worker killed during a call made the link process close its
  link 94 ms later and link again 1.1 s after; a worker still loading when the link dropped
  exited at once; stopping `serve` (SIGTERM) stops its worker.
