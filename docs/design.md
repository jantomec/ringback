# ringback — design

## Goal

Your Claude Code sessions call your iPhone when they finish or need you. Each one tells
you what it did, and you talk it through hands-free, with the phone locked in your pocket
and on mobile data.

## Requirements

1. **Conversation.** Real back-and-forth in one call. You can interrupt Claude
   mid-sentence.
2. **One call per session.** A call opens with the project name, then what's new, then
   what needs your attention. Your replies, including permission approvals, go to that
   session.
3. **Busy line.** If you're already on a call (a phone call or another session's), the
   session waits a few minutes and tries again.

## Architecture

```
sessions ──hooks──▶ switchboard (Mac) ──VoIP push via APNs──▶ iPhone app rings (CallKit)
   ▲                    │ voice pipeline: Whisper + local TTS + Claude Haiku
   └─channel: replies───┘◀──────────── call audio over Nebula ───────── iPhone app
```

1. **Hooks** (in each session). A Stop hook, plus a Notification hook for
   `permission_prompt`/`idle_prompt`, sends `session_id`, `cwd`,
   `last_assistant_message` and `transcript_path` to the switchboard and returns at once.
2. **Channel** (a custom MCP channel server in each session):
   - delivers your replies into its session (on the next turn if the session is busy);
   - relays permission prompts (`experimental['claude/channel/permission']`, needs
     Claude Code ≥ 2.1.234).

   Channels are a research preview, so sessions start with
   `--dangerously-load-development-channels server:<name>`.
   Docs: https://code.claude.com/docs/en/channels-reference.md
3. **Switchboard** (a daemon on the Mac):
   - keeps a FIFO queue of sessions waiting to call and places one call at a time;
   - starts a call with a VoIP push through APNs;
   - on busy, declined or unanswered, retries after a few minutes;
   - routes your replies to the calling session's channel;
   - forwards questions the voice can't answer to the session and waits for its reply.
4. **Voice pipeline** (on the Mac): Pipecat with local Whisper for speech-to-text, local
   TTS, and Claude Haiku 5.5 through the API. The voice is a fast front end that holds the
   session's report as context, because a Claude Code turn is too slow for live
   conversation. Voice-activity detection lets you interrupt.
5. **iPhone app** (Swift):
   - a PushKit VoIP push triggers CallKit, which rings on the lock screen and in your
     headphones;
   - when you answer, the app opens the audio connection to the Mac over Nebula (WebRTC,
     using the voice-processing audio session for echo cancellation). The Mac can't open
     it, because on mobile data only the phone can start a Nebula tunnel (see Stage 0);
   - `CXCallObserver` detects an active call. The app then reports "busy" and ends the
     new call, because Apple requires every VoIP push to report a call.
6. **Nebula** carries only the call; the push reaches the phone through Apple. The
   lighthouse must be a relay (`relay.am_relay: true`) and the Mac must list it in
   `relay.relays`. On mobile data the phone reaches the Mac only through that relay:
   direct tunnels to the phone fail, and Mobile Nebula (0.12.0) has no setting to list
   relays of its own.

## Open questions

- **When should sessions call?** An "away" toggle, or the Mac's idle time.
- **Retry timing:** a fixed few minutes, or ~30 s after hang-up when the wait was only for
  another session.
- **Authentication:** whether the app ↔ Mac link needs more than Nebula certificates. This
  must be settled before the permission relay is enabled.

## Risks (unverified)

- On a locked phone woken by a push, the time until the app's first packet brings the
  Nebula tunnel up is unmeasured, both normally and right after a Wi-Fi → mobile switch.
- Reporting and ending a call during a real phone call may flash or beep.
- The Pipecat details (local Whisper, TTS options, WebRTC transport) haven't been checked
  against current docs.
- Gym noise may trigger false interruptions.
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
2. **Conversation.** Interrupt mid-sentence, measure the reply latency, and test with
   gym-like noise.
3. **One session.** The session finishes and calls, you reply, it acts and calls again.
   Approve a permission prompt by voice.
4. **Several sessions + retries.** A second session waits for the first call to end. A
   real phone call makes a session retry.
