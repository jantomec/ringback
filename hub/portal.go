package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// The hub's portal: pages under /portal/ for setting up and for the rare times something's
// wrong ("Status, the hub's portal and speech on the phone" in docs/design.md). It shows the
// speech nodes and Macs linked, the calls, the phones and the log, and changes only what's
// kept in the hub's state (pausing a node); secrets stay in hub.toml. Plain HTML from
// templates in the binary, which refresh themselves with a small script.

//go:embed portal
var portalFiles embed.FS

var portalPages = parsePortalPages()

func parsePortalPages() map[string]*template.Template {
	base := template.Must(template.ParseFS(portalFiles, "portal/base.html"))
	pages := map[string]*template.Template{}
	for _, name := range []string{"status", "calls", "log", "setup", "login", "code", "noaccount", "message"} {
		pages[name] = template.Must(template.Must(base.Clone()).ParseFS(portalFiles, "portal/"+name+".html"))
	}
	return pages
}

// page is what every page's template gets: Data is the page's own.
type page struct {
	Title, Page, CSRF, User, Updated, Error string
	Refresh                                 int // seconds between refreshes; 0: none
	Data                                    any
}

const refreshEvery = 5

func portal(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self'; "+
		"connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cache-Control", "no-store")
	p := r.URL.Path
	if name, isStatic := strings.CutPrefix(p, "/portal/static/"); isStatic {
		portalStatic(w, name)
		return
	}
	if p == "/portal" {
		http.Redirect(w, r, "/portal/", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	a := loadAccount()
	if a == nil {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Redirect(w, r, "/portal/", http.StatusSeeOther)
			return
		}
		render(w, http.StatusOK, "noaccount", page{Title: "Create the portal's account", Refresh: refreshEvery})
		return
	}
	switch p {
	case "/portal/login":
		loginPage(w, r, a)
		return
	case "/portal/code":
		codePage(w, r, a)
		return
	}
	s := signedIn(r, a)
	if s == nil {
		if r.Header.Get(refreshHeader) != "" {
			http.Error(w, "signed out", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/portal/login", http.StatusSeeOther)
		return
	}
	base := page{CSRF: s.csrf, User: s.name}
	if r.Method == http.MethodPost {
		if !sameToken(r, s.csrf) {
			log("portal: a form without its CSRF token, from " + address(r) + ", refused: " + p)
			base.Title, base.Data = "This form has expired", "Go back, reload the page and try again."
			render(w, http.StatusForbidden, "message", base)
			return
		}
		switch p {
		case "/portal/logout":
			signOut(w, r)
			log("portal: " + s.name + " signed out, from " + address(r))
			http.Redirect(w, r, "/portal/login", http.StatusSeeOther)
		case "/portal/nodes/pause", "/portal/nodes/resume":
			name := strings.TrimSpace(r.PostFormValue("name"))
			if name == "" || len(name) > 200 {
				http.Error(w, "needs a node's name", http.StatusBadRequest)
				return
			}
			if err := pauseNode(name, p == "/portal/nodes/pause"); err != nil {
				log("portal: can't keep the nodes' settings: " + err.Error())
				http.Error(w, "can't keep the nodes' settings", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, "/portal/", http.StatusSeeOther)
		default:
			base.Title, base.Data = "Not found", "There's no such page."
			render(w, http.StatusNotFound, "message", base)
		}
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	base.Updated = now().Local().Format("15:04:05")
	switch p {
	case "/portal/":
		base.Title, base.Page, base.Refresh, base.Data = "Status", "status", refreshEvery, statusData()
		render(w, http.StatusOK, "status", base)
	case "/portal/calls":
		base.Title, base.Page, base.Refresh, base.Data = "Calls", "calls", refreshEvery, recentCalls(50)
		render(w, http.StatusOK, "calls", base)
	case "/portal/log":
		base.Title, base.Page, base.Refresh, base.Data = "Log", "log", refreshEvery, logData()
		render(w, http.StatusOK, "log", base)
	case "/portal/setup":
		base.Title, base.Page, base.Data = "Setup", "setup", setupData()
		render(w, http.StatusOK, "setup", base)
	default:
		base.Title, base.Data = "Not found", "There's no such page."
		render(w, http.StatusNotFound, "message", base)
	}
}

func render(w http.ResponseWriter, status int, name string, p page) {
	var out bytes.Buffer
	if err := portalPages[name].ExecuteTemplate(&out, "base", p); err != nil {
		log("portal: can't show " + name + ": " + err.Error())
		http.Error(w, "the portal can't show this page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(out.Bytes())
}

// portalStatic serves the stylesheet, the script and the icon, to anyone: the login page
// needs them.
func portalStatic(w http.ResponseWriter, name string) {
	data, err := fs.ReadFile(portalFiles, "portal/static/"+name)
	if err != nil || name == "" || strings.Contains(name, "/") {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
	_, _ = w.Write(data)
}

// loginPage: the first factor, a user name and password. Right, it waits for the passcode.
func loginPage(w http.ResponseWriter, r *http.Request, a *portalAccount) {
	if signedIn(r, a) != nil {
		http.Redirect(w, r, "/portal/", http.StatusSeeOther)
		return
	}
	p := page{Title: "Sign in", Data: ""}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		p.CSRF = formToken(w, r)
		render(w, http.StatusOK, "login", p)
	case http.MethodPost:
		addr, name := address(r), r.PostFormValue("name")
		p.Data = name
		switch {
		case !sameToken(r, cookie(r, formCookie)):
			log("portal: a login without its CSRF token, from " + addr + ", refused")
			p.CSRF, p.Error = formToken(w, r), "The form had expired. Try again."
			render(w, http.StatusForbidden, "login", p)
		case addressRefused(addr):
			p.CSRF, p.Error = formToken(w, r), "Too many failed attempts from your address. Try again in a minute."
			render(w, http.StatusTooManyRequests, "login", p)
		case !a.checkPassword(name, r.PostFormValue("password")):
			failedLogin(addr, "a wrong name or password")
			p.CSRF, p.Error = formToken(w, r), "That name and password don't match."
			render(w, http.StatusUnauthorized, "login", p)
		default:
			startLogin(w, a)
			http.Redirect(w, r, "/portal/code", http.StatusSeeOther)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// codePage: the second factor, a passcode from the authenticator app, within a few minutes
// of the password.
func codePage(w http.ResponseWriter, r *http.Request, a *portalAccount) {
	if signedIn(r, a) != nil {
		http.Redirect(w, r, "/portal/", http.StatusSeeOther)
		return
	}
	pending := pendingLogin(r, a)
	if pending == nil {
		http.Redirect(w, r, "/portal/login", http.StatusSeeOther)
		return
	}
	p := page{Title: "Passcode", CSRF: pending.csrf}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		render(w, http.StatusOK, "code", p)
	case http.MethodPost:
		addr := address(r)
		if !sameToken(r, pending.csrf) {
			log("portal: a passcode without its CSRF token, from " + addr + ", refused")
			p.Error = "The form had expired. Try again."
			render(w, http.StatusForbidden, "code", p)
			return
		}
		if addressRefused(addr) {
			p.Error = "Too many failed attempts from your address. Try again in a minute."
			render(w, http.StatusTooManyRequests, "code", p)
			return
		}
		ok, reused, err := takeCode(r.PostFormValue("code"), now())
		if err != nil {
			log("portal: can't check a passcode: " + err.Error())
			http.Error(w, "can't check the passcode", http.StatusInternalServerError)
			return
		}
		if !ok {
			why := "a wrong passcode"
			if reused {
				why = "a passcode already used"
			}
			failedLogin(addr, why)
			p.Error = "That code didn't work. Each code works once: wait for the next."
			render(w, http.StatusUnauthorized, "code", p)
			return
		}
		finishLogin(w, r, a)
		loggedIn(addr)
		log("portal: " + a.Name + " signed in, from " + addr)
		http.Redirect(w, r, "/portal/", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// How times and lengths read on the portal, in the hub's time zone.

// clock is "14:05" today, "Mon 14:05" within the week, "2 Nov 14:05" before.
func clock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	t, today := t.Local(), now().Local()
	y1, m1, d1 := t.Date()
	y2, m2, d2 := today.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return t.Format("15:04")
	case t.Before(today) && today.Sub(t) < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	}
	return t.Format("2 Jan 15:04")
}

// span is a length of time, roughly: "35 s", "4 min 10 s", "2 h 5 min", "3 days".
func span(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		if s%60 == 0 {
			return fmt.Sprintf("%d min", s/60)
		}
		return fmt.Sprintf("%d min %d s", s/60, s%60)
	case s < 48*3600:
		if s/60%60 == 0 {
			return fmt.Sprintf("%d h", s/3600)
		}
		return fmt.Sprintf("%d h %d min", s/3600, s/60%60)
	}
	return fmt.Sprintf("%d days", s/86400)
}

// since is "since 14:05 (2 h 5 min)".
func since(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return "since " + clock(t) + " (" + span(now().Sub(t)) + ")"
}

func isoClock(text string) string {
	if t, ok := parseISO8601(text); ok {
		return clock(t)
	}
	return text
}

// The status page.

type nodeView struct {
	Name, Linked, Models, PausedAt string
	Online, Loaded, Paused         bool
}

type sessionView struct{ Project, Subject, Since, Short string }

type macView struct {
	Name, Linked, Count string
	Sessions            []sessionView
}

type liveView struct {
	Short, Project, Subject, State, For, Mac, Node string
	Connected                                      bool
}

type waitingView struct {
	Short, Project, Subject, Why, Rings string
	Question, NoSpeech                  bool
}

type phoneView struct {
	Name, Environment, Token, Registered, CheckedIn, Hours, Hold, HoldSet, OnDevice, Push string
	OnHold, PushOK, PushFailed                                                            bool
}

type statusView struct {
	Problems  []string
	Nodes     []nodeView
	Macs      []macView
	Live      []liveView
	Waiting   []waitingView
	Phones    []phoneView
	Usable    int // speech nodes linked and not paused
	Sessions  int
	PushesBad int
}

func statusData() statusView {
	var v statusView
	registryMu.Lock()
	nodeLinks := make([]*link, 0, len(nodes))
	for l := range nodes {
		nodeLinks = append(nodeLinks, l)
	}
	type agentSessions struct {
		l        *link
		sessions []map[string]string
	}
	var agentList []agentSessions
	for l, sessions := range agents {
		agentList = append(agentList, agentSessions{l, sessions})
	}
	calls := make([]*Call, 0, len(live))
	for _, c := range live {
		calls = append(calls, c)
	}
	registryMu.Unlock()

	// Speech nodes, linked or paused.
	paused := pausedNodes()
	shown := map[string]bool{}
	sort.Slice(nodeLinks, func(i, j int) bool { return nodeLinks[i].name < nodeLinks[j].name })
	for _, l := range nodeLinks {
		n := nodeView{Name: l.name, Linked: "Linked " + since(l.since), Online: true, Models: "Models not loaded"}
		if loaded, at := l.models(); loaded {
			n.Loaded, n.Models = true, "Models loaded "+since(at)
		} else if !at.IsZero() {
			n.Models = "Models not loaded, let go at " + clock(at)
		}
		if at, ok := paused[l.name]; ok {
			n.Paused, n.PausedAt = true, "Paused at "+isoClock(at)
		} else {
			v.Usable++
		}
		shown[l.name] = true
		v.Nodes = append(v.Nodes, n)
	}
	var pausedOnly []string
	for name := range paused {
		if !shown[name] {
			pausedOnly = append(pausedOnly, name)
		}
	}
	sort.Strings(pausedOnly)
	for _, name := range pausedOnly {
		v.Nodes = append(v.Nodes, nodeView{Name: name, Linked: "Not linked", Paused: true, PausedAt: "Paused at " + isoClock(paused[name])})
	}

	// Macs and their sessions.
	sort.Slice(agentList, func(i, j int) bool { return agentList[i].l.name < agentList[j].l.name })
	for _, a := range agentList {
		m := macView{Name: a.l.name, Linked: "Linked " + since(a.l.since)}
		for _, s := range a.sessions {
			started := ""
			if t, ok := parseISO8601(s["since"]); ok {
				started = "since " + clock(t)
			}
			m.Sessions = append(m.Sessions, sessionView{Project: s["project"], Subject: s["subject"], Since: started, Short: prefix(s["session_id"], 8)})
		}
		m.Count = plural(len(m.Sessions), "session", "sessions") + " listening"
		v.Sessions += len(m.Sessions)
		v.Macs = append(v.Macs, m)
	}

	// Calls, live and waiting.
	sort.Slice(calls, func(i, j int) bool { return calls[i].offeredAt.Before(calls[j].offeredAt) })
	for _, c := range calls {
		c.mu.Lock()
		answered, connected, ended := c.answeredAt, c.connectedAt, c.ended
		c.mu.Unlock()
		if ended {
			continue
		}
		lv := liveView{Short: c.short, Mac: c.agent.name, Node: c.node.name, Connected: !connected.IsZero()}
		if note, ok := noteOf(c.id); ok {
			lv.Project, lv.Subject = note.project, note.subject
		}
		switch {
		case answered.IsZero() && connected.IsZero():
			lv.State, lv.For = "Ringing", span(now().Sub(c.offeredAt))
		case answered.IsZero():
			lv.State, lv.For = "Ringing, its audio connected", span(now().Sub(c.offeredAt))
		case connected.IsZero():
			lv.State, lv.For = "Answered, connecting", span(now().Sub(answered))
		default:
			lv.State, lv.For = "On the call", span(now().Sub(maxTime(answered, connected)))
		}
		v.Live = append(v.Live, lv)
	}
	stateMu.Lock()
	waitingCalls, _ := listWaiting()
	devices := loadDevices()
	stateMu.Unlock()
	waitingForSpeech := 0
	for _, call := range waitingCalls {
		wv := waitingView{Short: prefix(fmt.Sprint(call["call_id"]), 8), Question: call["question"] == true}
		wv.Project, _ = call["project"].(string)
		wv.Subject, _ = call["subject"].(string)
		switch until, _ := call["held_until"].(string); {
		case call["reason"] == "no speech":
			wv.NoSpeech, wv.Why, wv.Rings = true, "No speech node", "Rings when one is back"
			waitingForSpeech++
		case until != "":
			wv.Why, wv.Rings = "On hold", "Rings at "+isoClock(until)
		default:
			wv.Why, wv.Rings = "On hold", "Rings when you take calls"
		}
		v.Waiting = append(v.Waiting, wv)
	}

	// Phones.
	tokens := make([]string, 0, len(devices))
	for token := range devices {
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool { return devices[tokens[i]]["device"] < devices[tokens[j]]["device"] })
	for _, token := range tokens {
		d := devices[token]
		ph := phoneView{Name: orDefault(d["device"], "A phone"), Environment: orDefault(d["environment"], "sandbox"),
			Token: "…" + suffix(token, 8), Registered: isoClock(d["registered"]), Hours: "Off", OnDevice: "Not reported"}
		switch at, checked := parseISO8601(d["checked_in"]); {
		case d["notification_token"] == "":
			ph.CheckedIn = "No connection checks: notifications aren't allowed"
		case checked:
			ph.CheckedIn = clock(at) + ", " + span(now().Sub(at)) + " ago"
		default:
			ph.CheckedIn = "Not yet"
		}
		if h := describe(d); h != "" {
			ph.Hours = strings.TrimPrefix(h, "work hours ")
		}
		hold := newHold(d, now())
		ph.OnHold = hold.On
		switch {
		case hold.On && hold.Until.IsZero():
			ph.Hold = "On hold, until you take calls"
		case hold.On:
			ph.Hold = "On hold, until " + clock(hold.Until)
		case hold.Until.IsZero():
			ph.Hold = "Taking calls"
		default:
			ph.Hold = "Taking calls, until " + clock(hold.Until)
		}
		if set := d["hold_set"]; set != "" {
			ph.HoldSet = "Set on the phone at " + isoClock(set)
		}
		switch d["on_device"] {
		case "yes":
			ph.OnDevice = "On"
		case "no":
			ph.OnDevice = "Off"
		}
		if last, found := lastPush(d, token); !found {
			ph.Push = "None since the hub started"
		} else if last.ok() {
			ph.PushOK, ph.Push = true, "Delivered to Apple at "+clock(last.at)
		} else {
			ph.PushFailed = true
			ph.Push = "Failed at " + clock(last.at) + ": " + pushProblem(last)
			v.PushesBad++
			v.Problems = append(v.Problems, "The last push to "+ph.Name+" failed: "+pushProblem(last)+".")
		}
		v.Phones = append(v.Phones, ph)
	}

	// What's wrong, most serious first: as the app's banner, without the phone's own view.
	var speech []string
	switch {
	case v.Usable > 0:
	case len(nodeLinks) > 0:
		speech = append(speech, "Every linked speech node is paused, so calls wait instead of ringing. Resume one below.")
	case len(agentList) > 0 || waitingForSpeech > 0:
		speech = append(speech, "No speech node is linked, so calls wait instead of ringing. They ring once one links.")
	}
	if len(devices) == 0 {
		v.Problems = append(v.Problems, "No phone has registered: open the app and enter the hub's address in Settings.")
	}
	v.Problems = append(speech, v.Problems...)
	return v
}

// pushProblem is why a push failed, in words.
func pushProblem(p pushResult) string {
	detail := outcome(p.status, p.reason, p.err)
	switch p.problem() {
	case "key":
		return "Apple refused the hub's push key, or it has none (" + detail + ")"
	case "phone":
		return "Apple no longer knows this phone; open the app to register again (" + detail + ")"
	case "network":
		return "the hub couldn't reach Apple (" + detail + ")"
	}
	return "Apple refused it (" + detail + ")"
}

// The calls page: the last calls the phone reported, from events.jsonl.

type eventView struct {
	Time, Name, Detail string
	Bad                bool
}

type recentView struct {
	ID, Short, Project, Subject, When, Outcome, Duration string
	Bad                                                  bool
	Events                                               []eventView
	last                                                 int
	audioFailed                                          bool
}

// finalEvents are the events that say how a call ended, as the call's outcome.
var finalEvents = map[string]string{
	"ended": "Answered", "missed": "Missed", "declined": "Declined", "failed": "Failed",
	"claude app": "Its question went to the Claude app",
}

var badOutcomes = map[string]bool{"Missed": true, "Declined": true, "Failed": true, "Audio failed": true, "Answered, its audio failed": true}

// eventDetails are what an event says besides its name, as the calls page shows it.
var eventDetails = []struct{ key, format string }{
	{"note", "%v"}, {"error", "error: %v"}, {"duration_s", "lasted %v s"},
	{"since_answer_s", "%v s after answering"}, {"on_another_call", "on another call"},
}

// recentCalls are the last n calls in events.jsonl (its last MB), newest first, each with its
// events, its outcome and how long it lasted, as the phone reported them.
func recentCalls(n int) []recentView {
	lines := tailLines(eventsPath, 1<<20)
	byID := map[string]*recentView{}
	for i, line := range lines {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		id, _ := e["call_id"].(string)
		if id == "" {
			continue
		}
		c := byID[id]
		if c == nil {
			c = &recentView{ID: "call-" + prefix(id, 8), Short: prefix(id, 8)}
			if note, ok := noteOf(id); ok {
				c.Project, c.Subject = note.project, note.subject
			}
			byID[id] = c
		}
		c.last = i
		name := fmt.Sprint(e["event"])
		at := eventTime(e)
		if c.When == "" {
			c.When = clock(at)
		}
		ev := eventView{Time: clock(at), Name: name}
		var details []string
		for _, d := range eventDetails {
			if value, ok := e[d.key]; ok {
				if !strings.Contains(d.format, "%v") {
					details = append(details, d.format)
					continue
				}
				details = append(details, fmt.Sprintf(d.format, value))
			}
		}
		ev.Detail = strings.Join(details, " · ")
		_, ev.Bad = e["error"]
		c.Events = append(c.Events, ev)
		if label, final := finalEvents[name]; final {
			c.Outcome = label
		}
		c.audioFailed = c.audioFailed || name == "audio failed"
		if d, ok := e["duration_s"]; ok {
			var secs float64
			if _, err := fmt.Sscan(fmt.Sprint(d), &secs); err == nil {
				c.Duration = span(time.Duration(secs * float64(time.Second)))
			}
		}
	}
	list := make([]*recentView, 0, len(byID))
	for _, c := range byID {
		switch {
		case c.Outcome == "" && c.audioFailed:
			c.Outcome = "Audio failed"
		case c.Outcome == "":
			c.Outcome = "No outcome yet: " + c.Events[len(c.Events)-1].Name
		case c.Outcome == "Answered" && c.audioFailed:
			c.Outcome = "Answered, its audio failed"
		}
		c.Bad = badOutcomes[c.Outcome]
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].last > list[j].last })
	if len(list) > n {
		list = list[:n]
	}
	out := make([]recentView, len(list))
	for i, c := range list {
		out[i] = *c
	}
	return out
}

// eventTime is when an event happened: on the phone ("at"), or when the hub wrote it ("time",
// in the hub's zone).
func eventTime(e map[string]any) time.Time {
	if at, ok := parseISO8601(fmt.Sprint(e["at"])); ok {
		return at
	}
	t, _ := time.ParseInLocation("2006-01-02T15:04:05", fmt.Sprint(e["time"]), time.Local)
	return t
}

// tailLines are the whole lines in a file's last limit bytes.
func tailLines(name string, limit int64) []string {
	file, err := os.Open(name)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	start := max(info.Size()-limit, 0)
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // cut in the middle
	}
	return lines
}

// The log page.

type logLine struct {
	Text string
	Bad  bool
}

func logData() []logLine {
	var lines []logLine
	for _, text := range logTail() {
		lower := strings.ToLower(text)
		bad := false
		for _, word := range []string{"fail", "refused", "can't", "error", "went away"} {
			bad = bad || strings.Contains(lower, word)
		}
		lines = append(lines, logLine{text, bad})
	}
	return lines
}

// The setup page: what to put in a new speech node's or Mac's config, and the commands.

type setupView struct {
	Listen, Config, SpeechToken, AgentToken string
	SpeechSet, AgentSet                     bool
}

func setupData() setupView {
	config := loadConfig()
	shown := configPath
	if rest, inHome := strings.CutPrefix(configPath, home+"/"); inHome {
		shown = "~/" + rest
	}
	set := func(token string) bool { return token != "" && token != "…" && token != "..." }
	return setupView{Listen: config["listen"], Config: shown, SpeechToken: config["tokens.speech"], AgentToken: config["tokens.agent"],
		SpeechSet: set(config["tokens.speech"]), AgentSet: set(config["tokens.agent"])}
}
