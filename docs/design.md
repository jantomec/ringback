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
   - is one Swift binary of about 385 KB that needs nothing but macOS 13 or later (Intel
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
     of up to about 25 s of speech. Not yet measured: its speed inside our Pipecat
     service, and with Parakeet running at the same time. Its text-to-phoneme package
     (`misaki[en]`) pulls in torch (about 540 MB) through spaCy, though Kokoro doesn't use
     it.
   - **Interruptions.** Playback stops as soon as you speak, and only for your voice.
     Everything queued is dropped. The voice then waits until you've finished and always
     answers what you said, including a short "yeah" or "mhm".

     Only your voice reaches the detector that stops it, through three layers:
     1. on the phone, voice processing and the Voice Isolation mic mode remove background
        noise (you switch Voice Isolation on once, in Control Center);
     2. on the Mac, the VoxLock filter keeps only your voice and removes other people's.
        It's given the voice sample recorded in the app's settings, so it works from your
        first word;
     3. voice-activity detection then triggers the stop.

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
     - Measured: the first words come about 1.6 s after each of your turns (1.1–2.0 s),
       and 2.2–2.6 s on the first turn, which includes start-up.
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
     - **Your voice:** a guided recording of you reading a few sentences, about 30 s. The
       app keeps it and sends it to the Mac at the start of each call, where VoxLock uses
       it to recognise you. Until it's recorded, the app shows a reminder, and VoxLock
       learns your voice from your first 3 seconds of speech instead.
     - **Interruption sensitivity:** a slider. Higher stops the voice at quieter or
       shorter speech, which suits quiet places. Lower needs clearer, longer speech,
       which suits noisy ones. The app sends the value at the start of each call and
       whenever you change it during one. On the Mac it sets the thresholds of the
       voice-activity detection.
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
- **A one-off `claude -p` call per turn.** It took about 3.2 s, 1.7 s of it start-up.
  It's replaced by a Claude Code session that stays open for the call.
- **Haiku through the API.** Calls run on the Claude subscription instead, because this
  is a personal tool. Retry if replies through Claude Code prove too slow in Stage 2.
- **Pipecat's iOS client.** It always takes over the audio session (speaker, mixing),
  which clashes with CallKit. It's replaced by Google's WebRTC in manual-audio mode.
  Retry if the client gains a CallKit or manual-audio mode.
- **Ending a call as "busy" during another call.** You want to choose, so it's replaced by
  call waiting and "not now" (requirement 3).
- **The switchboard in Python** (aiohttp, run with uv). On an old Intel Mac it used
  about 50 MB of memory, and uv, its cache and the virtual environment took about 280 MB
  of disk on top of Python. The Swift binary uses about 4 MB and 385 KB. Retry if the
  switchboard needs a library that only Python has.
- **The switchboard in C.** Swift already brings it down to about 4 MB of memory. C would
  also need hand-written JSON and HTTP parsing on a network port, and its own decoding of
  the APNs key; macOS gives Swift all of these. Retry if a few MB on the always-on Mac
  ever matter.
- **A switchboard on every Mac.** That gives no single queue, so calls from two Macs could
  overlap.
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
- Pipecat expects a WebRTC data channel and logs a warning without one. The app will
  need one anyway, for live settings such as interruption sensitivity.
- Xcode needs device support for the phone's iOS version to install development builds.
- The voice filtering may not be perfect:
  - gym noise or other people's voices could still stop the voice;
  - the filters could swallow your own quiet speech.

  VoxLock is community-maintained and Pipecat doesn't test it. Stage 2 tests the filtering
  with real noise.
- Channels are a research preview, so APIs and flags may change.
- Development builds use the APNs sandbox, and provisioning profiles expire.

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
2. **Conversation.** To pick speech-to-text for a new voice, record
   `tools/stt-eval/sentences.txt` and score the candidates (see `tools/stt-eval/README.md`).
   Then interrupt mid-sentence, measure how quickly speech stops when you start talking
   and how long a reply takes, and test with gym-like noise.
3. **One session.** The session finishes and calls, you reply, it acts and calls again.
   Approve a permission prompt by voice.
4. **Several sessions + retries.** A second session waits for the first call to end.
   "Not now" retries on schedule while the queue waits. A final decline holds all calls
   until a call back releases them.
