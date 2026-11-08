package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// recorder is a link whose messages land in got, as a speech node would receive them.
func recorder(t *testing.T, whole bool) (*link, chan map[string]any) {
	t.Helper()
	got := make(chan map[string]any, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			_, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(data, &m) == nil {
				got <- m
			}
		}
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	return &link{kind: "speech", ws: ws, ctx: ctx, wholeReplies: whole}, got
}

// spoken is the next `speak` the node got, or "" if none came in a moment.
func spoken(got chan map[string]any) string {
	for {
		select {
		case m := <-got:
			if m["type"] == "speak" {
				return m["text"].(string)
			}
		case <-time.After(200 * time.Millisecond):
			return ""
		}
	}
}

func TestWholeRepliesGoInOneTake(t *testing.T) {
	node, got := recorder(t, true)
	agent, _ := recorder(t, false)
	c := &Call{id: "whole", short: "whole", node: node, agent: agent, stream: 7}
	c.write(1, "The first sentence. And the sec")
	c.write(1, "ond one.")
	if text := spoken(got); text != "" {
		t.Fatalf("spoken before the reply was all written: %q", text)
	}
	c.written(1, 0)
	if text := spoken(got); text != "The first sentence. And the second one." {
		t.Fatalf("spoken %q, want the whole reply at once", text)
	}
	if text := spoken(got); text != "" {
		t.Fatalf("a second piece: %q", text)
	}
}

func TestSentenceAtATimeWithoutIt(t *testing.T) {
	node, got := recorder(t, false)
	agent, _ := recorder(t, false)
	c := &Call{id: "pieces", short: "pieces", node: node, agent: agent, stream: 7}
	c.write(1, "The first sentence. And the sec")
	if text := spoken(got); text != "The first sentence." {
		t.Fatalf("spoken %q, want the first sentence at once", text)
	}
}

func TestBreakSpeaksTheBlockNow(t *testing.T) {
	node, got := recorder(t, true)
	agent, _ := recorder(t, false)
	c := &Call{id: "break", short: "break", node: node, agent: agent, stream: 7}
	c.write(1, "Sure, I'll pass that on.")
	c.breakReply(1)
	if text := spoken(got); text != "Sure, I'll pass that on." {
		t.Fatalf("spoken %q at the break, want the words before it", text)
	}
	c.write(1, " It's done.")
	c.written(1, 0)
	if text := spoken(got); text != "It's done." {
		t.Fatalf("spoken %q after the break, want the rest as its own take", text)
	}
}

// A take still streaming has sent more audio than has played, but less than it will be: what
// you've heard of it follows the voice's pace, not the audio so far.
func TestHeardOfATakeStillComing(t *testing.T) {
	text := "I finished the changes you asked for. The login page now remembers where you were before you signed in."
	p := &piece{id: 1, text: text, samples: int(3 * speakingRate)} // 3 s here of about 5.8 s
	c := &Call{pieces: []*piece{p}}
	p.start = now().Add(-1500 * time.Millisecond)
	heard := c.heard(now())
	// 1.5 s at 0.055 s a character is about 27 characters: "I finished the changes you".
	if !strings.HasPrefix(text, heard) || len(heard) < 15 || len(heard) > 40 {
		t.Fatalf("heard %q 1.5 s into the take; want about its first 27 characters", heard)
	}
}
