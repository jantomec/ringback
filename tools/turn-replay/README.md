# Turn replay

Could the call's model tell when you've finished speaking, from your words and the conversation?
This replays the turns of real calls through a model and compares what it chooses with what you
did. It doesn't touch a live call.

For each turn the speech node closed, the model gets the conversation as you'd heard it by then
(a reply you cut off shows only up to where you cut it, and a reply you never heard not at all)
and your words, and answers REPLY, WAIT or CONTINUE. Each turn is asked with and without Smart
Turn's score, which is how much intonation adds (`--hints`: `none`, `score`, or `low`, which says
only "They sound unfinished." below 0.3). `score.py` compares the answers with labels you make
by hand, by reading each turn and what you said next: had you more to say on that thought?

It needs the speech node's log, the agent's log and the calls' transcripts. They're private, so
keep them and the results out of this repository:

    ssh <speech node> 'journalctl --user -u ringback-speech --since "<date> 07:00" --no-pager -o short-iso' > speech.txt
    cp ~/Library/Logs/ringback-agent.log agent.txt
    python3 tools/turn-replay/replay.py --speech speech.txt --agent agent.txt \
        --calls <project>/.ringback/calls ... --out results.jsonl --since 07:00:00
    python3 tools/turn-replay/score.py results.jsonl labels.json

Take the agent's log from the day of the speech log: it has no dates, so `--since` is a time of
day. `--dry-run 3` prints three prompts and asks nothing. It uses your Claude subscription:
392 calls took 215 s, eight at a time.

Results on 2026-11-06, from 98 turns of 8 calls by one speaker, a non-native one who pauses
mid-sentence to find words. By hand, 50 of the turns were unfinished, 48 finished, and 11 of
those were short answers. Waiting on an unfinished turn is right; waiting on a finished one is a
stall, which a real call would end with a time limit.

| Who decides | Unfinished turns it waited on | Finished turns it stalled on | Short answers stalled |
|---|---|---|---|
| Smart Turn alone, as now | 7 of 50 | 2 of 48 | |
| Haiku, words only | 33 of 50 | 12 of 48 | 1 of 11 |
| Sonnet, words only | 40 of 50 | 8 of 48 | 1 of 11 |
| Sonnet, with Smart Turn's score | 37 of 50 | 6 of 48 | 1 of 11 |
| Sonnet, words only, short-answer rule added | 41 of 50 | 9 of 48 | 0 of 11 |
| Sonnet, the same, told when the score is under 0.3 | 39 of 50 | 9 of 48 | 0 of 11 |
| Opus, words only, short-answer rule added | 36 of 50 | 6 of 48 | 2 of 11 |

Smart Turn's score alone separated the turns barely better than chance (0.58, where 0.5 is
chance), and giving it to the model, whole or only when very low, didn't help.

The labels are one reader's, made after seeing the first run's answers for some turns, and the
short-answer rule was tuned on these same turns, so these numbers flatter it. Each answer is a
one-shot `claude -p` with start-up and no briefing, so the times here don't say how fast it would
be in a call's session: `tools/reply-speed` measures that. The conversation holds the replies the
voice gave to fragments, which wouldn't exist if it had waited.
