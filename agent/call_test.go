package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const answeredTurn = "[The developer has answered the call.]"

// A call a session places: Haiku writes the opening while the phone rings, the voice speaks
// it once you've answered and the audio connected, what Haiku passes on waits until you've
// heard it announced, and is dropped if you cut the voice off first.
func TestCallHeldUntilHeard(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("s1-session")
	sessions, _ := hub.wait(0, 3*time.Second, func(m map[string]any) bool {
		list, _ := m["sessions"].([]any)
		return m["type"] == "sessions" && len(list) == 1
	})
	if sessions == nil {
		t.Fatal("the hub didn't hear which sessions listen")
	}
	entry := sessions["sessions"].([]any)[0].(map[string]any)
	check(t, "the session listed", entry["session_id"] == "s1-session" && entry["project"] == "demo" && entry["since"] != nil, entry)

	a.setReplies(
		fakeReply{Match: answeredTurn, Steps: []fakeStep{say("Hi, the parser fix is done.")}},
		fakeReply{Match: "push the branch", Steps: []fakeStep{say("Sure, I'll pass that on."), tool("pass_to_session", map[string]any{"message": "Push the branch."})}},
		fakeReply{Match: "run the tests", Steps: []fakeStep{say("Okay, I'll pass that on too."), tool("pass_to_session", map[string]any{"message": "Run the tests."})}},
		fakeReply{Match: "Never mind", Steps: []fakeStep{say("Alright.")}},
	)
	callID, answer := a.placeCall("s1-session", "Parser fix", "The parser fix is done; it needs a push.")
	check(t, "the mod hears it rings", len(answer) == 1 && answer["ok"] == true, answer)
	place, _ := hub.wait(0, time.Second, is("place"))
	check(t, "place carries the call", place["session_id"] == "s1-session" && place["project"] == "demo" &&
		place["subject"] == "Parser fix" && place["question"] == false && len(callID) == 32, place)
	check(t, "Haiku is asked for the opening while it rings", a.waitTurn(1, 5*time.Second) == answeredTurn)
	start := a.starts()[0]
	check(t, "Haiku starts on a new session", start["model"] == "haiku" && start["how"] == "--session-id", start)
	check(t, "with the session's rules and briefing", strings.HasPrefix(start["system"].(string), rules+sessionRules) &&
		strings.HasSuffix(start["system"].(string), "The parser fix is done; it needs a push.\n"))
	check(t, "and the session's tools", slices.Equal(anyStrings(start["tools"]), []string{"pass_to_session", "answer_permission", "answer_question", "better_model"}), start["tools"])

	opening := a.answer(callID)
	check(t, "the opening", opening == "Hi, the parser fix is done.", opening)
	a.waitLog("Haiku: first words after", time.Second)
	a.played(callID, opening)

	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Tell it to push the branch."})
	check(t, "your words go to Haiku as they are", a.waitTurn(2, 5*time.Second) == "Tell it to push the branch.")
	text, _ := hub.reply(callID, 2, 5*time.Second)
	check(t, "reply 2", text == "Sure, I'll pass that on.", text)
	a.waitLog("Haiku → session, held until heard: 'Push the branch.'", 2*time.Second)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "Sure, I'll pass"})
	time.Sleep(100 * time.Millisecond)
	check(t, "nothing passed before you heard it announced", !hasKind(a.collect("s1-session"), "message"))
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "Sure, I'll pass that on."})
	passed := a.waitMessage("s1-session", "message", 2*time.Second)
	check(t, "passed once heard", passed["text"] == "Push the branch." &&
		regexp.MustCompile(`^\.ringback/calls/\d{4}-\d\d-\d\d-\d{6}\.md$`).MatchString(passed["transcript"].(string)), passed)
	hub.send(map[string]any{"type": "idle", "call_id": callID})

	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Also tell it to run the tests."})
	text, _ = hub.reply(callID, 3, 5*time.Second)
	check(t, "reply 3", text == "Okay, I'll pass that on too.", text)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "Okay, I'll"})
	hub.send(map[string]any{"type": "interrupted", "call_id": callID, "reply": 3, "heard": "Okay, I'll"})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Never mind."})
	want := "[You were interrupted. The developer heard your last reply up to: \"…Okay, I'll\"]\n" +
		"[Not passed to the session, because the developer cut you off before hearing you say so: \"Run the tests.\". " +
		"Pass it again only if they still want it.]\nNever mind."
	got := a.waitTurn(4, 5*time.Second)
	check(t, "cut off, Haiku hears what you heard and what wasn't passed", got == want, "\n got: ", got, "\nwant: ", want)
	text, _ = hub.reply(callID, 4, 5*time.Second)
	check(t, "reply 4", text == "Alright.", text)
	check(t, "what you didn't hear announced isn't passed", !hasKind(a.collect("s1-session"), "message"))

	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 75.4})
	outcome := a.waitMessage("s1-session", "outcome", 3*time.Second)
	path := passed["transcript"].(string)
	check(t, "the outcome", outcome["text"] == `The call about "Parser fix" was answered and lasted 1 min 15 s. Its transcript: `+path &&
		outcome["transcript"] == path, outcome)
	a.waitLog("finished: you talked 75.4 s", time.Second)
	transcript, _ := os.ReadFile(filepath.Join(a.project(), path))
	for _, line := range []string{
		"# Call: Parser fix\n", "## The briefing the voice had\n\nThe parser fix is done; it needs a push.\n",
		"**Voice:** Hi, the parser fix is done.\n", "**Developer:** Tell it to push the branch.\n",
		"**Voice:** Sure, I'll pass that on.\n", "**Passed to the session:** Push the branch.\n",
		"**Not passed to the session, you cut the voice off first:** Run the tests.\n",
		"**Note to the voice:** You were interrupted. The developer heard your last reply up to: \"…Okay, I'll\"\n",
		"**Developer:** Never mind.\n", "**Voice:** Alright.\n",
	} {
		check(t, "the transcript has "+line, strings.Contains(string(transcript), line))
	}
	ignore, _ := os.ReadFile(filepath.Join(a.project(), ".ringback", ".gitignore"))
	check(t, "git ignores the transcripts", string(ignore) == "*\n")
	eventually(t, "Haiku's process ends with the call", 5*time.Second, func() bool {
		for _, e := range a.claudeLog() {
			if e["exit"] == true {
				return true
			}
		}
		return false
	})
	a.calls.closing.Wait()
	projects := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects")
	left, _ := os.ReadDir(projects)
	kept := false
	for _, entry := range left {
		kept = kept || strings.Contains(entry.Name(), "ringback-voice-")
	}
	check(t, "what Claude Code kept of the call's session is deleted", !kept, left)
}

// Notes from the session wait for the voice's first words, then for it to be idle; what the
// session asks goes to the call you're on, and the voice's answer goes once you've heard it.
func TestNotesAndAsksOnACall(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("s2-session")
	a.setReplies(
		fakeReply{Match: answeredTurn, Steps: []fakeStep{say("Hi, it's pushed.")}},
		fakeReply{Match: "[The session: Pushed to main.]", Steps: []fakeStep{say("It says it's on main now.")}},
		fakeReply{Match: "Permission", Steps: []fakeStep{say("It wants to run the tests. Okay?")}},
	)
	callID, _ := a.placeCall("s2-session", "Pushed", "Pushed the branch.")
	a.answer(callID)
	_, news := a.mod("POST", "/sessions/s2-session/news", map[string]any{"text": "Pushed to main."})
	check(t, "news reaches the call you're on", news["live"] == true, news)
	time.Sleep(100 * time.Millisecond)
	check(t, "a note waits for the voice's first words", len(a.turns()) == 1, a.turns())
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	time.Sleep(100 * time.Millisecond)
	check(t, "and then for the voice to be idle", len(a.turns()) == 1, a.turns())
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "Hi, it's pushed."})
	hub.send(map[string]any{"type": "idle", "call_id": callID})
	check(t, "then it starts a reply", a.waitTurn(2, 5*time.Second) == "[The session: Pushed to main.]")
	text, _ := hub.reply(callID, 2, 5*time.Second)
	a.played(callID, text)

	status, asked := a.mod("POST", "/sessions/s2-session/permissions", map[string]any{
		"tool": "Bash", "description": "Run the tests", "input": "{\n  \"command\": \"go test\"\n}",
		"context": "I fixed the parser.", "project": "demo", "cwd": a.project(), "subject": "Permission to run a shell command",
	})
	id, _ := asked["permission"].(string)
	check(t, "a permission goes to the call you're on", status == 200 && asked["call"] == "live" &&
		regexp.MustCompile(`^[a-z]{4}$`).MatchString(id), asked)
	note := a.waitTurn(3, 5*time.Second)
	check(t, "as a note", note == "[The session asks, and waits for the answer.\nPermission "+id+": the session wants to use its "+
		"tool Bash. Run the tests\nIts input:\n{\n  \"command\": \"go test\"\n}\nWhat the session was doing, in its own last "+
		"words:\nI fixed the parser.\nPut it to the developer, then answer with answer_permission, once they've clearly said "+
		"yes or no.]", note)
	text, _ = hub.reply(callID, 3, 5*time.Second)
	a.played(callID, text)

	// Haiku writes on after the tool, unspoken, until the session has asked its next thing.
	writing := filepath.Join(t.TempDir(), "writing")
	a.setReplies(fakeReply{Match: "go ahead", Steps: []fakeStep{
		say("Okay, I'll let it run that."), tool("answer_permission", map[string]any{"permission": id, "allow": true, "said": "Yes, go ahead."}),
		{Wait: writing}, say("Anything else?"),
	}})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Yes, go ahead."})
	a.waitLog("Haiku answers the session, held until heard: permission "+id+": allow ('Yes, go ahead.')", 2*time.Second)
	_, verdict := a.mod("GET", "/sessions/s2-session/permissions/"+id+"?wait=0", nil)
	check(t, "pending until you've heard the answer", verdict["verdict"] == "pending" && verdict["said"] == nil, verdict)
	waited := make(chan map[string]any)
	go func() {
		_, got := a.mod("GET", "/sessions/s2-session/permissions/"+id+"?wait=10", nil)
		waited <- got
	}()
	time.Sleep(50 * time.Millisecond)
	heardAt := time.Now()
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "Okay, I'll let it run that."})
	select {
	case got := <-waited:
		check(t, "the long poll answers once heard", got["verdict"] == "allow" && got["said"] == "Yes, go ahead." &&
			time.Since(heardAt) < time.Second, got, time.Since(heardAt))
	case <-time.After(5 * time.Second):
		t.Fatal("the long poll didn't answer")
	}
	status, again := a.mod("POST", "/sessions/s2-session/permissions/"+id, map[string]any{"verdict": "deny"})
	check(t, "an answer is final", status == 409 && again["error"] == "answered already: allow", status, again)

	// Questions, answered with an option's label.
	status, asked = a.mod("POST", "/sessions/s2-session/questions", map[string]any{
		"questions": []any{map[string]any{"question": "Where should the fix go?", "header": "Branch", "multiSelect": false,
			"options": []any{map[string]any{"label": "A new branch", "description": "fix-parser"}, map[string]any{"label": "Main"}}}},
		"context": "", "project": "demo", "cwd": a.project(), "subject": "Question: Branch",
	})
	qid, _ := asked["question"].(string)
	check(t, "questions go to the call too", status == 200 && asked["call"] == "live", asked)
	os.WriteFile(writing, nil, 0o644)
	text, _ = hub.reply(callID, 4, 5*time.Second)
	check(t, "what Haiku wrote after the answer isn't spoken", text == "Okay, I'll let it run that.", text)
	paths, _ := filepath.Glob(filepath.Join(a.project(), ".ringback", "calls", "*.md"))
	written := ""
	if len(paths) == 1 {
		b, _ := os.ReadFile(paths[0])
		written = string(b)
	}
	said, gave, asks := strings.Index(written, "**Voice:** Okay, I'll let it run that.\n"),
		strings.Index(written, "**Answered the session:** permission "+id+": allow"), strings.Index(written, "**The session asks:** Question "+qid)
	check(t, "the transcript has what the voice said before the answer it gave, and that before the next ask",
		said >= 0 && said < gave && gave < asks, said, gave, asks, written)
	hub.send(map[string]any{"type": "idle", "call_id": callID})
	note = a.waitTurn(5, 5*time.Second)
	check(t, "as a note", strings.HasPrefix(note, "[The session asks, and waits for the answer.\nQuestion "+qid+", with 1 question(s)"), note)
	text, _ = hub.reply(callID, 5, 5*time.Second)
	a.played(callID, text)
	a.setReplies(fakeReply{Match: "new branch", Steps: []fakeStep{
		say("A new branch it is."), tool("answer_question", map[string]any{"question": qid,
			"answers": []any{map[string]any{"question": "Where should the fix go?", "answer": "a new branch"}}}),
	}})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "A new branch, please."})
	text, _ = hub.reply(callID, 6, 5*time.Second)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": text})
	_, answered := a.mod("GET", "/sessions/s2-session/questions/"+qid+"?wait=5", nil)
	answers, _ := answered["answers"].(map[string]any)
	check(t, "the option's label is the answer, with your words", answered["status"] == "answered" &&
		answers["Where should the fix go?"] == "A new branch" && answered["said"] == "A new branch, please.", answered)

	// A stop with no words while the voice waited for you goes unanswered: its reply (7) is
	// just its end, and Haiku isn't asked.
	hub.send(map[string]any{"type": "idle", "call_id": callID})
	saidN := func(n float64) func(map[string]any) bool {
		return func(m map[string]any) bool { return m["type"] == "said" && m["reply"] == n }
	}
	sayN := func(n float64) func(map[string]any) bool {
		return func(m map[string]any) bool { return m["type"] == "say" && m["reply"] == n }
	}
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "", "interrupted": false})
	ended, _ := hub.wait(0, 5*time.Second, saidN(7))
	check(t, "a stop with no words while the voice waited goes unanswered", ended != nil && hub.count(sayN(7)) == 0, ended)

	// One that cut the voice off has it carry on (reply 8), once in a row: the next goes
	// unanswered (reply 9).
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "", "interrupted": true})
	check(t, "an empty turn that cut the voice off has it carry on", a.waitTurn(7, 5*time.Second) == "["+noise+"]")
	hub.reply(callID, 8, 5*time.Second)
	hub.send(map[string]any{"type": "idle", "call_id": callID})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "", "interrupted": true})
	twice, _ := hub.wait(0, 5*time.Second, saidN(9))
	time.Sleep(200 * time.Millisecond)
	check(t, "a second one in a row goes unanswered", twice != nil && hub.count(sayN(9)) == 0 && len(a.turns()) == 7, twice)

	// A permission you don't answer: once the voice has finished and nothing has been heard for
	// a while (the hub's quiet), it asks again, once.
	_, asked = a.mod("POST", "/sessions/s2-session/permissions", map[string]any{"tool": "Write", "project": "demo", "cwd": a.project()})
	last := asked["permission"].(string)
	note = a.waitTurn(8, 5*time.Second)
	check(t, "a permission is put to the call", strings.HasPrefix(note, "[The session asks"), note)
	text, _ = hub.reply(callID, 10, 5*time.Second)
	a.played(callID, text)
	hub.send(map[string]any{"type": "quiet", "call_id": callID})
	check(t, "nothing heard: the voice asks again", a.waitTurn(9, 5*time.Second) == "["+reask+"]")
	text, _ = hub.reply(callID, 11, 5*time.Second)
	a.played(callID, text)
	hub.send(map[string]any{"type": "quiet", "call_id": callID})
	time.Sleep(200 * time.Millisecond)
	check(t, "but only once", len(a.turns()) == 9, a.turns())

	// A permission still waiting when you hang up gets none.
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 30.0})
	_, none := a.mod("GET", "/sessions/s2-session/permissions/"+last+"?wait=5", nil)
	check(t, "hung up without an answer: none", none["verdict"] == "none", none)
}

// The session's news that came to a call is heard once a reply given it has played to its end.
// Hung up before that, the session calls again with it; heard, it doesn't.
func TestUnheardNews(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("s3-session")
	a.setReplies(
		fakeReply{Match: "[The session: Tests pass.]", Steps: []fakeStep{say("It says the tests pass.")}},
		fakeReply{Match: "[The session: Merged.]", Steps: []fakeStep{say("It says it's merged, and the branch is gone.")}},
	)
	places := func() int { return hub.count(is("place")) }

	// Heard to the end, then hung up: no call.
	callID, _ := a.placeCall("s3-session", "Pushed", "Pushed the branch.")
	a.played(callID, a.answer(callID))
	_, news := a.mod("POST", "/sessions/s3-session/news", map[string]any{"text": "Tests pass."})
	check(t, "news reaches the call you're on", news["live"] == true, news)
	text, _ := hub.reply(callID, 2, 5*time.Second)
	a.played(callID, text)
	before := places()
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 20.0})
	time.Sleep(300 * time.Millisecond)
	check(t, "news heard to the end: no call again", places() == before, places()-before)

	// Hung up while the voice told it: the session calls again, with the news as its briefing.
	callID, _ = a.placeCall("s3-session", "Pushed", "Pushed the branch.")
	a.played(callID, a.answer(callID))
	a.mod("POST", "/sessions/s3-session/news", map[string]any{"text": "Merged."})
	hub.reply(callID, 2, 5*time.Second)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "It says"})
	from := hub.mark()
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 20.0})
	again, _ := hub.wait(from, 5*time.Second, is("place"))
	check(t, "hung up before hearing it: the session calls again", again != nil && again["subject"] == unheardSubject &&
		again["session_id"] == "s3-session" && again["call_id"] != callID, again)
	eventually(t, "its Haiku starts", 5*time.Second, func() bool { return len(a.starts()) == 3 })
	system := a.starts()[2]["system"].(string)
	check(t, "with the news you didn't hear as its briefing, and not the news you did",
		strings.HasSuffix(system, unheardBriefing+"\n\nMerged.\n") && !strings.Contains(system, "Tests pass."), system)
}

// Speaking once the voice has started on the session's news, as with a "bye" over it, makes
// hanging up your choice: the session doesn't call again. Noise that cuts the voice off
// doesn't count.
func TestSpokeOverNews(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("s3-session")
	a.setReplies(
		fakeReply{Match: "[The session: Tests pass.]", Steps: []fakeStep{say("It says the tests pass.")}},
		fakeReply{Match: "Okay, bye.", Steps: []fakeStep{say("Bye.")}},
		fakeReply{Match: noise, Steps: []fakeStep{say("It says the tests pass.")}},
	)
	places := func() int { return hub.count(is("place")) }
	// A call the voice is telling the news on, when you've heard its first words.
	telling := func() string {
		callID, _ := a.placeCall("s3-session", "Pushed", "Pushed the branch.")
		a.played(callID, a.answer(callID))
		a.mod("POST", "/sessions/s3-session/news", map[string]any{"text": "Tests pass."})
		hub.reply(callID, 2, 5*time.Second)
		hub.send(map[string]any{"type": "speaking", "call_id": callID})
		hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "It says"})
		return callID
	}
	spokeOver := "you spoke once the voice had started on the session's news, then hung up: it doesn't call again"

	// "Okay, bye." over it, and hung up before that turn came.
	before := places()
	callID := telling()
	hub.send(map[string]any{"type": "interrupted", "call_id": callID, "reply": 2, "heard": "It says"})
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 20.0})
	a.waitLog("call "+callID[:8]+": "+spokeOver, time.Second)
	time.Sleep(300 * time.Millisecond)
	check(t, "cut the news off and hung up: no call again", places()-before == 1, places()-before)

	// "Okay, bye.", and hung up while the voice said "Bye.".
	before = places()
	callID = telling()
	hub.send(map[string]any{"type": "interrupted", "call_id": callID, "reply": 2, "heard": "It says"})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Okay, bye.", "interrupted": true})
	text, _ := hub.reply(callID, 3, 5*time.Second)
	check(t, "the voice says bye", text == "Bye.", text)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 20.0})
	a.waitLog("call "+callID[:8]+": "+spokeOver, time.Second)
	time.Sleep(300 * time.Millisecond)
	check(t, "said bye over the news and hung up: no call again", places()-before == 1, places()-before)

	// Noise cut it off, the voice carried on, and you hung up without a word: it calls again.
	callID = telling()
	hub.send(map[string]any{"type": "interrupted", "call_id": callID, "reply": 2, "heard": "It says"})
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "", "interrupted": true})
	hub.reply(callID, 3, 5*time.Second)
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": "It says"})
	from := hub.mark()
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 20.0})
	again, _ := hub.wait(from, 5*time.Second, is("place"))
	check(t, "noise isn't you speaking: the session calls again", again != nil && again["subject"] == unheardSubject, again)
}

// The phone can offer a call again: one that ended unanswered stays placed, a new offer
// replaces the earlier one, and takes over the call's Haiku unless a voice has read it.
func TestOfferReplaced(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("s12-session")
	a.setReplies(fakeReply{Match: answeredTurn, Steps: []fakeStep{say("Hi there.")}})
	callID, _ := a.placeCall("s12-session", "Again", "Offered again.")
	ring := map[string]any{"type": "offer", "call_id": callID, "kind": "ring", "ringing": true}
	check(t, "an offer while it rings", hub.request(ring)["ok"] == true)
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": nil})
	a.waitLog("call "+callID[:8]+": finished, its audio never connected", time.Second)
	check(t, "a call you didn't answer isn't over", !hasKind(a.collect("s12-session"), "outcome"))

	check(t, "offered again", hub.request(map[string]any{"type": "offer", "call_id": callID, "kind": "ring", "ringing": true})["ok"] == true)
	check(t, "and again, replacing it", hub.request(map[string]any{"type": "offer", "call_id": callID, "kind": "ring", "ringing": true})["ok"] == true)
	a.waitLog("call "+callID[:8]+": a new offer replaces the earlier one", time.Second)
	a.waitLog("call "+callID[:8]+": replaced by a new offer", time.Second)
	hub.send(map[string]any{"type": "answered", "call_id": callID})
	hub.send(map[string]any{"type": "connected", "call_id": callID})
	opening, _ := hub.reply(callID, 1, 5*time.Second)
	check(t, "the replacing voice speaks the opening written while it rang", opening == "Hi there." && len(a.starts()) == 1, opening, len(a.starts()))

	from := hub.mark()
	check(t, "a new offer once the voice has spoken", hub.request(map[string]any{"type": "offer", "call_id": callID, "kind": "ring"})["ok"] == true)
	eventually(t, "its voice starts a Haiku of its own", 5*time.Second, func() bool { return len(a.starts()) == 2 })
	hub.send(map[string]any{"type": "connected", "call_id": callID})
	opening, ok := hub.replyAfter(from, callID, 1, 5*time.Second)
	check(t, "and opens", ok && opening == "Hi there.", opening)
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 9.0})
	outcome := a.waitMessage("s12-session", "outcome", 3*time.Second)
	check(t, "one outcome for the call", strings.HasPrefix(outcome["text"].(string), `The call about "Again" was answered and lasted 9 s.`), outcome)
}

// Opus takes the call over when you ask for a better model, once you've heard Haiku say so.
func TestHandOver(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("s3-session")
	a.setReplies(
		fakeReply{Match: answeredTurn, Steps: []fakeStep{say("Hi.")}},
		fakeReply{Match: "smarter model", Steps: []fakeStep{say("Sure, I'll put Opus on."), tool("better_model", map[string]any{})}},
		fakeReply{Match: "taking over this call", Steps: []fakeStep{say("Opus here.")}},
	)
	callID, _ := a.placeCall("s3-session", "Hand over", "Something to discuss.")
	a.played(callID, a.answer(callID))
	hub.send(map[string]any{"type": "turn", "call_id": callID, "text": "Can I talk to a smarter model?"})
	text, _ := hub.reply(callID, 2, 5*time.Second)
	a.waitLog("Haiku puts Opus on, held until heard", time.Second)
	check(t, "better_model answers that it's held", lastToolResult(a) == handOverHeld, lastToolResult(a))
	hub.send(map[string]any{"type": "speaking", "call_id": callID})
	hub.send(map[string]any{"type": "heard", "call_id": callID, "text": text})
	time.Sleep(100 * time.Millisecond)
	check(t, "Opus waits for the voice to be idle", len(a.starts()) == 1)
	hub.send(map[string]any{"type": "idle", "call_id": callID})
	check(t, "Opus's first turn", a.waitTurn(3, 10*time.Second) == "["+handOver+"]\n")
	starts := a.starts()
	check(t, "Opus resumes Haiku's session", len(starts) == 2 && starts[1]["model"] == "opus" && starts[1]["how"] == "--resume" &&
		starts[1]["id"] == starts[0]["id"], starts)
	text, _ = hub.reply(callID, 3, 5*time.Second)
	check(t, "Opus speaks next", text == "Opus here.", text)
	a.waitLog("handed over: Haiku closed and Opus started on its session in", time.Second)
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 20.0})
	a.waitMessage("s3-session", "outcome", 3*time.Second)
}

func lastToolResult(a *testAgent) string {
	result := ""
	for _, e := range a.claudeLog() {
		if r, ok := e["result"].(string); ok && e["tool"] != nil {
			result = r
		}
	}
	return result
}

func hasKind(messages []map[string]any, kind string) bool {
	for _, m := range messages {
		if m["kind"] == kind {
			return true
		}
	}
	return false
}

func anyStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, s := range list {
		out = append(out, s.(string))
	}
	return out
}
