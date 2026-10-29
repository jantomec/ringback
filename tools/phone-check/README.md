# Phone check

Plays the phone's side of calls against a voice server started here, to time the silence
after you answer without the phone. The voice server gets its own socket, in a temporary
folder, and ports 18797 and 18798, so it can run beside your own voice server and other
checks. Calls are placed through that socket, as a session's mod places them. A stand-in
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
