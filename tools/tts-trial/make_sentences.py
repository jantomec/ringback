#!/usr/bin/env python3
"""Writes sentences.json, the trial's test text. Written for this trial, as a Claude Code session
might report on a phone call; nothing here comes from anyone's calls.

Each sentence has:
  id, kind   kind is short, yes_no, wh, number, path or run
  text       what a session would actually write
  spoken     (number and path only) the same words as ringback's front end reads them
             (speech/README.md, "Words sherpa-onnx would drop"), so a model can be tried with a
             rewriting stage in front of it
  keys       (number and path only) what must come back from speech-to-text: a list of items,
             each a list of accepted forms, which the rough check looks for. "stdin" isn't one:
             Parakeet can't spell it for any voice, so it would measure Parakeet
"""
import json
import pathlib

SHORT = ["Okay.", "Done.", "Sure, go ahead.", "Yes.", "No, not yet.", "Got it, thanks."]

YES_NO = [
    "Should I push this to main?",
    "Do you want me to run the tests again?",
    "Is it safe to apply the migration now?",
    "Can I delete the old branch?",
]

WH = [
    "Which branch should I use?",
    "What do you want me to do with the old config file?",
    "Where should the new script go?",
    "How many retries do you want?",
]

NUMBER = [
    ("All 142 tests pass, and the run took 38 seconds.",
     "All one hundred forty two tests pass, and the run took thirty eight seconds.",
     [["142", "one hundred forty two", "one hundred and forty two", "one forty two"],
      ["38", "thirty eight"]]),
    ("Each build costs about $4.75, so roughly $1,200 a month.",
     "Each build costs about four dollars seventy five, so roughly one thousand two hundred dollars a month.",
     [["4.75", "4 75", "four dollars seventy five", "four dollars and seventy five", "four seventy five"],
      ["1200", "1 200", "twelve hundred", "one thousand two hundred", "1 thousand 200"]]),
    ("Coverage went up from 87.5% to 91%.",
     "Coverage went up from eighty seven point five percent to ninety one percent.",
     [["87.5", "87 5", "eighty seven point five"], ["91", "ninety one"]]),
    ("I upgraded Postgres from 16.4 to 17.2, and Node to version 22.11.0.",
     "I upgraded Postgres from sixteen point four to seventeen point two, and Node to version twenty two point eleven point zero.",
     [["16.4", "16 4", "sixteen point four"], ["17.2", "17 2", "seventeen point two"],
      ["22.11.0", "22 11 0", "twenty two point eleven point zero", "twenty two point eleven point oh"]]),
    ("The response time dropped from 1.8 seconds to 350 ms.",
     "The response time dropped from one point eight seconds to three hundred fifty milliseconds.",
     [["1.8", "1 8", "one point eight"],
      ["350", "three hundred fifty", "three hundred and fifty", "three fifty"]]),
    ("The image is 2.1 GB, and the server has 64 GB of RAM.",
     "The image is two point one gigabytes, and the server has sixty four gigabytes of RAM.",
     [["2.1", "2 1", "two point one"], ["64", "sixty four"]]),
    ("The job started at 12:30 and finished on March 3rd, 2026.",
     "The job started at twelve thirty and finished on March third, twenty twenty six.",
     [["12:30", "12.30", "12 30", "twelve thirty"], ["3rd", "third", "3"],
      ["2026", "twenty twenty six", "2 0 2 6", "two thousand twenty six", "two thousand and twenty six"]]),
]

PATH = [
    ("I fixed the bug in voice_server.py at line 214.",
     "I fixed the bug in voice server dot pie at line two hundred fourteen.",
     [["voice server", "voiceserver", "voice_server", "voice underscore server"], ["py", "pie", "pi"],
      ["214", "two hundred fourteen", "two hundred and fourteen", "two fourteen"]]),
    ("The config lives in src/config/settings.toml.",
     "The config lives in source slash config slash settings dot toml.",
     [["src", "source", "s r c", "sirc"], ["config"], ["settings"], ["toml", "tommel", "tom l", "t o m l", "tamil", "tomel"]]),
    ("getUserById now returns null instead of throwing.",
     "get user by ID now returns null instead of throwing.",
     [["get user by", "getuserby", "get userby"], ["id", "i d"], ["null"]]),
    ("The handleAuthCallback function in auth_middleware.ts was calling next twice.",
     "The handle auth callback function in auth middleware dot T S was calling next twice.",
     [["handle auth callback", "handleauthcallback", "handle off callback"],
      ["auth middleware", "off middleware", "authmiddleware", "auth underscore middleware"], ["ts", "t s", "typescript"]]),
    ("Set MAX_RETRY_COUNT to 5 in the .env file.",
     "Set max retry count to five in the dot env file.",
     [["max retry count", "maxretrycount", "max underscore retry underscore count"], ["5", "five"], ["env", "e n v", "nv", "n v", "environment"]]),
    ("I ran npm run build, and then git push origin main.",
     "I ran N P M run build, and then git push origin main.",
     [["npm", "n p m"], ["run build"], ["git push origin main"]]),
    ("The API returns JSON over HTTPS, and the CLI reads it from stdin.",
     "The A P I returns Jason over H T T P S, and the C L I reads it from standard in.",
     [["api", "a p i"], ["json", "jason"], ["https", "h t t p s"], ["cli", "c l i"]]),
    ("The CI failed because the GPU runner has no CUDA driver.",
     "The C I failed because the G P U runner has no CUDA driver.",
     [["ci", "c i"], ["gpu", "g p u"], ["cuda", "kuda", "cooter", "coda"]]),
    ("The XAML view uses a two-way binding, though the pre-commit hook still complains.",
     "The zammel view uses a two way binding, though the pre commit hook still complains.",
     [["xaml", "zammel", "zamel", "zamal", "x a m l"], ["two way"], ["though"], ["pre commit", "precommit"]]),
]

RUN = [
    "I finished the changes you asked for this morning.",
    "The login page now remembers where you were before you signed in.",
    "It took a little longer than I expected, because the old code was spread across three files.",
    "I moved all of it into one place and removed what was no longer used.",
    "While I was there, I noticed the error messages were hard to understand.",
    "I rewrote them in plain language, so people can tell what went wrong.",
    "The tests all pass on my side.",
    "I also tried it by hand in the browser, and it behaved as it should.",
    "There is one thing I would like you to look at when you have a moment.",
    "The settings page still uses the old layout, and it looks a bit out of place next to the new one.",
    "I didn't change it, because you said to keep this work small.",
    "If you want, I can do that next, and it should only take a few minutes.",
    "Another option is to leave it as it is until the design is settled.",
    "Either way, nothing is blocking the release.",
    "I haven't pushed anything yet, so you can still change your mind.",
    "The commit is ready, and the message describes what changed and why.",
    "When you say so, I will push it and open the pull request.",
    "After that, I can start on the report you mentioned yesterday.",
    "I read through your notes, and they cover most of what it needs.",
    "A few of the figures are missing, so I will ask you about those as I go.",
    "I don't think any of this should take more than an hour.",
    "Let me know if you would rather do it in a different order.",
    "Otherwise, I will carry on with the plan we agreed on.",
    "That's everything for now.",
]


def main():
    out = []
    def add(kind, items):
        for i, it in enumerate(items, 1):
            entry = {"id": f"{kind}-{i:02d}", "kind": kind}
            if isinstance(it, str):
                entry["text"] = it
            else:
                entry["text"], entry["spoken"], entry["keys"] = it
            out.append(entry)
    add("short", SHORT)
    add("yes_no", YES_NO)
    add("wh", WH)
    add("number", NUMBER)
    add("path", PATH)
    add("run", RUN)
    path = pathlib.Path(__file__).with_name("sentences.json")
    path.write_text(json.dumps(out, indent=1, ensure_ascii=False) + "\n")
    print(len(out), "sentences,", sum(1 for s in out if "spoken" in s), "with a spoken form")


main()
