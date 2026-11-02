# Speech trial

A measured trial of Apple's on-device speech recognition (SpeechAnalyzer with
SpeechTranscriber, iOS 26 and later) on the phone, for hearing you without a speech node
("Speech on the phone" in `docs/design.md`). It was in the app's debug builds, behind a
button at the top of Recents, until its runs were done; the app's commit `a89043e` is the last
that has it. It answers four questions:
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

The phone needs the debug build of commit `a89043e` installed (`git switch --detach a89043e`,
then `docs/setup.md`, step 7), and to be unlocked and on the same Wi-Fi as the Mac for the
copies. `UDID` is the phone's (`xcrun devicectl list devices`), `BUNDLE` the app's bundle ID
(`RINGBACK_BUNDLE_ID` in `app/Config/Local.xcconfig`), and `DATA_DIR` the recordings folder of
`tools/stt-eval`.

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

## Results

Accuracy, 2026-11-02, the test iPhone (4 GB) on iOS 27, the English model installed through
the trial: all 99 utterances of the three recordings transcribed, none failed, each in a median
of 0.09 s (0.21–0.26 s at most) from its first audio to its final words. Scored with
`tools/stt-eval`, the share of words wrong:

| | free speech | scripted, AirPods | scripted, phone | all |
|---|---|---|---|---|
| Apple, SpeechTranscriber | 7.5% | 14.7% | 12.1% | 10.3% |
| Apple, repaired by Haiku | 7.5% | 10.8% | 7.3% | 8.2% |
| Parakeet v2 (the speech node's) | 5.2% | 7.8% | 5.6% | 5.9% |
| Parakeet v2, repaired by Haiku | 5.2% | 3.4% | 4.3% | 4.6% |

Apple's model gets about 1.75 times as many words wrong as Parakeet, 1.8 times with Haiku's
repair, mostly in the scripted sentences: technical words ("webrtc" as "rtc", "ffmpeg" as "ffm
pack", "build" as "bill", "30 seconds" as "a 32nd"), and answers to permission prompts
("deny that one" as "I add one", "allow it once" as "I love it once"). The free speech's
reference was corrected from one model's transcript, which favours that model there
(`tools/stt-eval`). Parakeet's times aren't comparable: its 41 ms is on an Apple silicon Mac,
and the speech node takes 70–90 ms.

Live, in a call, the first run (2026-11-02, 1.4 min, the phone locked at the start, AirPods):
- **In the background it hears.** The app was in the background for the whole call, the screen
  locked; all the audio kept arriving (iOS gave it in 0.1 s buffers, 10 a second, rather than
  47 in the foreground), and all 12 final results came then.
- **Locked, untried.** iOS's data protection was locked only from 2.5 s to 10.5 s, before you
  started talking: a glance at the lock screen unlocks it by Face ID while the screen stays
  locked. Audio arrived through it; no speech fell in it.
- **Final words 0.15 s after they're asked for** (median, 0.25 s at most, 9 turns): from the
  end of speech the trial found to the final words, as a call's end of turn would ask. With
  the 0.6 s of quiet the trial waits for, 0.76 s from your last loud audio.
- **Tentative words in bursts**, every 3.7 s while you speak, not word by word; so the first
  ones came 3.4 s after you started. A call acts on the final words, so this matters only for
  interrupting the voice, which the call's own voice detection does.
- **The app's memory** stayed at 27–32 MB: the model is outside it.
- **Words:** "Parakeet" as "para kit" and "pararait", "compute node" as "compute note", "a
  trial call" as "uh, trial, cool" in the final words (the tentative ones had it right).
- **Battery:** 85% to 85%, too short to tell.

The second run (1.1 min) stayed in the foreground, the phone never locked (face down doesn't
lock it), so the locked case is still untried. Its final words came 0.09 s after they were
asked for (median), and the tentative ones again in bursts every 3.8 s: so that's how the model
reports without `fastResults`, not the background. One 16 s "latency" was the trial's own: a
stop that brought no words was timed against the next sentence's final words; a new start of
speech now clears it.

The trial's code was `app/Ringback/SpeechTrial.swift` (in `a89043e`); its call went through the
app's own `CallService`, so it rang and showed as a call, named "Speech trial". The app's
on-device voice (`app/Ringback/PhoneSpeech.swift`) took its hearing from it.
