# Line check

A call through the Go port's real parts (`docs/design.md`, "The Go port"), without the phone:
the hub and the agent run here, built from this checkout, and the speech node on its own
machine, started over ssh from its install directory and linked to this hub. Only the phone
is a stand-in: `tools/phone-check`'s, an aiortc peer making the offer as the app does. The
session's side is played through the agent's socket, as a mod plays it.

It places a call for a session with phone-check's briefing, makes the phone's offer while it
rings, answers 6 s after placing, and once the opening has played says phone-check's reply
("Yes, push it and open the pull request.", spoken by another Kokoro voice). It checks:
- the speech node and the agent link to the hub, with their tokens;
- the node loads its models when the call is placed (the hub's `warm`), before you answer;
- the call is placed through the hub, and the offer made while it rings is answered;
- nothing is heard while it rings, then the voice within 3 s of answering;
- the speech node heard your reply with its words (the hub's log), and the voice answered;
- your reply reached the session (its messages on the socket);
- hung up, the call ended and lasted as long as on the phone (within 3 s), no Haiku is left,
  and there is one transcript, with your words.

It then prints the timings: from answering to the voice, Haiku's first words, how long your
reply was and how soon the node recognised it, and from your last word to the voice's
answer. It uses your Claude subscription (one Haiku session).

Run from the repo root, with Go installed here and the node set up as in `speech/README.md`:

    uv run --project voice tools/line-check/line_check.py --listen ADDR --node HOST \
        [--node-dir DIR] [--ring S] [--keep DIR]

- `--listen` is where the hub listens: this Mac's address on the network the node reaches
  it on (its Nebula address), and a free port.
- `--node` is the node's ssh host, and `--node-dir` its install directory
  (`~/ringback-speech` by default). The node runs there with the check's token until the end,
  writing `line-check.log`, whose last lines the check keeps and then deletes.
- `--keep DIR` keeps the hub's, agent's and node's logs, the agent's state and the call's
  transcript.

Results on 2026-11-02, the hub and agent on an Apple silicon Mac, the node on a Linux
workstation with an NVIDIA GPU, over Nebula:
- the first run failed on the check itself: it didn't turn the phone's microphone on when it
  answered, so the node heard nothing;
- with the node unloading its models between calls (a later run, 12 checks with the warm-up,
  44 s): the models were loaded 2.11 s after the call was placed, and the voice was heard
  0.67 s after answering;
- the second passed all 11 checks in 44 s. The node linked 2.2 s after it started, its models
  loaded. The voice was heard 0.62 s after answering (the hub sent its first audio 0.15 s
  after `/answered`, as in the first run). Haiku's first words came 2.21 s after the opening was
  asked for, while the phone rang, and 1.53 s after your reply. The node recognised the reply
  word for word, 2.0 s of speech in 0.11 s. The voice answered 2.02 s after your last word.
