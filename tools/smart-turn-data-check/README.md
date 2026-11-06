# Smart Turn data check

Smart Turn hears a phone call after 200 ms of silence. How much silence did the clips it learned
from actually have after the speech, and does the length of that silence give the label away?
This checks both on the published clips. It needs no call and no private data.

For each clip it finds where the speech ends in two ways: Silero VAD at threshold 0.5, the way
Silero's `get_speech_timestamps` does (held until the probability falls under 0.35, plus 30 ms of
pad), which is how OpenHRI measured it, and the last 20 ms frame louder than max(floor + 12 dB,
-50 dBFS). Whatever follows is the clip's tail. AUC is the chance that a complete clip has a longer
tail than an incomplete one: 0.5 means the tail says nothing about the label.

Each shard of `pipecat-ai/smart-turn-data-v3.2-test` (10 shards) or `-train` (83) is about 490 MB.
Silero's ONNX file is in its repository, `src/silero_vad/data/silero_vad.onnx`:

    uv run --isolated --no-project --with pyarrow --with soundfile --with onnxruntime --with numpy \
        python -I tools/smart-turn-data-check/check.py <shard.parquet> --vad <silero_vad.onnx>

`--out clips.csv` keeps one row per clip. It takes about a minute a shard on one laptop core.

Result on 2026-11-06, from test shard 0 (3,153 clips) and train shard 40 (3,264 clips):

| Clips | Under 0.2 s of tail, Silero | Under 0.2 s of tail, energy | Median tail, Silero |
|---|---|---|---|
| All, test / train | 80.4% / 80.3% | 28.2% / 28.2% | 0.08 / 0.08 s |
| Synthetic, test / train | 93.2% / 91.8% | 31.1% / 30.8% | 0.07 / 0.07 s |
| English, test / train | 37.4% / 36.5% | 20.6% / 19.6% | 0.68 / 0.71 s |
| English human, test / train | 18.0% / 15.9% | 13.4% / 11.7% | 0.80 / 0.82 s |

So OpenHRI's 79% stands for the whole set with Silero's offset, but not for the English human clips,
and Silero's offset runs later than the sound does. Tail length says little about the label overall
(AUC 0.60 / 0.56 with Silero), but it almost gives it away in one English human source: `midcentury_1`,
AUC 0.96 / 0.93 with Silero and 0.90 / 0.89 with energy, on 108 and 95 clips. Its incomplete clips stop
where the speech stops (96 to 100% have under 50 ms of tail), and its complete ones run on (median
0.47 to 0.62 s).

The two shards are about 10% of the test split and 1.2% of the train split. It measures the clips, not
the model: it doesn't show that the model learned the tail.
