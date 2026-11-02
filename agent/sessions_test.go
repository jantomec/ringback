package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// withTimes shortens the agent's waits for a test.
func withTimes(t *testing.T, unansweredFor, reask time.Duration) {
	old := []time.Duration{unanswered, reaskAfter, retryAfter}
	unanswered, reaskAfter, retryAfter = unansweredFor, reask, min(time.Minute, reask)
	t.Cleanup(func() { unanswered, reaskAfter, retryAfter = old[0], old[1], old[2] })
}

func permissionBody(a *testAgent, tool string) map[string]any {
	return map[string]any{"tool": tool, "description": "", "input": "{}", "context": "", "project": "demo",
		"cwd": a.project(), "subject": "Permission to use " + tool}
}

func placesFor(hub *fakeHub, callID string) int {
	return hub.count(func(m map[string]any) bool { return m["type"] == "place" && m["call_id"] == callID })
}

// What a session asks with no call: it places one, asks share it, a call you don't answer
// leaves them pending, and the next call you answer asks them (as tools/ask-check does).
func TestAsksWithoutACall(t *testing.T) {
	withTimes(t, 1500*time.Millisecond, time.Minute)
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("s4-session")

	status, got := a.mod("GET", "/sessions/s4-session/permissions/abcd", nil)
	check(t, "an unknown id: 404", status == 404 && got["error"] == "unknown", status, got)
	status, got = a.mod("POST", "/sessions/s4-session/permissions", map[string]any{"description": "no tool"})
	check(t, "a permission without a tool: 400", status == 400 && got["error"] == "needs tool", status, got)
	status, got = a.mod("POST", "/sessions/s4-session/questions", map[string]any{"questions": []any{}})
	check(t, "no questions: 400", status == 400 && got["error"] == "needs questions, each with a question and options with labels", status, got)
	status, got = a.mod("POST", "/calls", map[string]any{"session_id": "s4-session"})
	check(t, "a briefing without its fields: 400", status == 400 && got["error"] == "needs session_id, project and briefing", status, got)

	// Two at once, as from parallel tool calls, share one call.
	var wg sync.WaitGroup
	ids := make([]string, 2)
	for i, tool := range []string{"Bash", "Edit"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, got := a.mod("POST", "/sessions/s4-session/permissions", permissionBody(a, tool))
			if status == 200 && got["call"] == "placed" {
				ids[i], _ = got["permission"].(string)
			}
		}()
	}
	wg.Wait()
	check(t, "both placed", regexp.MustCompile(`^[a-z]{4}$`).MatchString(ids[0]) && regexp.MustCompile(`^[a-z]{4}$`).MatchString(ids[1]), ids)
	check(t, "one call for both", hub.count(is("place")) == 1, hub.count(is("place")))
	place, _ := hub.wait(0, time.Second, is("place"))
	callID := place["call_id"].(string)
	check(t, "a permission isn't a question", place["question"] == false && strings.HasPrefix(place["subject"].(string), "Permission to use "), place)
	start := a.waitTurn(1, 5*time.Second)
	check(t, "Haiku is asked for the opening", start == answeredTurn, start)
	check(t, "its briefing asks it", strings.Contains(a.starts()[0]["system"].(string), "The session has stopped because it needs the developer's permission"))

	began := time.Now()
	_, got = a.mod("GET", "/sessions/s4-session/permissions/"+ids[0]+"?wait=0.5", nil)
	check(t, "a long poll waits the time asked for while it rings", got["verdict"] == "pending" && time.Since(began) >= 450*time.Millisecond, got, time.Since(began))
	status, got = a.mod("GET", "/sessions/s4-session/questions/"+ids[0], nil)
	check(t, "the wrong kind: 404", status == 404, status)
	status, _ = a.mod("GET", "/sessions/another/permissions/"+ids[0], nil)
	check(t, "another session's: 404", status == 404, status)
	status, got = a.mod("POST", "/sessions/s4-session/questions", map[string]any{
		"questions": []any{map[string]any{"question": "Which branch?", "header": "Branch", "options": []any{map[string]any{"label": "main"}}}},
		"project":   "demo", "cwd": a.project(), "subject": "Question: Branch",
	})
	qid, _ := got["question"].(string)
	check(t, "a question goes to the ringing call", status == 200 && got["call"] == "placed" && hub.count(is("place")) == 1, got)
	status, _ = a.mod("DELETE", "/sessions/s4-session/questions/"+qid, nil)
	status2, _ := a.mod("GET", "/sessions/s4-session/questions/"+qid, nil)
	check(t, "withdrawn, it's gone", status == 200 && status2 == 404, status, status2)

	// Nobody answers: the permissions wait for the session's next call you answer.
	waited := make(chan map[string]any)
	go func() {
		_, got := a.mod("GET", "/sessions/s4-session/permissions/"+ids[1]+"?wait=3", nil)
		waited <- got
	}()
	outcome := a.waitMessage("s4-session", "outcome", 5*time.Second)
	check(t, "the session hears the call wasn't answered", regexp.MustCompile(
		`^The call about "Permission to use (Bash|Edit)" wasn't answered within 2 s: declined, missed, or its audio didn't connect\.$`,
	).MatchString(outcome["text"].(string)) && outcome["transcript"] == nil, outcome)
	a.waitLog("call "+callID[:8]+": not answered, forgotten", time.Second)
	check(t, "a long poll waits on through it", (<-waited)["verdict"] == "pending")
	_, got = a.mod("GET", "/sessions/s4-session/permissions/"+ids[0], nil)
	check(t, "still pending", got["verdict"] == "pending", got)
	status, _ = a.mod("DELETE", "/sessions/s4-session/permissions/"+ids[0], nil)
	check(t, "one is withdrawn", status == 200)

	a.setReplies(fakeReply{Match: answeredTurn, Steps: []fakeStep{say("Hi.")}})
	next, _ := a.placeCall("s4-session", "Next", "The next thing.")
	a.played(next, a.answer(next))
	a.waitLog("call "+next[:8]+": permission "+ids[1]+", asked on a call you didn't answer, goes to this one", time.Second)
	note := a.waitTurn(3, 5*time.Second) // after both calls' openings
	check(t, "the next call you answer asks the other", strings.HasPrefix(note, "[The session asks, and waits for the answer.\nPermission "+ids[1]+":") &&
		!strings.Contains(note, ids[0]), note)
	hub.send(map[string]any{"type": "ended", "call_id": next, "lasted": 5.0})
	_, got = a.mod("GET", "/sessions/s4-session/permissions/"+ids[1]+"?wait=3", nil)
	check(t, "hung up without an answer: none", got["verdict"] == "none", got)
}

// Calls held outside your work hours: no Haiku while held, a new briefing and what the session
// asks go to the held call, the hub is asked again, and the question can go to the Claude app.
func TestHeldCalls(t *testing.T) {
	withTimes(t, 30*time.Second, 400*time.Millisecond)
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	heldUntil := time.Now().Add(time.Hour).Format(time.RFC3339)
	hub.mu.Lock()
	hub.place = func(map[string]any) map[string]any { return map[string]any{"held": true, "held_until": heldUntil} }
	hub.mu.Unlock()

	callID, answer := a.placeCall("s5-session", "Parser fix", "The fix is done.")
	check(t, "the mod hears it's held", answer["ok"] == true && answer["held"] == true && answer["held_until"] == heldUntil, answer)
	a.waitLog("call "+callID[:8]+": held until "+heldUntil+" for demo (s5-sessi): Parser fix", time.Second)
	reply := hub.request(map[string]any{"type": "offer", "call_id": callID, "kind": "ring", "ringing": true})
	check(t, "an offer for a held call is refused", reply["ok"] == false && reply["status"] == float64(403) && reply["error"] == "unknown call", reply)
	a.waitLog("call "+callID[:8]+": refused, held until", time.Second)
	_, again := a.placeCall("s5-session", "Parser fix, rebased", "The fix is rebased.")
	check(t, "a new briefing goes to the held call", again["held"] == true && a.logged_("still held until "+heldUntil+", with a new briefing: Parser fix, rebased"), again)
	eventually(t, "asked again within reaskAfter, with the new subject", 3*time.Second, func() bool {
		m, _ := hub.wait(0, 0, func(m map[string]any) bool { return m["type"] == "place" && m["subject"] == "Parser fix, rebased" })
		return m != nil && m["call_id"] == callID
	})
	check(t, "no Haiku while held", len(a.starts()) == 0)

	from := hub.mark()
	status, got := a.mod("POST", "/sessions/s5-session/questions", map[string]any{
		"questions": []any{map[string]any{"question": "Push now?", "header": "Push", "options": []any{map[string]any{"label": "Yes"}}}},
		"project":   "demo", "cwd": a.project(), "subject": "Question: Push",
	})
	qid, _ := got["question"].(string)
	check(t, "a question joins the held call", status == 200 && got["call"] == "held" && got["held"] == true && got["held_until"] == heldUntil, got)
	m, _ := hub.wait(from, 300*time.Millisecond, func(m map[string]any) bool { return m["type"] == "place" && m["question"] == true })
	check(t, "and the hub is asked again at once, with a question", m != nil && m["call_id"] == callID, m)
	_, got = a.mod("GET", "/sessions/s5-session/questions/"+qid, nil)
	check(t, "it waits for the call", got["status"] == "pending" && got["held"] == true, got)

	reply = hub.request(map[string]any{"type": "claude_app", "call_id": "not-a-call"})
	check(t, "the Claude app, for a call that doesn't wait: 403", reply["status"] == float64(403) && reply["error"] == "not waiting", reply)
	reply = hub.request(map[string]any{"type": "claude_app", "call_id": callID})
	check(t, "for a session that isn't listening: 409", reply["status"] == float64(409) && reply["error"] == "not listening", reply)
	a.collect("s5-session")
	waited := make(chan map[string]any)
	go func() {
		_, got := a.mod("GET", "/sessions/s5-session/questions/"+qid+"?wait=10", nil)
		waited <- got
	}()
	time.Sleep(50 * time.Millisecond)
	reply = hub.request(map[string]any{"type": "claude_app", "call_id": callID})
	check(t, "sent to the Claude app", reply["ok"] == true, reply)
	check(t, "the long poll hears terminal", (<-waited)["status"] == "terminal")
	outcome := a.waitMessage("s5-session", "outcome", time.Second)
	check(t, "the session is told", outcome["text"] == `The call about "Parser fix, rebased" won't ring: the developer chose on the phone to answer its question in the Claude app instead, with Remote Control.`, outcome)
	a.waitLog("call "+callID[:8]+": question "+qid+" sent to the Claude app from the phone, and the call forgotten", time.Second)
	reply = hub.request(map[string]any{"type": "claude_app", "call_id": callID})
	check(t, "forgotten, it doesn't wait any more", reply["status"] == float64(403), reply)
	asked := placesFor(hub, callID)
	time.Sleep(600 * time.Millisecond)
	check(t, "and the hub isn't asked about it again", placesFor(hub, callID) == asked, placesFor(hub, callID), asked)

	// Held with no time, until you take calls: reask has it ask again at once, and it rings.
	hub.mu.Lock()
	hub.place = func(map[string]any) map[string]any { return map[string]any{"held": true} }
	hub.mu.Unlock()
	withTimes(t, 30*time.Second, time.Hour)
	onHoldID, answer := a.placeCall("s6-session", "On hold", "Waiting for you.")
	check(t, "held with no time", len(answer) == 2 && answer["held"] == true, answer)
	a.waitLog("held until you take calls for demo", time.Second)
	hub.mu.Lock()
	hub.place = func(map[string]any) map[string]any { return map[string]any{"ok": true} }
	hub.mu.Unlock()
	from = hub.mark()
	hub.send(map[string]any{"type": "reask", "call_ids": []any{onHoldID, "unknown"}})
	m, _ = hub.wait(from, time.Second, is("place"))
	check(t, "you take calls: asked again at once", m != nil && m["call_id"] == onHoldID, m)
	a.waitLog("call "+onHoldID[:8]+": you take calls, asking the hub again", time.Second)
	a.waitLog("call "+onHoldID[:8]+": placed for demo (s6-sessi), held until now: On hold", time.Second)
	check(t, "then it rings, and Haiku starts", a.waitTurn(1, 5*time.Second) == answeredTurn)

	// A held call you call now from the app's waiting calls: released at once, without ringing.
	hub.mu.Lock()
	hub.place = func(map[string]any) map[string]any { return map[string]any{"held": true, "held_until": heldUntil} }
	hub.mu.Unlock()
	nowID, _ := a.placeCall("s7-session", "Waiting", "It waited for you.")
	reply = hub.request(map[string]any{"type": "offer", "call_id": "unknown-call", "kind": "now"})
	check(t, "calling now a call that doesn't wait: 403", reply["status"] == float64(403), reply)
	asked = placesFor(hub, nowID)
	a.setReplies(fakeReply{Match: pickedUp, Steps: []fakeStep{say("It waited for you.")}})
	reply = hub.request(map[string]any{"type": "offer", "call_id": nowID, "kind": "now"})
	check(t, "a held call called now goes ahead", reply["ok"] == true, reply)
	a.waitLog("call "+nowID[:8]+": called now from the app, held until "+heldUntil+": 'Waiting'", time.Second)
	a.waitLog("call "+nowID[:8]+": offer answered in", time.Second)
	check(t, "its voice picks up", a.waitTurn(2, 5*time.Second) == pickedUp)
	starts := a.starts()
	check(t, "with the lead that says you called", strings.Contains(starts[len(starts)-1]["system"].(string), calledNow+"\n\nThe call's briefing:\nIt waited for you."))
	hub.send(map[string]any{"type": "connected", "call_id": nowID})
	opening, _ := hub.reply(nowID, 1, 5*time.Second)
	check(t, "and speaks at once", opening == "It waited for you.", opening)
	hub.send(map[string]any{"type": "ended", "call_id": nowID, "lasted": 24.0})
	outcome = a.waitMessage("s7-session", "outcome", 3*time.Second)
	check(t, "the session is told you called", strings.HasPrefix(outcome["text"].(string), `The developer called about "Waiting" from the app while it waited and it lasted 24 s.`), outcome)
	check(t, "and the hub wasn't asked about it again", placesFor(hub, nowID) == asked)
}

// Calling back a call from the phone's history, and calling a session from Contacts.
func TestCallBackAndContacts(t *testing.T) {
	hub := newFakeHub(t)
	a := startAgent(t, hub, "")
	a.collect("s8-session")
	a.setReplies(fakeReply{Match: answeredTurn, Steps: []fakeStep{say("Hi.")}}, fakeReply{Match: pickedUp, Steps: []fakeStep{say("You called.")}})
	callID, _ := a.placeCall("s8-session", "Parser fix", "The fix is done.")
	a.played(callID, a.answer(callID))
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 12.0})
	a.waitMessage("s8-session", "outcome", 3*time.Second)

	reply := hub.request(map[string]any{"type": "offer", "call_id": "never-placed-here", "kind": "back"})
	check(t, "a call back of a call this agent doesn't know: 403", reply["status"] == float64(403) && reply["error"] == "unknown call", reply)
	a.waitLog("call back refused, not a call this agent remembers", time.Second)
	reply = hub.request(map[string]any{"type": "offer", "call_id": callID, "kind": "back"})
	check(t, "a call back of a call you answered", reply["ok"] == true, reply)
	a.waitLog("call "+callID[:8]+": called back about 'Parser fix', the session listens, answered 0 min ago", time.Second)
	check(t, "picks up", a.waitTurn(2, 5*time.Second) == pickedUp)
	starts := a.starts()
	system := starts[len(starts)-1]["system"].(string)
	check(t, "with the lead of a call you talked on", strings.Contains(system, "This call is the developer calling back. You talked with them about the briefing below less than a minute\nago") &&
		strings.Contains(system, "The earlier call's briefing:\nThe fix is done."), system)
	hub.send(map[string]any{"type": "connected", "call_id": callID})
	hub.reply(callID, 1, 5*time.Second)
	hub.send(map[string]any{"type": "ended", "call_id": callID, "lasted": 30.0})
	outcome := a.waitMessage("s8-session", "outcome", 3*time.Second)
	check(t, "the session hears you called back", strings.HasPrefix(outcome["text"].(string), `The developer called back about "Parser fix" and it lasted 30 s. Its transcript: .ringback/calls/`), outcome)

	contactID := "contact-call-0123456789"
	reply = hub.request(map[string]any{"type": "offer", "call_id": contactID, "kind": "contact", "session_id": "nobody-listens"})
	check(t, "a session that isn't listening: 403", reply["status"] == float64(403) && reply["error"] == "not listening", reply)
	reply = hub.request(map[string]any{"type": "offer", "call_id": "short", "kind": "contact", "session_id": "s8-session"})
	check(t, "an ID too short to be a secret: 403", reply["status"] == float64(403), reply)
	reply = hub.request(map[string]any{"type": "offer", "call_id": contactID, "kind": "contact", "session_id": "s8-session"})
	check(t, "a session from Contacts", reply["ok"] == true, reply)
	a.waitLog("call "+contactID[:8]+": called from Contacts, demo (s8-sessi), with its latest briefing 'Parser fix'", time.Second)
	a.waitTurn(3, 5*time.Second)
	starts = a.starts()
	system = starts[len(starts)-1]["system"].(string)
	check(t, "with the lead that says you called it, and what you heard", strings.Contains(system, calledContact+"\nBelow is the session's latest briefing, written less than a minute ago. The developer already heard it"), system)
	hub.send(map[string]any{"type": "connected", "call_id": contactID})
	hub.reply(contactID, 1, 5*time.Second)
	hub.send(map[string]any{"type": "ended", "call_id": contactID, "lasted": 10.0})
	outcome = a.waitMessage("s8-session", "outcome", 3*time.Second)
	check(t, "the session hears you called it", strings.HasPrefix(outcome["text"].(string), "The developer called the session from the app's Contacts and it lasted 10 s. Its transcript: "), outcome)
}

// A restart keeps what matters: held calls are held again, a ringing call has ended and its
// session is told, and what sessions asked keeps its ids.
func TestRestart(t *testing.T) {
	withTimes(t, 30*time.Second, 300*time.Millisecond)
	state := t.TempDir()
	hub := newFakeHub(t)
	first := startAgent(t, hub, state)
	heldUntil := time.Now().Add(time.Hour).Format(time.RFC3339)
	hub.mu.Lock()
	hub.place = func(m map[string]any) map[string]any {
		if strings.HasPrefix(m["subject"].(string), "Held") {
			return map[string]any{"held": true, "held_until": heldUntil}
		}
		return map[string]any{"ok": true}
	}
	hub.mu.Unlock()
	heldID, _ := first.placeCall("s9-session", "Held work", "Done for today.")
	_, got := first.mod("POST", "/sessions/s9-session/permissions", permissionBody(first, "Bash"))
	permissionID := got["permission"].(string)
	check(t, "a permission joins the held call", got["call"] == "held", got)
	ringingID, _ := first.placeCall("s10-session", "Ringing", "It rings.")
	first.waitTurn(1, 5*time.Second)
	data, _ := os.ReadFile(filepath.Join(state, stateFile))
	info, _ := os.Stat(filepath.Join(state, stateFile))
	check(t, "the state file is yours alone", info != nil && info.Mode().Perm() == 0o600, info)
	check(t, "and holds both calls", strings.Contains(string(data), heldID) && strings.Contains(string(data), ringingID))
	first.stop()
	first.closeAll()

	second := startAgent(t, hub, state)
	second.waitLog("restored 1 held calls, 1 to call back, 1 asks, 1 messages for sessions", time.Second)
	second.waitLog("call "+heldID[:8]+": still held until "+heldUntil+" after a restart: Held work", time.Second)
	second.waitLog("call "+ringingID[:8]+": ended by the restart: Ringing", time.Second)
	outcome := second.waitMessage("s10-session", "outcome", time.Second)
	check(t, "the ringing call's session is told", outcome["text"] == `The call about "Ringing" ended when the voice server restarted, before it was answered.`, outcome)
	_, got = second.mod("GET", "/sessions/s9-session/permissions/"+permissionID, nil)
	check(t, "what was asked keeps its id", got["verdict"] == "pending" && got["held"] == true, got)
	from := hub.mark()
	m, _ := hub.wait(from, 2*time.Second, func(m map[string]any) bool { return m["type"] == "place" && m["call_id"] == heldID })
	check(t, "the held call asks the hub again", m != nil, m)
	reply := hub.request(map[string]any{"type": "offer", "call_id": ringingID, "kind": "back"})
	check(t, "the ended call can be called back", reply["ok"] == true, reply)
	second.waitLog("call "+ringingID[:8]+": called back about 'Ringing'", time.Second)

	// Once it has stopped writing it.
	second.stop()
	second.closeAll()
	second.calls.mu.Lock()
	os.WriteFile(filepath.Join(state, stateFile), []byte(`{"calls": [`), 0o600)
	third := startAgent(t, hub, state)
	second.calls.mu.Unlock()
	third.waitLog("can't read "+filepath.Join(state, stateFile), time.Second)
	_, err := os.Stat(filepath.Join(state, stateFile+".unreadable"))
	check(t, "an unreadable state file is set aside", err == nil, err)
}

// The link to the hub: a refused token, opening it again, and the calls on a link that closed.
func TestHubLink(t *testing.T) {
	oldFirst := firstRetry
	firstRetry = 50 * time.Millisecond
	t.Cleanup(func() { firstRetry = oldFirst })
	hub := newFakeHub(t)

	var lines []string
	var mu sync.Mutex
	refused := newHubLink(hub.addr(), "wrong", "test-mac", func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	})
	refused.handle = func(map[string]any) {}
	ctx, cancel := context.WithCancel(context.Background())
	go refused.run(ctx)
	eventually(t, "the hub refuses a wrong token, and it's tried again", 3*time.Second, func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return hub.refusals >= 2
	})
	cancel()
	mu.Lock()
	check(t, "and logged", len(lines) > 0 && strings.Contains(lines[0], "the hub refused the token (401)"), lines)
	mu.Unlock()
	_, err := refused.request(ctx, map[string]any{"type": "place"})
	check(t, "a request while unlinked fails at once", err == errNotLinked, err)

	a := startAgent(t, hub, "")
	a.collect("s11-session")
	a.setReplies(fakeReply{Match: answeredTurn, Steps: []fakeStep{say("Hi.")}})
	callID, _ := a.placeCall("s11-session", "Linked", "About the link.")
	a.played(callID, a.answer(callID))
	sessionsBefore := hub.count(is("sessions"))
	hub.mu.Lock()
	conn := hub.conn
	hub.mu.Unlock()
	conn.CloseNow()
	outcome := a.waitMessage("s11-session", "outcome", 3*time.Second)
	check(t, "a call on a link that closed has ended", strings.HasPrefix(outcome["text"].(string), `The call about "Linked" was answered and lasted 0 s.`), outcome)
	a.waitLog("call "+callID[:8]+": the hub's link closed, so the call has ended", time.Second)
	eventually(t, "the link opens again, says hello, and the hub hears the sessions again", 5*time.Second, func() bool {
		hub.mu.Lock()
		hellos := hub.hellos
		hub.mu.Unlock()
		return hellos == 2 && hub.count(is("sessions")) > sessionsBefore
	})
	_, answer := a.mod("POST", "/calls", map[string]any{"session_id": "s11-session", "project": "demo", "briefing": "Again."})
	check(t, "and calls are placed through it", answer["ok"] == true, answer)
}
