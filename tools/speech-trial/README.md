# Speech trial

A measured trial of Apple's on-device speech recognition (SpeechAnalyzer with
SpeechTranscriber, iOS 26 and later) on the phone, for hearing you without a speech node
("Speech on the phone" in `docs/design.md`). It's in the app's debug builds: the waveform
button at the top of Recents opens it. It answers four questions:
- **Does it hear on a locked phone during a call?** Nothing in Apple's documentation says either
  way.
- **How soon do your words come** after you stop speaking?
- **How accurate is it** on your voice, next to Parakeet on the same recordings
  (`tools/stt-eval`)?
- **What does it cost** the app's memory, and the battery?

What Apple's documentation says, checked on 2026-11-02 (sources in the design):
- the model runs outside the app's memory, in a system service, which installs it once
  (`AssetInventory`);
- results come tentative and fast (`volatileResults`), then final;
- it reports no end of speech: its `SpeechDetector` only saves power. So the trial finds the
  end itself, from the level of your voice, and asks for the final words through there
  (`finalize(through:)`), as a call would with Silero.

## Running it

The phone needs the debug build installed (`docs/setup.md`, step 6), and to be unlocked and
on the same Wi-Fi as the Mac for the copies. `UDID` is the phone's (`xcrun devicectl list
devices`), `BUNDLE` the app's bundle ID (`RINGBACK_BUNDLE_ID` in `app/Config/Local.xcconfig`),
and `DATA_DIR` the recordings folder of `tools/stt-eval`.

1. **The model.** In the trial, tap **Install English**; the model line then says "installed".
2. **Accuracy.** Copy the recordings and their segments to the app:

       for d in wav segments; do
         xcrun devicectl device copy to --device UDID --domain-type appDataContainer \
           --domain-identifier BUNDLE --source DATA_DIR/$d --destination Documents/stt/$d
       done

   Tap **Transcribe Recordings**. Each utterance is transcribed on its own, as on a call, and
   the results go to `Documents/stt/hyp/apple-speech/`, in `tools/stt-eval`'s format. Copy them
   back and score them beside the other models:

       xcrun devicectl device copy from --device UDID --domain-type appDataContainer \
         --domain-identifier BUNDLE --source Documents/stt/hyp/apple-speech \
         --destination DATA_DIR/hyp/apple-speech
       uv run --with jiwer==4.0.0 --with whisper-normalizer==0.1.15 tools/stt-eval/score.py DATA_DIR

3. **Live, in a call.** Tap **Start Trial Call**. It's a call through CallKit, with the call's
   audio settings and echo cancellation, but no other end: the app hears the microphone
   itself. Lock the phone, put your headphones in, and talk for a few minutes, a sentence at a
   time, with a pause after each. Unlock and end the call. Each call's log is
   `Documents/trial/live-<time>.jsonl`. Copy it back, and summarise it:

       xcrun devicectl device copy from --device UDID --domain-type appDataContainer \
         --domain-identifier BUNDLE --source Documents/trial --destination OUT_DIR
       python3 tools/speech-trial/summary.py OUT_DIR/live-<time>.jsonl

   The summary says whether audio and final results kept coming while the phone was locked,
   how soon the final words came after you stopped (from your last loud audio, so including the
   0.6 s of quiet the trial waits for), how soon the first tentative words came after you
   started, the app's memory, and the battery per hour.

The trial's code is `app/Ringback/SpeechTrial.swift`; the call goes through the app's own
`CallService`, so it rings and shows as a call, named "Speech trial".
