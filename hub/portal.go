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
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// The hub's portal: pages under /portal/ for setting up and for the rare times something's
// wrong ("The hub's portal" in docs/design.md). It shows a
// map of the system, the calls, the phones and the log, and changes only what's kept in the
// hub's state: the hold, on every phone at once, and the parts it remembers having seen;
// secrets stay in hub.toml. Plain HTML and SVG from templates in the binary, which refresh
// themselves with a small script.

//go:embed portal
var portalFiles embed.FS

var portalPages = parsePortalPages()

func parsePortalPages() map[string]*template.Template {
	funcs := template.FuncMap{
		"n":    num, // a number in an SVG attribute
		"part": func(csrf string, p partView) map[string]any { return map[string]any{"CSRF": csrf, "Part": p} },
	}
	base := template.Must(template.New("base").Funcs(funcs).ParseFS(portalFiles, "portal/base.html"))
	pages := map[string]*template.Template{}
	for _, name := range []string{"status", "calls", "log", "setup", "pair", "login", "code", "noaccount", "message"} {
		pages[name] = template.Must(template.Must(base.Clone()).ParseFS(portalFiles, "portal/"+name+".html"))
	}
	return pages
}

// page is what every page's template gets: Data is the page's own; Hold, the hub's hold, is
// in every signed-in page's bar.
type page struct {
	Title, Page, CSRF, User, Updated, Error string
	Refresh                                 int // seconds between refreshes; 0: none
	Hold                                    holdView
	Data                                    any
	Next                                    string       // the login's: the portal page to go to once signed in
	Open                                    template.URL // a link the page opens at once (pairing's)
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
		// A page you opened comes back once you've signed in, as the app's Pair button needs.
		login := "/portal/login"
		if next := portalPath(p); next != "" && next != "/portal/" && r.Method == http.MethodGet {
			login += "?next=" + url.QueryEscape(next)
		}
		http.Redirect(w, r, login, http.StatusSeeOther)
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
		case "/portal/hold":
			holdForm(w, r, s)
		case "/portal/pair":
			pairForm(w, r, s, base)
		case "/portal/forget":
			// Any name the hub has seen, however long: a link's hello gives it, unbounded.
			kind, name := r.PostFormValue("kind"), r.PostFormValue("name")
			if (kind != "speech" && kind != "agent") || name == "" {
				http.Error(w, "needs a speech node's or a Mac's name", http.StatusBadRequest)
				return
			}
			forgot, err := forgetPart(kind, name)
			if err != nil {
				log("portal: can't keep the parts seen: " + err.Error())
				http.Error(w, "can't keep the parts seen", http.StatusInternalServerError)
				return
			}
			if forgot {
				log("portal: " + s.name + " forgot " + map[string]string{"speech": "the speech node ", "agent": "the Mac "}[kind] + name)
			}
			http.Redirect(w, r, "/portal/#parts", http.StatusSeeOther)
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
	base.Hold = currentHold()
	switch p {
	case "/portal/":
		base.Title, base.Page, base.Refresh = "Status", "status", refreshEvery
		status := statusData()
		base.Data, base.Hold = status, status.Hold
		render(w, http.StatusOK, "status", base)
	case "/portal/calls":
		base.Title, base.Page, base.Refresh, base.Data = "Calls", "calls", refreshEvery, callsData()
		render(w, http.StatusOK, "calls", base)
	case "/portal/log":
		base.Title, base.Page, base.Refresh, base.Data = "Log", "log", refreshEvery, logData()
		render(w, http.StatusOK, "log", base)
	case "/portal/setup":
		base.Title, base.Page, base.Data = "Setup", "setup", setupData()
		render(w, http.StatusOK, "setup", base)
	case "/portal/pair":
		base.Title, base.Page, base.Data = "Pair this phone", "setup", pairView{}
		render(w, http.StatusOK, "pair", base)
	default:
		base.Title, base.Data = "Not found", "There's no such page."
		render(w, http.StatusNotFound, "message", base)
	}
}

// holdForm sets the hub's hold, which is every paired phone's hold switch: "on", and no
// call rings, whatever speech there is, until you take calls or, as the app's switch, work
// hours next start; "off", and the calls held ask again, and ring. The app shows it the next
// time it asks the hub, as it does on opening.
func holdForm(w http.ResponseWriter, r *http.Request, s *portalSession) {
	value := r.PostFormValue("hold")
	if value != "on" && value != "off" {
		http.Error(w, "needs hold: on or off", http.StatusBadRequest)
		return
	}
	on := value == "on"
	var held []any
	n, err := func() (int, error) {
		stateMu.Lock()
		defer stateMu.Unlock() // a panic here mustn't leave every later request waiting
		n, err := holdEveryPhone(on)
		if err == nil && !on {
			held = waitingFor("")
		}
		return n, err
	}()
	if err != nil {
		log("portal: can't set the hold: " + err.Error())
		http.Error(w, "can't set the hold", http.StatusInternalServerError)
		return
	}
	state := "took calls"
	if on {
		state = "held calls"
	}
	log("portal: " + s.name + " " + state + ", on " + plural(n, "phone", "phones"))
	if len(held) > 0 {
		log("portal: " + plural(len(held), "call", "calls") + " held, they ask again")
		broadcast(map[string]any{"type": "reask", "call_ids": held})
	}
	http.Redirect(w, r, "/portal/#hold", http.StatusSeeOther)
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
	next := portalPath(r.URL.Query().Get("next"))
	if r.Method == http.MethodPost {
		next = portalPath(r.PostFormValue("next"))
	}
	if signedIn(r, a) != nil {
		http.Redirect(w, r, orDefault(next, "/portal/"), http.StatusSeeOther)
		return
	}
	p := page{Title: "Sign in", Data: "", Next: next}
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
			startLogin(w, a, next)
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
		http.Redirect(w, r, orDefault(portalPath(pending.next), "/portal/"), http.StatusSeeOther)
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

// The calls page: the calls waiting, then the last calls the phone reported, from
// events.jsonl.

type callsView struct {
	Waiting []waitingView
	Days    []callDay
}

func callsData() callsView {
	stateMu.Lock()
	waiting, _ := listWaiting()
	stateMu.Unlock()
	return callsView{waitingViews(waiting), byDay(recentCalls(50))}
}

type eventView struct {
	Time, Name, Detail string
	Bad                bool
}

type recentView struct {
	ID, Short, Project, Subject, Title, When, Outcome, Duration string
	Bad, Answered, Untitled, Live                               bool
	Events                                                      []eventView
	last                                                        int
	audioFailed                                                 bool
	at                                                          time.Time // its first event's
}

// callDay is the calls of one day, as the calls page lists them: "Today", "Yesterday", a
// weekday within the week, or the date.
type callDay struct {
	Label string
	Calls []recentView
}

// byDay groups calls, newest first, by the day they started, in the hub's time zone.
func byDay(calls []recentView) []callDay {
	var days []callDay
	today := now().Local()
	for _, c := range calls {
		t := c.at.Local()
		label := t.Format("2 January 2006")
		if t.Year() == today.Year() {
			label = t.Format("2 January")
		}
		y1, m1, d1 := t.Date()
		for back := range 7 {
			y2, m2, d2 := today.AddDate(0, 0, -back).Date()
			if y1 == y2 && m1 == m2 && d1 == d2 {
				label = [...]string{"Today", "Yesterday"}[min(back, 1)]
				if back > 1 {
					label = t.Format("Monday")
				}
				break
			}
		}
		if c.at.IsZero() {
			label = "Earlier"
		}
		if len(days) == 0 || days[len(days)-1].Label != label {
			days = append(days, callDay{Label: label})
		}
		days[len(days)-1].Calls = append(days[len(days)-1].Calls, c)
	}
	return days
}

// finalEvents are the events that say how a call ended, as the call's outcome.
var finalEvents = map[string]string{
	"ended": "Answered", "missed": "Missed", "declined": "Declined", "failed": "Failed",
	"claude app": "Its question went to the Claude app",
}

var badOutcomes = map[string]bool{"Missed": true, "Declined": true, "Failed": true, "Audio failed": true, "Answered, its audio failed": true}

// seconds is a number of seconds as an event gives it, as text or a number.
func seconds(value any) (float64, bool) {
	var secs float64
	_, err := fmt.Sscan(fmt.Sprint(value), &secs)
	return secs, err == nil
}

// eventDetail is what an event says besides its name, as the calls page shows it; how long, as
// everywhere on the portal ("3 min 12 s").
func eventDetail(e map[string]any) string {
	var details []string
	if note, ok := e["note"]; ok {
		details = append(details, fmt.Sprint(note))
	}
	if err, ok := e["error"]; ok {
		details = append(details, fmt.Sprintf("error: %v", err))
	}
	if secs, ok := seconds(e["duration_s"]); ok {
		details = append(details, "lasted "+span(time.Duration(secs*float64(time.Second))))
	}
	if secs, ok := seconds(e["since_answer_s"]); ok {
		details = append(details, fmt.Sprintf("%.1f s after answering", secs))
	}
	if _, ok := e["on_another_call"]; ok {
		details = append(details, "on another call")
	}
	return strings.Join(details, "; ")
}

// recentCalls are the last n calls in events.jsonl (its last MB), newest first, each with its
// events, its outcome and how long it lasted, as the phone reported them. One without its
// project (placed before the hub last started) is titled by its Mac if the hub knows it.
func recentCalls(n int) []recentView {
	lines := tailLines(eventsPath, 1<<20)
	byID := map[string]*recentView{}
	ids := map[*recentView]string{}
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
			c = &recentView{ID: "call-" + prefix(id, 8), Short: prefix(id, 8), Title: "Project unknown", Untitled: true}
			if note, ok := noteOf(id); ok {
				c.Project, c.Subject = note.project, note.subject
				switch {
				case note.project != "":
					c.Title, c.Untitled = note.project, false
				case note.mac != "":
					c.Title, c.Untitled = "From "+note.mac, false
				}
			}
			byID[id], ids[c] = c, id
		}
		c.last = i
		name := fmt.Sprint(e["event"])
		at := eventTime(e)
		if c.When == "" {
			c.When, c.at = at.Local().Format("15:04"), at
		}
		ev := eventView{Time: at.Local().Format("15:04:05"), Name: name, Detail: eventDetail(e)}
		_, ev.Bad = e["error"]
		c.Events = append(c.Events, ev)
		if label, final := finalEvents[name]; final {
			c.Outcome = label
		}
		c.audioFailed = c.audioFailed || name == "audio failed"
		if secs, ok := seconds(e["duration_s"]); ok {
			c.Duration = span(time.Duration(secs * float64(time.Second)))
		}
	}
	list := make([]*recentView, 0, len(byID))
	for _, c := range byID {
		switch {
		case c.Outcome == "" && liveCall(ids[c]) != nil:
			lc := liveCall(ids[c])
			lc.mu.Lock()
			answered := !lc.answeredAt.IsZero()
			lc.mu.Unlock()
			c.Outcome, c.Live = "Ringing now", true
			if answered {
				c.Outcome = "Live now"
			}
		case c.Outcome == "" && c.audioFailed:
			c.Outcome = "Audio failed"
		case c.Outcome == "":
			c.Outcome = "No outcome yet: " + c.Events[len(c.Events)-1].Name
		case c.Outcome == "Answered" && c.audioFailed:
			c.Outcome = "Answered, its audio failed"
		}
		c.Bad = badOutcomes[c.Outcome]
		c.Answered = strings.HasPrefix(c.Outcome, "Answered")
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].at.Equal(list[j].at) {
			return list[i].at.After(list[j].at)
		}
		return list[i].last > list[j].last
	})
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

// The log page: the hub's last lines, newest first.

type logLine struct {
	Time, Text string
	Bad        bool
}

func logData() []logLine {
	tail := logTail()
	lines := make([]logLine, 0, len(tail))
	for i := len(tail) - 1; i >= 0; i-- {
		text := tail[i]
		lower := strings.ToLower(text)
		bad := false
		for _, word := range []string{"fail", "refused", "can't", "error", "went away"} {
			bad = bad || strings.Contains(lower, word)
		}
		line := logLine{Text: text, Bad: bad}
		if at, rest, timed := strings.Cut(text, "  "); timed && len(at) == len("15:04:05") {
			line.Time, line.Text = at, rest
		}
		lines = append(lines, line)
	}
	return lines
}

// The setup page: what each part needs to work with the hub, and which are set up already:
// what to put in a new speech node's or Mac's config and the commands, how the phone finds the
// hub, and Apple push in the hub's config.

type setupPart struct {
	Done   bool
	Status string // "gpu-tower is linked.", "Not set up yet."
}

type setupView struct {
	// How the phone, an agent and a speech node reach the hub.
	PhoneHub, AgentHub, SpeechHub                string
	Config, SpeechToken, AgentToken, PushProblem string
	SpeechSet, AgentSet                          bool
	Node, Mac, Phone, Push                       setupPart
}

func setupData() setupView {
	config := loadConfig()
	shown := configPath
	if rest, inHome := strings.CutPrefix(configPath, home+"/"); inHome {
		shown = "~/" + rest
	}
	set := func(token string) bool { return token != "" && token != "…" && token != "..." }
	stateMu.Lock()
	devices := loadDevices()
	stateMu.Unlock()
	seenMu.Lock()
	seen := loadSeen()
	seenMu.Unlock()
	nodeNames, macNames := linked()
	var macs []string
	for _, mac := range macNames {
		macs = append(macs, fmt.Sprint(mac["name"]))
	}
	v := setupView{Config: shown, SpeechToken: config["tokens.speech"], AgentToken: config["tokens.agent"],
		SpeechSet: set(config["tokens.speech"]), AgentSet: set(config["tokens.agent"])}
	// At the listen address, the shortest they can be; otherwise the hub's URL, which the
	// agent turns into its link's.
	v.PhoneHub, v.AgentHub, v.SpeechHub = config["listen"], config["listen"], "ws://"+config["listen"]+"/speech"
	if config["url"] != "" || config["tls.cert"] != "" {
		base := hubURL()
		v.PhoneHub, v.AgentHub, v.SpeechHub = base, base, "ws"+strings.TrimPrefix(base, "http")+"/speech"
	}
	v.Node = partSetUp(nodeNames, len(seen["speech"]), "speech node")
	v.Mac = partSetUp(macs, len(seen["agent"]), "Mac")
	var phones, unpaired []string
	for _, d := range devicesByName(devices) {
		if d["key"] == "" {
			unpaired = append(unpaired, orDefault(d["device"], "A phone"))
		} else {
			phones = append(phones, orDefault(d["device"], "A phone"))
		}
	}
	switch {
	case len(phones) == 1:
		v.Phone = setupPart{true, phones[0] + " is paired."}
	case len(phones) > 1:
		v.Phone = setupPart{true, andList(phones) + " are paired."}
	case len(unpaired) > 0:
		v.Phone = setupPart{false, andList(unpaired) + " registered but hasn't paired yet."}
	default:
		v.Phone = setupPart{false, "No phone is paired yet."}
	}
	push := checkPush(config, devices)
	switch {
	case !push.set:
		v.Push = setupPart{false, "Not set up yet."}
	case push.problem != "":
		v.Push, v.PushProblem = setupPart{false, "Set up, but " + push.problem + "."}, push.detail
	default:
		v.Push = setupPart{true, "Set up."}
	}
	return v
}

// partSetUp says whether speech nodes or Macs are set up: linked now, or seen before.
func partSetUp(linkedNow []string, seen int, kind string) setupPart {
	switch {
	case len(linkedNow) == 1:
		return setupPart{true, linkedNow[0] + " is linked."}
	case len(linkedNow) > 1:
		return setupPart{true, andList(linkedNow) + " are linked."}
	case seen > 0:
		return setupPart{true, plural(seen, kind+" has", kind+"s have") + " linked before, and none is linked now."}
	}
	return setupPart{false, "None has linked yet."}
}
