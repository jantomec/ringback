# Hub check

Checks the Go hub's side of a call (`hub/`, the Go port in `docs/design.md`) with stand-ins
for everything around it:
- the phone: `tools/phone-check`'s, an aiortc peer making the offer as the app does;
- an agent: a WebSocket client speaking `docs/protocol.md`, which owns the calls whose ID
  starts with `known-` and speaks a reply once you've answered and the audio connected;
- a speech node: a WebSocket client that answers each piece to speak with a 440 Hz tone, 60 ms
  per character, in 100 ms chunks, and stops a stream when it's told to.

It runs the hub built in `hub/build` on localhost, with its config (tokens included) and
state in a throwaway HOME, and checks:
- a link with a wrong token is refused (401), and the agent's and node's hellos are
  welcomed;
- an offer for a call no agent owns is refused (403), as the voice server refused it;
- an offer the agent accepts is answered; the agent hears `answered` and `connected`, and the
  node is asked to hear the call with the offer's sensitivity and background filter;
- the phone hears the voice, within 3 s of the offer; your audio reaches the node in 20 ms
  frames at 16 kHz;
- the agent hears `speaking`, then what you've heard growing to the whole reply, then
  `idle`; the reply goes to the node in pieces, the first sentence alone;
- your turn reaches the agent;
- cut off in a long reply (the node's `speech_started`), the agent hears what you'd heard of
  that reply (its number, and a start of it); the node is told to stop, and the phone hears no
  more of the voice; more of the cut reply, sent late, isn't spoken;
- talking on after your turn, before its reply has come, cuts that reply off unheard
  (`reply` 3, `heard` empty); it's dropped when it comes, and only the next turn's reply is
  spoken, heard to its end, then `idle`; the agent never hears an empty `heard`;
- a reply with nothing to say (just `said`) leaves the voice idle;
- hung up, the agent hears the call ended and how long you talked, within 3 s of the time on
  the phone, and the node to stop hearing.

Build the hub first, then run from the repo root:

    (cd hub && go build -o build/ringback-hub .)
    uv run --project voice tools/hub-check/hub_check.py [--keep DIR]

It prints the hub's log and one line per check. `--keep` keeps the throwaway HOME (config,
state, log).

Result on 2026-11-02, the last version (five runs; the three timed took 9–11 s): all 25 checks passed each time.
Without the hub clearing what you'd heard when cut off, the check of an empty `heard` fails
(tried once, to see that it measures it). The first version's run (19 checks, 8 s): The audio connected 0.03 s after
the offer and the voice was heard 0.15 s after it; your audio reached the node in 644-byte
frames (20 ms); cut off 1.5 s into a long reply, the agent heard "This is a long reply"; hung
up, the call was over at once and had lasted 7.05 s. The first run failed: Pion leaves
loopback addresses out, so the hub on 127.0.0.1 offered none (fixed: loopback is included when
the hub listens on one); then a reply after a finished one was taken as more of it, and the
end of a reply wasn't reported as heard (both fixed). The cut-off checks came after: a reply
cut while still being written went on being spoken from the slices still coming, so replies
are numbered now (`docs/protocol.md`); and at the end of a reply the last frame wasn't counted
as heard ("Going on" for "Going on now.").
