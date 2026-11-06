#!/usr/bin/env python3
"""Checks that blind.py's ffmpeg settings give what the hub's encoder gives. It encodes a WAV (24 kHz,
16-bit, mono) with libopus directly, making the same calls as hub/opus.go, then with blind.py's
ffmpeg arguments, and compares the packets byte for byte.

    python3 check_opus.py IN.wav
"""
import ctypes
import pathlib
import subprocess
import sys
import tempfile
import wave

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from opus_args import OPUS  # noqa: E402

APPLICATION_VOIP, SET_BITRATE, SET_INBAND_FEC, SET_PACKET_LOSS = 2048, 4002, 4012, 4014
lib = ctypes.CDLL("libopus.so.0")
lib.opus_encoder_create.restype = ctypes.c_void_p


def hub_packets(pcm):
    """newOpusEncoder and encode in hub/opus.go: 480 samples at a time, the last padded with zeros."""
    err = ctypes.c_int()
    e = ctypes.c_void_p(lib.opus_encoder_create(24000, 1, APPLICATION_VOIP, ctypes.byref(err)))
    for request, value in ((SET_BITRATE, 24000), (SET_INBAND_FEC, 1), (SET_PACKET_LOSS, 10)):
        assert lib.opus_encoder_ctl(e, request, ctypes.c_int(value)) == 0
    out, buffer, step = [], ctypes.create_string_buffer(1500), 960
    for i in range(0, len(pcm), step):
        frame = pcm[i: i + step].ljust(step, b"\0")
        n = lib.opus_encode(e, frame, 480, buffer, 1500)
        assert n > 0
        out.append(buffer.raw[:n])
    return out


def ogg_packets(path):
    data, i, packets, partial = path.read_bytes(), 0, [], b""
    while i < len(data):
        assert data[i:i + 4] == b"OggS"
        count = data[i + 26]
        lacing = data[i + 27: i + 27 + count]
        i += 27 + count
        for size in lacing:
            partial += data[i: i + size]
            i += size
            if size < 255:
                packets.append(partial)
                partial = b""
    return packets[2:]  # past OpusHead and OpusTags


pcm = wave.open(sys.argv[1]).readframes(10 ** 9)
direct = hub_packets(pcm)
with tempfile.TemporaryDirectory() as t:
    ogg = pathlib.Path(t) / "out.ogg"
    subprocess.run(["ffmpeg", "-y", "-v", "error", "-i", sys.argv[1], *OPUS, str(ogg)], check=True)
    via = ogg_packets(ogg)
n = min(len(direct), len(via))
same = sum(direct[i] == via[i] for i in range(n))
print(f"hub's calls: {len(direct)} packets, {sum(map(len, direct)) * 8 / len(direct) / 0.02 / 1000:.1f} kbps; "
      f"ffmpeg: {len(via)} packets, {sum(map(len, via)) * 8 / len(via) / 0.02 / 1000:.1f} kbps; "
      f"{same} of {n} packets identical")
