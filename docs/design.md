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
3. **Busy line.** If you're already on a call (a phone call or another session's), the
   session waits a few minutes and tries again.
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
   - on busy, declined or unanswered, retries after a few minutes.
4. **Voice pipeline** (on the Mac running the calling session). That Mac must be awake
   anyway, and this keeps the always-on Mac light. It holds a sleep assertion while its
   call is queued or in progress.
   - Pipecat, with Parakeet TDT 0.6B v2 (MLX) for speech-to-text, Kokoro-82M with the
     `af_heart` voice for speech, and Claude Haiku 5.5. Pipecat has no Parakeet service,
     so we write a small one.
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
     between sentences, against 0.24–0.5 s in one continuous passage. Kokoro generated a
     15 s passage in about 0.6 s with MLX. Its speed inside Pipecat hasn't been measured,
     and the one-second lead depends on it.
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
   - when you answer, the app opens the audio connection to the Mac named in the push,
     over Nebula (WebRTC,
     using the voice-processing audio session for echo cancellation). The Mac can't open
     it, because on mobile data only the phone can start a Nebula tunnel (see Stage 0);
   - `CXCallObserver` detects an active call. The app then reports "busy" and ends the
     new call, because Apple requires every VoIP push to report a call.
   - It reports each call's events to the switchboard: ringing, answered, ended (with
     its duration), declined, missed and busy. Each report is stamped with when it
     happened on the phone, and retried for about a minute. After the phone changes
     network, Nebula can take a while to reach the Mac again. The "ringing" report also
     brings the tunnel up while the phone rings.
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

## Open questions

- **When should sessions call?** An "away" toggle, or the Mac's idle time.
- **Retry timing:** a fixed few minutes, or ~30 s after hang-up when the wait was only for
  another session.
- **Authentication:** whether the app ↔ Mac and session ↔ switchboard links need more
  than Nebula certificates. This must be settled before the permission relay is enabled.
- **The voice's context:** a fork of the calling session gives the whole history. But a
  long history slows the first turn and uses more of the subscription than starting
  fresh with the session's briefing.

## Risks (unverified)

- On a locked phone woken by a push, the time until the app's first packet brings the
  Nebula tunnel up is unmeasured, both normally and right after a Wi-Fi → mobile switch.
- Reporting and ending a call during a real phone call may flash or beep.
- Pipecat's iOS client (`pipecat-client-ios-small-webrtc`) always sets its own audio
  session: play-and-record, mixed with other audio, on the speaker. That will probably
  clash with CallKit, so expect a fork or our own WebRTC audio code.
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
1. **Ring + audio.** A VoIP push rings the locked phone, you answer from the headphones,
   and audio flows both ways over Nebula. Measure the time from answering to two-way
   audio, also right after a Wi-Fi → mobile switch. Check busy detection during a real
   call.
   - Ringing works: a locked phone on mobile data rang, showed the caller, and was
     answered from AirPods.
   - In that test the phone had just left Wi-Fi, and its first packets reached the Mac
     about 16 s after the push (one measurement).
2. **Conversation.** To pick speech-to-text for a new voice, record
   `tools/stt-eval/sentences.txt` and score the candidates (see `tools/stt-eval/README.md`).
   Then interrupt mid-sentence, measure how quickly speech stops when you start talking
   and how long a reply takes, and test with gym-like noise.
3. **One session.** The session finishes and calls, you reply, it acts and calls again.
   Approve a permission prompt by voice.
4. **Several sessions + retries.** A second session waits for the first call to end. A
   real phone call makes a session retry.
