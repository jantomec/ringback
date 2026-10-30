# Phone check

Plays the phone's side of calls against a voice server started here, to time the silence
after you answer without the phone. The voice server gets its own socket, in a temporary
folder, and ports 18797 and 18798, so it can run beside your own voice server and other
checks. With `--port P` (in every mode) they're P and P + 1, so two phone checks can run at
once. Calls are placed through that socket, as a session's mod places them. A stand-in
switchboard in the script takes them, because the real one gives the call's ID only to
the phone, in its push, and the check needs it for the phone's offer. Nothing rings.

aiortc then plays the phone as the app does: one offer with all ICE candidates, and a
microphone that sends nothing until you answer, then silence. Each call is timed from
when you answer to the first audio that isn't silence, in three ways, `N` times each:
- **ringing:** the offer goes as soon as the call is placed, marked `"ringing"`, and
  `/answered` (twice, as the app may retry it) `S` seconds after placing;
- **answered:** the offer goes `S` seconds after placing, as from an app that makes its
  offer only once you've answered;
- **by hand:** a call rung by hand, whose Haiku starts only when the offer comes, as every
  call's did before.

It also checks:
- `/answered` is refused (404) for a call with no offer live: never offered, before its
  offer, and after the phone hung up;
- nothing at all, not even silence, reaches the phone while it rings: silence queued on
  the phone played first when you answered, about 2 s late and garbled;
- a call declined while it rings: its pipeline ends, it leaves no transcript, and the call
  stays placed with its Haiku. Offered again once you answer, as the app does when its
  offer made while ringing failed, it uses that Haiku's opening;
- a new offer for a call replaces one still live: the first hears nothing, the second
  hears the opening;
- a call rung by hand and offered while it rings stays silent until `/answered`;
- nothing reaches the session from calls where you said nothing, and your one reply ("Yes,
  push it and open the pull request.", spoken by Kokoro) does;
- one transcript for each answered call, each starting with the voice's opening;
- no Haiku is left after each call, and at the end no session folder that Claude Code keeps
  for the voice's sessions is left in `~/.claude/projects` (the voice deletes it when a
  call's session closes; see `tools/handover-check`).

Run from the repo root. It uses your Claude subscription, one Haiku session for each call
(`3 N + 5`):

    uv run --project voice tools/phone-check/phone_check.py [--runs N] [--ring S] [--keep DIR]

- `--runs` is the number of calls of each kind (3 by default).
- `--ring` is the seconds from placing a call to answering it (6 by default).
- `--keep DIR` keeps the voice's log and the calls' transcripts.

It prints the last transcript, the voice's log and the checks, then the silence for each
kind, when Haiku's first words of each opening came, and how soon after hanging up no
Haiku was left.

Nebula and the phone aren't part of it. On this Mac WebRTC connects in about 0.06 s,
against 2.5–2.8 s on the phone over Nebula, so here "answered" is about as quick as
"ringing". On the phone, "answered" waits for that connection, and "ringing" doesn't, if
the offer made while ringing has connected by the time you answer. The phone's own audio
path (its jitter buffer, CallKit, AirPods) isn't part of it either. The first audio counts
from an int16 level of 100, as in `tools/call-check`, so it includes the quiet lead-in of
Kokoro's first word.

Result on 2026-10-29, Claude Code 2.1.312, 6 s from placing to answering, five runs of
each kind: all 30 checks passed. Silence after you answer:
- ringing: median 0.58 s (0.53–0.63);
- answered: median 0.65 s (0.59–0.69);
- by hand: median 2.84 s (2.46–3.19), as every call before.

Haiku's first words came 1.4–2.5 s after it was asked for the opening, so in the first
two kinds the opening was ready before you answered. No Haiku was left 0.6–0.8 s after
hanging up.

With 2 s from placing to answering, before Haiku's opening is ready (three runs, all 24
checks passed): ringing 0.58–1.01 s, answered 0.62–1.60 s, by hand 2.90–3.01 s.

After merging with the call-outcome notes, which are queued for the session too and which
the check therefore leaves out (three runs of each kind, all 24 checks passed): ringing
median 0.58 s (0.58–0.67), answered 0.65 s (0.62–0.68), by hand 2.60 s (2.28–2.92).

With the voice server holding its audio until you answer, and the check above (three runs
of each kind, all 25 checks passed): ringing median 0.67 s (0.57–0.67), answered 0.74 s
(0.63–0.79), by hand 2.85 s (2.80–3.23).

With the voice's sessions kept on disk under a known ID until the call ends, so Opus can
take a call over, and calls rung by hand given the better-model tool (2026-10-30, one run
of each kind, all 20 checks passed): ringing 0.53 s, answered 0.63 s, by hand 2.65 s. No
session folder was left.

In an earlier version, a tool call Haiku made in its opening waited for the call's voice.
Once, the opening stalled 8.7 s on it, and a message nobody had said reached the session.
Now a tool call in the opening passes nothing and is answered at once (`haiku.py`).

## Call backs (`--back`)

    uv run --project voice tools/phone-check/phone_check.py --back [--unanswered S] [--keep DIR]

Calls back calls you didn't answer, as the app does from its call history: an offer with
the missed call's ID and `"back": true`, answered at once. The voice server waits `S`
seconds (8 by default, instead of 90) before it forgets a call you don't answer. A
stand-in for the session's mod collects the session's messages and call outcomes once a
second, so the session listens. It runs with `--report`, so it also takes calls rung by
hand. It checks:
- a call back for a call the server doesn't know is refused (403), though it takes calls
  rung by hand;
- **missed:** a call you don't answer is forgotten, and its session told so. Called back,
  the voice is heard, its opening is about the missed call, and the session is told "The
  developer called back about "…" and it lasted …";
- **moved on:** the session places a later call, which isn't answered either. The first
  call, called back once already, is called back again, and the opening starts with what
  the later briefing says changed (the pull request), within its first 30 words;
- **not listening:** a call for a session no mod collects for. Called back, the voice
  server says the session isn't listening, and the opening is about that call. Asked
  "Please ask the session to rebuild the docs once more.", the voice passes nothing on and
  says it can't;
- **permission:** a permission with no call places one, which isn't answered. Once that
  call is forgotten, the permission is still `pending`. Called back, the opening asks it,
  and "Yes, go ahead." allows it;
- **inside the window:** a call declined after 1 s and called back at once, while it's
  still placed. The call back lasts past the missed call's `S` seconds, and the session is
  told only that you called back, not that the call wasn't answered;
- no opening introduces the voice or says it's calling (you called);
- no Haiku is left, and none of its voice server's session folders in
  `~/.claude/projects`. That server gets its own `TMPDIR`, so the folders of other voice
  servers on this Mac, yours or another check's, don't count.

It prints the voice's log, the checks, what the voice said first on each call back, and
the time from each offer to the voice's first audio. It uses your Claude subscription, 10
Haiku sessions.

Result on 2026-10-30, Claude Code 2.1.314, five runs of about 2.5 min each (two after
merging with work hours): all checks passed, but for one session folder left in one run,
which was gone minutes later, after that run's voice server had stopped; so it was likely
another voice server's, and the check now counts only its own. Every opening was about the
missed call, and on "moved on" started with the pull request. From the offer to the first
audio: a median 2.72 s (2.13–3.48 s, 25 call backs), about as on a call rung by hand,
since Haiku starts only at the offer; its first words came 1.5–2.5 s after. In the first
run, before the lead said not to, one opening began "Hi, I'm calling about…"; the check for
that came after it, and passed in the four runs since.

## Contacts and answered call backs (`--contacts`)

    uv run --project voice tools/phone-check/phone_check.py --contacts [--port P] [--keep DIR]

Calls a session from the app's Sessions tab (Contacts until 2026-10-30, still the name of the
offer's field and of the voice server's notes), as the app does (an offer with a call ID made on the
phone, `"contact"` with the session's ID, and `"back": true`), and calls back a call you
answered. A stand-in for the session's mod collects once a second and says the session's
project and folder; the stand-in switchboard also takes what the voice server says of its
sessions that listen (`/sessions`). The voice server says so every 3 s instead of 30
(`RINGBACK_PUBLISH`), and a session not collected from for 4 s isn't listening, instead of
30 (`RINGBACK_LISTENING`). It checks:
- the switchboard hears the session listen, with its project and no briefing yet, and with
  nothing changed, hears the same again every 3 s;
- a call from Contacts to a session that isn't listening, or with a call ID shorter than 16
  characters, gets 403 `not listening`;
- **no briefing yet:** called from Contacts, the voice is heard, says the session has
  nothing to report yet, and the session is told "The developer called the session from the
  app's Contacts and it lasted …";
- the session's briefing places a call that names the session to the switchboard
  (`session_id`), and the switchboard hears the briefing's subject and when at once;
- **answered call back:** you answer that call, then call it back: answered, the voice server
  logs that you answered it, the voice doesn't say you missed it, and the session is told you
  called back;
- **a briefing you've heard:** called from Contacts again, the voice doesn't say you missed a
  call, says nothing is new and doesn't retell the briefing, and your spoken "Please ask the
  session to run the whole test suite again." reaches the session as one message;
- a call from Contacts can be called back;
- no opening introduces the voice or says it's calling;
- ringback off (the mod stops collecting): the switchboard hears the session no longer
  listens, and a call from Contacts is then refused;
- no Haiku left, and none of its server's session folders.

It prints the voice's log, the checks, what the voice said first on each call, and the time
from each offer to the voice's first audio. It uses your Claude subscription, 5 Haiku
sessions, and takes about 3 min.

Result on 2026-10-30, Claude Code 2.1.314, four runs of 2–3 min:
- the first: 22 of 23 checks passed. The one that failed was the check's own: the voice server
  told the switchboard no session listened 3.6 s after the mod stopped, but the check took
  the empty list for none heard. Fixed in the check;
- the second: all 23 passed. But in both runs, called from Contacts with the briefing you'd
  already talked through, the voice went through the briefing again ("The config parser
  crash is fixed…") instead of saying nothing was new. Its lead now says not to retell it,
  and a check says so (it fails on both openings);
- the third, with that lead: 21 of 24. The voice said "Nothing new has come in since that
  call. The session is still waiting on one thing: shall it push the config parser fix to a
  branch and open a pull request?". The three that failed were the first three calls, whose
  voice the phone didn't hear within its 20 s: Haiku's first words came 22 and 20 s after it
  was asked (and none at all on the answered call), while `tools/restart-check` ran its own
  Haiku sessions beside it. Each offer was answered in 0.14 s at most, and audio connected
  in 0.44 s at most; on the calls after, Haiku answered in 1.3–2.7 s;
- the fourth, alone, after merging with main (Remote Control taken as on): all 24 passed, in
  2 min 5 s. With the briefing you'd heard: "Nothing new has come in since that call. The
  session is asking whether to push the config parser fix to a branch and open a pull
  request. What would you like to do?". From the offer to the voice's first audio, a median
  2.67 s (1.75–3.27 s).

`--back` after the same merge (one run, 2 min 32 s): 21 of 22. The one that failed: the missed
call's call back opened "Hi, I'm calling about the config parser crash…", as one did twice
before (above, and in `tools/restart-check`); that lead is unchanged. Moved on, not listening,
the permission and inside the window passed.

Openings, as said: with no briefing, "Hi there. The session hasn't written a briefing yet,
so I don't know what it's been working on. What would you like me to pass along?"; calling
back the answered call, "Nothing new has come in since our last call. The config parser fix
is still waiting on you: shall I push it to a branch and open a pull request?" (the same in
all three runs); calling back the first call from Contacts, which had no briefing then,
"Since we last spoke, the config parser crash is fixed…", what changed since. From the offer
to the voice's first audio: a median 3.90 s (2.86–4.32 s) in the first run and 3.43 s
(2.54–4.30 s) in the second, five calls each, as a call back's: Haiku starts only at the
offer.

## Weak signal (`--signal`)

    uv run --project voice tools/phone-check/phone_check.py --signal [--loss 0,5,15] [--runs N] [--port P] [--keep DIR]

Says known sentences on calls rung by hand whose line loses some of the phone's packets,
and compares what the voice server heard with what was said. For each run, one call for
each loss in `--loss` (percent; 0, 5 and 15 by default):
- the sentences are `tools/stt-eval/sentences.txt`, but for one Haiku could take for asking
  for a better model: 29 sentences, 215 words, about 97 s of speech, spoken by another
  Kokoro voice, 2.5 s apart, after the voice's opening;
- each RTP packet the phone sends is lost at random with that chance, on its own, not in
  bursts. The same packets are lost for the same run and loss, counted from the call's
  first packet;
- the phone's Opus is set up as Google's WebRTC sets it up from the voice server's answer
  (libwebrtc's `audio_encoder_opus.cc`): mono, at `maxaveragebitrate`, or else 32 kbps (20
  kbps if `maxplaybackrate` is 16 kHz or less), in the band `maxplaybackrate` allows, and
  with forward error correction only if the answer has `useinbandfec=1`. WebRTC sets
  Opus's expected loss from the loss the Mac reports; here it's the loss the check makes.
  aiortc's own encoder would send 96 kbps stereo.

It prints, for each call:
- the packets lost, and the words heard wrong: the edit distance in words between what was
  said and what the voice server heard (its log), after lowercasing and dropping
  punctuation, so "200" for "two hundred" counts as wrong at any loss;
- a median of how soon after the speech in each sentence ended (its last sample above the
  level of 100) its words were heard, as the voice server's log times them. It includes
  the voice-activity detector's wait for silence, so compare it between runs, not with
  zero. Measured from the end of each clip instead, as at first, it missed the words the
  voice server heard before the clip's trailing silence had all gone;
- each way, the kbps of Opus over the call, and on average in the seconds with speech and
  in those without (where the voice or you spoke in at least 3 of 4 frames, or in none),
  the packets a second, and the bytes of RTP and SRTP each of the phone's packets carried.
  "Through Nebula" adds those and 88 bytes a packet for UDP and IP inside Nebula, Nebula's
  header and tag, and UDP and IP outside, over IPv4, counted, not measured;
- the voice server's own line about the call's audio, and what it heard.

Then the words heard wrong for each loss over all runs. It uses your Claude subscription,
one Haiku session for each call, and takes about 3.2 min a call.

The stand-in phone's microphone sends digital silence between sentences, which Opus
encodes at 10–14 kbps in its full band and about 3 kbps in wideband; a real microphone's
background noise costs more. The phone's audio path, its jitter buffer and concealment, and
a real network's late or reordered packets aren't part of it.

Result on 2026-10-30, Claude Code 2.1.314, three runs before Stage 6 changed the voice
server's Opus (`voice/ringback_voice/opus.py`) and three after, each about 30 min:
- words heard wrong with 0%, 5% and 15% lost: before 9, 141 and 473 of 645 (1.4%, 21.9%,
  73.3%; per run 3, 3, 3; 57, 43, 41; 165, 150, 158), after 9, 11 and 12 (1.4%, 1.7%,
  1.9%; 3, 3, 3; 3, 4, 4; 5, 4, 3). With none lost, the 3 words wrong are numbers written
  as digits;
- Mac to phone, kbps of Opus while the voice spoke, in silence and over the call, and
  through Nebula: before 88–90, 1.5–2.4, 28–33 and 78–83; after 22.7–23.5, 3.2–3.4,
  8.3–9.8 and 58–59. The voice spoke about a third of each call;
- phone to Mac, the same: before 30.5–31.1, 13.5 (digital silence), 21.5 and 71; after,
  asked for 24 kbps in wideband with FEC, 21.2–21.9, 3.2, 11.8 and 61;
- 50 packets a second each way, with 36 bytes of RTP and SRTP each, in every call; the
  voice server counted the same packets lost as the check dropped.
- the words were heard a median 0.27–0.31 s after the speech in each sentence ended, at
  every loss, after; before (one more run, with this measure) 0.57, 0.68 and 0.93 s at 0%,
  5% and 15% (48 and 150 words wrong at 5% and 15%). With the phone's encoder as before
  and the voice server's Opus as now, one call at 0%: 0.29 s, so the gain is the voice
  server's receive path. aiortc's jitter buffer held 80 ms of it back; the rest isn't
  explained.

After merging Stages 4 and 5 (one run, about 11 min): 5, 3 and 3 words wrong at 0%, 5% and
15% lost (at 0%, "Approved." heard for "Approve it.", with nothing lost), heard 0.27–0.30 s
after, and the same kbps as above.
