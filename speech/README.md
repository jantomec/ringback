# ringback-speech

The speech node of the Go port ("Moving to one hub, in Go" in `docs/design.md`): it dials
out to the hub over a WebSocket (`docs/protocol.md`, "The speech node") and does all the
hearing and speaking for calls, natively on a machine with a GPU (or a CPU):

- **Hearing**, per stream: Silero VAD v5 and Smart Turn v3.2 through ONNX Runtime's C API,
  Parakeet TDT 0.6B v2 through sherpa-onnx. It decides "speech started", "speech stopped"
  and "turn" as the Python voice does with Pipecat: the same thresholds from the app's
  sensitivity and background filter (`voice/ringback_voice/hearing.py`), the same volume gate
  (BS.1770 loudness of the last 400 ms), the same state machine and model resets, Smart Turn
  on the same Whisper features, and the turn wait (1.5 s). One transcription per stretch of
  speech, with 1 s before it and 0.5 s of silence after, as Pipecat's segmented STT.
- **Speaking**: Kokoro v1.0 `af_heart` through sherpa-onnx, a chunk (about a sentence) at a
  time, each sent as soon as it's made.
- One GPU worker: your words go before speech, and speech goes a chunk at a time, so your
  words wait at most for one chunk.

## Two processes, so the GPU is free between calls

The models take about 2.8 GB of memory and 2.9 GB of the GPU's, and letting them go inside
one process wouldn't free that: CUDA, cuDNN and ONNX Runtime keep their memory, and the
CUDA context, until their process exits. So the node is two processes of the same binary:

- **`ringback-speech serve`, the link process**, keeps the WebSocket to the hub (hello,
  token, pings, reconnecting) and never touches the models or the GPU. When a call is coming
  (`warm`, which the hub sends while the phone rings) or a stream needs one (`hear`,
  `speak`), it starts a worker and relays every message of the hub's to it, and the worker's
  to the hub. The hub's messages wait for the worker in a queue with no limit, in order,
  audio included, so the hub's link is never held up while the models load (about 2.2 s).
- **`ringback-speech work`, the worker**, loads the models and hears and speaks, as above.
  It talks to the link process on its stdin and stdout: frames of one byte of kind (text,
  binary, or control between the two), 4 of length and the message (`cmd/ringback-speech/pipe.go`).
  Its log goes to stderr, which is the node's log.
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
by context, and any other word through espeak-ng, mapped to Kokoro's phonemes as the Python
voice's misaki does. Without espeak-ng such words are split into known words or spelled.

## Install directory

Everything lives in one folder (by default `~/ringback-speech`), nothing outside it:

| Path | What |
|---|---|
| `sherpa-onnx/` | sherpa-onnx v2.0.1 prebuilt, the CUDA 13 / cuDNN 9 build (`lib/`, `include/`); its ONNX Runtime is 1.28.2 |
| `lib/` | the NVIDIA runtime libraries it needs (cudart, cuBLAS, cuRAND, NVRTC, cuDNN 9), unpacked from their PyPI wheels |
| `onnxruntime/include/` | the headers of ONNX Runtime 1.28.2, from its release `onnxruntime-linux-x64-1.28.2.tgz` |
| `models/silero-vad-v5.onnx` | Silero VAD, the file Pipecat ships (snakers4/silero-vad, `src/silero_vad/data/silero_vad.onnx`) |
| `models/smart-turn-v3.2-cpu.onnx` | Smart Turn v3.2, the file Pipecat ships (Hugging Face `pipecat-ai/smart-turn-v3`) |
| `models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16/` | Parakeet, from sherpa-onnx's asr-models release (fp16 on a GPU; the int8 one on a CPU) |
| `models/kokoro-multi-lang-v1_0/` | Kokoro, from sherpa-onnx's tts-models release |
| `espeak-ng/` | `lib/libespeak-ng.so.1` and `espeak-ng-data/` (only `en_dict`, `phon*`, `intonations`, `lang/`, `voices/`), from the `espeakng-loader` 0.2.4 wheel, as the Python voice uses; optional |
| `go/` | a Go toolchain, if the machine has none; Go's caches go to `gopath/` and `gocache/` |
| `env.sh` | sets `LD_LIBRARY_PATH` to `sherpa-onnx/lib` and `lib`: source it before running |
| `speech.toml` | the settings, from `speech.example.toml` |
| `bin/` | `ringback-speech` and `fakehub` (`build.sh` can build into another folder, such as `bin-next/`, to try a build beside the one that runs) |

## Build, run, check

```sh
speech/build.sh                     # on the speech node, into bin/
. ~/ringback-speech/env.sh
~/ringback-speech/bin/ringback-speech serve
~/ringback-speech/bin/ringback-speech phonemes "All 142 tests pass."   # what the voice would say
~/ringback-speech/bin/ringback-speech say "All 142 tests pass." out.wav
speech/check.sh                     # the node against fakehub, about 3 min
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
a turn Smart Turn finds unfinished; one with a pause; two streams at once), checking the
events and words and timing the turns, and prints the memory of the link process and the
worker. With the idle time at 5 s (`IDLE`), it checks that the worker exits that long after
the last message and the GPU no longer lists it, that `hear` starts a new one that hears,
and that one started by `warm` alone exits too; then it kills a worker during a call (the
node must close its link and link again) and drops the link while a worker loads (the worker
must exit). The node's log ends with each worker's start, load time and exit.

The pure Go parts have tests that run anywhere (`go test ./internal/hearing
./internal/speaking`); with `RINGBACK_KOKORO_DIR` set to Kokoro's folder they check that
every word of sentences sherpa-onnx dropped words from gets sounds.
`internal/hearing/parity_test.go` (build tag `parity`, on the speech node) compares the
voice-activity states, frame by frame, and Smart Turn's verdict with Pipecat's own, from
reference values made with the Python voice's environment (`RINGBACK_HEARING_REF`; the
script that makes them isn't in the repo).

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
- **End of speech → `turn`:** median 318 ms (258–341) when Smart Turn ends the turn:
  `speech_stopped` comes 180–260 ms after (it needs 0.2 s of pause), then Smart Turn (about
  50 ms on one CPU thread) runs alongside Parakeet (75–90 ms, its queue included). When
  Smart Turn finds the turn unfinished, the turn wait ends it 1.70 s after the speech.
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
