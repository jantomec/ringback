package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// The link process keeps the hub's link and nothing else: the models live in a worker, a
// child process (`ringback-speech work`, work.go), because CUDA, cuDNN and ONNX Runtime keep
// their memory, on the GPU and off it, until their process exits. The link process starts a
// worker when a call is coming (`warm`) or a stream needs one (`hear`, `speak`), relays the
// hub's messages to it and its messages to the hub, and lets it go once it has been idle for
// the idle time (`idle` in speech.toml), so both are free between calls.

// relay is the link process's side of its workers.
type relay struct {
	c    config
	mu   sync.Mutex
	w    *worker  // the worker the hub's messages go to, or nil
	last *worker  // the last one started: the next waits for it to exit
	hub  *hubLink // the hub's link, while there is one
}

type hubLink struct {
	send func(kind websocket.MessageType, data []byte)
	drop func() // closes the link
}

// worker is a worker process, as the link process sees it.
type worker struct {
	r       *relay
	cmd     *exec.Cmd          // once it has started
	in      *queue             // the hub's messages for it, in order
	sent    int64              // frames queued for it
	closing bool               // let go, or stopped: nothing more goes to it, nothing more comes from it
	open    map[uint32]bool    // hearing streams opened and not ended
	pending map[[2]uint32]bool // pieces asked for, neither spoken nor cancelled
	why     string             // the message it was started for
	started time.Time
	exited  chan struct{}
}

func newRelay(c config) *relay { return &relay{c: c} }

// linked and unlinked: the hub's link opened and closed. A worker doesn't outlive the link:
// the hub ends its calls when the link drops.
func (r *relay) linked(h *hubLink) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hub = h
}

func (r *relay) unlinked() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hub = nil
	if w := r.w; w != nil {
		r.w = nil
		w.stop()
	}
}

// wait waits for the last worker to exit.
func (r *relay) wait() {
	r.mu.Lock()
	w := r.last
	r.mu.Unlock()
	if w != nil {
		<-w.exited
	}
}

// fromHub relays one of the hub's messages to the worker, starting one if it needs it.
func (r *relay) fromHub(kind websocket.MessageType, data []byte) {
	var m message
	if kind == websocket.MessageText {
		if err := json.Unmarshal(data, &m); err != nil {
			logf("not JSON from the hub: %.80q", data)
			return
		}
		if m.Type == "reply" {
			if m.Error != "" {
				logf("the hub: %s", m.Error)
			}
			return
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.w
	if w == nil {
		switch {
		case m.Type == "warm" || m.Type == "hear" || m.Type == "speak":
			w = r.start(m.Type)
		case kind == websocket.MessageBinary || m.Type == "hear_end" || m.Type == "speak_cancel":
			return // no worker holds that stream: nothing to hear, end or cancel
		default:
			logf("unknown message from the hub: %.80q", data)
			return
		}
	}
	switch m.Type {
	case "hear":
		w.open[m.Stream] = true
	case "hear_end":
		delete(w.open, m.Stream)
	case "speak":
		w.pending[[2]uint32{m.Stream, m.Piece}] = true
	case "speak_cancel":
		for p := range w.pending {
			if p[0] == m.Stream {
				delete(w.pending, p)
			}
		}
	}
	w.sent++
	f := frame{kind: frameText, data: data}
	if kind == websocket.MessageBinary {
		f.kind = frameBinary
	}
	w.in.push(f)
}

// start starts a worker; the caller holds r.mu.
func (r *relay) start(why string) *worker {
	w := &worker{r: r, in: newQueue(), open: map[uint32]bool{}, pending: map[[2]uint32]bool{},
		why: why, started: time.Now(), exited: make(chan struct{})}
	prev := r.last
	r.w, r.last = w, w
	go w.run(prev)
	return w
}

// stop ends a worker whose streams are gone with the hub's link; the caller holds r.mu.
func (w *worker) stop() {
	w.closing = true
	w.in.close()
	if w.cmd == nil {
		return // not started yet, and now it won't be
	}
	w.cmd.Process.Signal(syscall.SIGTERM)
	time.AfterFunc(5*time.Second, func() {
		select {
		case <-w.exited:
		default:
			w.cmd.Process.Kill()
		}
	})
}

// run starts the worker process, once the one before has exited (so their models are never
// loaded twice at once), feeds it the hub's messages and reads its own until it exits.
func (w *worker) run(prev *worker) {
	defer close(w.exited)
	if prev != nil {
		<-prev.exited
	}
	// The running binary, even if a new one has been installed since: the two processes
	// must frame the pipe alike.
	exe, _ := os.Executable()
	path := "/proc/self/exe"
	if _, err := os.Stat(path); err != nil {
		path = exe
	}
	cmd := exec.Command(path, "--config", w.r.c.path, "--idle", strconv.FormatFloat(w.r.c.idle.Seconds(), 'f', -1, 64), "work")
	cmd.Args[0] = exe
	cmd.Stderr = os.Stderr // the worker's log goes to the node's
	w.r.mu.Lock()
	if w.closing {
		w.r.mu.Unlock()
		return
	}
	stdin, err := cmd.StdinPipe()
	var stdout io.ReadCloser
	if err == nil {
		stdout, err = cmd.StdoutPipe()
	}
	if err == nil {
		err = cmd.Start() // which closes the pipes if it fails
	}
	if err == nil {
		w.cmd = cmd
	}
	w.r.mu.Unlock()
	if err != nil {
		w.r.exited(w, err)
		return
	}
	logf("worker %d: started, for `%s`", cmd.Process.Pid, w.why)

	go func() {
		for {
			f, ok := w.in.pop()
			if !ok || writeFrame(stdin, f) != nil {
				break
			}
		}
		stdin.Close() // the worker exits when its stdin ends
	}()
	out := bufio.NewReaderSize(stdout, 64<<10)
	for {
		f, err := readFrame(out)
		if err != nil {
			if err != io.EOF {
				logf("worker %d: %v", cmd.Process.Pid, err)
				cmd.Process.Kill()
			}
			break
		}
		w.fromWorker(f)
	}
	w.r.exited(w, cmd.Wait())
}

// fromWorker relays one of the worker's messages to the hub, or takes a control message.
func (w *worker) fromWorker(f frame) {
	r := w.r
	var m struct {
		Type          string
		Stream, Piece uint32
		Read          int64
	}
	if f.kind != frameBinary {
		json.Unmarshal(f.data, &m)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case f.kind == frameControl && m.Type == "ready":
		logf("worker %d: models loaded, %.2f s after `%s`", w.cmd.Process.Pid, time.Since(w.started).Seconds(), w.why)
	case f.kind == frameControl && m.Type == "idle":
		// It has had no stream and no speech for the idle time. It goes only if it has seen
		// everything sent to it; otherwise what's still on its way keeps it.
		if !w.closing && m.Read == w.sent {
			logf("worker %d: idle for %s, so it exits", w.cmd.Process.Pid, r.c.idle)
			w.closing = true
			if r.w == w {
				r.w = nil
			}
			w.in.close()
		}
	case f.kind == frameControl || w.closing || r.hub == nil:
	case f.kind == frameText:
		if m.Type == "spoken" {
			delete(w.pending, [2]uint32{m.Stream, m.Piece})
		}
		r.hub.send(websocket.MessageText, f.data)
	default:
		r.hub.send(websocket.MessageBinary, f.data)
	}
}

// exited: the worker's process has ended, or never started. If it went unasked with streams
// open, the link to the hub closes and opens again, so that the hub ends their calls rather
// than wait on a worker that's gone.
func (r *relay) exited(w *worker, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w == w {
		r.w = nil
	}
	if w.cmd == nil {
		logf("the worker didn't start: %v", err)
	} else if w.closing {
		logf("worker %d: exited after %s; its memory and the GPU's are free", w.cmd.Process.Pid, time.Since(w.started).Round(time.Second))
		return
	}
	w.closing = true
	w.in.close()
	streams := map[uint32]bool{}
	for s := range w.open {
		streams[s] = true
	}
	for p := range w.pending {
		streams[p[0]] = true
	}
	if w.cmd != nil {
		logf("worker %d: exited unasked (%s), with %d streams open", w.cmd.Process.Pid, w.cmd.ProcessState, len(streams))
	}
	if len(streams) > 0 && r.hub != nil {
		logf("closing the link to the hub, so that it ends their calls")
		r.hub.drop()
	}
}
