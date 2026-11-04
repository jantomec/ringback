# Hub check

Checks the hub's side of a call (`hub/`, "The parts" in `docs/design.md`) with stand-ins for
everything around it:
- the phone: `tools/stand-in-phone`, an aiortc peer making the offer as the app does;
- an agent: a WebSocket client speaking `docs/protocol.md`, which owns the calls whose ID
  starts with `known-` and speaks a reply once you've answered and the audio connected;
- a speech node: a WebSocket client that answers each piece to speak with a 440 Hz tone, 60 ms
  per character, in 100 ms chunks, and stops a stream when it's told to.

It runs the hub built in `hub/build` on localhost, with its config (tokens included) and
state in a throwaway HOME, and checks:
- a link with a wrong token is refused (401), and the agent's and node's hellos are
  welcomed;
- the phone pairs, with a code from `ringback-hub pair` traded at `/pair` for its key, which
  its later requests carry;
- before any speech node links, a call the agent places waits instead of ringing (held, reason
  "no speech"); the app's waiting calls list it with its reason, and the status with them says
  calls can't be talked through, that the last push to the phone failed for want of a key
  (`problem` "key": there's no APNs key here), and which Macs are linked. Once the node links,
  the agent is asked to ask again, and asked again, the call rings and the status is fine;
- an offer for a call no agent owns is refused (403);
- an offer the agent accepts is answered; the agent hears `answered` and `connected`, and the
  node is told to warm up (`warm`, as for every offer and every call placed that rings), then
  asked to hear the call with the offer's sensitivity and background filter;
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
- a reply with nothing to say (just `said`) leaves the voice idle; each turn says whether you
  cut the voice off since the last (the agent answers a stop with no words only then);
- once the voice is idle and you haven't spoken for 8 s, the agent hears `quiet`, once, and
  not before: none came while you kept talking;
- hung up, the agent hears the call ended and how long you talked, within 3 s of the time on
  the phone, and the node to stop hearing;
- with no node linked, a call waiting for speech asks again once the phone turns its own
  voice on (`on_device` on `/register`);
- with no node linked, a phone that speaks itself: a call rings
  rather than waiting, and the status says calls can be talked through; the phone's link is
  welcomed once the agent takes the call, and told to listen once you've answered; it gets the
  reply in pieces, the first sentence alone, and from its `playing` and `played` the agent
  hears what you've heard, to the end, then `idle`; cut off in a long reply, it's told to stop
  and the agent hears what you'd heard; your turn reaches the agent; it hangs up, and the call
  ended and lasted as long as on the phone, within 2 s;
- the agent's link dropping during a call ends the call, and the phone is told so.

Build the hub first, then run from the repo root:

    (cd hub && go build -o build/ringback-hub .)
    uv run --project tools tools/hub-check/hub_check.py [--keep DIR]

It prints the hub's log and one line per check. `--keep` keeps the throwaway HOME (config,
state, log). `RINGBACK_HUB` runs another binary, such as the universal one `hub/build.sh`
makes, and `RINGBACK_HUB_ARCH="arch -x86_64"` runs its Intel half under Rosetta.

On 2026-11-04 it failed now and then while the Mac was busy: 4 of 12 runs (and 1 of 3 with
the hub of the day before), most with an app build running beside it; with the first fix
below, still 2 of 8. Each time it was one of three reasons: the check answered a turn
before the hub had taken it, on the other link, so the hub took the next speech as cutting off
a reply still to come (the check now waits for the turn to reach the agent first, as an agent
does); the hub told the agent an empty `heard`, as the share of a piece played shrank while
its audio still came (fixed in the hub: what you've heard never shrinks); and `quiet` was
measured 7.99 s after `idle`, which reached the check late (it now takes 7.9 s). After the
fixes, 8 of 8 runs passed with app builds running beside them.

Result on 2026-11-03, with `quiet` (49 checks): all passed, in 29 s, about 11 s of it waiting
for `quiet`, which came 8.0 s after the last `idle`. On 2026-11-02 (28 checks, with the
warm-up): all passed. The version
before (27 checks) passed three runs, 9–10 s each. The version before, with 25 checks, passed five runs.
Without the hub clearing what you'd heard when cut off, the check of an empty `heard` fails
(tried once, to see that it measures it). The first version's run (19 checks, 8 s): The audio connected 0.03 s after
the offer and the voice was heard 0.15 s after it; your audio reached the node in 644-byte
frames (20 ms); cut off 1.5 s into a long reply, the agent heard "This is a long reply"; hung
up, the call was over at once and had lasted 7.05 s. The first run failed: Pion leaves
loopback addresses out, so the hub on 127.0.0.1 offered none (fixed: loopback is included when
the hub listens on one); then a reply after a finished one was taken as more of it, and the
end of a reply wasn't reported as heard (both fixed). The universal binary from `hub/build.sh` (libopus 1.6.1 linked in statically; it links only
macOS's own libraries) passed all 25 too, its Apple silicon half and its Intel half alone
(under Rosetta), and the hub's smoke test (139). The cut-off checks came after: a reply
cut while still being written went on being spoken from the slices still coming, so replies
are numbered now (`docs/protocol.md`); and at the end of a reply the last frame wasn't counted
as heard ("Going on" for "Going on now.").
