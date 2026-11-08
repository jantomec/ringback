package audiocpp

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func pcm(values ...int16) string {
	b := make([]byte, 2*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// fake serves /v1/audio/speech as audio.cpp does, with the events given, one at a time.
func fake(t *testing.T, events []string, pause time.Duration) *Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(pause):
			}
			fmt.Fprintf(w, "data: %s\n\n", e)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return &Server{voice: Voice{Rate: 24000, Stall: 200 * time.Millisecond}, base: srv.URL, exit: make(chan struct{})}
}

func TestSpeakStreamsSamples(t *testing.T) {
	s := fake(t, []string{
		`{"type":"speech.audio.delta","audio":"` + pcm(16384, -16384) + `"}`,
		`{"type":"speech.text.delta","delta":"ignored"}`,
		`{"type":"speech.audio.delta","audio":"` + pcm(0) + `"}`,
		`{"type":"speech.audio.done"}`, `[DONE]`,
	}, 0)
	var got []float32
	if err := s.Speak(context.Background(), "Hello.", func(x []float32) { got = append(got, x...) }); err != nil {
		t.Fatal(err)
	}
	if want := []float32{0.5, -0.5, 0}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("samples %v, want %v", got, want)
	}
}

func TestSilentServerIsHung(t *testing.T) {
	s := fake(t, []string{`{"type":"speech.audio.delta","audio":"` + pcm(1) + `"}`}, time.Second)
	if err := s.Speak(context.Background(), "Hello.", func([]float32) {}); !errors.Is(err, ErrHung) {
		t.Fatalf("err %v, want ErrHung", err)
	}
}

func TestStreamThatStopsIsHung(t *testing.T) {
	s := fake(t, []string{`{"type":"speech.audio.delta","audio":"` + pcm(1) + `"}`}, 0)
	if err := s.Speak(context.Background(), "Hello.", func([]float32) {}); !errors.Is(err, ErrHung) {
		t.Fatalf("err %v, want ErrHung for a stream with no end", err)
	}
}

func TestCutOff(t *testing.T) {
	events := []string{}
	for i := 0; i < 50; i++ {
		events = append(events, `{"type":"speech.audio.delta","audio":"`+pcm(1, 2)+`"}`)
	}
	s := fake(t, append(events, `{"type":"speech.audio.done"}`, `[DONE]`), 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	chunks := 0
	start := time.Now()
	err := s.Speak(ctx, "A long reply.", func([]float32) {
		if chunks++; chunks == 3 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > 300*time.Millisecond || chunks > 4 {
		t.Fatalf("cut off after %d chunks and %v; it should stop at once", chunks, took)
	}
}

func TestServerError(t *testing.T) {
	s := fake(t, []string{`{"type":"error","error":{"message":"no model"}}`}, 0)
	err := s.Speak(context.Background(), "Hello.", func([]float32) {})
	if err == nil || !strings.Contains(err.Error(), "no model") {
		t.Fatalf("err %v, want the server's error", err)
	}
}

// A server that stops partway through a take, as one that hung mid-reply, counts as hung
// too, not only one that never starts.
func TestStallPartwayIsHung(t *testing.T) {
	delta := `{"type":"speech.audio.delta","audio":"` + pcm(1, 2) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 6; i++ {
			pause := 20 * time.Millisecond
			if i == 3 {
				pause = 2 * time.Second
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(pause):
			}
			fmt.Fprintf(w, "data: %s\n\n", delta)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: {\"type\":\"speech.audio.done\"}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	s := &Server{voice: Voice{Rate: 24000, Stall: 200 * time.Millisecond}, base: srv.URL, exit: make(chan struct{})}
	chunks, start := 0, time.Now()
	err := s.Speak(context.Background(), "A long reply.", func([]float32) { chunks++ })
	if !errors.Is(err, ErrHung) || chunks != 3 {
		t.Fatalf("err %v after %d chunks, want ErrHung after 3", err, chunks)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("hung found after %v; the stall is 200 ms", took)
	}
}

// A server that goes away, as one that crashed, is hung however it shows: the connection
// reset partway, refused, or the process already gone.
func TestServerGoneIsHung(t *testing.T) {
	delta := `{"type":"speech.audio.delta","audio":"` + pcm(1, 2) + `"}`
	reset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", delta)
		w.(http.Flusher).Flush()
		time.Sleep(20 * time.Millisecond)
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.(*net.TCPConn).SetLinger(0) // closing sends a reset
		conn.Close()
	}))
	t.Cleanup(reset.Close)
	refused := httptest.NewServer(http.NotFoundHandler())
	refused.Close()
	exited := make(chan struct{})
	close(exited)
	for _, c := range []struct {
		name string
		s    *Server
	}{
		{"reset partway", &Server{base: reset.URL, exit: make(chan struct{})}},
		{"refused", &Server{base: refused.URL, exit: make(chan struct{})}},
		{"already exited", &Server{base: reset.URL, exit: exited}},
	} {
		c.s.voice = Voice{Rate: 24000, Stall: 2 * time.Second}
		start := time.Now()
		err := c.s.Speak(context.Background(), "Hello.", func([]float32) {})
		if !errors.Is(err, ErrHung) || time.Since(start) > time.Second {
			t.Errorf("%s: err %v after %v, want ErrHung at once", c.name, err, time.Since(start))
		}
	}
}
