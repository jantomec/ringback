package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	errBadRequest = errors.New("bad request")
	errForbidden  = errors.New("forbidden")
)

type endpoint func(body map[string]any) (map[string]any, error)

// serve answers the phone (/pair, /register, /events, /check, /waiting, /contacts, /hold,
// /shown), the Macs and speech nodes pairing (/pair-link), what sessions show you (/show), and
// the links they open: over plain HTTP on a private network
// such as Nebula, or HTTPS and WSS with [tls] in hub.toml (tlsConfig). The phone's requests are small
// and rare, and are handled one at a time (stateMu); each gets at most 5 s, so a stalled
// connection can't hold the next one past the app's 10 s timeout. Meanwhile it asks the
// phones to check their connection every 30 min.
func serve(listen string) {
	endpoints := map[string]endpoint{
		"/pair": pair, "/pair-link": pairPart, "/register": register, "/events": event, "/check": checkIn, "/waiting": waiting,
		"/contacts": contacts, "/hold": holdSwitch, "/shown": shownList, "/model": modelSetting,
	}
	listener, err := net.Listen("tcp4", listen)
	if err != nil {
		// Fails if Nebula isn't up yet; the service manager then starts it again.
		fail(fmt.Sprintf("can't listen on %s: %v", listen, err))
	}
	how := ""
	if _, err := audioNetwork(loadConfig()); err != nil {
		fail("The call's audio: " + err.Error())
	}
	config, err := tlsConfig(loadConfig())
	if err != nil {
		fail("Can't serve TLS: " + err.Error())
	}
	if config != nil {
		listener, servingTLS, how = tls.NewListener(listener, config), true, ", HTTPS and WSS"
	}
	log("hub listening on " + listen + how)
	partsGoneAtStart()
	checkPeriodically()
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: limited(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			switch {
			case path == "/agent":
				acceptLink(w, r, "agent", agentOpened, fromAgent, agentFrame, agentClosed)
			case path == "/speech":
				acceptLink(w, r, "speech", nodeOpened, nodeMessage, nodeAudio, nodeClosed)
			case path == "/phone-speech":
				phoneSpeech(w, r)
			case path == "/show":
				showFile(w, r)
			case path == "/view":
				if r.Method != http.MethodPost {
					phoneJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false})
					return
				}
				// Its agent starts the model's renderer meanwhile.
				withDeadline(w, 45*time.Second)
				viewModel(w, r)
			case path == "/shown/file":
				if r.Method != http.MethodPost {
					phoneJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false})
					return
				}
				withDeadline(w, 5*time.Minute)
				shownFile(w, r)
			case path == "/portal" || strings.HasPrefix(path, "/portal/"):
				withDeadline(w, 15*time.Second)
				portal(w, r)
			case path == "/ping":
				// The phone checks it can reach the hub while it rings.
				phoneJSON(w, http.StatusOK, map[string]any{"ok": true})
			case phoneRoutes[path] != nil:
				if r.Method != http.MethodPost {
					phoneJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false})
					return
				}
				withDeadline(w, 15*time.Second)
				phoneRoutes[path](w, r)
			default:
				withDeadline(w, 5*time.Second)
				status, body := answer(endpoints, r)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Connection", "close")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
			}
		})),
	}
	fail(server.Serve(listener).Error())
}

const notOK = `{"ok": false}`

// answer runs the endpoint a request asks for, which answers its own JSON. The phone only
// checks for a 200, apart from /waiting and /hold.
func answer(endpoints map[string]endpoint, r *http.Request) (int, string) {
	run, ok := endpoints[r.URL.Path]
	if !ok {
		return http.StatusNotFound, notOK
	}
	if r.Method != http.MethodPost {
		return http.StatusMethodNotAllowed, notOK
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 65536+1))
	if err != nil || len(data) > 65536 {
		return http.StatusBadRequest, notOK
	}
	var body map[string]any
	if json.Unmarshal(data, &body) != nil || body == nil {
		return http.StatusBadRequest, notOK
	}
	result, err := func() (map[string]any, error) {
		stateMu.Lock()
		defer stateMu.Unlock() // a panic in an endpoint mustn't leave every later request waiting
		return run(body)
	}()
	switch {
	case errors.Is(err, errBadRequest):
		return http.StatusBadRequest, notOK
	case errors.Is(err, errForbidden):
		return http.StatusForbidden, notOK
	case errors.Is(err, errNotPaired):
		return http.StatusUnauthorized, `{"ok": false, "error": "not paired"}`
	case errors.Is(err, errPairingCode):
		return http.StatusForbidden, `{"ok": false, "error": "pairing code not valid"}`
	case err != nil:
		log(r.URL.Path + " failed: " + err.Error())
		return http.StatusInternalServerError, notOK
	}
	encoded, _ := json.Marshal(result)
	return http.StatusOK, string(encoded)
}

var ok = map[string]any{"ok": true}

func str(body map[string]any, key string) string {
	s, _ := body[key].(string)
	return s
}

// register: a paired phone registers its push tokens, with its key, and its work hours if it
// has any. A registration without them clears them, and ones that can't be read are a 400. It
// also says whether its On-device voice is on (on_device), which the portal shows. A phone that
// didn't pair, or whose key doesn't match: 401. A key with a new push token (APNs can change
// it) moves the phone to it.
func register(body map[string]any) (map[string]any, error) {
	token := str(body, "token")
	if _, valid := workHoursFields(body); token == "" || !valid {
		return nil, errBadRequest
	}
	if paired, err := movePairedPhone(token, str(body, "key")); err != nil {
		return nil, err
	} else if !paired {
		return nil, errNotPaired
	}
	return ok, keepPhone(body)
}

// keepPhone keeps what a phone registers (register, pair). The caller has checked the body.
func keepPhone(body map[string]any) error {
	token := str(body, "token")
	hours, _ := workHoursFields(body)
	environment := orDefault(str(body, "environment"), "sandbox")
	device := orDefault(str(body, "device"), "a phone")
	notificationToken := str(body, "notification_token")
	onDevice := ""
	if on, sent := body["on_device"].(bool); sent {
		onDevice = map[bool]string{true: "yes", false: "no"}[on]
	}
	was := loadDevices()[token]["on_device"]
	if err := registerDevice(token, environment, device, notificationToken, onDevice, hours); err != nil {
		return err
	}
	registeredAgain(token, notificationToken)
	// Its own voice turned on: the calls that waited for speech can be talked through now.
	if ids := waitingFor("no speech"); onDevice == "yes" && was != "yes" && len(ids) > 0 {
		log(fmt.Sprintf("%s speaks itself now: %d calls waited for speech, they ask again", device, len(ids)))
		broadcast(map[string]any{"type": "reask", "call_ids": ids})
	}
	checks := ""
	if notificationToken != "" {
		checks = ", with connection checks"
	}
	if h := describe(hours); h != "" {
		checks += ", " + h
	}
	log(fmt.Sprintf("registered %s (%s), token …%s%s", device, environment, suffix(token, 8), checks))
	return nil
}

// placeCall: an agent (its `place`, over its link) asks to ring the phone for one of its
// sessions, on the Mac it names (mac). The phone sends the call's audio to the hub. The call ID
// is that call's secret: the agent accepts only an offer that carries it, and only the push
// takes it to the phone. The push goes out after the answer, so a slow APNs doesn't hold up
// the agent.
//
// While every phone is on hold (Hold: outside its work hours, or you held calls in the app or
// on the portal) nothing rings: the answer says so (held) and when the first comes off hold
// by its hours (held_until), if one will, and the agent asks again by then, or
// sooner, with the same call ID. Meanwhile the call waits, and the app shows it. The agent
// says whether the call has a question; the first time a held call has one, the phone
// gets a notification, once for that call. It also names the calling session (session_id),
// which goes to the phone with the call, so the app can call that session from Sessions once
// the agent has forgotten the call.
func placeCall(mac string, body map[string]any) (map[string]any, error) {
	project, callID := str(body, "project"), str(body, "call_id")
	if n := utf8.RuneCountInString(callID); project == "" || n < 16 || n > 128 {
		return nil, errBadRequest
	}
	voice, subject, session := hubURL(), str(body, "subject"), str(body, "session_id")
	about := ""
	if subject != "" {
		about = ": " + subject
	}
	log("call " + prefix(callID, 8) + ": asked by " + mac + " for " + project + about)
	if entry := loadWaiting()[callID]; entry["reason"] == cancelled {
		// You cancelled it while its agent couldn't be told: it is now.
		log("call " + prefix(callID, 8) + ": it was cancelled on the " + entry["cancelled_by"] + "; its agent is told")
		return map[string]any{"ok": true, "cancelled": true, "by": entry["cancelled_by"]}, nil
	}
	noteCall(callID, project, subject)
	if held, until := heldUntil(now()); held {
		question, _ := body["question"].(bool)
		holding := "until you take calls"
		if until != "" {
			holding = "until " + until
		}
		with := ""
		if question {
			with = ", with a question"
		}
		log("call " + prefix(callID, 8) + ": held " + holding + ", every phone on hold" + with)
		notify, err := holdCall(callID, project, subject, voice, until, question, session, "")
		if err != nil {
			return nil, err
		}
		if notify {
			at, _ := parseISO8601(until)
			go func() {
				for _, line := range notifyQuestion(context.Background(), callID, project, subject, at) {
					log(line)
				}
			}()
		}
		answer := map[string]any{"ok": true, "held": true}
		if until != "" {
			answer["held_until"] = until
		}
		return answer, nil
	}
	// A call needs a speech node, or a phone that speaks itself: without one it can't be talked
	// through, so it waits instead of ringing, and asks again once there's one.
	if !speechLinked() && !phoneSpeaks() {
		question, _ := body["question"].(bool)
		log("call " + prefix(callID, 8) + ": waits, no speech node is linked")
		notify, err := holdCall(callID, project, subject, voice, "", question, session, "no speech")
		if err != nil {
			return nil, err
		}
		if notify {
			go func() {
				for _, line := range notifyNoSpeech(context.Background(), callID, project, subject) {
					log(line)
				}
			}()
		}
		return map[string]any{"ok": true, "held": true, "reason": "no speech"}, nil
	}
	// One call rings at a time (line.go).
	question, _ := body["question"].(bool)
	waiting := freshWaiting()
	entry := waiting[callID]
	if why, until := lineWaits(callID, entry, waiting); why != "" {
		if _, err := holdCall(callID, project, subject, voice, until, question, session, why); err != nil {
			return nil, err
		}
		if entry["reason"] != why || entry["held_until"] != until {
			how := map[string]string{inTurn: "in turn", missed: "missed, until " + until, callBack: "until you call its session back"}[why]
			if why == inTurn && until != "" {
				how = "in turn, until the line has rested after the last call, " + until
			}
			log("call " + prefix(callID, 8) + ": waits, " + how)
		}
		answer := map[string]any{"ok": true, "held": true, "reason": why}
		if until != "" {
			answer["held_until"] = until
		}
		return answer, nil
	}
	takeLine(callID, project, subject, voice, session, question, entry)
	if removed, err := removeWaiting(callID); err != nil {
		return nil, err
	} else if removed {
		log("call " + prefix(callID, 8) + ": no longer waiting, it rings")
	}
	go func() {
		lines := ring(context.Background(), project, subject, voice, callID, session)
		if len(lines) == 0 {
			log("call " + prefix(callID, 8) + ": no phone has paired")
		}
		for _, line := range lines {
			log(line)
		}
	}()
	// Its own map, not the shared ok: the agent's reply is written into it.
	return map[string]any{"ok": true}, nil
}

// checkIn: the phone reached the hub on a connection check. Without a token it still gets a
// 200: the check is whether it got through. With one, the phone must have paired (401
// otherwise, which tells the app so). Its work hours come along, so a new time zone reaches the
// hub within the half hour between checks. Hours that can't be read are left out rather than
// refused, for the same reason.
func checkIn(body map[string]any) (map[string]any, error) {
	if _, isString := body["token"].(string); !isString {
		return ok, nil
	}
	token, _, err := pairedPhone(body)
	if err != nil {
		return nil, err
	}
	sent := false
	for _, key := range workHoursKeys {
		_, present := body[key]
		sent = sent || present
	}
	var hours map[string]string
	if sent {
		fields, valid := workHoursFields(body)
		if valid {
			hours = fields
		} else {
			log("check from …" + suffix(token, 8) + ": work hours that can't be read, left out")
		}
	}
	return ok, checkedIn(token, hours)
}

// event: the phone reports what happened to a call. A waiting call you called from the app
// ("called now"), or whose question you sent to the Claude app ("claude app"), no longer
// waits.
func event(body map[string]any) (map[string]any, error) {
	if err := appendEvent(body); err != nil {
		return nil, err
	}
	extra := ""
	for _, key := range []string{"on_another_call", "duration_s", "since_answer_s", "error", "note"} {
		if value, present := body[key]; present {
			extra += fmt.Sprintf(", %s %v", key, value)
		}
	}
	at := ""
	if t, parsed := parseISO8601(str(body, "at")); parsed {
		at = " (on the phone at " + timeOfDay(t.Local()) + ")"
	}
	callID := orDefault(str(body, "call_id"), "?")
	name := "?"
	if value, present := body["event"]; present {
		name = fmt.Sprint(value)
	}
	log("call " + prefix(callID, 8) + ": " + name + extra + at)
	switch str(body, "event") {
	case "answered":
		lineAnswered(callID)
	case "missed", "declined", "failed":
		lineUnanswered(callID, str(body, "event"))
	case "ended":
		lineEnded(callID)
	}
	leaves := map[string]string{"called now": "called from the app", "claude app": "its question went to the Claude app"}
	if why, leaving := leaves[str(body, "event")]; leaving && str(body, "call_id") != "" {
		if removed, err := removeWaiting(str(body, "call_id")); err != nil {
			return nil, err
		} else if removed {
			log("call " + prefix(callID, 8) + ": no longer waiting, " + why)
			// A missed call that held the others up no longer does.
			nextInTurn()
		}
	}
	return ok, nil
}

// waiting: the calls that wait, for the app to show, soonest first: {"calls": [...]}, each
// with its call ID, project, subject, where the phone sends its audio (voice), when it rings
// (held_until, missing if it waits until you take calls), whether it has a question
// (question); and the phone's hold switch (hold, as /hold answers). Only a paired phone gets
// them, by its push token and key: a call's ID lets whoever has it take the call.
func waiting(body map[string]any) (map[string]any, error) {
	token, device, err := pairedPhone(body)
	if err != nil {
		return nil, err
	}
	calls, err := listWaiting()
	if err != nil {
		return nil, err
	}
	return map[string]any{"calls": calls, "hold": newHold(device, now()).answer(device), "status": status(token, device, calls)}, nil
}

// status is what the app shows a banner for, with the waiting calls: whether calls can be
// talked through (a speech node is linked, or no call the hub carries needs
// one: no agent is linked and none waits for one), whether the last push to this phone got
// through, and the Macs linked, with how many of their sessions listen.
func status(token string, device map[string]string, calls []map[string]any) map[string]any {
	waitingForSpeech := 0
	for _, call := range calls {
		if call["reason"] == "no speech" {
			waitingForSpeech++
		}
	}
	nodes, macs := linked()
	onDevice := device["on_device"] == "yes"
	speech := map[string]any{
		"ok":    speechLinked() || onDevice || (len(macs) == 0 && waitingForSpeech == 0),
		"nodes": nodes, "waiting": waitingForSpeech, "on_device": onDevice,
	}
	pushed := map[string]any{"ok": true}
	if last, found := lastPush(device, token); found {
		pushed = map[string]any{"ok": last.ok(), "at": last.at.UTC().Format(time.RFC3339), "status": last.status}
		if !last.ok() {
			pushed["problem"] = last.problem()
		}
		if last.reason != "" {
			pushed["reason"] = last.reason
		}
		if last.err != nil {
			pushed["error"] = last.err.Error()
		}
	}
	return map[string]any{"speech": speech, "push": pushed, "macs": macs}
}

// holdSwitch: the phone's hold switch. {"token", "hold": Bool} sets it, without "hold" it's
// only read. Answers {"hold": Bool, "until": ISO 8601}, until being when its work hours next
// change it, missing if they won't. On taking calls, the app asks the hub to have the waiting
// calls ask again now (/reask).
func holdSwitch(body map[string]any) (map[string]any, error) {
	token, device, err := pairedPhone(body)
	if err != nil {
		return nil, err
	}
	if on, isBool := body["hold"].(bool); isBool {
		set, err := setHold(token, on)
		if err != nil {
			return nil, err
		}
		if set != nil {
			device = set
			state := "taking calls"
			if on {
				state = "on hold"
			}
			log("…" + suffix(token, 8) + " " + orDefault(device["device"], "a phone") + ": " + state + ", set on the phone")
		}
	}
	return newHold(device, now()).answer(device), nil
}

// keepSessions checks the sessions an agent says listen (ringback on), for the phone's Sessions
// tab: each with its session_id and project, and when it can, since when it listens (since)
// and its latest briefing's subject and briefed_at. The list replaces the one the agent gave
// before, kept under key; one not given again within 90 s is dropped. It's logged when it
// changes.
func keepSessions(key string, given []any) ([]map[string]string, error) {
	listed := []map[string]string{}
	var names []string
	for _, item := range given {
		session, isObject := item.(map[string]any)
		if !isObject {
			return nil, errBadRequest
		}
		id, project := str(session, "session_id"), str(session, "project")
		if n := utf8.RuneCountInString(id); n < 1 || n > 128 || project == "" {
			return nil, errBadRequest
		}
		kept := map[string]string{"session_id": id, "project": project}
		for _, key := range []string{"subject", "briefed_at", "since"} {
			if value := str(session, key); value != "" {
				kept[key] = value
			}
		}
		listed = append(listed, kept)
		names = append(names, project+" ("+prefix(id, 8)+")")
	}
	changed, err := updateContacts(key, listed)
	if err != nil {
		return nil, err
	}
	if changed {
		list := strings.Join(names, ", ")
		if list == "" {
			list = "none"
		}
		log("sessions listening on " + key + ": " + list)
	}
	return listed, nil
}

// contacts: the sessions that listen on every Mac, for the app's Sessions tab:
// {"contacts": [...]}, by project, each with the URL the phone calls it at (voice). Only a
// paired phone gets them, as for /waiting: a session's ID lets whoever has it call the session.
func contacts(body map[string]any) (map[string]any, error) {
	if _, _, err := pairedPhone(body); err != nil {
		return nil, err
	}
	list, err := listContacts()
	if err != nil {
		return nil, err
	}
	return map[string]any{"contacts": list}, nil
}
