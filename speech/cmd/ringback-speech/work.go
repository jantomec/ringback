package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"ringback/speech/internal/hearing"
)

// work is the worker process (relay.go): it loads the models, then hears and speaks for the
// hub's messages, which come on its stdin, and sends its own on its stdout (pipe.go). Once it
// has had no stream, no speech to make and no message for the idle time, it asks the link
// process to let it go; it exits when its stdin ends. The messages that came while it loaded
// are read once it has, so a warm-up without a call ends the idle time after loading.
func work(c config) {
	start := time.Now()
	pipe := takeStdout()
	m, err := loadModels(c, true, true)
	if err != nil {
		fail(err.Error())
	}
	n := newNode(m)

	out := make(chan frame, 8192)
	var dropped atomic.Int64
	send := func(kind byte, data []byte) {
		select {
		case out <- frame{kind, data}:
		default:
			if dropped.Add(1) == 1 {
				logf("the link process is behind; messages are dropped")
			}
		}
	}
	go func() {
		for f := range out {
			writeFrame(pipe, f) // if the link process is gone, stdin ends too
		}
	}()
	control := func(msg any) {
		data, _ := json.Marshal(msg)
		send(frameControl, data)
	}
	n.sink.Store(&sink{
		text: func(msg any) {
			data, _ := json.Marshal(msg)
			send(frameText, data)
		},
		bin: func(data []byte) { send(frameBinary, data) },
	})
	control(map[string]any{"type": "ready"})

	// mu is held while a message is handled, so that the idle check sees what it did, and
	// says how many it has handled.
	var mu sync.Mutex
	var read int64
	last := start
	go func() {
		t := time.NewTicker(max(10*time.Millisecond, min(time.Second, c.idle/4)))
		for range t.C {
			mu.Lock()
			if n.busy() {
				last = time.Now()
			} else if time.Since(last) >= c.idle {
				control(map[string]any{"type": "idle", "read": read})
				last = time.Now()
			}
			mu.Unlock()
		}
	}()

	in := bufio.NewReaderSize(os.Stdin, 64<<10)
	for {
		f, err := readFrame(in)
		if err != nil {
			if err != io.EOF {
				logf("the pipe from the link process: %v", err)
			}
			os.Exit(0)
		}
		mu.Lock()
		read++
		handle(n, f)
		last = time.Now()
		mu.Unlock()
	}
}

// takeStdout keeps stdout for the frames alone, on a new descriptor, and points descriptor 1
// at stderr, so that whatever else writes to stdout (the log, or a C library) goes to the log
// and can't break a frame.
func takeStdout() *os.File {
	fd, err := syscall.Dup(1)
	if err == nil {
		syscall.CloseOnExec(fd)
		err = syscall.Dup3(2, 1, 0)
	}
	if err != nil {
		fail("stdout: " + err.Error())
	}
	return os.NewFile(uintptr(fd), "pipe")
}

// handle takes one of the hub's messages.
func handle(n *node, f frame) {
	if f.kind == frameBinary {
		if len(f.data) < 4 || len(f.data)%2 != 0 {
			return
		}
		samples := make([]int16, (len(f.data)-4)/2)
		for i := range samples {
			samples[i] = int16(binary.LittleEndian.Uint16(f.data[4+2*i:]))
		}
		n.audio(binary.BigEndian.Uint32(f.data), samples)
		return
	}
	if f.kind != frameText {
		return
	}
	var m message
	if err := json.Unmarshal(f.data, &m); err != nil {
		logf("not JSON from the hub: %.80q", f.data)
		return
	}
	switch m.Type {
	case "warm": // the models are loaded by the time it's read
	case "hear":
		n.hear(m.Stream, setting(m.Sensitivity, hearing.DefaultSensitivity), setting(m.Gate, hearing.DefaultGate))
	case "hear_end":
		n.endHearing(m.Stream)
	case "speak":
		n.speak(m.Stream, m.Piece, m.Text)
	case "speak_cancel":
		n.cancel(m.Stream)
	default:
		logf("unknown message from the hub: %.80q", f.data)
	}
}

func setting(v *float64, fallback float64) float64 {
	if v == nil {
		return fallback
	}
	return *v
}
