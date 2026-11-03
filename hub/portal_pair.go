package main

// The portal's pairing page, /portal/pair: opened on the phone, from the app's "not paired"
// banner or by hand, it signs you in first if need be and comes back. Its one button makes a
// one-time pairing code (pairing.go) and opens Ringback with it at once (ringback://pair), with
// an Open Ringback link in case Safari doesn't follow.

import (
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

// pairView is the pairing page's: the link to Ringback once made, and until when it works.
type pairView struct {
	Link  template.URL
	Until string
}

func pairForm(w http.ResponseWriter, r *http.Request, s *portalSession, base page) {
	stateMu.Lock()
	code, expires, err := newPairingCode()
	stateMu.Unlock()
	if err != nil {
		log("portal: can't make a pairing code: " + err.Error())
		http.Error(w, "can't make a pairing code", http.StatusInternalServerError)
		return
	}
	log("portal: " + s.name + " made a link to pair a phone, from " + address(r))
	// The link is the hub's own: its address, query-escaped, and a base64url code.
	link := template.URL(pairingLink(code))
	base.Title, base.Page, base.Open = "Pair this phone", "setup", link
	base.Data = pairView{Link: link, Until: expires.Local().Format("15:04")}
	render(w, http.StatusOK, "pair", base)
}

// portalPath is next if it's a page of the portal's own (/portal/…, a path alone), the only
// place a login may go back to; "" otherwise, so a link can't send you elsewhere once you've
// signed in.
func portalPath(next string) string {
	if !strings.HasPrefix(next, "/portal/") || strings.ContainsAny(next, "\\\r\n\t") ||
		strings.Contains(next, "//") || strings.Contains(next, "/..") {
		return ""
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Path != next {
		return ""
	}
	return next
}
