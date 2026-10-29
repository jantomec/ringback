# VoxLock delay

Measures how much later the voice hears you start speaking when your side goes through
VoxLock, which is how much later it stops when you interrupt it. Each recording is played
in 20 ms frames, as a call delivers them, into the voice's own voice-activity detection:
once directly, and once through the voice's VoxLock filter with a voice sample. Starts of
speech are matched between the two runs.

Run from the repo root, with your recordings (16 kHz mono WAV, see `tools/stt-eval`).
The first argument is the voice sample, at least 3 s of your voice; the rest are the
recordings:

    uv run --project voice tools/voxlock-delay/voxlock_delay.py \
      DATA_DIR/wav/airpods-quiet.wav:0-15 DATA_DIR/wav/*.wav [--sensitivity S]

It prints how much later the starts came through VoxLock (median, middle half, range),
the starts that only one run heard, with their times, and how long VoxLock took to compute
each 80 ms chunk. The delay is measured on the recording's own clock. On a call, the
computing time comes on top, because each chunk is computed once it has arrived.
