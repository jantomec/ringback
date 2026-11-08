// fakehub stands in for the hub to check a speech node: it takes the node's link, warms it
// up with a turn and speech sent before its models are loaded, asks for speech and checks
// what comes back, then sends speech in 20 ms frames, in real time, and checks the events and
// words. It measures the time from the end of your speech to `turn` and from `speak` to the
// first audio. Given the node's process ID, it measures the memory of the node and its worker,
// and checks that the worker exits after the idle time, that a new one starts for the next
// call, and what happens when the worker or the link dies.
//
//	fakehub --listen 127.0.0.1:8799 --token T --wav sherpa-test.wav --out DIR [--pid N --idle 5s]
//
// then start the node with --hub ws://127.0.0.1:8799/speech --token T --idle 5. It exits after
// one run, 0 if every check passed.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"ringback/speech/internal/wav"
)

const (
	hearRate  = 16000
	speakRate = 24000
	frame     = hearRate / 50 // 20 ms
)

type event struct {
	msg map[string]any
	at  time.Time
}

type audioChunk struct {
	piece   uint32
	samples []int16
	at      time.Time
}

// hub is the fake hub's side of one link.
type hub struct {
	conn     *websocket.Conn
	mu       sync.Mutex
	events   map[uint32][]event      // JSON messages per stream
	audio    map[uint32][]audioChunk // binary messages per stream
	changed  chan struct{}
	gone     chan struct{} // closed when the link is
	failed   int
	wmu      sync.Mutex
	lastSent time.Time
	links    chan *websocket.Conn // the node's links after this one
	loading  time.Duration        // from `warm` to the first event, while the models loaded
	whole    bool                 // the node speaks whole replies, through audio.cpp's server
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8799", "address to listen on")
	token := flag.String("token", "", "the node's token")
	wavPath := flag.String("wav", "", "a recording of real speech (16 kHz)")
	out := flag.String("out", "fakehub-out", "folder for the audio the node made")
	pid := flag.Int("pid", 0, "the node's process ID, to measure its memory and see its worker")
	idle := flag.Duration("idle", 0, "the node's idle time (--idle), to check its worker exits; 0 skips that")
	flag.Parse()
	os.MkdirAll(*out, 0o755)

	links := make(chan *websocket.Conn, 4)
	http.HandleFunc("/speech", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+*token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		select {
		case links <- conn:
		default:
			conn.CloseNow()
		}
	})
	srv := &http.Server{Addr: *listen}
	go srv.ListenAndServe()
	// A wrong token must be refused.
	time.Sleep(200 * time.Millisecond)
	if _, resp, err := websocket.Dial(context.Background(), "ws://"+*listen+"/speech", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer wrong"}},
	}); err == nil || resp == nil || resp.StatusCode != 401 {
		fmt.Println("FAIL a wrong token wasn't refused with 401")
		os.Exit(1)
	}
	fmt.Println("waiting for the node at ws://" + *listen + "/speech")
	h := newHub(<-links)
	h.links = links
	code := h.run(*wavPath, *out, *pid, *idle)
	h.conn.Close(websocket.StatusNormalClosure, "done")
	os.Exit(code)
}

func newHub(conn *websocket.Conn) *hub {
	return &hub{conn: conn, events: map[uint32][]event{}, audio: map[uint32][]audioChunk{},
		changed: make(chan struct{}, 1), gone: make(chan struct{})}
}

func (h *hub) check(ok bool, format string, args ...any) {
	if ok {
		fmt.Printf("ok    "+format+"\n", args...)
	} else {
		h.failed++
		fmt.Printf("FAIL  "+format+"\n", args...)
	}
}

func (h *hub) send(msg map[string]any) time.Time {
	data, _ := json.Marshal(msg)
	h.wmu.Lock()
	defer h.wmu.Unlock()
	h.conn.Write(context.Background(), websocket.MessageText, data)
	h.lastSent = time.Now()
	return h.lastSent
}

func (h *hub) sendAudio(stream uint32, samples []int16) {
	b := make([]byte, 4+2*len(samples))
	binary.BigEndian.PutUint32(b, stream)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[4+2*i:], uint16(s))
	}
	h.wmu.Lock()
	defer h.wmu.Unlock()
	h.conn.Write(context.Background(), websocket.MessageBinary, b)
	h.lastSent = time.Now()
}

// read takes everything the node sends.
func (h *hub) read() {
	defer close(h.gone)
	for {
		kind, data, err := h.conn.Read(context.Background())
		if err != nil {
			return
		}
		now := time.Now()
		h.mu.Lock()
		if kind == websocket.MessageBinary && len(data) >= 8 {
			stream, piece := binary.BigEndian.Uint32(data), binary.BigEndian.Uint32(data[4:])
			samples := make([]int16, (len(data)-8)/2)
			for i := range samples {
				samples[i] = int16(binary.LittleEndian.Uint16(data[8+2*i:]))
			}
			h.audio[stream] = append(h.audio[stream], audioChunk{piece, samples, now})
		} else {
			var m map[string]any
			json.Unmarshal(data, &m)
			stream := uint32(num(m["stream"]))
			h.events[stream] = append(h.events[stream], event{m, now})
		}
		h.mu.Unlock()
		select {
		case h.changed <- struct{}{}:
		default:
		}
	}
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

// waitFor waits until cond holds for what has come, or the timeout passes.
func (h *hub) waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.After(timeout)
	for {
		h.mu.Lock()
		ok := cond()
		h.mu.Unlock()
		if ok {
			return true
		}
		select {
		case <-h.changed:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			return false
		}
	}
}

func (h *hub) eventsOf(stream uint32, kind string) []event {
	var out []event
	for _, e := range h.events[stream] {
		if e.msg["type"] == kind {
			out = append(out, e)
		}
	}
	return out
}

func (h *hub) run(wavPath, outDir string, pid int, idle time.Duration) int {
	if !h.welcome() {
		return 1
	}
	recording := h.recording(wavPath)
	worker := h.warming(recording, pid)
	clips := h.speaking(outDir)
	h.cancelling()
	h.hearing(clips, recording)
	if pid > 0 {
		h.check(worker == 0 || slices.Equal(children(pid), []int{worker}), "one worker did all of that: %v", children(pid))
		h.memory("models loaded", pid)
		if h.whole {
			h.crashing(pid, syscall.SIGKILL, "dies", 80, outDir)
			h.crashing(pid, syscall.SIGSTOP, "hangs", 81, outDir)
		}
		if idle > 0 {
			h.idling(pid, idle, recording)
		}
		h.dying(pid)
	}
	if h.failed > 0 {
		fmt.Printf("%d checks failed\n", h.failed)
		return 1
	}
	fmt.Println("all checks passed")
	return 0
}

// welcome takes the node's hello and welcomes it.
func (h *hub) welcome() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, data, err := h.conn.Read(ctx)
	cancel()
	if err != nil {
		h.check(false, "no hello: %v", err)
		return false
	}
	var hello map[string]any
	json.Unmarshal(data, &hello)
	h.check(hello["type"] == "hello" && num(hello["version"]) == 1 && hello["hearing"] == true && hello["speaking"] == true,
		"hello: %s", data)
	h.whole = hello["whole_replies"] == true
	h.send(map[string]any{"type": "welcome", "version": 1})
	go h.read()
	return true
}

// recording reads the recording of real speech, at 16 kHz, trimmed.
func (h *hub) recording(wavPath string) []int16 {
	if wavPath == "" {
		return nil
	}
	real, rate, err := wav.Read(wavPath)
	if err != nil {
		h.check(false, "%s: %v", wavPath, err)
		return nil
	}
	return trim(wav.Resample(real, rate, hearRate))
}

var recordingWords = [][]string{{"wish"}, {"phebe", "phoebe"}, {"portrait"}}

// --- Warming up, idling and dying

// warming sends `warm` and at once a turn (its audio all at once) and a piece of speech, all
// before the models are loaded, and checks that the link answers a ping meanwhile and that the
// worker then hears the turn and speaks the piece. It returns the worker's process ID.
func (h *hub) warming(recording []int16, pid int) int {
	fmt.Println("== warming up")
	if pid > 0 {
		h.check(len(children(pid)) == 0, "no worker before `warm`: %v", children(pid))
		h.memory("no worker", pid)
	}
	if recording == nil {
		return 0
	}
	hearStream, speakStream := uint32(40), uint32(41)
	audio := append(append(make([]int16, hearRate), recording...), make([]int16, 3*hearRate)...)
	warm := h.send(map[string]any{"type": "warm"})
	h.send(map[string]any{"type": "hear", "stream": hearStream, "sensitivity": 0.5, "gate": 0.5})
	for i := 0; i+frame <= len(audio); i += frame {
		h.sendAudio(hearStream, audio[i:i+frame])
	}
	text := "The models are loaded, and this is the first thing I say."
	h.send(map[string]any{"type": "speak", "stream": speakStream, "piece": 1, "text": text})
	sent := time.Since(warm)
	// The link reads on while the worker loads: a ping comes back long before the models are
	// loaded, although it follows everything just sent.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := h.conn.Ping(ctx)
	cancel()
	pong := time.Since(warm)
	worker := 0
	if pid > 0 {
		h.waitFor(2*time.Second, func() bool { return len(children(pid)) > 0 })
		kids := children(pid)
		h.check(len(kids) == 1, "a worker started for `warm`: %v", kids)
		if len(kids) == 1 {
			worker = kids[0]
		}
	}

	// The audio came at once, so the turns come within a moment of each other: once there is
	// one, wait until 2 s pass with nothing more.
	ok := h.waitFor(30*time.Second, func() bool {
		return len(h.eventsOf(hearStream, "turn")) > 0 && len(h.eventsOf(speakStream, "spoken")) > 0
	})
	for ok {
		h.mu.Lock()
		n := len(h.events[hearStream])
		h.mu.Unlock()
		if !h.waitFor(2*time.Second, func() bool { return len(h.events[hearStream]) > n }) {
			break
		}
	}
	h.send(map[string]any{"type": "hear_end", "stream": hearStream})
	h.mu.Lock()
	turns := h.eventsOf(hearStream, "turn")
	var spoken event
	if s := h.eventsOf(speakStream, "spoken"); len(s) > 0 {
		spoken = s[0]
	}
	first := time.Time{}
	for _, s := range []uint32{hearStream, speakStream} {
		if e := h.events[s]; len(e) > 0 && (first.IsZero() || e[0].at.Before(first)) {
			first = e[0].at
		}
	}
	var samples []int16
	var firstAudio time.Time
	for _, a := range h.audio[speakStream] {
		if firstAudio.IsZero() {
			firstAudio = a.at
		}
		samples = append(samples, a.samples...)
	}
	h.mu.Unlock()
	h.check(err == nil && pong < first.Sub(warm), "the link answered a ping %.0f ms after `warm`, before the first event (%.0f ms): %v",
		pong.Seconds()*1000, first.Sub(warm).Seconds()*1000, err)
	h.check(sent < first.Sub(warm), "`warm`, `hear`, %.1f s of audio and `speak` sent in %.0f ms, all before the first event",
		float64(len(audio))/hearRate, sent.Seconds()*1000)
	var texts []string
	for _, t := range turns {
		s, _ := t.msg["text"].(string)
		texts = append(texts, s)
	}
	missing := missingWords(strings.Join(texts, " "), recordingWords)
	h.check(ok && len(missing) == 0, "the turn sent while loading: %d turns, %q%s", len(turns), strings.Join(texts, " | "), describeMissing(missing))
	secs := float64(len(samples)) / speakRate
	h.check(ok && len(samples) > 0 && math.Abs(secs-num(spoken.msg["secs"])) < 0.002,
		"the piece asked for while loading: %.2f s of audio, `spoken` says %.3f s", secs, num(spoken.msg["secs"]))
	if ok {
		h.loading = first.Sub(warm)
		fmt.Printf("      warm → first event %.0f ms, → first audio %.0f ms, → last turn %.0f ms\n", first.Sub(warm).Seconds()*1000,
			firstAudio.Sub(warm).Seconds()*1000, turns[len(turns)-1].at.Sub(warm).Seconds()*1000)
	}
	return worker
}

// idling checks that the worker exits after the idle time, and that its memory and the GPU's
// are free then; that `hear` starts a new one, which hears; and that one started by `warm`
// alone exits too, the idle time after it has loaded the models.
func (h *hub) idling(pid int, idle time.Duration, recording []int16) {
	fmt.Println("== idling")
	h.waitExit(pid, idle, 0, "after the last message")
	h.memory("the worker gone", pid)

	if recording != nil {
		h.hearOne(50, hearCase{name: "a recording, the worker started by `hear`", speech: [][]int16{recording},
			sensitivity: 0.5, gate: 0.5, minTurns: 1, maxTurns: 3, words: recordingWords})
		h.waitExit(pid, idle, 0, "after the last message")
	}

	h.send(map[string]any{"type": "warm"})
	h.waitFor(5*time.Second, func() bool { return len(children(pid)) > 0 })
	h.check(len(children(pid)) == 1, "a worker started for `warm` alone: %v", children(pid))
	h.waitExit(pid, idle, h.loading, "after `warm` alone")
}

// waitExit waits for the worker to exit, which it should once the idle time has passed since
// the last message, and the time it took to load the models if it read that message only
// then; and checks that the GPU no longer lists it.
func (h *hub) waitExit(pid int, idle, loading time.Duration, since string) {
	worker := children(pid)
	from := h.lastSent
	gone := h.waitFor(idle+loading+15*time.Second, func() bool { return len(children(pid)) == 0 })
	after := time.Since(from)
	h.check(len(worker) == 1 && gone && after >= idle && after < idle+loading+3*time.Second,
		"worker %v exited %.1f s %s (the idle time is %s)", worker, after.Seconds(), since, idle)
	if len(worker) == 1 && gone {
		exited := time.Now()
		listed := true
		for time.Since(exited) < 10*time.Second {
			if _, listed = gpuApps()[worker[0]]; !listed {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		h.check(!listed, "the GPU no longer lists worker %d, %.1f s after it exited", worker[0], time.Since(exited).Seconds())
	}
}

// dying kills the worker during a call, and checks that the node then closes its link, so
// that the hub ends the call, and links again; and that the worker exits with the link.
func (h *hub) dying(pid int) {
	fmt.Println("== a worker that dies, a link that drops")
	h.send(map[string]any{"type": "hear", "stream": 60, "sensitivity": 0.5, "gate": 0.5})
	h.send(map[string]any{"type": "speak", "stream": 61, "piece": 1, "text": "Hello."})
	h.waitFor(30*time.Second, func() bool { return len(h.eventsOf(61, "spoken")) > 0 })
	worker := children(pid)
	if !h.checkf(len(worker) == 1, "a worker for a call: %v", worker) {
		return
	}
	syscall.Kill(worker[0], syscall.SIGKILL)
	killed := time.Now()
	select {
	case <-h.gone:
		h.check(true, "the worker killed during a call: the node closed its link %.0f ms later", time.Since(killed).Seconds()*1000)
	case <-time.After(5 * time.Second):
		h.check(false, "the worker killed during a call: the node kept its link")
		return
	}
	var again *hub
	select {
	case conn := <-h.links:
		again = newHub(conn)
		again.links = h.links
	case <-time.After(10 * time.Second):
		h.check(false, "the node didn't link again")
		return
	}
	if !again.welcome() {
		h.failed++
		return
	}
	h.check(true, "the node linked again %.1f s after the worker was killed", time.Since(killed).Seconds())
	h.check(len(children(pid)) == 0, "no worker after linking again: %v", children(pid))

	again.send(map[string]any{"type": "warm"})
	again.waitFor(5*time.Second, func() bool { return len(children(pid)) > 0 })
	worker = children(pid)
	again.conn.CloseNow()
	dropped := time.Now()
	gone := again.waitFor(5*time.Second, func() bool { return len(children(pid)) == 0 })
	h.check(len(worker) == 1 && gone, "worker %v, loading for `warm`, exited %.1f s after the link dropped", worker, time.Since(dropped).Seconds())
	h.failed += again.failed
}

// --- Speaking

type spokenCase struct {
	text  string
	heard [][]string // for each, one of these must be among the words heard back
}

var speakCases = []spokenCase{
	{"I fixed the bug in voice_server.py at line 42, it was an off-by-one error.",
		[][]string{{"voice"}, {"server"}, {"42", "forty-two", "forty two"}, {"off"}}},
	{"All 142 tests pass, and the build took 38 seconds.",
		[][]string{{"142", "one hundred forty-two", "one hundred and forty-two", "one hundred forty two"}, {"38", "thirty-eight", "thirty eight"}, {"seconds"}}},
	{"The ringback mod runs Parakeet through sherpa-onnx on the Nebula tunnel.",
		[][]string{{"parakeet"}, {"sherpa"}, {"onnx", "onyx", "onx", "on x", "anks", "onks"}, {"nebula"}}},
	{"The node uses 2.1 GB of VRAM, and 500ms is 0.5 s.",
		[][]string{{"2.1", "two point one"}, {"gigabytes", "gigabyte", "gb"}, {"500", "five hundred"}, {"milliseconds"}, {"0.5", "zero point five", "point five"}}},
	{"Sure.", [][]string{{"sure"}}},
}

// speaking asks for each case as a piece and checks its audio and `spoken`; it returns the
// audio, for hearing it back.
func (h *hub) speaking(outDir string) [][]int16 {
	fmt.Println("== speaking")
	var clips [][]int16
	var firsts []float64
	for i, c := range speakCases {
		stream, piece := uint32(1), uint32(i+1)
		asked := h.send(map[string]any{"type": "speak", "stream": stream, "piece": piece, "text": c.text})
		var spoken event
		ok := h.waitFor(30*time.Second, func() bool {
			for _, e := range h.eventsOf(stream, "spoken") {
				if uint32(num(e.msg["piece"])) == piece {
					spoken = e
					return true
				}
			}
			return false
		})
		if !h.checkf(ok, "piece %d: no `spoken`", piece) {
			continue
		}
		h.mu.Lock()
		var samples []int16
		var first time.Time
		for _, a := range h.audio[stream] {
			if a.piece == piece {
				if first.IsZero() {
					first = a.at
				}
				samples = append(samples, a.samples...)
			}
		}
		h.mu.Unlock()
		secs := float64(len(samples)) / speakRate
		firstMs := first.Sub(asked).Seconds() * 1000
		firsts = append(firsts, firstMs)
		h.check(len(samples) > 0 && math.Abs(secs-num(spoken.msg["secs"])) < 0.002 && !first.After(spoken.at),
			"piece %d: %.2f s of audio, `spoken` says %.3f s, made in %.3f s; first audio %.0f ms after `speak`: %q",
			piece, secs, num(spoken.msg["secs"]), num(spoken.msg["took"]), firstMs, c.text)
		wav.Write(filepath.Join(outDir, fmt.Sprintf("piece-%d.wav", piece)), samples, speakRate)
		clips = append(clips, samples)
	}
	sort.Float64s(firsts)
	if len(firsts) > 0 {
		fmt.Printf("      speak → first audio: median %.0f ms, max %.0f ms\n", firsts[len(firsts)/2], firsts[len(firsts)-1])
	}
	return clips
}

func (h *hub) checkf(ok bool, format string, args ...any) bool {
	if !ok {
		h.check(false, format, args...)
	}
	return ok
}

// cancelling asks for three pieces, cancels once the first audio comes, and checks nothing
// more comes for them, and that the stream speaks again after.
func (h *hub) cancelling() {
	fmt.Println("== cancelling")
	stream := uint32(2)
	long := "This is a long reply that goes on for a while. It has several sentences in it, so that it takes time to make. " +
		"And then it carries on with more of the same, sentence after sentence, until it is cancelled."
	for p := uint32(1); p <= 3; p++ {
		h.send(map[string]any{"type": "speak", "stream": stream, "piece": p, "text": long})
	}
	h.waitFor(10*time.Second, func() bool { return len(h.audio[stream]) > 0 })
	cancelled := h.send(map[string]any{"type": "speak_cancel", "stream": stream})
	time.Sleep(2 * time.Second)
	h.mu.Lock()
	late, later := 0, 0
	for _, a := range h.audio[stream] {
		if a.at.After(cancelled) {
			late++
			if a.at.After(cancelled.Add(200 * time.Millisecond)) {
				later++
			}
		}
	}
	spokenCount := len(h.eventsOf(stream, "spoken"))
	h.mu.Unlock()
	h.check(later == 0 && spokenCount == 0, "after `speak_cancel`: %d audio messages in flight, none 200 ms later (%d), no `spoken` (%d)", late, later, spokenCount)
	h.send(map[string]any{"type": "speak", "stream": stream, "piece": 4, "text": "And now I speak again."})
	ok := h.waitFor(10*time.Second, func() bool { return len(h.eventsOf(stream, "spoken")) == 1 })
	h.check(ok, "the stream speaks again after a cancel")
}

// crashReply is long enough that the voice is still making it when its server is stopped.
var crashReply = []string{
	"I looked through the parser change this morning and it holds up well.",
	"The old version read the whole file before it checked the header, so a broken file took a long time to fail.",
	"Now it checks the header first and stops at once.",
	"I also added a test with a file that ends halfway through a line.",
	"The last thing left is the release note, which I can write after lunch.",
}

// crashing stops the voice's server partway through a long reply, by killing it or by
// freezing it (sig), and checks the reply goes on: more audio comes, then `spoken`, and
// hearing it back has its first words, the notice that the server was restarted, and its
// last words.
func (h *hub) crashing(pid int, sig syscall.Signal, how string, stream uint32, outDir string) {
	fmt.Printf("== a voice server that %s partway through a reply\n", how)
	server := 0
	for _, w := range children(pid) {
		for _, c := range children(w) {
			if comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", c)); strings.TrimSpace(string(comm)) == "audiocpp_server" {
				server = c
			}
		}
	}
	if !h.checkf(server != 0, "no audio.cpp server under the worker") {
		return
	}
	audio := func() int {
		n := 0
		for _, a := range h.audio[stream] {
			n += len(a.samples)
		}
		return n
	}
	h.send(map[string]any{"type": "speak", "stream": stream, "piece": 1, "text": strings.Join(crashReply, " ")})
	if !h.checkf(h.waitFor(20*time.Second, func() bool { return audio() >= 8*speakRate }), "no 8 s of audio to stop the server in") {
		return
	}
	syscall.Kill(server, sig)
	stopped := time.Now()
	h.mu.Lock()
	before := audio()
	h.mu.Unlock()
	var spoken event
	ok := h.waitFor(60*time.Second, func() bool {
		if e := h.eventsOf(stream, "spoken"); len(e) > 0 {
			spoken = e[0]
			return true
		}
		return false
	})
	h.mu.Lock()
	var samples []int16
	var again time.Time
	for _, a := range h.audio[stream] {
		samples = append(samples, a.samples...)
		if again.IsZero() && a.at.After(stopped.Add(200*time.Millisecond)) {
			again = a.at
		}
	}
	h.mu.Unlock()
	if !h.checkf(ok && !again.IsZero(), "the reply didn't go on after the server %s: %.1f s of audio, `spoken` %v", how, float64(len(samples))/speakRate, ok) {
		return
	}
	h.check(math.Abs(float64(len(samples))/speakRate-num(spoken.msg["secs"])) < 0.002,
		"the server %s %.1f s into the audio; it went on %.1f s later, to %.1f s in all; `spoken` says %.3f s",
		how, float64(before)/speakRate, again.Sub(stopped).Seconds(), float64(len(samples))/speakRate, num(spoken.msg["secs"]))
	wav.Write(filepath.Join(outDir, "crash-"+how+".wav"), samples, speakRate)
	h.hearOne(stream+10, hearCase{name: "hearing it back", speech: [][]int16{trim(wav.Resample(samples, speakRate, hearRate))},
		sensitivity: 0.5, gate: 0.5, minTurns: 1, maxTurns: 12,
		words: [][]string{{"parser"}, {"crashed"}, {"restarted"}, {"continuing"}, {"release note"}, {"lunch"}}})
}

// --- Hearing

type hearCase struct {
	name        string
	speech      [][]int16 // stretches of speech, with pause between them
	pause       float64
	sensitivity float64
	gate        float64
	// A turn ends at every pause of about 0.7 s, inside a recording too, as on a call, so some
	// cases take more than one; the words are checked over all of them.
	minTurns, maxTurns int
	words              [][]string
	cutOff             bool // the audio stops in the middle of the speech, as if the phone had gone
}

func (h *hub) hearing(clips [][]int16, recording []int16) {
	fmt.Println("== hearing")
	at16 := func(c []int16) []int16 { return trim(wav.Resample(c, speakRate, hearRate)) }
	var cases []hearCase
	for i, c := range clips {
		cases = append(cases, hearCase{name: fmt.Sprintf("the voice's piece %d", i+1), speech: [][]int16{at16(c)},
			sensitivity: 0.5, gate: 0.5, minTurns: 1, maxTurns: 3, words: speakCases[i].heard})
	}
	if recording != nil {
		quiet := scale(recording, 0.1)
		cases = append(cases,
			hearCase{name: "a recording", speech: [][]int16{recording}, sensitivity: 0.5, gate: 0.5, minTurns: 1, maxTurns: 3,
				words: recordingWords},
			hearCase{name: "the recording 20 dB quieter, filter off", speech: [][]int16{quiet}, sensitivity: 0.5, gate: 0, minTurns: 1, maxTurns: 3,
				words: [][]string{{"portrait"}}},
			hearCase{name: "the recording 20 dB quieter, filter strongest", speech: [][]int16{quiet}, sensitivity: 0.5, gate: 1})
	}
	// Made by the node itself: a turn that stops short, which ends after the turn wait as any
	// does; the same followed by the rest of it after a pause shorter than the wait, which is
	// one turn with the words of both; and after a longer pause, which is two.
	unfinished := h.say("So the next thing I want to do is")
	cases = append(cases,
		hearCase{name: "an unfinished turn", speech: [][]int16{unfinished}, sensitivity: 0.5, gate: 0.5, minTurns: 1, maxTurns: 1,
			words: [][]string{{"next"}}},
		hearCase{name: "a turn with a 0.4 s pause", speech: [][]int16{unfinished, h.say("merge the branch into main.")}, pause: 0.4,
			sensitivity: 0.5, gate: 0.5, minTurns: 1, maxTurns: 1, words: [][]string{{"next"}, {"merge"}, {"main"}}},
		hearCase{name: "two turns, with a 1.2 s pause", speech: [][]int16{unfinished, h.say("merge the branch into main.")}, pause: 1.2,
			sensitivity: 0.5, gate: 0.5, minTurns: 2, maxTurns: 2, words: [][]string{{"next"}, {"merge"}, {"main"}}},
		hearCase{name: "silence", sensitivity: 0.5, gate: 0.5},
		hearCase{name: "audio that stops mid-speech (stopped after 1 s without audio)", speech: [][]int16{unfinished[:len(unfinished)/2]},
			sensitivity: 0.5, gate: 0.5, maxTurns: 1, cutOff: true})

	stream := uint32(10)
	var waited []float64 // how long after you stop each turn ends
	for _, c := range cases {
		stream++
		if turn, _ := h.hearOne(stream, c); turn != 0 {
			waited = append(waited, turn)
		}
	}
	// Two at once.
	if len(cases) >= 2 {
		var wg sync.WaitGroup
		for i, c := range cases[:2] {
			wg.Add(1)
			go func(s uint32, c hearCase) {
				defer wg.Done()
				c.name += ", alongside another stream"
				h.hearOne(s, c)
			}(uint32(30+i), c)
		}
		wg.Wait()
	}
	sort.Float64s(waited)
	if len(waited) > 0 {
		fmt.Printf("      end of speech → turn: median %.0f ms, min %.0f, max %.0f (%d turns)\n",
			waited[len(waited)/2], waited[0], waited[len(waited)-1], len(waited))
	}
}

// say makes speech with the node's own voice, at 16 kHz.
func (h *hub) say(text string) []int16 {
	stream := uint32(3)
	h.mu.Lock()
	piece := uint32(len(h.eventsOf(stream, "spoken")) + 1)
	h.mu.Unlock()
	h.send(map[string]any{"type": "speak", "stream": stream, "piece": piece, "text": text})
	h.waitFor(30*time.Second, func() bool { return len(h.eventsOf(stream, "spoken")) >= int(piece) })
	h.mu.Lock()
	defer h.mu.Unlock()
	var samples []int16
	for _, a := range h.audio[stream] {
		if a.piece == piece {
			samples = append(samples, a.samples...)
		}
	}
	return trim(wav.Resample(samples, speakRate, hearRate))
}

// hearOne sends 1 s of silence, the speech and 3 s of silence in real time, and checks the
// events; it returns the time from the end of the speech to the last turn and to the last
// speech_stopped, in ms (0 if none).
func (h *hub) hearOne(stream uint32, c hearCase) (float64, float64) {
	h.send(map[string]any{"type": "hear", "stream": stream, "sensitivity": c.sensitivity, "gate": c.gate})
	audio := make([]int16, hearRate)
	for i, s := range c.speech {
		if i > 0 {
			audio = append(audio, make([]int16, int(c.pause*hearRate))...)
		}
		audio = append(audio, s...)
	}
	speechEnd := len(audio)
	if !c.cutOff {
		audio = append(audio, make([]int16, 3*hearRate)...)
	}

	start := time.Now()
	var ended time.Time
	for i := 0; i+frame <= len(audio); i += frame {
		time.Sleep(time.Until(start.Add(time.Duration(i) * time.Second / hearRate)))
		h.sendAudio(stream, audio[i:i+frame])
		if i < speechEnd && (i+frame >= speechEnd || i+2*frame > len(audio)) {
			ended = time.Now() // the frame with the speech's end, or the last one sent
		}
	}
	// Leave time for a turn that comes after the turn wait.
	h.waitFor(2*time.Second, func() bool {
		return c.minTurns > 0 && len(h.eventsOf(stream, "turn")) >= c.minTurns && len(h.eventsOf(stream, "turn")) == len(h.eventsOf(stream, "speech_stopped"))
	})
	h.send(map[string]any{"type": "hear_end", "stream": stream})

	h.mu.Lock()
	started, stopped, turns := h.eventsOf(stream, "speech_started"), h.eventsOf(stream, "speech_stopped"), h.eventsOf(stream, "turn")
	h.mu.Unlock()
	var texts []string
	var latency, stopLatency float64
	for _, t := range turns {
		s, _ := t.msg["text"].(string)
		texts = append(texts, s)
	}
	text := strings.Join(texts, " | ")
	if len(turns) > 0 && !ended.IsZero() {
		latency = turns[len(turns)-1].at.Sub(ended).Seconds() * 1000
	}
	if len(stopped) > 0 && !ended.IsZero() {
		stopLatency = stopped[len(stopped)-1].at.Sub(ended).Seconds() * 1000
	}
	missing := missingWords(text, c.words)
	h.check(len(turns) >= c.minTurns && len(turns) <= c.maxTurns && len(missing) == 0 && len(started) == len(stopped) &&
		(c.maxTurns > 0 || len(started) == 0) && (!c.cutOff || (len(stopped) == 1 && stopLatency > 900 && stopLatency < 1400)),
		"%s: %d started, %d stopped, %d turns (want %d to %d); stopped %.0f ms and turn %.0f ms after the speech ended: %q%s",
		c.name, len(started), len(stopped), len(turns), c.minTurns, c.maxTurns, stopLatency, latency, text, describeMissing(missing))
	for _, t := range turns {
		fmt.Printf("      turn: secs %.2f, took %.3f\n", num(t.msg["secs"]), num(t.msg["took"]))
	}
	return latency, stopLatency
}

var nonWord = regexp.MustCompile(`[^a-z0-9.' -]+`)

func missingWords(text string, want [][]string) []string {
	t := " " + nonWord.ReplaceAllString(strings.ToLower(strings.ReplaceAll(text, "-", " ")), " ") + " "
	var missing []string
	for _, options := range want {
		found := false
		for _, o := range options {
			found = found || strings.Contains(t, strings.ReplaceAll(strings.ToLower(o), "-", " "))
		}
		if !found {
			missing = append(missing, strings.Join(options, "/"))
		}
	}
	return missing
}

func describeMissing(m []string) string {
	if len(m) == 0 {
		return ""
	}
	return " — missing " + strings.Join(m, ", ")
}

// trim cuts the quiet before and after speech (below -40 dBFS).
func trim(s []int16) []int16 {
	const level = 330
	a, b := 0, len(s)
	for a < b && abs(s[a]) < level {
		a++
	}
	for b > a && abs(s[b-1]) < level {
		b--
	}
	return s[a:b]
}

func abs(v int16) int16 {
	if v < 0 {
		return -v
	}
	return v
}

func scale(s []int16, f float64) []int16 {
	out := make([]int16, len(s))
	for i, v := range s {
		out[i] = int16(float64(v) * f)
	}
	return out
}

// memory prints the resident memory of the node's link process and of its worker, and the GPU
// memory of each; the GPU must never list the link process.
func (h *hub) memory(what string, pid int) {
	apps := gpuApps()
	describe := func(p int) string {
		data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", p))
		field := map[string]string{}
		for _, line := range strings.Split(string(data), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok {
				field[k] = strings.TrimSpace(v)
			}
		}
		vram := "the GPU doesn't list it"
		if v, ok := apps[p]; ok {
			vram = "GPU " + v + " MiB"
		}
		return fmt.Sprintf("RSS %s (peak %s; %s its own, %s of mapped libraries and models), %s",
			field["VmRSS"], field["VmHWM"], field["RssAnon"], field["RssFile"], vram)
	}
	fmt.Printf("== memory, %s: the link process %d: %s\n", what, pid, describe(pid))
	for _, w := range children(pid) {
		fmt.Printf("      its worker %d: %s\n", w, describe(w))
	}
	if out, err := exec.Command("nvidia-smi", "--query-gpu=memory.used", "--format=csv,noheader").Output(); err == nil {
		fmt.Printf("      the GPU's memory in use, by every process: %s\n", strings.TrimSpace(string(out)))
	}
	_, listed := apps[pid]
	h.check(!listed, "the GPU doesn't list the link process")
}

// gpuApps is the GPU memory, in MiB, of each process the GPU lists; none without nvidia-smi.
func gpuApps() map[int]string {
	apps := map[int]string{}
	out, err := exec.Command("nvidia-smi", "--query-compute-apps=pid,used_memory", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return apps
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, ",")
		if len(f) == 2 {
			if p, err := strconv.Atoi(strings.TrimSpace(f[0])); err == nil {
				apps[p] = strings.TrimSpace(f[1])
			}
		}
	}
	return apps
}

// children are the live child processes of pid: the node's worker.
func children(pid int) []int {
	entries, _ := os.ReadDir("/proc")
	parent := strconv.Itoa(pid)
	var kids []int
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(data)
		f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:]) // the state, then the parent
		if len(f) > 1 && f[1] == parent && f[0] != "Z" {
			kids = append(kids, p)
		}
	}
	sort.Ints(kids)
	return kids
}
