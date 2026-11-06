package main

// The call's model in a Claude Code session, on the Claude subscription.
//
// It stays open for the whole call, so it holds the conversation and each turn sends only
// what's new. A call that a session placed starts it as soon as the hub takes the call, so
// The model writes its opening while the phone rings, and closes it when the call ends or is
// forgotten; a voice whose call has none starts and closes its own, from the phone's offer
// on. voice.go is the call's voice that reads it.
//
// the model's tools are a small MCP server (this binary's `mcp`, tools.go) that relays each tool
// call over a socket in the session's private folder. Every call has better_model, and a
// call for a session also pass_to_session, answer_permission and answer_question. The call's
// voice takes the tool calls from your first words on. One made in the opening does nothing:
// you haven't said anything yet. Once, writing its opening while the phone rang, the model passed
// a message nobody had said.
//
// A better model: the session is kept on disk under a known ID, so Opus can take the call
// over by resuming it (`--resume`), with the whole conversation, tool calls included.
// Claude Code keeps it under ~/.claude/projects, in a folder named
// for the working directory, which is the call's own. That folder is deleted when the
// session closes, so calls don't pile up there or show in /resume.

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// claudeCommand is Claude Code; RINGBACK_CLAUDE names another, for checks.
var claudeCommand = envOr("RINGBACK_CLAUDE", "claude")

// selfCommand is this binary, which serves the voice's tools to Claude Code (`mcp`).
var selfCommand = func() string {
	path, err := os.Executable()
	if err != nil {
		return "ringback-agent"
	}
	return path
}()

var claudeArgs = []string{
	"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
	"--include-partial-messages",
	// The voice talks, and passes messages and answers with its own tools (added in run):
	// no built-in tools, no other MCP servers, no slash commands, and no settings files,
	// whose hooks could start another call.
	"--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--setting-sources", "",
}

var sessionTools = []string{"pass_to_session", "answer_permission", "answer_question"}

const betterModel = "better_model"

// event is one line Claude Code writes (stream-json); nil once the session has ended.
type event = map[string]any

// toolHandler answers one of the model's tool calls with the text the model reads.
type toolHandler func(name string, input map[string]any) string

type Talker struct {
	instructions string
	log          func(string)
	withTools    bool   // the session's tools, on a call for a session
	opening      string // the first turn, which asks for the opening
	ID           string // of its Claude Code session, which Opus resumes
	// What it writes, as it writes it; nil once it has ended.
	Events    chan event
	OpeningAt time.Time // when the opening was asked for

	mu         sync.Mutex
	model      chosenModel // Opus's once it has taken the call over
	firstWords time.Time   // when the first words of the last turn's reply came
	used       bool        // a call's voice has read its replies, so a new offer's voice can't take it over
	tool       toolHandler
	proc       *process // claude, while it runs
	ended      bool     // nil was put in Events

	switching sync.Mutex // closing waits for a hand-over under way
	dir       string
	listener  net.Listener
}

// process is one run of claude: the model's, or Opus's once it has taken over.
type process struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	exited   chan struct{} // its output has ended and it has exited
	stopping chan struct{} // what it still writes isn't read
}

func newTalker(model chosenModel, instructions string, log func(string), tools bool, opening string) *Talker {
	return &Talker{
		instructions: instructions, log: log, withTools: tools, opening: opening, ID: uuid4(),
		Events: make(chan event, 1<<16), model: model,
	}
}

// Name is the model on the line, for the log and the transcript, such as "Opus 5.5".
func (h *Talker) Name() string { return h.Model().name() }

// Model is the model on the line.
func (h *Talker) Model() chosenModel {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.model
}

func (h *Talker) FirstWords() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.firstWords
}

func (h *Talker) Used() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.used
}

func (h *Talker) markUsed() {
	h.mu.Lock()
	h.used = true
	h.mu.Unlock()
}

// Start starts the session and asks for the call's opening.
func (h *Talker) Start() error {
	h.switching.Lock()
	defer h.switching.Unlock()
	// An empty working directory, so no project's CLAUDE.md is loaded.
	dir, err := os.MkdirTemp("", "ringback-voice-")
	if err != nil {
		return err
	}
	h.dir = dir
	// the model's tools reach this call over a socket in its private folder.
	listener, err := net.Listen("unix", h.socket())
	if err != nil {
		return err
	}
	h.listener = listener
	go h.serveTools(listener)
	if err := h.run("--session-id"); err != nil {
		return err
	}
	at, err := h.Send(h.opening)
	h.OpeningAt = at
	return err
}

// HandOver puts Opus on the call in the model's place, between turns: closes the model's process and
// resumes its session with Opus, which then has the whole conversation.
func (h *Talker) HandOver() error {
	h.switching.Lock()
	defer h.switching.Unlock()
	if h.dir == "" {
		return nil // closed already
	}
	// The end of the model's process isn't the end of the session.
	h.stop()
	h.mu.Lock()
	h.model = opusModel
	h.mu.Unlock()
	return h.run("--resume")
}

// run starts claude on the session: a new one (--session-id) or resumed (--resume).
func (h *Talker) run(how string) error {
	names := []string{betterModel}
	if h.withTools {
		names = append(append([]string{}, sessionTools...), betterModel)
	}
	server := map[string]any{"command": selfCommand, "args": append([]string{"mcp", h.socket()}, names...)}
	config, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"ringback": server}})
	allowed := make([]string, len(names))
	for i, name := range names {
		allowed[i] = "mcp__ringback__" + name
	}
	args := append(append([]string{}, claudeArgs...), h.Model().flags()...)
	args = append(args, how, h.ID,
		// Resumed, the session reuses the system prompt it recorded; given again, it
		// doesn't depend on that.
		"--system-prompt", h.instructions,
		"--mcp-config", string(config),
		"--allowedTools", strings.Join(allowed, ","),
		"--disallowedTools", "EndConversation",
	)
	cmd := exec.Command(claudeCommand, args...)
	cmd.Dir = h.dir
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	p := &process{cmd: cmd, stdin: stdin, exited: make(chan struct{}), stopping: make(chan struct{})}
	h.mu.Lock()
	h.proc = p
	h.mu.Unlock()
	go h.read(p, stdout)
	return nil
}

func (h *Talker) socket() string { return filepath.Join(h.dir, "tools.sock") }

// Send sends a turn and returns when it was sent.
func (h *Talker) Send(message string) (time.Time, error) {
	h.mu.Lock()
	h.firstWords = time.Time{}
	p := h.proc
	h.mu.Unlock()
	if p == nil {
		return time.Now(), fmt.Errorf("the Claude Code session has ended")
	}
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": message}})
	_, err := p.stdin.Write(append(line, '\n'))
	return time.Now(), err
}

// Interrupt stops the reply being written, as when you go on before it's spoken. Its turn stays
// in the session, your words with it, and the next turn can go at once: tried on Claude Code
// 2.1.319, a stop ended a reply 0.01 s later, and the model still knew the words of two stopped
// turns (`stop` in tools/reply-speed).
func (h *Talker) Interrupt() {
	h.mu.Lock()
	p := h.proc
	h.mu.Unlock()
	if p == nil {
		return
	}
	line, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": "stop-" + uuid4(),
		"request": map[string]any{"subtype": "interrupt"}})
	p.stdin.Write(append(line, '\n'))
}

// TakeTools lets the call's voice answer the model's tool calls from now on.
func (h *Talker) TakeTools(tool toolHandler) {
	h.mu.Lock()
	h.tool = tool
	h.mu.Unlock()
}

func (h *Talker) Close() {
	h.switching.Lock()
	defer h.switching.Unlock()
	if h.listener != nil {
		h.listener.Close()
		h.listener = nil
	}
	h.stop()
	h.mu.Lock()
	ended := h.ended
	h.ended = true
	h.mu.Unlock()
	if !ended {
		select {
		case h.Events <- nil: // nothing more comes
		default:
		}
	}
	if h.dir != "" {
		if more := forget(h.ID, h.dir); len(more) > 0 {
			h.log("deleted more of the session's files than expected: " + strings.Join(more, ", "))
		}
		os.RemoveAll(h.dir)
		h.dir = ""
	}
}

// stop ends claude's process: its input closed, it has 2 s to finish, then it's killed.
func (h *Talker) stop() {
	h.mu.Lock()
	p := h.proc
	h.proc = nil
	h.mu.Unlock()
	if p == nil {
		return
	}
	p.stdin.Close()
	close(p.stopping)
	select {
	case <-p.exited:
	case <-time.After(2 * time.Second):
		p.cmd.Process.Kill()
		<-p.exited
	}
}

func (h *Talker) read(p *process, stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 1<<16)
	for {
		line, err := reader.ReadBytes('\n')
		var e event
		if len(line) > 0 && json.Unmarshal(line, &e) == nil && e != nil {
			h.mu.Lock()
			if h.firstWords.IsZero() && delta(e)["type"] == "text_delta" {
				h.firstWords = time.Now()
			}
			h.mu.Unlock()
			select {
			case h.Events <- e:
			case <-p.stopping:
			}
		}
		if err != nil {
			break
		}
	}
	p.cmd.Wait()
	close(p.exited)
	// It ended by itself, not stopped for Opus or by Close: so did the session.
	h.mu.Lock()
	ends := h.proc == p && !h.ended
	if ends {
		h.ended = true
		h.proc = nil
	}
	h.mu.Unlock()
	if ends {
		h.Events <- nil
	}
}

// serveTools answers each tool call that the MCP server relays: one JSON line in, one out.
func (h *Talker) serveTools(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			text := "The call couldn't read that tool call."
			line, err := bufio.NewReader(conn).ReadBytes('\n')
			var request map[string]any
			decoder := json.NewDecoder(strings.NewReader(string(line)))
			decoder.UseNumber()
			if (err == nil || len(line) > 0) && decoder.Decode(&request) == nil && request != nil {
				name, _ := request["tool"].(string)
				input, _ := request["input"].(map[string]any)
				if input == nil {
					input = map[string]any{}
				}
				h.mu.Lock()
				tool := h.tool
				h.mu.Unlock()
				if tool != nil {
					text = tool(name, input)
				} else {
					h.log(fmt.Sprintf("%s's %s in its opening, not passed: %s", h.Name(), pyrepr(request["tool"]), pyrepr(request["input"])))
					text = nothingYet
				}
			}
			answer, _ := json.Marshal(map[string]string{"text": text})
			conn.Write(append(answer, '\n'))
		}()
	}
}

// stream is the part of an event that streams the reply, if it is one.
func stream(e event) map[string]any {
	if e["type"] != "stream_event" {
		return nil
	}
	s, _ := e["event"].(map[string]any)
	return s
}

func delta(e event) map[string]any {
	d, _ := stream(e)["delta"].(map[string]any)
	return d
}

var notAlphanumeric = regexp.MustCompile(`[^a-zA-Z0-9]`)

// forget deletes what Claude Code kept of session id, run in cwd: the folder it keeps for
// cwd, which is the call's own and holds the session's transcript. Returns what else was
// named for the session, and deleted too; that should be nothing.
func forget(id, cwd string) []string {
	config := os.Getenv("CLAUDE_CONFIG_DIR")
	if config == "" {
		config = filepath.Join(homeDir(), ".claude")
	}
	projects := filepath.Join(config, "projects")
	// The folder is named for the working directory's real path, with every character but
	// letters and digits made "-".
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		real, _ = filepath.Abs(cwd)
	}
	os.RemoveAll(filepath.Join(projects, notAlphanumeric.ReplaceAllString(real, "-")))
	var more []string
	matches, _ := filepath.Glob(filepath.Join(projects, "*", id+"*"))
	for _, path := range matches {
		rel, _ := filepath.Rel(projects, path)
		more = append(more, filepath.ToSlash(rel))
		os.RemoveAll(path)
	}
	return more
}

func uuid4() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
