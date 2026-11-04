package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// An address refused too often is shut out for a minute, others aren't, and the hub's own
// machine never is; a WebSocket still upgrades through the count.
func TestRefusalLimit(t *testing.T) {
	refusalsMu.Lock()
	refusals = map[string]*refusalCount{}
	refusalsMu.Unlock()
	handler := limited(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte("ok"))
	}))
	from := func(addr, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = addr
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for i := 1; i <= refusalsAllowed; i++ {
		if got := from("198.51.100.7:1234", "/bad").Code; got != http.StatusUnauthorized {
			t.Fatalf("refusal %d: %d", i, got)
		}
	}
	shut := from("198.51.100.7:4321", "/good")
	if shut.Code != http.StatusTooManyRequests || shut.Header().Get("Retry-After") != "61" {
		t.Errorf("after %d refusals: %d, Retry-After %q", refusalsAllowed, shut.Code, shut.Header().Get("Retry-After"))
	}
	if got := from("198.51.100.8:1234", "/good").Code; got != http.StatusOK {
		t.Errorf("another address: %d", got)
	}
	for i := 0; i < 2*refusalsAllowed; i++ {
		from("127.0.0.1:1234", "/bad")
	}
	if got := from("127.0.0.1:1234", "/good").Code; got != http.StatusOK {
		t.Errorf("the hub's own machine: %d", got)
	}
	saved := now()
	advance(refusalWindow + time.Second)
	got := from("198.51.100.7:1234", "/good").Code
	setClock(saved)
	if got != http.StatusOK {
		t.Errorf("a minute later: %d", got)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "198.51.100.9:5555" // counted, as from the internet
		limited(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ws, err := websocket.Accept(w, r, nil); err == nil {
				ws.Close(websocket.StatusNormalClosure, "")
			}
		})).ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("a WebSocket through the count: %v", err)
	}
	ws.CloseNow()
}
