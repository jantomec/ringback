"""ffmpeg's arguments for a call's Opus, as hub/opus.go sets it up: 24 kbps, mono, 20 ms frames, VoIP,
FEC at 10% expected loss, and libopus's defaults for what the hub doesn't set (constrained VBR,
complexity 9). check_opus.py shows they give the hub's packets byte for byte."""
RATE = 24000  # speakingRate in hub/opus.go
OPUS = ["-c:a", "libopus", "-b:a", "24k", "-vbr", "constrained", "-application", "voip",
        "-frame_duration", "20", "-fec", "1", "-packet_loss", "10", "-compression_level", "9",
        "-ac", "1", "-ar", str(RATE)]
