package main

// A model's live view (docs/design.md, "Showing you things"): the phone asks the hub to view a
// model a session showed, the hub asks its agents (`view`), and the one whose session showed it
// starts its renderer's stream (`serve`, render/README.md) and relays it. Its frames go to the
// hub over the link as binary messages ("V", the view's ID, then the frame), and what it says
// as `view_message`; the phone's camera, frame and play come back as `view_input`, which go to
// the renderer's stdin. One view at a time: a new one ends the last. It ends when the hub says
// (`view_end`), the link closes, or the renderer stops.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// viewer is a view's renderer, streaming.
type viewer struct {
	id    string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	ended atomic.Bool // asked to stop, so its exit is no failure
}

// modelPaths are where the models sessions showed are on this computer, by what the hub calls
// them, kept beside the agent's state for as long as the hub keeps what's shown.
type modelPaths struct {
	mu   sync.Mutex
	file string // "" keeps them in memory only
	byID map[string]modelPath
}

type modelPath struct {
	Path string    `json:"path"`
	At   time.Time `json:"at"`
}

var shownModels = &modelPaths{byID: map[string]modelPath{}}

// load reads them from file, which keeps them from now on.
func (m *modelPaths) load(file string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.file = file
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &m.byID)
	}
	if m.byID == nil {
		m.byID = map[string]modelPath{}
	}
}

func (m *modelPaths) remember(id, path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for old, kept := range m.byID {
		if time.Since(kept.At) > 8*24*time.Hour {
			delete(m.byID, old)
		}
	}
	m.byID[id] = modelPath{path, time.Now()}
	if m.file != "" {
		if data, err := json.Marshal(m.byID); err == nil {
			_ = os.WriteFile(m.file, data, 0o600)
		}
	}
}

func (m *modelPaths) find(id string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept, ok := m.byID[id]
	return kept.Path, ok
}

// view starts a model's live view, for the hub's `view`: {"view_id", "shown_id", "width",
// "height"}. 404 if no session here showed it, 409 if this computer has no renderer, 410 if the
// model is gone.
func (a *agent) view(m map[string]any) (int, string) {
	viewID, _ := m["view_id"].(string)
	shown, _ := m["shown_id"].(string)
	width, height := evenSize(m["width"]), evenSize(m["height"])
	path, ok := shownModels.find(shown)
	switch {
	case viewID == "" || !ok:
		return 404, "not shown from here"
	case renderer == "":
		return 409, noRenderer()
	}
	if _, err := os.Stat(path); err != nil {
		return 410, "the model is no longer at " + path
	}
	cmd := exec.Command(renderer, "serve", path, strconv.Itoa(width), strconv.Itoa(height))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 500, err.Error()
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 500, err.Error()
	}
	var stderr strings.Builder
	cmd.Stderr = &lastLines{to: &stderr}
	if err := cmd.Start(); err != nil {
		return 500, "can't start its renderer: " + err.Error()
	}
	v := &viewer{id: viewID, cmd: cmd, stdin: stdin}
	a.viewMu.Lock()
	last := a.viewing
	a.viewing = v
	a.viewMu.Unlock()
	if last != nil {
		a.stopView(last, "another view started")
	}
	a.log(fmt.Sprintf("view %s: %s, %d × %d", prefix(viewID, 8), path, width, height))
	go a.relay(v, stdout)
	go func() {
		err := cmd.Wait()
		why := "ended"
		if err != nil && !v.ended.Load() {
			why = or(lastLine(stderr.String()), err.Error())
		}
		a.viewMu.Lock()
		if a.viewing == v {
			a.viewing = nil
		}
		a.viewMu.Unlock()
		_ = a.hub.send(map[string]any{"type": "view_ended", "view_id": viewID, "why": why})
		a.log("view " + prefix(viewID, 8) + ": " + why)
	}()
	return 0, ""
}

// relay passes what the renderer writes on to the hub: frames as binary messages, the rest as
// view_message.
func (a *agent) relay(v *viewer, from io.Reader) {
	read := bufio.NewReaderSize(from, 1<<20)
	head := make([]byte, 4)
	for {
		if _, err := io.ReadFull(read, head); err != nil {
			return
		}
		length := binary.BigEndian.Uint32(head)
		if length == 0 || length > 16<<20 {
			return
		}
		packet := make([]byte, length)
		if _, err := io.ReadFull(read, packet); err != nil {
			return
		}
		switch packet[0] {
		case 'V':
			_ = a.hub.sendBinary(append([]byte("V"+v.id), packet[1:]...))
		case 'R', 'S':
			var message map[string]any
			if json.Unmarshal(packet[1:], &message) == nil {
				_ = a.hub.send(map[string]any{"type": "view_message", "view_id": v.id, "message": message})
			}
		}
	}
}

// viewInput passes the phone's camera, frame or play on to the view's renderer.
func (a *agent) viewInput(m map[string]any) {
	viewID, _ := m["view_id"].(string)
	a.viewMu.Lock()
	v := a.viewing
	a.viewMu.Unlock()
	if v == nil || v.id != viewID {
		return
	}
	if data, err := json.Marshal(m["input"]); err == nil {
		_, _ = v.stdin.Write(append(data, '\n'))
	}
}

// endView ends a view, as the hub says, or every one if id is "".
func (a *agent) endView(id, why string) {
	a.viewMu.Lock()
	v := a.viewing
	if v != nil && (id == "" || v.id == id) {
		a.viewing = nil
	} else {
		v = nil
	}
	a.viewMu.Unlock()
	if v != nil {
		a.stopView(v, why)
	}
}

// stopView closes the renderer's stdin, which ends it, and kills it if it hasn't in 2 s.
func (a *agent) stopView(v *viewer, why string) {
	v.ended.Store(true)
	_ = v.stdin.Close()
	a.log("view " + prefix(v.id, 8) + ": stopping, " + why)
	go func() {
		time.Sleep(2 * time.Second)
		if v.cmd.ProcessState == nil {
			_ = v.cmd.Process.Kill()
		}
	}()
}

// evenSize is a view's width or height as the renderer takes it: even, from 64 to 2048.
func evenSize(value any) int {
	n, _ := value.(float64)
	size := min(max(int(n), 64), 2048)
	return size - size%2
}

// lastLines keeps the end of what a program writes: enough for why it failed.
type lastLines struct {
	to *strings.Builder
}

func (l *lastLines) Write(p []byte) (int, error) {
	if l.to.Len() > 4096 {
		tail := l.to.String()[l.to.Len()-1024:]
		l.to.Reset()
		l.to.WriteString(tail)
	}
	return l.to.Write(p)
}
