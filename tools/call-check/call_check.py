#!/usr/bin/env python3
"""Plays a scripted call through the voice's pipeline on this Mac, without the phone.

    uv run --project voice tools/call-check/call_check.py OUT_DIR TURN... [--report FILE] [--turn-wait S]
        [--sensitivity S] [--background SOUND [--background-db DB]]

Each TURN is what you say: text, which another Kokoro voice speaks ("[1.2]" in it is a
pause of that many seconds), or a CLIP: a 16 kHz mono WAV file, optionally cut to a span
("file.wav:12.5-19"). A turn starts 1 s after the voice
stops talking. A turn written "@S:..." starts S seconds after the voice starts its reply
instead, to interrupt it. The call's audio connects CONNECT seconds after it starts, as on
the phone. A background SOUND (text spoken by a third Kokoro voice, or a CLIP) plays under
the whole call, looped. Prints the voice's own log, then each turn's timing measured here,
and writes both sides of the call to OUT_DIR/call.wav.
"""

import argparse
import asyncio
import re
import time
import wave
from datetime import datetime
from pathlib import Path

import numpy as np
import soxr
from pipecat.frames.frames import InputAudioRawFrame, LLMRunFrame, OutputAudioRawFrame, StartFrame
from pipecat.pipeline.runner import PipelineRunner
from pipecat.transports.base_input import BaseInputTransport
from pipecat.transports.base_output import BaseOutputTransport
from pipecat.transports.base_transport import BaseTransport, TransportParams

from ringback_voice import hearing, models
from ringback_voice.__main__ import TURN_WAIT, call_task

YOUR_VOICE = "am_michael"  # Kokoro voice for text turns
SOMEONE_ELSE = "bf_emma"  # Kokoro voice for a background of text
PAUSE = 1.0  # seconds of the voice's silence before a normal turn starts
TICK = 0.02  # seconds of audio per input frame
LOUD = 100  # int16 level above which the voice counts as speaking
LIMIT = 300  # seconds before a call that never finishes is given up
CONNECT = 2.5  # seconds from the start of a call to connected audio (2.2–2.9 s on the phone)
UNANSWERED = 15  # seconds after your last turn without a reply before the call ends
AFTER_YOU = 1.0  # seconds after your turn in which the voice's log can still be about it


class Call:
    """Who is talking when, shared by the input and output side."""

    def __init__(self):
        self.t0 = time.monotonic()
        self.voice_start = None  # when the voice's current reply started
        self.voice_end = 0.0  # when its last audio ends
        self.your_end = 0.0  # when your last turn ended
        self.interrupting = None  # when your interrupting turn started
        self.speaking = False  # you're in a normal turn, which the voice shouldn't talk over
        self.talked_over = None  # when the voice started talking over you in it
        self.went_on = None  # when the voice's audio ended after that
        self.recording: list[tuple[float, np.ndarray]] = []  # time, int16 samples at 24 kHz
        self.yours: list[list[float]] = []  # start and end of your speech in each turn
        self.background_at = None  # when the background started
        self.background_len = 0  # samples of it played
        self.timings: list[str] = []
        self.done = asyncio.Event()

    def now(self) -> float:
        return time.monotonic() - self.t0

    def you_spoke(self) -> bool:
        """Whether you're speaking, or just stopped."""
        now = self.now()
        return any(start <= now <= end + AFTER_YOU for start, end in self.yours)


class ScriptedInput(BaseInputTransport):
    """Your side: plays each turn when it's due, and silence in between, in real time."""

    def __init__(self, params, turns: list[tuple[float | None, np.ndarray, float, float]], background, call: Call):
        super().__init__(params)
        self._turns = turns
        self._background = background
        self._shared = call
        self._player = None

    async def start(self, frame: StartFrame):
        await super().start(frame)
        await self.set_transport_ready(frame)
        self._player = self.create_task(self._play())

    async def cleanup(self):
        await super().cleanup()
        if self._player:
            await self.cancel_task(self._player)

    def _unanswered(self) -> bool:
        """Whether the voice has left your last turn unanswered for UNANSWERED seconds."""
        c = self._shared
        replied = c.voice_start is not None and c.voice_start > c.your_end
        return c.your_end > 0 and not replied and c.now() - max(c.your_end, c.voice_end) > UNANSWERED

    def _due(self, at: float | None) -> bool:
        c, now = self._shared, self._shared.now()
        if self._unanswered():
            return True
        replied = c.voice_start is not None and c.voice_start > c.your_end
        if at is None:
            return replied and now - c.voice_end >= PAUSE
        return replied and now - c.voice_start >= at

    async def _play(self):
        c, step = self._shared, int(models.HEARING_RATE * TICK)
        turns, playing, pos, started = list(self._turns), None, 0, 0.0
        background, bpos = self._background, 0
        c.background_at = c.now()
        tick = time.monotonic()
        while True:
            if playing is None and (turns or not c.done.is_set()) and self._unanswered():
                c.timings.append(f"the voice didn't answer within {UNANSWERED} s")
            if playing is None and turns and self._due(turns[0][0]):
                at, playing, onset, offset = turns.pop(0)
                pos, started = 0, c.now()
                c.recording.append((started, soxr.resample(playing, models.HEARING_RATE, models.SPEAKING_RATE)))
                c.yours.append([started + onset, float("inf")])
                if at is None:
                    c.speaking, c.talked_over = True, None
                    c.timings.append(f"you spoke at {started + onset:.1f} s, {started + onset - c.voice_end:.1f} s after the voice ended")
                else:
                    c.interrupting, c.went_on = started + onset, None
                    c.timings.append(f"you interrupted at {started + onset:.1f} s, {started + onset - c.voice_start:.1f} s into the reply")
            if playing is not None:
                chunk, pos = playing[pos : pos + step], pos + step
                if pos >= len(playing):
                    playing, c.your_end = None, started + offset
                    c.yours[-1][1] = c.your_end
                    c.timings.append(f"you stopped at {c.your_end:.1f} s")
                    if c.talked_over is not None:
                        c.timings.append(f"THE VOICE TALKED OVER YOU at {c.talked_over:.1f} s")
                    c.speaking = False
                    if c.interrupting is not None:
                        kept = (c.went_on or c.interrupting) - c.interrupting
                        c.timings.append(f"the voice went on {kept:.2f} s after you started speaking")
                        c.interrupting = None
            else:
                chunk = np.zeros(0, dtype=np.int16)
            chunk = np.pad(chunk, (0, step - len(chunk)))
            if background is not None:
                noise = np.take(background, range(bpos, bpos + step), mode="wrap")
                chunk = np.clip(chunk.astype(np.int32) + noise, -32768, 32767).astype(np.int16)
                bpos += step
                c.background_len = bpos
            await self.push_audio_frame(InputAudioRawFrame(chunk.tobytes(), models.HEARING_RATE, 1))
            if not turns and playing is None and not c.done.is_set():
                replied = c.voice_start and c.voice_start > c.your_end
                if (replied and c.voice_end < c.now() - 3) or self._unanswered():
                    c.done.set()
            tick += TICK
            await asyncio.sleep(tick - time.monotonic())


class ClockedOutput(BaseOutputTransport):
    """The voice's side: "plays" each chunk in real time and records it."""

    def __init__(self, params, call: Call):
        super().__init__(params)
        self._shared = call
        self._free_at = 0.0

    async def start(self, frame: StartFrame):
        await super().start(frame)
        await self.set_transport_ready(frame)

    async def write_audio_frame(self, frame: OutputAudioRawFrame) -> bool:
        c = self._shared
        samples = np.frombuffer(frame.audio, dtype=np.int16)
        start = max(c.now(), self._free_at)
        self._free_at = start + len(samples) / frame.sample_rate
        await asyncio.sleep(start - c.now())
        if np.abs(samples).max(initial=0) > LOUD:
            if not c.speaking and (c.voice_start is None or c.voice_start <= c.your_end):
                c.voice_start = start
                c.timings.append(f"voice started at {start:.1f} s, {start - c.your_end:.2f} s after you stopped")
            c.voice_end = self._free_at
            if c.interrupting is not None and start >= c.interrupting:
                c.went_on = self._free_at
            if c.speaking and c.talked_over is None:
                c.talked_over = start
            c.recording.append((start, samples))
        return True


class ScriptedTransport(BaseTransport):
    def __init__(self, turns, background, call: Call):
        super().__init__()
        params = TransportParams(audio_in_enabled=True, audio_out_enabled=True)
        self._input = ScriptedInput(params, turns, background, call)
        self._output = ClockedOutput(params, call)

    def input(self):
        return self._input

    def output(self):
        return self._output


def clip(spec: str) -> np.ndarray | None:
    """The int16 samples of "file.wav" or "file.wav:START-END", or None if spec isn't one."""
    if not (m := re.fullmatch(r"(.+\.wav)(?::([\d.]+)-([\d.]+))?", spec)):
        return None
    with wave.open(m[1]) as w:
        assert w.getframerate() == models.HEARING_RATE and w.getnchannels() == 1, f"{spec}: need 16 kHz mono"
        audio = np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16)
    if m[2]:
        audio = audio[int(float(m[2]) * models.HEARING_RATE) : int(float(m[3]) * models.HEARING_RATE)]
    return audio


async def sound(spec: str, voice: str) -> np.ndarray:
    """The int16 samples of a clip, or of text spoken by the Kokoro voice."""
    audio = clip(spec)
    if audio is not None:
        return audio
    parts = []
    for i, part in enumerate(re.split(r"\[([\d.]+)\]", spec)):
        if i % 2:
            parts.append(np.zeros(int(float(part) * models.SPEAKING_RATE), dtype=np.float32))
        elif part.strip():
            parts.append(await models.run(models.synthesize, part.strip(), voice))
    speech = soxr.resample(np.concatenate(parts), models.SPEAKING_RATE, models.HEARING_RATE)
    return (np.clip(speech, -1, 1) * 32767).astype(np.int16)


async def your_turn(turn: str) -> tuple[float | None, np.ndarray, float, float]:
    """When it's due, its audio, and where in it your speech starts and ends (s)."""
    at = None
    if turn.startswith("@"):
        head, turn = turn[1:].split(":", 1)
        at = float(head)
    audio = await sound(turn, YOUR_VOICE)
    loud = np.flatnonzero(np.abs(audio.astype(np.int32)) > LOUD)
    return at, audio, loud[0] / models.HEARING_RATE, (loud[-1] + 1) / models.HEARING_RATE


def save(recording, path: Path):
    end = max(t + len(s) / models.SPEAKING_RATE for t, s in recording)
    mix = np.zeros(int(end * models.SPEAKING_RATE) + 1, dtype=np.int32)
    for t, s in recording:
        i = int(t * models.SPEAKING_RATE)
        mix[i : i + len(s)] += s.astype(np.int32)
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1), w.setsampwidth(2), w.setframerate(models.SPEAKING_RATE)
        w.writeframes(np.clip(mix, -32768, 32767).astype(np.int16).tobytes())


def level(audio: np.ndarray) -> str:
    """The RMS level while there's sound: of the 20 ms frames above -60 dBFS, so pauses
    don't make one side seem quieter than it is."""
    step = int(models.HEARING_RATE * TICK)
    frames = audio[: len(audio) // step * step].astype(np.float64).reshape(-1, step) / 32768
    power = (frames**2).mean(axis=1)
    on = power > 10 ** (-60 / 10)
    return f"{10 * np.log10(power[on].mean()):.0f} dBFS" if on.any() else "silent"


async def run(turns: list[str], report: str | None, turn_wait: float, out: Path, args):
    turns = [await your_turn(t) for t in turns]
    background = None
    if args.background:
        b = (await sound(args.background, SOMEONE_ELSE)).astype(np.float64) * 10 ** (args.background_db / 20)
        background = np.clip(b, -32768, 32767).astype(np.int16)
        print(f"background {level(background)}, your turns {level(np.concatenate([t[1] for t in turns]))} (RMS while there's sound)")
    call = Call()

    def say(line: str):
        print(f"{datetime.now():%H:%M:%S.%f}"[:-3] + f"  {call.now():5.1f} s  {line}", flush=True)
        if line.startswith(("interrupted", "you (")) and not call.you_spoke():
            call.timings.append(f"NOT YOU at {call.now():.1f} s: {line}")

    task = call_task(ScriptedTransport(turns, background, call), report, say, turn_wait, args.sensitivity)
    runner = asyncio.create_task(PipelineRunner(handle_sigint=False).run(task))
    await asyncio.sleep(CONNECT)
    call.timings.append(f"audio connected at {call.now():.1f} s")
    await task.queue_frames([LLMRunFrame()])
    try:
        await asyncio.wait_for(call.done.wait(), LIMIT)
    except TimeoutError:
        print(f"\ngave up: the call didn't finish within {LIMIT} s")
    await task.cancel()
    await runner
    print("\nmeasured here:")
    for line in call.timings:
        print(f"  {line}")
    if background is not None:
        played = np.take(background, range(call.background_len), mode="wrap")
        call.recording.append((call.background_at, soxr.resample(played, models.HEARING_RATE, models.SPEAKING_RATE)))
    out.mkdir(parents=True, exist_ok=True)
    save(call.recording, out / "call.wav")
    print(f"\nboth sides: {out / 'call.wav'}")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("out", type=Path)
    p.add_argument("turns", nargs="+")
    p.add_argument("--report", type=Path)
    p.add_argument("--turn-wait", type=float, default=TURN_WAIT)
    p.add_argument("--sensitivity", type=float, default=hearing.SENSITIVITY, help="0 to 1, as in the app")
    p.add_argument("--background", help="text or CLIP played under the whole call")
    p.add_argument("--background-db", type=float, default=0.0, help="gain for the background")
    args = p.parse_args()
    models.load()
    asyncio.run(run(args.turns, args.report.read_text() if args.report else None, args.turn_wait, args.out, args))


if __name__ == "__main__":
    main()
