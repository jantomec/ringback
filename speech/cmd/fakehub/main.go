// fakehub stands in for the hub to check a speech node: it takes the node's link, asks for
// speech and checks what comes back, then sends speech in 20 ms frames, in real time, and
// checks the events and words. It measures the time from the end of your speech to `turn`
// and from `speak` to the first audio, and, given the node's process ID, its memory.
//
//	fakehub --listen 127.0.0.1:8799 --token T --wav sherpa-test.wav --out DIR [--pid N]
//
// then start the node with --hub ws://127.0.0.1:8799/speech --token T. It exits after one
// run, 0 if every check passed.
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
	"sort"
	"strings"
	"sync"
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
	conn    *websocket.Conn
	mu      sync.Mutex
	events  map[uint32][]event      // JSON messages per stream
	audio   map[uint32][]audioChunk // binary messages per stream
	changed chan struct{}
	failed  int
	wmu     sync.Mutex
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8799", "address to listen on")
	token := flag.String("token", "", "the node's token")
	wavPath := flag.String("wav", "", "a recording of real speech (16 kHz)")
	out := flag.String("out", "fakehub-out", "folder for the audio the node made")
	pid := flag.Int("pid", 0, "the node's process ID, to measure its memory")
	flag.Parse()
	os.MkdirAll(*out, 0o755)

	done := make(chan int, 1)
	var once sync.Once
	http.HandleFunc("/speech", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+*token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		once.Do(func() {
			h := &hub{conn: conn, events: map[uint32][]event{}, audio: map[uint32][]audioChunk{}, changed: make(chan struct{}, 1)}
			done <- h.run(*wavPath, *out, *pid)
		})
		conn.Close(websocket.StatusNormalClosure, "done")
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
	os.Exit(<-done)
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
	return time.Now()
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
}

// read takes everything the node sends.
func (h *hub) read() {
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

func (h *hub) run(wavPath, outDir string, pid int) int {
	_, data, err := h.conn.Read(context.Background())
	if err != nil {
		fmt.Println("FAIL no hello:", err)
		return 1
	}
	var hello map[string]any
	json.Unmarshal(data, &hello)
	h.check(hello["type"] == "hello" && num(hello["version"]) == 1 && hello["hearing"] == true && hello["speaking"] == true,
		"hello: %s", data)
	h.send(map[string]any{"type": "welcome", "version": 1})
	go h.read()

	clips := h.speaking(outDir)
	h.cancelling()
	h.hearing(clips, wavPath)
	if pid > 0 {
		memory(pid)
	}
	if h.failed > 0 {
		fmt.Printf("%d checks failed\n", h.failed)
		return 1
	}
	fmt.Println("all checks passed")
	return 0
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
		[][]string{{"2.1", "two point one"}, {"gigabytes", "gb"}, {"500", "five hundred"}, {"milliseconds"}, {"0.5", "zero point five", "point five"}}},
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

// --- Hearing

type hearCase struct {
	name        string
	speech      [][]int16 // stretches of speech, with pause between them
	pause       float64
	sensitivity float64
	gate        float64
	// Smart Turn may end a turn at a pause inside a recording, as it would on a call, so
	// some cases take more than one; the words are checked over all of them.
	minTurns, maxTurns int
	words              [][]string
	cutOff             bool // the audio stops in the middle of the speech, as if the phone had gone
}

func (h *hub) hearing(clips [][]int16, wavPath string) {
	fmt.Println("== hearing")
	at16 := func(c []int16) []int16 { return trim(wav.Resample(c, speakRate, hearRate)) }
	var cases []hearCase
	for i, c := range clips {
		cases = append(cases, hearCase{name: fmt.Sprintf("the voice's piece %d", i+1), speech: [][]int16{at16(c)},
			sensitivity: 0.5, gate: 0.5, minTurns: 1, maxTurns: 3, words: speakCases[i].heard})
	}
	if wavPath != "" {
		if real, rate, err := wav.Read(wavPath); err == nil {
			cases = append(cases, hearCase{name: "a recording", speech: [][]int16{trim(wav.Resample(real, rate, hearRate))},
				sensitivity: 0.5, gate: 0.5, minTurns: 1, maxTurns: 3, words: [][]string{{"wish"}, {"phebe", "phoebe"}, {"portrait"}}})
			quiet := scale(trim(wav.Resample(real, rate, hearRate)), 0.1)
			cases = append(cases,
				hearCase{name: "the recording 20 dB quieter, filter off", speech: [][]int16{quiet}, sensitivity: 0.5, gate: 0, minTurns: 1, maxTurns: 3,
					words: [][]string{{"portrait"}}},
				hearCase{name: "the recording 20 dB quieter, filter strongest", speech: [][]int16{quiet}, sensitivity: 0.5, gate: 1})
		} else {
			h.check(false, "%s: %v", wavPath, err)
		}
	}
	// Made by the node itself: a turn Smart Turn finds unfinished, which ends after the turn
	// wait, and the same followed by the rest of it after a pause shorter than the wait,
	// which is one turn with the words of both.
	unfinished := h.say("So the next thing I want to do is")
	cases = append(cases,
		hearCase{name: "an unfinished turn", speech: [][]int16{unfinished}, sensitivity: 0.5, gate: 0.5, minTurns: 1, maxTurns: 1,
			words: [][]string{{"next"}}},
		hearCase{name: "a turn with a 0.8 s pause", speech: [][]int16{unfinished, h.say("merge the branch into main.")}, pause: 0.8,
			sensitivity: 0.5, gate: 0.5, minTurns: 1, maxTurns: 1, words: [][]string{{"next"}, {"merge"}, {"main"}}},
		hearCase{name: "silence", sensitivity: 0.5, gate: 0.5},
		hearCase{name: "audio that stops mid-speech (stopped after 1 s without audio)", speech: [][]int16{unfinished[:len(unfinished)/2]},
			sensitivity: 0.5, gate: 0.5, maxTurns: 1, cutOff: true})

	stream := uint32(10)
	var smart, waited []float64 // turns Smart Turn ended, and turns the turn wait ended
	for _, c := range cases {
		stream++
		turn, stopped := h.hearOne(stream, c)
		switch {
		case turn == 0:
		case turn-stopped < 1000:
			smart = append(smart, turn)
		default:
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
	for _, l := range []struct {
		what string
		ms   []float64
	}{{"Smart Turn", smart}, {"the turn wait", waited}} {
		sort.Float64s(l.ms)
		if len(l.ms) > 0 {
			fmt.Printf("      end of speech → turn, ended by %s: median %.0f ms, min %.0f, max %.0f (%d turns)\n",
				l.what, l.ms[len(l.ms)/2], l.ms[0], l.ms[len(l.ms)-1], len(l.ms))
		}
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

// memory prints the node's resident memory and its GPU memory.
func memory(pid int) {
	data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	field := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			field[k] = strings.TrimSpace(v)
		}
	}
	vram := "?"
	if out, err := exec.Command("nvidia-smi", "--query-compute-apps=pid,used_memory", "--format=csv,noheader,nounits").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Split(line, ",")
			if len(f) == 2 && strings.TrimSpace(f[0]) == fmt.Sprint(pid) {
				vram = strings.TrimSpace(f[1]) + " MiB"
			}
		}
	}
	fmt.Printf("== the node's memory: RSS %s (peak %s; %s its own, %s of mapped libraries and models), VRAM %s\n",
		field["VmRSS"], field["VmHWM"], field["RssAnon"], field["RssFile"], vram)
}
