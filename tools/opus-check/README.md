# Opus check

Checks the call's Opus offline, for the choices in "Data and weak signal" in
`docs/design.md`. Neither part needs the phone or a network; `tools/phone-check --signal`
measures whole calls.

## The Mac's side (`opus_check.py`)

Run from the repo root (it takes about 30 s); `OUT_DIR`, if given, gets each decoded clip
as a WAV file to listen to, so keep it outside the repo:

    uv run --project voice tools/opus-check/opus_check.py [OUT_DIR]

It encodes 10 s of Kokoro speech (`af_heart`) and 2 s of digital silence, as Pipecat hands
the voice to aiortc, and prints:
- which libopus options PyAV takes: those it doesn't know stay unused;
- for aiortc's encoder, the voice server's (`voice/ringback_voice/opus.py`), and mono at 24
  and 32 kbps, in Opus's full band or wideband, with forward error correction (FEC) set for
  0–20% loss: the kbps while speaking and in silence, Opus's mode and band, and how many
  packets carry FEC;
- the Opus lines of the voice server's answer to an offer with WebRTC's Opus parameters;
- the length and level of the voice's audio decoded by aiortc's decoder (FFmpeg, standing in
  for the phone's) and by the voice server's;
- what 5% and 15% of your packets lost at random become through aiortc's jitter buffer and
  decoder, aiortc's buffer with the new decoder, and the new buffer and decoder: how much of
  the 12 s comes through, its level, and how far it is from the audio with nothing lost
  (SNR, at the lag where they match best; Opus doesn't keep the waveform, so compare paths,
  not with any target);
- what aiortc's audio jitter buffer does with 400 packets in order and none, one or 5% lost.

Result on 2026-10-30, libopus 1.6.1 through PyAV 17.1.0 (the numbers move a little between
runs, as Kokoro's speech does):
- `dtx` stays unused, like a made-up option: PyAV's libopus can't send almost nothing in
  silence here. `fec`, `packet_loss` and `cutoff` are taken.
- aiortc's encoder: 80 kbps while speaking, 1.2 kbps in silence, CELT full band, no FEC.
  The voice server's (24 kbps, FEC for 10% loss): 21.4 and 3.2 kbps, SILK wideband (up to
  8 kHz), FEC in 382 of 500 packets with speech.
- At 24 kbps in its full band, Opus adds FEC only from 10% expected loss, and then drops to
  wideband; at 0–5% it stays in hybrid full band without FEC, and costs 10 kbps in digital
  silence. Limited to wideband, it adds FEC from 1%. At 32 kbps it stays in full band and
  adds FEC from 1%.
- The answer: `a=fmtp:96 useinbandfec=1;maxaveragebitrate=24000;maxplaybackrate=16000`,
  with `opus/48000/2` as before.
- The voice decodes to 11.98 s of the 12 s (11.90 s through aiortc's jitter buffer, which
  holds the last 4 packets) at -26.2 dB, against -25.9 dB for the original. aiortc's
  stereo encoder came out at -28.9 dB: its mono-to-stereo resampling makes the voice 3 dB
  quieter.
- 5% lost (36 of 599 packets, 25 with FEC in the next): aiortc's path gives 8.28 s of 12 s,
  SNR -2.5 dB; aiortc's buffer with the new decoder 11.68 s, 6.4 dB; the new path all 11.98
  s, 15.7–18.0 dB. 15% lost (80 packets, 40 with FEC in the next): 5.04 s, -2.3 dB; 11.58 s,
  -1.5 dB; 11.98 s, 11.6–12.1 dB.
- aiortc's jitter buffer holds 4 packets back. After one lost packet it holds 14 for the
  rest of the stream (0.28 s), having dropped the 4 good packets before the lost one; with
  5% lost (23 packets), it dropped 85 good ones.

## The phone's WebRTC (`webrtc_offer.swift`)

Runs Google's WebRTC, from the macOS build in the package the app uses, as the app makes
its offer, and prints the offer's audio lines. Then it applies an answer like the voice
server's, with the Opus parameters given, and prints what WebRTC's sender then uses for
Opus. Build the app first (the checks in `CLAUDE.md`), which fetches the package into
`app/build`, then from the repo root:

    W="$PWD/app/build/SourcePackages/artifacts/webrtc/WebRTC/WebRTC.xcframework/macos-x86_64_arm64"
    swiftc -F "$W" -framework WebRTC -Xlinker -rpath -Xlinker "$W" tools/opus-check/webrtc_offer.swift -o /tmp/webrtc-offer
    /tmp/webrtc-offer 'useinbandfec=1;maxaveragebitrate=24000;maxplaybackrate=16000'

Result on 2026-10-30, WebRTC 156.0.0 (stasel/WebRTC): the offer's Opus is
`a=fmtp:111 minptime=10;useinbandfec=1` (no `usedtx`, `stereo` or `maxaveragebitrate`); it
also offers RED (`red/48000/2`), which aiortc doesn't take. With the answer above, the
sender's Opus parameters are those three; with an answer without them, as aiortc's was,
none. In libwebrtc's `audio_encoder_opus.cc` these set the encoder: FEC only with
`useinbandfec=1`, the bitrate from `maxaveragebitrate` (else 32 kbps, or 20 kbps up to a
16 kHz `maxplaybackrate`), the band from `maxplaybackrate`, and the expected loss from the
loss the other end reports, smoothed and capped at 20%. How the phone's encoder then runs
isn't seen here: no audio flows.
