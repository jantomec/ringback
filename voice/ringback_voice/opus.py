"""The call's Opus on this side, set up for weak signal (see "Data and weak signal" in
docs/design.md). `install` changes aiortc's defaults at start, as only_address does aioice's:
- the voice goes out mono at 24 kbps, with Opus's forward error correction (FEC: a rougher
  copy of each packet in the next), set for 10% loss. aiortc sends 96 kbps stereo;
- the answer to the phone's offer asks the phone for 24 kbps, only the band speech
  recognition uses (it hears at 16 kHz), and FEC. Without useinbandfec=1 in the answer the
  phone sends none. Limited to that band, Opus adds FEC from 1% loss on; at 24 kbps in its
  full band, only from 10% (`tools/opus-check`);
- what you say goes through a jitter buffer that passes each packet on as soon as those before
  it have come, and gives up on a missing one once 3 later ones have. aiortc's held 4 packets
  back, and on a loss waited for 16 more, then dropped the 4 before the loss too: with 5% of
  packets lost, about 30% of the audio went missing;
- a lost packet is rebuilt from the next one's FEC, or else concealed, before speech
  recognition hears it. aiortc decodes with FFmpeg, which does neither: the gap is left out.

The decoder uses libopus directly, the copy PyAV ships, because FFmpeg can't decode FEC. If
it can't be loaded, aiortc's buffer and decoder stay.
"""

import ctypes
import ctypes.util
from pathlib import Path

import aiortc.codecs
import aiortc.jitterbuffer
import aiortc.rtcrtpreceiver
import av
import numpy as np
from aiortc.codecs.base import Decoder
from aiortc.codecs.opus import SAMPLE_RATE, SAMPLES_PER_FRAME, TIME_BASE, OpusEncoder
from aiortc.jitterbuffer import JitterFrame
from aiortc.utils import uint16_add

BITRATE = 24000
EXPECTED_LOSS = 10  # percent: FEC needs some, and at 5% Opus left it out at 24 kbps
# What the phone's encoder is asked for, in the answer: Google's WebRTC sends mono at 32 kbps
# in Opus's full band and without FEC unless the answer says otherwise.
PHONE = {"useinbandfec": 1, "maxaveragebitrate": BITRATE, "maxplaybackrate": 16000}
WAIT = 3  # later packets after which a missing one is given up on
CONCEAL = SAMPLE_RATE  # longest gap made up, in samples (1 s); a longer one is left out


class VoiceEncoder(OpusEncoder):
    """The voice as Opus: mono at 24 kbps, with FEC."""

    def __init__(self):
        self.codec = av.CodecContext.create("libopus", "w")
        self.codec.bit_rate = BITRATE
        self.codec.format = "s16"
        self.codec.layout = "mono"
        self.codec.options = {"application": "voip", "fec": "1", "packet_loss": str(EXPECTED_LOSS)}
        self.codec.sample_rate = SAMPLE_RATE
        self.codec.time_base = TIME_BASE
        self.resampler = av.AudioResampler(format="s16", layout="mono", rate=SAMPLE_RATE, frame_size=SAMPLES_PER_FRAME)
        self.first_packet_pts = None


class Packets(JitterFrame):
    """Packets the jitter buffer releases together, in order. The receiver maps the first one's
    timestamp; the others keep their distance from it."""

    def __init__(self, packets):
        super().__init__(data=packets[0]._data, timestamp=packets[0].timestamp)
        self.packets = [(p._data, (p.timestamp - packets[0].timestamp) % (1 << 32)) for p in packets]


class JitterBuffer:
    """aiortc's audio jitter buffer, for a listener rather than a speaker: speech recognition
    takes audio as it comes, so nothing is held back but for a missing packet, and that only
    until WAIT later ones have come. A packet that comes after that is dropped; its FEC came
    in the next one."""

    def __init__(self):
        self._next: int | None = None  # the sequence number to pass on next
        self._held: dict[int, object] = {}

    def add(self, packet) -> tuple[bool, Packets | None]:
        seq = packet.sequence_number
        if self._next is None:
            self._next = seq
        ahead = uint16_add(seq, -self._next)
        if ahead >= 1 << 15:
            if (1 << 16) - ahead <= 100:
                return False, None  # late, or a repeat
            self._held.clear()  # far behind: the stream started over
            self._next = seq
        self._held[seq] = packet
        out = []
        while self._held:
            if self._next in self._held:
                out.append(self._held.pop(self._next))
                self._next = uint16_add(self._next, 1)
            elif len(self._held) >= WAIT:
                self._next = min(self._held, key=lambda s: uint16_add(s, -self._next))
            else:
                break
        return False, Packets(out) if out else None


def _jitter_buffer(capacity: int, prefetch: int = 0, is_video: bool = False):
    """aiortc's receiver makes its jitter buffer through this: video keeps aiortc's."""
    return aiortc.jitterbuffer.JitterBuffer(capacity, prefetch, is_video) if is_video else JitterBuffer()


def _libopus() -> ctypes.CDLL | None:
    """libopus as PyAV ships it on macOS, or else as installed; None if neither loads."""
    for path in (Path(av.__file__).parent / ".dylibs" / "libopus.0.dylib", ctypes.util.find_library("opus")):
        try:
            lib = ctypes.CDLL(str(path)) if path else None
        except OSError:
            continue
        if lib:
            lib.opus_decoder_create.restype = ctypes.c_void_p
            lib.opus_decoder_create.argtypes = [ctypes.c_int32, ctypes.c_int, ctypes.POINTER(ctypes.c_int)]
            lib.opus_decoder_destroy.argtypes = [ctypes.c_void_p]
            lib.opus_decode.argtypes = [
                ctypes.c_void_p, ctypes.c_char_p, ctypes.c_int32, ctypes.c_void_p, ctypes.c_int, ctypes.c_int
            ]  # fmt: skip
            lib.opus_packet_get_nb_samples.argtypes = [ctypes.c_char_p, ctypes.c_int32, ctypes.c_int32]
            return lib
    return None


_lib = _libopus()


class OpusDecoder(Decoder):
    """Decodes what you say, to mono, and makes up for lost packets: a gap in the timestamps
    is filled from the next packet's FEC, and where that has none, or for all but the last of
    several lost packets, by Opus's concealment."""

    def __init__(self):
        error = ctypes.c_int()
        self._decoder = _lib.opus_decoder_create(SAMPLE_RATE, 1, ctypes.byref(error))
        if error.value:
            raise RuntimeError(f"opus_decoder_create failed: {error.value}")
        self._next: int | None = None  # the timestamp the next packet should have

    def __del__(self):
        if _lib and getattr(self, "_decoder", None):
            _lib.opus_decoder_destroy(self._decoder)

    def decode(self, encoded_frame: JitterFrame) -> list[av.AudioFrame]:
        packets = getattr(encoded_frame, "packets", [(encoded_frame.data, 0)])
        frames = []
        for data, offset in packets:
            frames += self._packet(data, encoded_frame.timestamp + offset)
        return frames

    def _packet(self, data: bytes, timestamp: int) -> list[av.AudioFrame]:
        samples = _lib.opus_packet_get_nb_samples(data, len(data), SAMPLE_RATE)
        if samples <= 0:
            return []
        frames = []
        lost = timestamp - self._next if self._next is not None else 0
        lost -= lost % 120  # Opus makes up whole 2.5 ms
        if 0 < lost <= CONCEAL:
            frames.append(self._run(data, lost, self._next, fec=1))
        frames.append(self._run(data, samples, timestamp, fec=0))
        self._next = timestamp + samples
        return [f for f in frames if f]

    def _run(self, data: bytes, samples: int, timestamp: int, fec: int) -> av.AudioFrame | None:
        pcm = np.zeros(samples, dtype=np.int16)
        done = _lib.opus_decode(self._decoder, data, len(data), pcm.ctypes.data, samples, fec)
        if done <= 0:
            return None
        frame = av.AudioFrame.from_ndarray(pcm[:done].reshape(1, -1), format="s16", layout="mono")
        frame.sample_rate, frame.pts, frame.time_base = SAMPLE_RATE, timestamp, TIME_BASE
        return frame


def install() -> str:
    """Sets up aiortc as above, and says how, for the log."""
    opus = next(c for c in aiortc.codecs.CODECS["audio"] if c.mimeType.lower() == "audio/opus")
    opus.parameters = dict(PHONE)
    aiortc.codecs.OpusEncoder = VoiceEncoder
    done = f"Opus: the voice mono at {BITRATE // 1000} kbps with FEC, the phone asked for {PHONE}"
    if _lib is None:
        return done + "; libopus didn't load, so your lost packets are left out, as aiortc does"
    aiortc.codecs.OpusDecoder = OpusDecoder
    aiortc.rtcrtpreceiver.JitterBuffer = _jitter_buffer
    return done + "; your lost packets rebuilt from FEC or concealed"
