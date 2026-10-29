# ringback — design

## Goal

Your Claude Code sessions call your iPhone when they finish or need you. Each one tells
you what it did, and you talk it through hands-free, with the phone locked in your pocket
and on mobile data.

## Requirements

1. **Conversation.** Real back-and-forth in one call. You can interrupt Claude
   mid-sentence. It stops talking the moment it hears you and waits for you to finish,
   and it answers everything you say.
2. **One call per session.** The phone shows the call as "Claude from \<project\>", so the
   voice doesn't repeat it. It opens with what's new, then what needs your attention.
   Your replies, including permission approvals, go to that session.
3. **Not now.** You can decline or leave unanswered any call, including one that comes
   in as call waiting during another call. The switchboard then tries again 5 min,
   20 min, 1 h, 2½ h and 6 h after the first ring, and holds every other call
   meanwhile. If the 6-hour try isn't answered either, the call is marked declined and
   all calls stay held until you call that session back from the app. Sessions' own
   calls never overlap: they wait in one queue.
4. **Understanding you.** Recognition copes with non-native accents. When a reply is
   unclear, the call asks instead of guessing, and a permission is only granted on a
   clear yes.

## Architecture

```
 Mac running the session          always-on Mac              iPhone
 sessions ──hooks─────────────▶ switchboard ──VoIP push───▶ app rings (CallKit)
    ▲                           (queue, retries) via APNs        │
    └─channel── voice pipeline ◀───── call audio over Nebula ────┘
                (STT, TTS, Haiku)
```

Sessions can run on several Macs. All of them share one switchboard, so there is one
queue, and each call's audio goes to the Mac whose session is calling.

1. **Hooks** (in each session). A Stop hook, plus a Notification hook for
   `permission_prompt`/`idle_prompt`, sends `session_id`, `cwd`,
   `last_assistant_message` and `transcript_path` to the switchboard over Nebula and
   returns at once.
2. **Channel** (a custom MCP channel server in each session):
   - delivers your replies into its session (on the next turn if the session is busy);
   - relays permission prompts (`experimental['claude/channel/permission']`, needs
     Claude Code ≥ 2.1.234).

   Channels are a research preview, so sessions start with
   `--dangerously-load-development-channels server:<name>`.
   Docs: https://code.claude.com/docs/en/channels-reference.md
3. **Switchboard** (a daemon on one always-on Mac on the Nebula network, such as the
   lighthouse):
   - keeps one FIFO queue for the sessions on every Mac and places one call at a time;
   - starts a call with a VoIP push through APNs that names the Mac taking the call;
   - on "not now" (declined or unanswered), retries on the schedule in requirement 3,
     with the whole queue paused in between. After a final decline it holds all calls
     until the app calls that session back;
   - every 30 min, wakes the app with a background push so it checks that calls can still
     reach the phone (the connection check in the iPhone app);
   - is one Swift binary of about 470 KB that needs nothing but macOS 13 or later (Intel
     or Apple silicon), so an old Mac can run it. On an old Intel Mac it used about
     4 MB of memory. It handles requests one at a time, so a stalled connection holds up
     the next request for up to 5 s, which is within the app's 10 s timeout.
4. **Voice pipeline** (on the Mac running the calling session). That Mac must be awake
   anyway, and this keeps the always-on Mac light. It holds a sleep assertion while its
   call is queued or in progress.
   - Pipecat, with Parakeet TDT 0.6B v2 (MLX) for speech-to-text, Kokoro-82M (MLX, through
     mlx-audio) with the `af_heart` voice for speech, and Claude Haiku 5.5. Pipecat has no
     Parakeet service, and its own Kokoro service is too slow (see rejected
     alternatives), so we write a small service for each.
   - The phone posts its WebRTC offer to `/offer` on the voice server (`voice/`), which
     answers it with Pipecat's SmallWebRTC connection.
   - **Speaking a reply.** Haiku streams its reply, and Kokoro speaks it in pieces:
     1. As soon as the first sentence is complete, it's spoken on its own, so the voice
        starts fast.
     2. One second before the current piece finishes playing, everything Haiku has
        completed since, up to the last finished sentence, becomes the next piece. If no
        sentence has finished yet, the next piece waits for the first one that does.
     3. This repeats until the reply ends.

     Larger pieces keep intonation and pauses natural. Pieces play exactly as Kokoro
     generates them: trimming the silence between pieces sounded worse in the listening
     test.

     In that test, speaking each sentence separately left about 0.75 s of silence
     between sentences, against 0.24–0.5 s in one continuous passage.

     The one-second lead holds because Kokoro through MLX generates speech about 27×
     faster than real time on a recent Apple silicon Mac (`tools/tts-speed`): 0.14 s for
     one sentence (about 4 s of speech) and 0.77 s for a 22 s reply. So it covers pieces
     of up to about 25 s of speech. Inside the voice, pieces of 1–24 s took 0.07–0.9 s.
     In five scripted calls (`tools/call-check`), no silence inside a reply was longer
     than the 0.6–0.8 s that Kokoro leaves where one piece ends and the next begins.
     Parakeet and Kokoro share one thread, so they never run at the same time.

     Kokoro's text-to-phoneme package (`misaki[en]`) pulls in torch through spaCy, though
     Kokoro doesn't use it. With it, the voice's environment takes about 1.6 GB of disk,
     against 0.5 GB before.
   - **Interruptions.** Playback stops as soon as you speak, and only for your voice.
     Everything queued is dropped. The voice then waits until you've finished and always
     answers what you said, including a short "yeah" or "mhm".

     Only your voice should reach the detector that stops it:
     1. on the phone, voice processing and the Voice Isolation mic mode remove background
        noise (you switch Voice Isolation on once, in Control Center). With AirPods or the
        phone at your ear, other people are also much farther from the microphone than
        you (how much quieter they arrive isn't measured);
     2. voice-activity detection then triggers the stop. Two settings in the app set its
        thresholds: the interruption sensitivity sets how sure and how long speech must be,
        and the background filter sets a volume threshold. That threshold is a level gate
        that ignores speech quieter than you sound in the microphone. Other people farther
        away arrive quieter, so on the Mac this gate keeps them out, not a model. Its
        default is a rough estimate that kept out a voice 20 dB below yours offline
        (Stage 2). It's absolute, so it depends on how loud a microphone delivers your
        voice. If that proves fragile, the next step is a gate set relative to your own
        level on the call.

     Removing other people's voices with a model as well (VoxLock) was built and is
     parked; see rejected alternatives.

     If a stop happens anyway and no words are recognised, the voice carries on, starting
     again from the sentence it was in.

     Haiku's session already holds the whole reply it wrote. So the next message tells it
     where it was cut off, giving the last words you actually heard.
   - Haiku runs as a Claude Code session on the Claude subscription, with no API key.
     - It starts while the phone rings and stays open until hang-up (`claude -p
       --input-format stream-json --output-format stream-json`), so it holds the whole
       call.
     - It can start as a fork of the calling session (`--resume <id> --fork-session`) to
       get that session's full context.
     - Pipecat has no Claude Code service, so we write one around that process.
     - Measured (`tools/reply-speed`): the first words come about 1.2 s after each of
       your turns (0.9–3.2 s), and 1.0–3.2 s on the first turn, which includes start-up.
       Neither lower effort nor turning thinking off made a clear difference.
   - The voice is a fast front end that holds the session's report as context, because a
     Claude Code turn is too slow for live conversation.
   - It delivers your replies to its session's channel. It forwards questions it can't
     answer to the session and waits for the reply.
   - Speech-to-text was chosen by measuring on accented English with `tools/stt-eval`
     (464 scripted words plus 519 of free speech):
     - Parakeet v2 got about 6% of words wrong at about 40 ms per utterance.
     - Whisper large-v3-turbo was about as accurate, but took about 0.5 s and invented
       words on short clips.
     - Letting Haiku repair the transcript from context cut Parakeet's errors on the
       scripted commands from 31 to 18, but changed nothing in the free speech.
5. **Conversation rules**, in the voice's instructions and in a skill for the session:
   - spoken style: short sentences, rounded numbers, no tables, lists or code read
     aloud; offer detail instead of reciting it; stop at once when interrupted;
   - your words come from speech recognition, so expect similar-sounding wrong words,
     especially technical terms. Read them in context, and ask when a reply is too
     garbled to be sure;
   - act on a permission prompt only after a clear yes or no, otherwise ask again;
   - the session writes its call briefing (project, what's new, what needs you) in a
     form that can be spoken, and answers forwarded questions the same way.
6. **iPhone app** (Swift):
   - a PushKit VoIP push triggers CallKit, which rings on the lock screen and in your
     headphones;
   - when you answer, the app opens the audio connection to the voice server named in the
     push (`voice` in its payload), over Nebula. The Mac can't open it, because on mobile
     data only the phone can start a Nebula tunnel (see Stage 0).
     - The audio is WebRTC, using Google's WebRTC library directly. Pipecat's iOS client
       would take over the audio session.
     - WebRTC runs in manual-audio mode: CallKit activates the voice-processing audio
       session, which provides echo cancellation, and hands it to WebRTC.
     - The offer carries all ICE candidates at once, because the Mac's server doesn't
       take them one by one;
   - During another call (phone, WhatsApp, FaceTime), a ringback call rings as call
     waiting, with iOS's own Hold & Accept, End & Accept and Decline. The app doesn't
     end it by itself. `CXCallObserver` only records that you were on another call.
     (Apps can't add buttons to that screen.)
   - It reports each call's events to the switchboard: ringing, answered, ended (with
     its duration), declined and missed. Each report is stamped with when it
     happened on the phone, and retried for about a minute. After the phone changes
     network, Nebula can take a while to reach the Mac again. The "ringing" report also
     brings the tunnel up while the phone rings.
   - **Call screens.** iOS decides which screens you see, the same for every calling app:
     - On a locked phone: slide to answer, then iOS's own call screen.
     - On an unlocked phone: Accept and Decline, then the app's own call screen.

     Ours copies iOS's style (timer above the name, round glass buttons) with only Audio
     (iOS's route picker), End and Mute. End and Mute go through CallKit, so iOS and
     AirPods stay in step.
   - **If the app is closed during a call** (killed or crashed), the call is recorded as
     ended at the last heartbeat, which is saved every 2 s, and reported to the
     switchboard at the next launch.
   - **Microphone access** is asked for when you open the app, because a locked phone in a
     call can't show the prompt. While access is off, a banner links to Ringback's page in
     Settings.
   - **Connection check.** Nebula on the phone can go off without you noticing (it did
     once, with Always On set), and then calls ring but can't connect. So the
     switchboard's background push every 30 min wakes the app, which calls the switchboard
     back over Nebula, the way a call reaches the Mac. If three tries in about 25 s fail,
     the app shows a notification: "Claude can't call you: Ringback can't reach your
     switchboard over Nebula. Check that Nebula is on." It comes again after 3 h if the
     problem stays. Until the switchboard is reachable again, a banner on the main screen
     says the same, because tapping the notification removes it. Notifications are asked
     for when you open the app.
     - On the phone (one test each): unlocked and locked, the app checked in within 2 s of
       the push. With Nebula off and the phone locked, the warning came, but later than
       35 s after the push; how much later wasn't timed.
   - **Calls:** the whole call history.
     - Each call shows its caller ("Claude from \<project\>") and a 3–8 word subject.
       Before the call is queued, the calling Mac asks Haiku for that subject, based on
       the session's last message, and the push carries it.
     - A green phone and the duration mean answered. A red one means declined, missed
       or busy.
     - The history is stored on the phone for now and will move to iCloud later.
   - **Call back** (Stage 3). Tapping a declined or missed call starts a call to that
     session straight away, without ringing. The voice has the same context the missed
     call had. If the session has moved on since, it says what changed first.
   - **Held calls.** While all calls are held after a final decline, the app says so and
     shows which call to call back. Calling it back releases them.
   - **Settings:**
     - **Switchboard:** the switchboard's Nebula address. The app registers its push
       token there.
     - **Interruption Sensitivity:** a slider from "Less" to "More". More stops the voice
       at shorter, less certain speech, which suits quiet places. Less needs clearer,
       longer speech, which suits noisy ones. On the Mac it sets the confidence and length
       thresholds of voice-activity detection (`voice/ringback_voice/hearing.py`). The
       middle is Pipecat's defaults, which all calls so far used. The ends need 0.35 s or
       0.1 s of speech, with confidence to match. Only the low end has been tried,
       offline (Stage 2).
     - **Ignore Background Noise:** the background filter, the level gate above. It's a
       switch, on by default, and while it's on a slider from "Gentle" to "Strong" shows
       below it, as with Reduce Loud Audio in iOS Settings. On the Mac it sets the volume
       threshold of voice-activity detection, on Pipecat's scale: 0.6 when off (-50 LUFS,
       Pipecat's default, which all phone calls so far used), and from 0.62 to 0.8
       (-30 LUFS) when on, 0.7 in the middle (the default). The strongest would ignore
       some of your own quieter speech.
     - Like iOS's own Settings, the sliders show words, not numbers. The app sends both
       settings with each call's offer, so a change applies from the next call.
7. **Nebula** carries only the call; the push reaches the phone through Apple. The
   lighthouse must be a relay (`relay.am_relay: true`) and the Mac must list it in
   `relay.relays`. On mobile data the phone reaches the Mac only through that relay:
   direct tunnels to the phone fail, and Mobile Nebula (0.12.0) has no setting to list
   relays of its own.

## Rejected alternatives

Each entry says why it lost and what would make it worth retrying.

- **Calling through the phone network (e.g. Twilio) instead of an app.** Per-minute and
  number costs are hard to justify for a personal tool. Retry if those costs become
  negligible.
- **The Mac opening the Nebula tunnel to the phone.** It can't reach a phone on mobile
  data, and Mobile Nebula 0.12.0 can't list relays of its own (Stage 0). Retry if Mobile
  Nebula gains `relay.relays`.
- **Speech-to-text on the phone (Apple SpeechTranscriber).**
  - It takes no vocabulary.
  - It was weaker on accented English in the one published comparison.
  - Whether it runs on a locked phone during a call is undocumented.

  Retry if Apple adds vocabulary biasing to SpeechTranscriber, or it beats Parakeet on
  your own recordings (`tools/stt-eval`).
- **Whisper large-v3-turbo.** It was about as accurate as Parakeet v2, but about 12×
  slower (about 0.5 s per utterance), and it invented words on short clips. Retry if a
  build without the per-clip overhead appears.
- **Qwen3-ASR 1.7B and Parakeet v3.** Neither did better on the test voice, and v3's extra
  languages aren't needed for English-only calls. Retry for speakers of other languages.
- **Granite Speech 5.0 2B.** Untested: mlx-audio 0.6.2 doesn't expose its keyword list,
  which was the reason to try it. Retry once it does.
- **Apple's voices for speech, and Pocket TTS.** In a listening test the standard Apple
  voices sounded poor, and Kokoro `af_heart` was preferred over Pocket TTS. Apple's
  Premium voices weren't tried. Retry with a Premium voice.
- **Pipecat's own Kokoro service (kokoro-onnx).** On the CPU, its default, it ran 8×
  faster than real time, against 27× for MLX: 1.54 s for 12 s of speech, against 0.45 s
  (`tools/tts-speed`). Pieces longer than about 7 s of speech would leave gaps, and the
  first sentence would start about 0.35 s later. CoreML made it no faster, because it
  took only about half of the model's operations. Retry if kokoro-onnx gets much faster
  on Apple silicon.
- **One sentence per Kokoro piece.** It left about 0.75 s of silence between sentences.
  It's replaced by growing pieces.
- **Trimming the silence between Kokoro pieces.** It sounded worse than untrimmed pieces.
  Retry only with crossfades or a gentler trim.
- **Pipecat's default end-of-turn wait (3 s).** When Smart Turn judged a short turn
  ("mm-hmm", "wait") unfinished, the reply came about 4.9 s after it. Replaced by 1.5 s
  (Stage 2). Retry if calls show the voice answering the first half of your sentences.
- **A 1 s end-of-turn wait.** It split none of the recorded turns, but leaves little room
  over pauses of up to 0.74 s in free speech. Retry if short turns still feel slow and
  calls show no pauses over 1 s.
- **A one-off `claude -p` call per turn.** It took about 3.2 s, 1.7 s of it start-up.
  It's replaced by a Claude Code session that stays open for the call.
- **Haiku through the API.** Calls run on the Claude subscription instead, because this
  is a personal tool. Retry if replies through Claude Code prove too slow in Stage 2.
- **Pipecat's iOS client.** It always takes over the audio session (speaker, mixing),
  which clashes with CallKit. It's replaced by Google's WebRTC in manual-audio mode.
  Retry if the client gains a CallKit or manual-audio mode.
- **Changing interruption sensitivity during a call.** The in-call screen covers the app's
  settings, so it would need a fourth button or a slider on the call screen, and a WebRTC
  data channel to the Mac. You chose to change it between calls. Retry if calls show you
  need it mid-call, for example when moving from a quiet room to the gym.
- **VoxLock: removing other people's voices on the Mac.** Parked on the branch `voxlock`,
  with the app's recording of your voice and `tools/voxlock-delay`. You recorded your
  voice once in the app, and each call sent 3 s of it with the offer. VoxLock then passed
  only your voice to voice-activity detection and Parakeet. Offline, with another voice
  talking 6 dB below yours, none of its words got through. Without VoxLock, that call
  derailed (Stage 2). It lost because, with AirPods, other people are far from the
  microphone and arrive quieter, so a level gate (the background filter) keeps them out
  without a model. Offline, it alone kept out a voice 20 dB below yours (Stage 2).
  VoxLock, by contrast, costs something on every call:
  - an interruption stops the voice about 0.1 s later (a median 80 ms of buffering, over
    187 starts of speech, plus 23 ms of computing per 80 ms chunk);
  - Parakeet got 7.0% of words wrong instead of 5.9%, on your recordings with nobody else
    talking;
  - it can mute you for a whole call. Without a recorded sample, it learned the voice of
    whoever spoke first, and offline that was the other person. A sample recorded through
    the app's audio path rather than the call's might also not match how you sound on
    calls (untested).

  Retry if calls show other people's voices stopping the voice or heard as yours when
  they're too close or loud for a level gate to tell apart from you.
- **Ending a call as "busy" during another call.** You want to choose, so it's replaced by
  call waiting and "not now" (requirement 3).
- **The switchboard in Python** (aiohttp, run with uv). On an old Intel Mac it used
  about 50 MB of memory, and uv, its cache and the virtual environment took about 280 MB
  of disk on top of Python. The Swift binary uses about 4 MB and 470 KB. Retry if the
  switchboard needs a library that only Python has.
- **The switchboard in C.** Swift already brings it down to about 4 MB of memory. C would
  also need hand-written JSON and HTTP parsing on a network port, and its own decoding of
  the APNs key; macOS gives Swift all of these. Retry if a few MB on the always-on Mac
  ever matter.
- **A switchboard on every Mac.** That gives no single queue, so calls from two Macs could
  overlap.
- **The switchboard pinging the phone to check its connection.** A locked, idle phone
  stopped answering over Nebula within 30 s (Stage 0), so pings would raise false alarms
  while the phone is fine. It would also test the direction calls don't use, and need an
  inbound rule in Mobile Nebula for the lighthouse. Replaced by the connection check, in
  which the app calls the switchboard. Retry if background pushes prove too unreliable.
- **A connection check on a timer in the app.** iOS doesn't run apps on a timer. It runs
  background refresh when it decides, which for a rarely opened app can be a few times a
  day or never.
- **The voice pipeline on the always-on Mac.** It would load that Mac. The calling Mac
  must be awake anyway.

## Open questions

- **When should sessions call?** An "away" toggle, or the Mac's idle time.
- **Authentication:** whether the app ↔ Mac and session ↔ switchboard links need more
  than Nebula certificates. This must be settled before the permission relay is enabled.
- **The voice's context:** a fork of the calling session gives the whole history. But a
  long history slows the first turn and uses more of the subscription than starting
  fresh with the session's briefing.

## Risks (unverified)

- Right after the phone leaves Wi-Fi, its first packets once took about 16 s to reach
  the Mac. If that happens just before you answer, audio takes that long to start.
  Answer-to-audio has been measured on mobile data only once (2.6 s).
- Pipecat expects a WebRTC data channel and logs a warning without one. Nothing needs one
  yet: the interruption settings go with the offer.
- Xcode needs device support for the phone's iOS version to install development builds.
- Only the phone's processing and the background filter keep other sounds out, so:
  - gym noise or other people's voices near you could still stop the voice, or be heard
    as yours. Offline, with the filter off, someone talking even 20 dB below you was heard
    as clearly as you (Stage 2). The filter's default kept them out, but it's absolute,
    and how loud each microphone delivers your voice on a call isn't measured. Neither is
    how well the phone's voice processing and Voice Isolation remove other people's
    voices;
  - the filter or voice processing could swallow your own quiet speech: the default
    leaves about 5 dB under your quietest speech in your AirPods recordings.

  Stage 2 tests this with real noise.
- Channels are a research preview, so APIs and flags may change.
- Development builds use the APNs sandbox, and provisioning profiles expire.
- The connection check relies on background pushes, which iOS treats as low priority: it
  delivers at most a few an hour, sometimes late, and none after you swipe the app away,
  until you open it again. So a warning can come late or not at all. Without notification
  permission, only the banner shows, when you open the app.
- Nebula on the phone went off by itself once (2026-10-29, between 10:26 and 10:42), with
  Always On set. It wasn't killed for memory: its extension was running in all of the
  phone's memory-pressure reports that morning, the last at 10:26 (read from a paired Mac
  with `xcrun devicectl device info files --device <UDID> --domain-type systemCrashLogs`).
  Mobile Nebula's log starts afresh with each connection, so the cause is unknown.

## Stages

Verify each stage on the real phone (locked, on mobile data, with headphones) before the
next. A simulator run or a unit test isn't evidence for ringing, push or audio.

0. **Nebula** (done). With the phone on mobile data, the phone loads a page served by the
   Mac at its Nebula IP, then `tools/nebula_check.py` pings the phone from the Mac.
   Mobile Nebula drops all inbound traffic by default, so the ping needs an inbound ICMP
   rule for the Mac. Results:
   - the Mac can't open a tunnel to the phone, whether it's locked or not;
   - when the phone sends to the Mac, a tunnel comes up through the lighthouse relay in
     about 0.2 s, and the Mac can then reach the phone (about 60 ms round trip);
   - that tunnel stopped answering within 30 s of locking the phone, so a locked, idle
     phone can only be reached by push. The cause is unconfirmed: iOS suspending Nebula,
     or the carrier's NAT timing out.
1. **Ring + audio** (done). A VoIP push rings the locked phone, you answer from the
   headphones, and audio flows both ways over Nebula. Results:
   - **Ringing:** a locked phone on mobile data rang as "Claude from \<project\>" and was
     answered from AirPods.
   - **Two-way audio:** a test pipeline greeted you and played your words back a second
     later. The sound in AirPods was clear.
   - **Answer to two-way audio:**
     - 2.6 s on a locked phone on mobile data, just after Wi-Fi was turned off (one call);
     - 1.2–1.8 s on Wi-Fi (five calls).
   - **After leaving Wi-Fi:** in an early test the phone's first packets took about 16 s
     to reach the Mac. In the final test, the ringing report arrived 4 s after the push.
   - **Call waiting during a real phone call:** the call-waiting screen stayed until you
     declined. (The first version ended the call by itself after about a second; that's
     replaced by "not now", requirement 3.)
   - **Closing the app during a call:** the call is recorded as ended within 2 s of the
     real end, and reported at the next launch.
   - **Swift switchboard on an old Intel Mac** (the lighthouse): a locked phone on mobile
     data rang 5 s after the push and was answered from AirPods; two-way audio came up
     2.7 s after answering, and ringing, answered, audio connected and ended all reached
     the switchboard (one call).
2. **Conversation** (in progress). To pick speech-to-text for a new voice, record
   `tools/stt-eval/sentences.txt` and score the candidates (see `tools/stt-eval/README.md`).
   Then interrupt mid-sentence, measure how quickly speech stops when you start talking
   and how long a reply takes, and test with gym-like noise. Results so far:
   - **The conversation works** on a locked phone on mobile data, with AirPods (two
     calls). The voice opened with the report in pieces. It stopped whenever interrupted,
     Haiku knew which words had been heard, and it answered a lone "mm-hmm".
   - **Answer to first words:** 2.9 s. Haiku writes its opening while audio connects
     (2.8 s), so the voice started 0.09 s after connecting. In the first call, which
     started Haiku only once audio was connected, it took 6.1 s.
   - **After your turn:** the voice answered 1.5–2.4 s after Pipecat noticed you'd
     stopped, with Haiku's first words after 1.1–1.5 s. A short turn ("mm-hmm", "wait")
     took about 4.9 s instead (on the phone and offline): Pipecat's end-of-turn model
     (Smart Turn v3) sometimes judges it unfinished and then waits its full 3 s for more.
     With that wait cut to 1.5 s (below), the second call answered "okay", "mm-hmm",
     "wait" and a short sentence in 1.4–1.8 s.
   - **Waiting for the end of your turn** (offline, `tools/call-check`, your own
     recordings): Smart Turn kept two 18–19 s stretches of free speech, with pauses of
     up to 0.67 s, as one turn whether it waited 3, 1.5 or 1 s. In five minutes of free
     speech, no pause was longer than 0.74 s, apart from one 3.2 s hesitation. So the
     voice now waits 1.5 s, which halves the wait after a turn Smart Turn misjudges.
     A pause after a complete sentence ends the turn, as it should. If you then go on,
     the reply is dropped before it's spoken, and Haiku is told you heard none of it
     (on the phone, and offline with pauses of 0.5–1 s).
   - **Very short words can go unheard.** Your recorded "Great." (about 0.3 s) never
     triggered voice-activity detection offline. On the phone, one "wait" said while the
     voice talked wasn't heard either; said again, it was. Unknown: whether that was its
     length alone, or the phone's echo cancellation softening speech over the voice.
   - **Stopping when you talk:** 0.36–0.44 s after your speech starts, measured offline
     with `tools/call-check` (two calls), so without the network and the phone's buffer.
   - **Noise:** coughing and clapping while the voice talked didn't stop it (one call).
   - **Someone else talking**, offline (`tools/call-check --background`, one call each),
     with another Kokoro voice talking under the whole call, mixed in without the phone's
     voice processing or Voice Isolation. Levels are while there's sound:
     - with your recorded turns, interrupting twice, and the other voice 6 dB below you,
       Parakeet took its sentences as yours about 25 times, and the voice answered them.
       With nobody else talking, both interruptions stopped the voice, 0.40 and 0.59 s
       after you started;
     - with two Kokoro turns, one interrupting, and the other voice 20 dB below you, it
       still stopped the voice 3 times and was taken as you 4 times, word for word. At
       9 dB below, with the sensitivity at 0.3, it did the same and once replaced your own
       words: Parakeet heard only its sentence in a turn where you said "Wait, stop for a
       second."

     These calls ran with the volume threshold at Pipecat's default, the background filter
     off. So a quieter voice is heard as clearly as yours unless something gates by level.
     With the other voice 20 dB below you:
     - at sensitivity 0, which then also raised the volume threshold to 0.7, none of it got
       through and both your turns were heard. The interruption stopped the voice 0.54 s
       after you started (0.35–0.59 s in the other calls here); that end also needs
       0.35 s of speech instead of 0.2;
     - with only the volume threshold raised to 0.7 (about -40 LUFS), now the background
       filter's default, it started no turn and no stop, and the interruption stopped the
       voice after 0.35 s. But where it overlapped a turn of yours, Parakeet heard its last
       words too ("around six. Wait, stop for a second."): the gate decides when a turn
       starts, not what's heard in it;
     - at the app's defaults, with your four recorded turns from the 6 dB call above and
       the other voice 20 dB below you, none of it got through in 70 s. All your turns
       were heard word for word, and both interruptions stopped the voice, 0.45 and
       0.63 s after you started;
     - the margins are small. On Pipecat's volume scale (0 to 1 for -110 to -10 LUFS, so
       0.1 is 10 dB), the other voice measured 0.63–0.68 in half-second windows, against
       the gate's 0.7. The first minute of your scripted AirPods recording measured
       0.75–0.89 (median 0.84). So a gate at 0.7 keeps out voices roughly 15 dB or more
       below yours, with about 5 dB to spare under your quietest speech.
   - **VoxLock** was tried offline and parked: see rejected alternatives. Its full results
     are in `docs/design.md` on the branch `voxlock`.
   - **Not yet tested:** the sensitivity and the background filter on the phone, the
     sensitivity's high end (1), the filter's strong end, gym-like noise, and someone
     talking near you on a call. Tuning the filter waits until testers report other
     people's voices getting through, or their own being missed.
   - **Left until testers report them as real problems**, because the calls so far went
     fine: very short words going unheard (above), and a stop where no words are
     recognised (the voice should carry on; the cough and clap didn't cause one).
3. **One session.** The session finishes and calls, you reply, it acts and calls again.
   Approve a permission prompt by voice.
4. **Several sessions + retries.** A second session waits for the first call to end.
   "Not now" retries on schedule while the queue waits. A final decline holds all calls
   until a call back releases them.
