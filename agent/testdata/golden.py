"""What the Python voice server says, for the agent's tests to compare with: the texts the
agent took over (texts.go) and the Python functions' answers for sample inputs
(golden.json).

    uv run --project voice python agent/testdata/golden.py texts | gofmt > agent/texts.go
    uv run --project voice python agent/testdata/golden.py golden > agent/testdata/golden.json

Run from the repo root, with the voice server's environment. texts.go was made this way once
and is the agent's own since; golden.json is made again whenever the Python changes, and
`go test` in agent/ checks the agent against it.
"""

import asyncio
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "voice"))

from ringback_voice import call_tools, claude_code, haiku, instructions, sessions  # noqa: E402
from ringback_voice import __main__ as voice_main  # noqa: E402

TEXTS = [
    ("rules", instructions.RULES, "How the voice talks on the phone, for every call."),
    ("sessionRules", instructions.SESSION, "What the voice can do for a session that listens."),
    ("noSession", instructions.NO_SESSION, "A call with no session behind it."),
    ("notListening", instructions.NOT_LISTENING, "A call back to a session that isn't listening any more."),
    ("standIn", instructions.STAND_IN, "The report of a call with nothing to report."),
    ("calledBackLead", instructions.CALLED_BACK, "The lead of a call back's briefing."),
    ("calledBackTalked", instructions.CALLED_BACK_TALKED,
     "The lead of a call back of a call the developer answered, or called themselves."),
    ("movedOn", instructions.MOVED_ON, ""),
    ("settledLead", instructions.SETTLED, ""),
    ("noBriefing", instructions.NO_BRIEFING,
     "An earlier call made from the app's Contacts before the session had written a briefing."),
    ("calledNow", instructions.CALLED_NOW,
     "The lead of the briefing of a call that was waiting for the developer's work hours, which they\n"
     "// called from the app."),
    ("calledContact", instructions.CALLED_CONTACT, "The lead of a call the developer makes to a session from the app's Contacts."),
    ("contactBriefed", instructions.CONTACT_BRIEFED, ""),
    ("contactHeard", instructions.CONTACT_HEARD, ""),
    ("contactNone", instructions.CONTACT_NONE, ""),
    ("ack", claude_code.ACK, "Said at once when Haiku reaches for a tool before saying anything."),
    ("heldText", claude_code.HELD, "What pass_to_session answers: the message waits until it's heard."),
    ("answerHeld", claude_code.ANSWER_HELD, "What answer_permission and answer_question answer."),
    ("handOverHeld", claude_code.HAND_OVER_HELD, "What better_model answers."),
    ("opusAlready", claude_code.OPUS_ALREADY, ""),
    ("handOver", claude_code.HAND_OVER, "The note that starts Opus's first turn, which it speaks first."),
    ("opening", haiku.OPENING,
     "The first turn. A call for a session asks for it while the phone rings, but its reply is\n"
     "// spoken only once you've answered, so it says what's true when the reply is heard."),
    ("pickedUp", haiku.PICKED_UP, "The first turn of a call back: you called, so the voice picks up."),
    ("nothingYet", haiku.NOTHING_YET, "What a tool call in the opening answers: you haven't said anything yet."),
    ("noise", voice_main.NOISE,
     "Sent to Haiku when something stopped the voice but no words were recognised (see\n"
     "// \"Interruptions\" in docs/design.md): the hub's turn with no words."),
]


def go_string(text: str) -> str:
    if "`" not in text and "\r" not in text:
        return "`" + text + "`"
    return json.dumps(text, ensure_ascii=False)


def texts() -> None:
    print("// What the voice is told, and what its tools answer: the voice server's texts, word for word")
    print("// (voice/ringback_voice: instructions.py, claude_code.py, haiku.py, __main__.py and")
    print("// call_tools.py). Made by testdata/golden.py.")
    print()
    print("package main")
    print()
    print("const (")
    for name, text, comment in TEXTS:
        if comment:
            print(f"\t// {comment}")
        print(f"\t{name} = {go_string(text)}")
    print(")")
    print()
    print("// toolsJSON are the voice's tools as its MCP server lists them (call_tools.py).")
    print(f"const toolsJSON = {go_string(json.dumps(call_tools.TOOLS, indent=2, ensure_ascii=False))}")


class Stub:
    """A Haiku that only holds its events, for the language-model service's own methods."""

    def __init__(self, model: str = "haiku"):
        self.model = model
        self.events = asyncio.Queue()
        self.first_words = None
        self.used = False

    @property
    def name(self) -> str:
        return self.model.capitalize()


def permission_request() -> dict:
    return sessions.permission({
        "tool": "Bash", "description": "Push the branch", "input": '{\n  "command": "git push"\n}',
        "context": "I fixed the parser.", "project": "demo", "cwd": "/tmp/demo", "subject": "Permission to run",
    })  # fmt: skip


def question_request() -> dict:
    return sessions.questions({
        "questions": [
            {"question": "Where should the fix go?", "header": "Branch", "multiSelect": False,
             "options": [{"label": "A new branch", "description": "fix-parser"}, {"label": "Main"}]},
            {"question": "Which checks to run?", "header": "Checks", "multiSelect": True,
             "options": [{"label": "Unit tests"}, {"label": "Lint", "description": "ruff"}]},
        ],
        "context": "", "project": "demo", "cwd": "/tmp/demo", "subject": "2 questions: Branch, Checks",
    })  # fmt: skip


def stream(text: str) -> list[dict]:
    """Haiku's events for a text block, a word at a time."""
    words = text.split(" ")
    events = [{"type": "stream_event", "event": {"type": "content_block_start", "content_block": {"type": "text"}}}]
    for n, word in enumerate(words):
        piece = word + (" " if n < len(words) - 1 else "")
        events.append({"type": "stream_event", "event": {"type": "content_block_delta", "delta": {"type": "text_delta", "text": piece}}})
    return events


TOOL_START = {"type": "stream_event", "event": {"type": "content_block_start", "content_block": {"type": "tool_use"}}}
RESULT = {"type": "result", "is_error": False}

# Replies as Haiku writes them: its events, with the tool calls it makes among them ("tool"),
# for your words in that turn ("said"). Each is checked for what's spoken, what each tool
# answers, how many words of the reply you must hear before what it held goes, and the notes
# when you cut the voice off at each point.
REPLIES = [
    {"name": "announced, then passed", "said": "Tell it to push the branch.",
     "steps": [*stream("Sure, I'll pass that on."), {"tool": "pass_to_session", "input": {"message": "Push the branch."}}, RESULT]},
    {"name": "passed before saying anything", "said": "Tell it to push.",
     "steps": [TOOL_START, {"tool": "pass_to_session", "input": {"message": "Push."}},
               *stream("Okay, I'll tell it to push. Anything else?"), RESULT]},
    {"name": "said again after the tool", "said": "Push it.",
     "steps": [*stream("Passing that on now."), TOOL_START, {"tool": "pass_to_session", "input": {"message": "Push it."}},
               *stream("I've passed it on."), RESULT]},
    {"name": "permission allowed", "said": "Yes, go ahead.", "asks": True,
     "steps": [*stream("Okay, I'll let it run that."), TOOL_START,
               {"tool": "answer_permission", "input": {"permission": "perm", "allow": True, "said": "Yes, go ahead."}}, RESULT]},
    {"name": "permission unknown and malformed", "said": "Yes.", "asks": True,
     "steps": [TOOL_START, {"tool": "answer_permission", "input": {"permission": "zzzz", "allow": True}},
               {"tool": "answer_permission", "input": {"permission": "perm", "allow": "yes"}},
               *stream("Okay."), RESULT]},
    {"name": "questions answered", "said": "A new branch, and both checks.", "asks": True,
     "steps": [*stream("Got it, a new branch with both checks."), TOOL_START,
               {"tool": "answer_question", "input": {"question": "ques", "answers": [
                   {"question": "Where should the fix go?", "answer": "a new branch"},
                   {"question": "checks", "answer": "unit tests, lint"}], "said": "A new branch, and both checks."}},
               RESULT]},
    {"name": "questions half answered", "said": "Main.", "asks": True,
     "steps": [TOOL_START, {"tool": "answer_question", "input": {"question": "ques", "answers": [{"question": "Where should the fix go?", "answer": "Main"}]}},
               *stream("Which checks should it run?"), RESULT]},
    {"name": "questions in order", "said": "Main, and lint.", "asks": True,
     "steps": [*stream("Main, with lint."), TOOL_START,
               {"tool": "answer_question", "input": {"question": "ques", "answers": [{"question": "1", "answer": "main"}, {"question": "2", "answer": "Lint"}], "said": "Main, and lint."}},
               RESULT]},
    {"name": "a better model", "said": "Can I talk to a smarter model?",
     "steps": [*stream("Sure, I'll put Opus on."), TOOL_START, {"tool": "better_model", "input": {}}, RESULT]},
    {"name": "Opus asked for a better model", "said": "A better model, please.", "model": "opus",
     "steps": [TOOL_START, {"tool": "better_model", "input": {}}, *stream("I'm Opus already."), RESULT]},
    {"name": "not listening", "said": "Tell it to stop.", "listening": False,
     "steps": [TOOL_START, {"tool": "pass_to_session", "input": {"message": "Stop."}}, *stream("I can't pass that on."), RESULT]},
    {"name": "no message", "said": "Tell it.",
     "steps": [TOOL_START, {"tool": "pass_to_session", "input": {"message": "  "}}, *stream("What should I tell it?"), RESULT]},
    {"name": "short answer after the ack", "said": "Yes.",
     "steps": [TOOL_START, {"tool": "pass_to_session", "input": {"message": "Yes."}}, *stream("Done."), RESULT]},
    {"name": "the opening", "said": "",
     "steps": [*stream("Hi, the parser fix is done. It needs your yes to push."), RESULT]},
]


async def replay(reply: dict) -> dict:
    """Runs a reply through the voice server's language-model service, as far as it goes
    without Pipecat: what's spoken, what the tools answer, what each held thing needs heard,
    and what's passed and noted when you cut the voice off after each word."""
    sent = []
    link = sessions.Link(lambda text: sent.append(text))
    link.listening = reply.get("listening", True)
    if reply.get("asks"):
        link.asks["perm"] = sessions.Ask("perm", "permission", "s1", permission_request())
        link.asks["ques"] = sessions.Ask("ques", "question", "s1", question_request())
    service = claude_code.ClaudeCodeLLMService("rules", None, lambda line: None, link)
    service._haiku = Stub(reply.get("model", "haiku"))
    service._said = reply["said"]
    service._turn = 2
    service._unfinished = 1
    spoken, answers = [], []
    for step in reply["steps"]:
        if "tool" in step:
            answers.append(service._tool(step["tool"], step["input"]))
            continue
        service._haiku.events.put_nowait(step)
        text = await service._next_text()
        if text:
            spoken.append(text)
    held = [{"kind": h["kind"], "text": h["text"], "words": h["words"], "needed": service._needed(h)} for h in service._held]
    reply_text = service._reply
    # Cut off after each word of the reply: the notes for the next turn, and what's passed.
    cuts = []
    words = reply_text.split()
    for n in range(len(words) + 1):
        heard = " ".join(words[:n])
        copy = claude_code.ClaudeCodeLLMService("rules", None, lambda line: None, link)
        copy._haiku = service._haiku
        copy._reply, copy._ack, copy._turn = service._reply, service._ack, service._turn
        copy._held = [dict(h, give=lambda: None) for h in service._held]
        copy._timeline = type("T", (), {"cut_off": heard})()
        note = copy._cut_off_note()
        got = copy._heard_words
        note += copy._pass(lambda h: got is None or copy._needed(h) <= got, dropped="you cut the voice off first")
        cuts.append({"heard": heard, "note": note, "left": len(copy._held)})
    return {
        "name": reply["name"], "spoken": spoken, "reply": reply_text, "answers": answers, "held": held,
        "unsaid": service._unsaid, "cuts": cuts, "sent": sent,
    }  # fmt: skip


def golden() -> None:


    def call(**kw) -> sessions.Call:
        base = dict(id="c" * 32, session="s1", project="demo", cwd="", subject="Parser fix", briefing="b",
                    placed=time.monotonic())  # fmt: skip
        base.update(kw)
        return sessions.Call(**base)

    def link(path=None):
        l = sessions.Link(lambda text: None)
        l.transcript_path = path
        return l

    outcomes = []
    for name, c, transcript in [
        ("answered", call(answered_at=1.0, lasted=75.4), ".ringback/calls/x.md"),
        ("answered, no transcript", call(answered_at=1.0, lasted=44.5), None),
        ("answered, no audio", call(answered_at=1.0), None),
        ("answered, no subject", call(subject="", answered_at=1.0, lasted=120), None),
        ("contact", call(contact=True, answered_at=1.0, lasted=61), ".ringback/calls/y.md"),
        ("contact, no audio", call(contact=True, answered_at=1.0), None),
        ("back", call(back=call(), answered_at=1.0, lasted=12.5), ".ringback/calls/z.md"),
        ("back, no subject", call(subject="", back=call(), answered_at=1.0, lasted=13.5), None),
        ("back, not connected", call(back=call()), None),
        ("now", call(back=call(), now=True, answered_at=1.0, lasted=3600), None),
        ("now, not connected", call(back=call(), now=True), None),
    ]:
        outcomes.append({"name": name, "subject": c.subject, "answered": c.answered_at is not None,
                         "lasted": c.lasted, "contact": c.contact, "back": c.back is not None, "now": c.now,
                         "transcript": transcript, "text": sessions.outcome(c, transcript)})  # fmt: skip
    restarts = []
    for name, c in [
        ("ringing", call(link=link())),
        ("ringing, no subject", call(subject="", link=link())),
        ("answered", call(answered_at=1.0, link=link(".ringback/calls/a.md"))),
        ("answered, no transcript", call(answered_at=1.0, link=link())),
        ("contact", call(contact=True, link=link(".ringback/calls/b.md"))),
        ("back", call(back=call(), link=link())),
        ("now", call(back=call(), now=True, link=link(".ringback/calls/c.md"))),
    ]:
        restarts.append({"name": name, "subject": c.subject, "answered": c.answered_at is not None,
                         "contact": c.contact, "back": c.back is not None, "now": c.now,
                         "transcript": c.link.transcript_path, "text": sessions.restarted(c)})  # fmt: skip

    seconds = [0, 0.4, 0.5, 1.5, 2.5, 29.5, 30, 44.5, 59.5, 60, 61, 89.5, 90, 119.5, 150.5, 3599.5, 3600, 7290.5]
    agos = [0, 29, 30, 31, 89, 90, 91, 150, 3569, 3570, 3600, 5399, 5400, 9000, 86400]
    leads = []
    for since, settled, talked in [(None, [], None), (("Newer briefing.", 600), [], None),
                                   (None, ["permission abcd", "question efgh"], None), (None, [], 1200),
                                   (("Newer.", 30), ["permission abcd"], 5)]:  # fmt: skip
        leads.append({"kind": "back", "briefing": "  The briefing.\n", "ago": 3000, "since": since, "settled": settled,
                      "talked": talked, "text": instructions.called_back("  The briefing.\n", 3000, since, settled, talked)})
    leads.append({"kind": "back", "briefing": " ", "ago": 10, "since": None, "settled": [], "talked": None,
                  "text": instructions.called_back(" ", 10)})
    leads.append({"kind": "now", "briefing": "The briefing.\n", "text": instructions.called_now("The briefing.\n")})
    for briefing, ago, heard in [(None, 0, None), ("  ", 0, None), ("The latest.", 700, None), ("The latest.", 700, 300)]:
        leads.append({"kind": "contact", "briefing": briefing, "ago": ago, "heard": heard,
                      "text": instructions.called_contact(briefing, ago, heard)})  # fmt: skip
    rules = []
    for report, session, listening in [(None, False, True), ("A report.", False, True), ("Brief.\n", True, True), ("Brief.", True, False)]:
        rules.append({"report": report, "session": session, "listening": listening,
                      "text": instructions.instructions(report, session=session, listening=listening)})  # fmt: skip

    p = sessions.Ask("abcd", "permission", "s1", permission_request())
    q = sessions.Ask("efgh", "question", "s1", question_request())
    bare = sessions.Ask("ijkl", "permission", "s1", sessions.permission({"tool": "Read"}))
    asks = []
    for a in (p, q, bare):
        asks.append({"id": a.id, "kind": a.kind, "request": a.request, "details": a.details(), "note": a.note(),
                     "briefing": a.briefing()})  # fmt: skip
    answered = []
    for a, outcome, said, ans in [(p, "allow", "Yes, go ahead.", None), (p, "deny", None, None),
                                  (q, "answered", "Main.", {"Where should the fix go?": "Main", "Which checks to run?": "Lint"}),
                                  (q, "none", None, None)]:  # fmt: skip
        a2 = sessions.Ask(a.id, a.kind, a.session, a.request)
        a2.settle(outcome, said, ans)
        answered.append({"kind": a.kind, "request": a.request, "id": a.id, "outcome": outcome, "said": said,
                         "answers": ans, "text": a2.answered()})  # fmt: skip
    matches = []
    for given in [
        [{"question": "Where should the fix go?", "answer": "a new branch"}, {"question": "checks", "answer": "unit tests, lint"}],
        [{"question": "x", "answer": "main"}, {"question": "y", "answer": "Lint"}],
        [{"question": "Where should the fix go?", "answer": "Somewhere else"}],
        [{"question": "Branch", "answer": "  MAIN  "}, {"question": "Which checks to run?", "answer": "lint, docs"}],
        "not a list", [], [{"question": "Where should the fix go?", "answer": ""}, {"answer": "Lint"}],
    ]:
        got, missing = q.match(given)
        matches.append({"given": given, "answers": [[k, v] for k, v in got.items()], "missing": missing})

    texts_in = ["Okay, I'll", "Sure.", "Hello there. How are", "Okay, I will", "Dr. Smith is here. Yes", "It costs 3.5 dollars. Ok",
                "Done! Next", "Yes; and", "e.g. this is it. And", 'I said "stop." Then', "Okay… so", "The U.S. is big. Yes",
                "Okay, I'll tell it to push. Anything else?", "I'll tell it to push. Anything else?",
                "Got it, a new branch with both checks.", "What should I tell it?", "Right? Yes.", "Wait... what"]  # fmt: skip
    from pipecat.utils.string import match_endofsentence

    sentences = [{"text": t, "end": match_endofsentence(t)} for t in texts_in]
    reprs = ["plain", "it's", 'say "hi"', "both ' and \"", "line\nbreak\ttab", "back\\slash", "uni … ü", "\x07bell"]
    replies = [asyncio.run(replay(r)) for r in REPLIES]
    json.dump({
        "outcomes": outcomes, "restarts": restarts,
        "durations": [{"seconds": s, "text": sessions.duration(s)} for s in seconds],
        "whileAgo": [{"seconds": s, "text": instructions.while_ago(s)} for s in agos],
        "leads": leads, "instructions": rules, "asks": asks, "answered": answered, "matches": matches,
        "plain": [{"text": t, "plain": sessions.plain(t)} for t in ["Where should the fix go?", "  A New-Branch!  ", "Ünïcode ok", ""]],
        "cut": [{"text": t, "most": m, "cut": sessions.cut(t, m)} for t, m in [("short", 10), ("exactly10!", 10), ("one too long", 11), ("ü" * 5, 3)]],
        "heldFields": [{"held_until": h, "fields": sessions.held_fields(h)} for h in [None, "", sessions.ON_HOLD, "2026-11-03T08:15:00+01:00"]],
        "until": [{"held_until": h, "text": sessions.until(h)} for h in [sessions.ON_HOLD, "2026-11-03T08:15:00+01:00"]],
        "sentences": sentences, "reprs": [{"text": t, "repr": repr(t)} for t in reprs],
        "replies": replies,
        "scripts": {r["name"]: {"said": r["said"], "model": r.get("model", ""), "listening": r.get("listening", True),
                                "asks": r.get("asks", False), "steps": r["steps"]} for r in REPLIES},
    }, sys.stdout, ensure_ascii=False, indent=1)  # fmt: skip
    print()


if __name__ == "__main__":
    {"texts": texts, "golden": golden}[sys.argv[1]]()
