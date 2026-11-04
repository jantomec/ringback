package main

// Refusals: an address the hub keeps refusing (401 or 403: a key, token, pairing code or call
// ID that isn't right) is shut out for a while, so nothing on the internet can try many of them
// quickly. Every one of those is random and long, so this is about load more than guessing.
// The portal's sign-in has its own, tighter limit (portal_auth.go). The hub's own machine
// (loopback) isn't counted, as the checks that run there refuse many on purpose.

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	refusalsAllowed = 30          // refused requests from one address within refusalWindow
	refusalWindow   = time.Minute // and for how long it's shut out after that
)

var (
	refusalsMu sync.Mutex
	refusals   = map[string]*refusalCount{}
)

type refusalCount struct {
	count          int
	since, shutOut time.Time
}

// limited answers 429 to an address shut out, and counts the requests the hub refuses.
func limited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from := address(r)
		if ip := net.ParseIP(from); ip != nil && ip.IsLoopback() {
			next.ServeHTTP(w, r)
			return
		}
		if until := shutOutUntil(from); !until.IsZero() {
			w.Header().Set("Retry-After", fmt.Sprint(int(until.Sub(now()).Seconds())+1))
			http.Error(w, `{"ok": false, "error": "too many refused requests"}`, http.StatusTooManyRequests)
			return
		}
		seen := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(seen, r)
		if seen.status == http.StatusUnauthorized || seen.status == http.StatusForbidden {
			countRefusal(from)
		}
	})
}

// shutOutUntil is when an address may try again; zero if it may now. It also forgets the
// addresses that have been quiet for a while.
func shutOutUntil(from string) time.Time {
	refusalsMu.Lock()
	defer refusalsMu.Unlock()
	t := now()
	for a, c := range refusals {
		if t.After(c.shutOut) && t.Sub(c.since) > refusalWindow {
			delete(refusals, a)
		}
	}
	if c := refusals[from]; c != nil && t.Before(c.shutOut) {
		return c.shutOut
	}
	return time.Time{}
}

func countRefusal(from string) {
	refusalsMu.Lock()
	defer refusalsMu.Unlock()
	t := now()
	c := refusals[from]
	if c == nil || t.Sub(c.since) > refusalWindow {
		c = &refusalCount{since: t}
		refusals[from] = c
	}
	c.count++
	if c.count == refusalsAllowed {
		c.shutOut = t.Add(refusalWindow)
		log(fmt.Sprintf("%s: %d refused requests within a minute, shut out for a minute", from, refusalsAllowed))
	}
}

// statusWriter notes the status a handler answers. A WebSocket's upgrade reaches the
// connection through Unwrap.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
