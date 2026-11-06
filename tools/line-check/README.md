# Line check

A call through ringback's real parts ("The parts" in `docs/design.md`), without the phone:
the hub and the agent run here, built from this checkout, and the speech node on its own
machine, started over ssh from its install directory and linked to this hub. Only the phone
is a stand-in: `tools/stand-in-phone`, an aiortc peer making the offer as the app does. The
session's side is played through the agent's socket, as a mod plays it.

It places a call for a session with the stand-in phone's sample briefing, makes the phone's
offer while it rings, answers 6 s after placing, and once the opening has played says its reply
("Yes, push it and open the pull request.", spoken by another Kokoro voice). It checks:
- the speech node and the agent link to the hub, with their tokens;
- the node loads its models when the call is placed (the hub's `warm`), before you answer;
- the call is placed through the hub, and the offer made while it rings is answered;
- nothing is heard while it rings, then the voice within 3 s of answering;
- the speech node heard your reply with its words (the hub's log), and the voice answered;
- your reply reached the session (its messages on the socket);
- it stays silent through a 2.2 s pause in the middle of your sentence, answers once you've
  finished, and the whole sentence reaches the session;
- a "mm-hmm" over the voice stops it and it carries on, saying whether a word list or the model
  decided that;
- a real interruption stops it, and its answer takes the place of the rest of the reply;
- hung up, the call ended and lasted as long as on the phone (within 3 s), no Haiku is left,
  and there is one transcript, with your words.

It then prints the timings: from answering to the voice, Haiku's first words, how long your
reply was and how soon the node recognised it, and from your last word to the voice's
answer. It uses your Claude subscription (one session, on your default model).

Run from the repo root, with Go installed here and the node set up as in `speech/README.md`:

    uv run --project tools tools/line-check/line_check.py --listen ADDR --node HOST \
        [--node-dir DIR] [--bin DIR] [--ring S] [--keep DIR]

- `--listen` is where the hub listens: this Mac's address on the network the node reaches
  it on (its Nebula address), and a free port.
- `--node` is the node's ssh host, and `--node-dir` its install directory
  (`~/ringback-speech` by default). The node runs there with the check's token until the end,
  writing `line-check.log`, whose last lines the check keeps and then deletes.
- `--bin` is the folder there with the node's binary, to try a build beside the installed one.
- `--keep DIR` keeps the hub's, agent's and node's logs, the agent's state and the call's
  transcript.

Results on 2026-11-06, two runs, the model on the line Opus 5.5, by Claude Code's default: all 18
checks in about 65 s each. The voice was silent through the pause in your sentence, the model
having written `[wait]`, and answered 1.41–1.49 s after you finished. The "Mm-hmm." was written
down as "Mammal." both times, so the word list didn't catch it; the model read it in context and
carried on, the voice speaking again 1.75–1.9 s after you stopped, from the start of its sentence.
The interruption was answered 1.34–1.59 s after you stopped, and the rest of the reply was dropped.
Your side is Kokoro, not a person, so none of this says how well it reads a real pause, and the
word list hasn't been seen to work with real audio.

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
