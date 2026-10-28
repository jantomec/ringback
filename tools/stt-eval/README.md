# Speech-to-text evaluation

Measures candidate speech-to-text models on your own voice, the way the call pipeline
will use them: the recording is split into utterances at pauses, and each utterance is
transcribed separately. Everything is local except the optional Haiku step.

Keep the recordings and transcripts outside this repo. They hold your voice and whatever
you said; `DATA_DIR` below is that private folder.

1. **Record** with the microphone you'll use for calls (Voice Memos is fine):
   - each scripted recording is `sentences.txt` read aloud, pausing about 2 s between
     sentences;
   - add a few minutes of free speech about your own work.

   Copy the files to `DATA_DIR/raw/`.
2. **Convert** each file to 16 kHz mono WAV in `DATA_DIR/wav/`. The file name, minus
   `.wav`, names the recording, and names containing `free` are treated as unscripted:

       afconvert -f WAVE -d LEI16@16000 -c 1 in.m4a DATA_DIR/wav/airpods-quiet.wav

3. **Split** at pauses:

       uv run --with numpy tools/stt-eval/segment.py DATA_DIR

4. **Transcribe** with each model (see `MODELS` in `transcribe.py`). The first run
   downloads the model:

       uv run --with mlx-audio==0.6.2 tools/stt-eval/transcribe.py parakeet-v2 DATA_DIR

5. **Reference for free speech:** take the best transcript and correct it into
   `DATA_DIR/ref/<recording>.txt`, one line per utterance. Lines starting with `#` are
   ignored. The correction starts from one model's text, so that model is favoured on
   this recording.
6. **Optional:** let Haiku repair the transcripts from context, through Claude Code.
   This writes `<model>+haiku` results:

       python3 tools/stt-eval/correct.py DATA_DIR

7. **Score:**

       uv run --with jiwer==4.0.0 --with whisper-normalizer==0.1.15 tools/stt-eval/score.py DATA_DIR

   This prints the share of words wrong per model and recording, and writes word-by-word
   alignments to `DATA_DIR/report/`.
