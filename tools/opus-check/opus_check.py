#!/usr/bin/env python3
"""Checks the call's Opus on the Mac's side offline, without WebRTC or the phone: what
PyAV's libopus takes, what each encoder setup sends, that the voice's audio decodes to its
length and level, and what lost packets of yours become through aiortc's receive path and
through the voice server's (voice/ringback_voice/opus.py).

    uv run --project voice tools/opus-check/opus_check.py [OUT_DIR]

OUT_DIR, if given, gets each decoded clip as a WAV file, to listen to.
"""

import asyncio
import ctypes
import fractions
import random
import sys
import wave
from pathlib import Path

import av
import numpy as np
import soxr
from aiortc import RTCPeerConnection, RTCSessionDescription
from aiortc.codecs import opus as stock
from aiortc.jitterbuffer import JitterBuffer as StockBuffer
from aiortc.rtp import RtpPacket

from ringback_voice import models, opus

TEXT = (
    "I fixed the crash in the config parser. It choked on empty lines between sections. "
    "The fix is two lines, with a new test, and the whole suite passes."
)
# The packet's first byte says Opus's mode and band.
MODES = (["SILK NB"] * 4 + ["SILK MB"] * 4 + ["SILK WB"] * 4 + ["hybrid SWB"] * 2 + ["hybrid FB"] * 2
         + ["CELT NB"] * 4 + ["CELT WB"] * 4 + ["CELT SWB"] * 4 + ["CELT FB"] * 4)  # fmt: skip


def frames(pcm: np.ndarray):
    """The voice's audio as Pipecat hands it to aiortc: 20 ms of mono at 24 kHz at a time."""
    for i in range(0, len(pcm) - 480 + 1, 480):
        f = av.AudioFrame.from_ndarray(pcm[i : i + 480].reshape(1, -1), format="s16", layout="mono")
        f.sample_rate, f.pts, f.time_base = 24000, i, fractions.Fraction(1, 24000)
        yield f


def encoder(layout: str, bitrate: int, options: dict) -> stock.OpusEncoder:
    """aiortc's encoder with its codec set up otherwise."""
    e = stock.OpusEncoder()
    e.codec = av.CodecContext.create("libopus", "w")
    e.codec.bit_rate, e.codec.format, e.codec.layout = bitrate, "s16", layout
    e.codec.sample_rate, e.codec.time_base = 48000, fractions.Fraction(1, 48000)
    e.codec.options = options
    e.resampler = av.AudioResampler(format="s16", layout=layout, rate=48000, frame_size=960)
    return e


def encode(e, pcm: np.ndarray) -> list[tuple[bytes, int]]:
    """Each packet and its RTP timestamp, as aiortc's sender makes them."""
    out = []
    for f in frames(pcm):
        payloads, ts = e.encode(f)
        out += [(p, ts) for p in payloads]
    return out


def receive(packets, buffer, decoder, lost: set[int]) -> np.ndarray:
    """Through a jitter buffer and a decoder, as aiortc's receiver does: mono, at 48 kHz."""
    out = [np.zeros(0, dtype=np.float32)]
    for seq, (payload, ts) in enumerate(packets):
        if seq in lost:
            continue
        p = RtpPacket(payload_type=111, sequence_number=seq, timestamp=ts)
        p._data = payload
        _, frame = buffer.add(p)
        for f in decoder.decode(frame) if frame is not None else []:
            a = f.to_ndarray().astype(np.float32).reshape(-1) / 32768
            out.append(a.reshape(-1, 2).mean(axis=1) if f.layout.name == "stereo" else a)
    return np.concatenate(out)


def level(x: np.ndarray) -> float:
    return 20 * np.log10(np.sqrt(np.mean(x**2)) + 1e-12)


def snr(reference: np.ndarray, x: np.ndarray) -> float:
    """dB of reference over what x differs from it, at the lag (under 20 ms) where they match best."""
    n = min(len(reference), len(x)) - 960
    lag = max(range(960), key=lambda k: float(np.dot(reference[:n], x[k : k + n])))
    return 10 * np.log10(np.sum(reference[:n] ** 2) / (np.sum((reference[:n] - x[lag : lag + n]) ** 2) + 1e-12))


def save(out: Path | None, name: str, x: np.ndarray) -> None:
    if out:
        with wave.open(str(out / f"{name}.wav"), "wb") as w:
            w.setnchannels(1)
            w.setsampwidth(2)
            w.setframerate(48000)
            w.writeframes((np.clip(x, -1, 1) * 32767).astype(np.int16).tobytes())


async def answer() -> str:
    """The voice server's Opus lines in its answer to an offer with WebRTC's Opus parameters,
    once it has set up aiortc as at its start. The encoders and decoders above are made
    directly, so that doesn't change them."""
    print(opus.install())
    phone = RTCPeerConnection()
    phone.addTransceiver("audio")
    await phone.setLocalDescription(await phone.createOffer())
    offer = phone.localDescription.sdp.replace("a=rtpmap:96 opus/48000/2", "a=rtpmap:96 opus/48000/2\r\na=fmtp:96 minptime=10;useinbandfec=1")
    mac = RTCPeerConnection()
    await mac.setRemoteDescription(RTCSessionDescription(sdp=offer, type="offer"))
    await mac.setLocalDescription(await mac.createAnswer())
    await phone.close()
    await mac.close()
    return " ".join(line for line in mac.localDescription.sdp.splitlines() if line.startswith(("a=rtpmap:96", "a=fmtp:96")))


def main() -> None:
    out = Path(sys.argv[1]) if len(sys.argv) > 1 else None
    if out:
        out.mkdir(parents=True, exist_ok=True)
    lib = opus._lib
    lib.opus_get_version_string.restype = ctypes.c_char_p
    print(f"{lib.opus_get_version_string().decode()}, through PyAV {av.__version__}")

    from mlx_audio.tts.utils import load_model

    kokoro = load_model(models.KOKORO)
    speech = np.concatenate([np.array(r.audio, dtype=np.float32) for r in kokoro.generate(TEXT, voice="af_heart", lang_code="a")])
    pcm = np.concatenate([(np.clip(speech, -1, 1) * 32767).astype(np.int16), np.zeros(48000, dtype=np.int16)])
    talking = int(len(speech) / 24000 / 0.02)  # packets with speech; then 2 s of digital silence
    print(f"speech: Kokoro af_heart, {len(speech) / 24000:.1f} s, then 2 s of silence\n")

    print("libopus options PyAV leaves unused (it doesn't know them):")
    for option in ("fec", "packet_loss", "cutoff", "dtx", "no_such_option"):
        e = encoder("mono", 24000, {"application": "voip", option: "8000" if option == "cutoff" else "1"})
        encode(e, pcm[:4800])
        print(f"  {option}: {'unused' if option in e.codec.options else 'taken'}")

    print("\nwhat each encoder setup sends (kbps of Opus while speaking, in silence; mode; packets with FEC):")
    setups = [("aiortc's", "stereo", 96000, {"application": "voip"}), ("the voice's", None, None, None)]
    for bitrate in (24000, 32000):
        for band in ("full", "wide"):
            for loss in (0, 1, 5, 10, 20):
                options = {"application": "voip"} | ({"cutoff": "8000"} if band == "wide" else {})
                options |= {"fec": "1", "packet_loss": str(loss)} if loss else {}
                setups.append((f"{bitrate // 1000} kbps, {band} band, FEC for {loss:2}% loss", "mono", bitrate, options))
    for name, layout, bitrate, options in setups:
        packets = encode(opus.VoiceEncoder() if layout is None else encoder(layout, bitrate, options), pcm)
        sizes = np.array([len(p) for p, _ in packets])
        modes = sorted({MODES[p[0] >> 3] for p, _ in packets[:talking]})
        fec = sum(lib.opus_packet_has_lbrr(p, len(p)) == 1 for p, _ in packets[:talking])
        print(f"  {name}: {sizes[:talking].mean() * 0.4:.1f}, {sizes[talking + 10 :].mean() * 0.4:.1f}; "
              f"{', '.join(modes)}; {fec} of {talking}")  # fmt: skip

    print(f"\nthe answer's Opus: {asyncio.run(answer())}")

    print("\nthe voice's audio decoded (length; level, against the original's):")
    original = soxr.resample(pcm.astype(np.float32) / 32768, 24000, 48000)
    print(f"  original: {len(original) / 48000:.3f} s; {level(original[: len(speech) * 2]):.1f} dB")
    for name, e in (("aiortc's encoder", stock.OpusEncoder()), ("the voice's encoder", opus.VoiceEncoder())):
        packets = encode(e, pcm)
        for dname, buffer, decoder in (
            ("aiortc's decoder, as the phone's stand-in", StockBuffer(capacity=16, prefetch=4), stock.OpusDecoder()),
            ("the voice server's", opus.JitterBuffer(), opus.OpusDecoder()),
        ):
            x = receive(packets, buffer, decoder, set())
            print(f"  {name}, {dname}: {len(x) / 48000:.3f} s; {level(x[: len(speech) * 2]):.1f} dB")
            save(out, f"{name.split()[0][:-2] if name.startswith('aiortc') else 'voice'}-{dname.split()[0].strip(chr(39) + 's,')}", x)

    print("\nwhat you say with packets lost, the phone's encoder as phone-check plays it (audio out of 12 s;"
          " level; SNR against no loss):")  # fmt: skip
    for loss in (5, 15):
        packets = encode(encoder("mono", 24000, {"application": "voip", "cutoff": "8000", "fec": "1", "packet_loss": str(loss)}), pcm)
        clean = receive(packets, opus.JitterBuffer(), opus.OpusDecoder(), set())
        dice = random.Random(loss)
        lost = {i for i in range(len(packets)) if dice.random() < loss / 100}
        fec = sum(lib.opus_packet_has_lbrr(packets[i + 1][0], len(packets[i + 1][0])) == 1
                  for i in lost if i + 1 < len(packets) and i + 1 not in lost)  # fmt: skip
        print(f"  {loss}% lost, {len(lost)} of {len(packets)} packets, {fec} with FEC in the next one:")
        for path, buffer, decoder in (
            ("aiortc's buffer and decoder", StockBuffer(capacity=16, prefetch=4), stock.OpusDecoder()),
            ("aiortc's buffer, the new decoder", StockBuffer(capacity=16, prefetch=4), opus.OpusDecoder()),
            ("the new buffer and decoder", opus.JitterBuffer(), opus.OpusDecoder()),
        ):
            x = receive(packets, buffer, decoder, lost)
            print(f"    {path}: {len(x) / 48000:.2f} s; {level(x[: len(speech) * 2]):.1f} dB "
                  f"({level(clean[: len(speech) * 2]):.1f}); {snr(clean, x):.1f} dB")  # fmt: skip
            save(out, f"lost{loss}-{path.replace(' ', '-').replace(',', '').replace(chr(39), '')}", x)

    print("\naiortc's audio jitter buffer, 400 packets in order (packets held back at the end; good ones dropped):")
    for name, lost in (("none lost", set()), ("one lost", {100}), ("5% lost", {i for i in range(400) if random.Random(i).random() < 0.05})):
        buffer, released, held = StockBuffer(capacity=16, prefetch=4), [], 0
        for seq in (s for s in range(400) if s not in lost):
            p = RtpPacket(payload_type=111, sequence_number=seq, timestamp=seq * 960)
            p._data = b"x"
            _, frame = buffer.add(p)
            if frame:
                released.append(frame.timestamp // 960)
                held = seq - released[-1]
        dropped = [s for s in range(released[-1]) if s not in lost and s not in released]
        print(f"  {name} ({len(lost)}): {held} held back; {len(dropped)} good ones dropped")


if __name__ == "__main__":
    main()
