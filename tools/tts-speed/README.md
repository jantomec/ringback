# Speech speed

Times Kokoro with the `af_heart` voice on this Mac, for three replies from one sentence to
a long answer (`texts.py`): how long until the first audio, and until all of it. Each piece
of a spoken reply has to be ready within the voice's one-second lead (see "Speaking a
reply" in `docs/design.md`), so this decides how Kokoro runs.

Run from the repo root. `OUT_DIR` is optional: with it, the long reply is also saved there
as a WAV for listening. Keep it outside the repo.

1. **Pipecat's own Kokoro service** (kokoro-onnx), inside a Pipecat pipeline. It runs on
   the CPU; `ONNX_PROVIDER` picks another onnxruntime provider. The first run downloads
   the model (about 350 MB) to `~/.cache/pipecat/kokoro-onnx`.

       uv run --no-project --with 'pipecat-ai[kokoro]==1.14.0' tools/tts-speed/pipecat_kokoro.py OUT_DIR
       ONNX_PROVIDER=CoreMLExecutionProvider uv run --no-project --with 'pipecat-ai[kokoro]==1.14.0' tools/tts-speed/pipecat_kokoro.py OUT_DIR

2. **Kokoro through MLX** (mlx-audio), as the voice runs it. The first run downloads the
   model from Hugging Face and spaCy's English model.

       uv run --no-project --with mlx-audio==0.6.2 --with 'misaki[en]==0.9.4' tools/tts-speed/mlx_kokoro.py OUT_DIR

Each prints, per text, the length of the audio and the median, minimum and maximum of five
runs after one warm-up. Run them one at a time, on an otherwise idle Mac.

With CoreML, Pipecat sometimes reports a request as finished with no audio ("runs with no
audio" above 0). Its audio then arrives during the next run, which looks impossibly fast,
so that text's numbers don't count. Run it again.
