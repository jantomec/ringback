// Package audiocpp runs audio.cpp's server beside the speech node's worker, for one
// text-to-speech model, and streams the speech it makes. A server of its own, rather than a
// library, because audio.cpp's releases ship the server and not the library, and because its
// CUDA 12 libraries stay apart from the node's CUDA 13 ones in a process of their own.
//
// The server holds one model and makes one take at a time, a whole reply in one pass,
// streamed as it's made. A take is cut off by closing its connection, which the server notices
// within a chunk or two (docs/design.md, "Speaking a reply").
package audiocpp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Voice says which model speaks and how: the request fields every take carries.
type Voice struct {
	Family  string         // audio.cpp's model family, as "qwen3_tts"
	Model   string         // the model's GGUF file
	Rate    int            // the model's sample rate
	Ref     string         // a clip of the voice to speak in; empty for a built-in voice
	RefText string         // what the clip says
	Options map[string]any // the model's request options, as {"language": "en"}
	Stall   time.Duration  // how long a take may go with no audio, at its start or partway, before the server counts as hung
}

// Server is audio.cpp's server, started for one voice.
type Server struct {
	dir   string // audio.cpp's folder: audiocpp_server, model_specs/ and lib/
	voice Voice
	log   io.Writer

	mu   sync.Mutex
	cmd  *exec.Cmd
	exit chan struct{} // closed when the process has exited
	base string        // http://127.0.0.1:PORT
}

// ErrHung is a take that went the voice's Stall with no audio, at its start or partway, or a
// server that went away, before the take or during it: the caller restarts the server.
var ErrHung = errors.New("audio.cpp's server stopped answering")

// Start starts the server and waits until it has loaded the model.
func Start(dir string, voice Voice, log io.Writer) (*Server, error) {
	if voice.Stall == 0 {
		voice.Stall = 5 * time.Second
	}
	// The server runs in its own folder, so every path it's given is absolute.
	for _, p := range []*string{&dir, &voice.Model, &voice.Ref} {
		if *p == "" {
			continue
		}
		abs, err := filepath.Abs(*p)
		if err != nil {
			return nil, err
		}
		*p = abs
	}
	s := &Server{dir: dir, voice: voice, log: log}
	return s, s.start()
}

func (s *Server) start() error {
	port, err := freePort()
	if err != nil {
		return err
	}
	conf, err := json.Marshal(map[string]any{
		"host": "127.0.0.1", "port": port, "backend": "cuda", "device": 0, "threads": 4, "lazy_load": false,
		"models": []map[string]any{{"id": "m", "family": s.voice.Family, "path": s.voice.Model, "task": "tts", "mode": "streaming"}},
	})
	if err != nil {
		return err
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("ringback-audiocpp-%d.json", os.Getpid()))
	if err := os.WriteFile(path, conf, 0o600); err != nil {
		return err
	}
	cmd := exec.Command(filepath.Join(s.dir, "audiocpp_server"), "--config", path)
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+filepath.Join(s.dir, "lib"))
	cmd.Stdout, cmd.Stderr = s.log, s.log
	dieWithParent(cmd)
	if err := onSpawnThread(cmd.Start); err != nil {
		return fmt.Errorf("audio.cpp's server didn't start: %v", err)
	}
	exit := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exit)
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(90 * time.Second)
	for {
		select {
		case <-exit:
			return fmt.Errorf("audio.cpp's server exited while loading %s (its log is above)", filepath.Base(s.voice.Model))
		default:
		}
		if r, err := http.Get(base + "/health"); err == nil {
			r.Body.Close()
			if r.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			return errors.New("audio.cpp's server didn't load its model in 90 s")
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.mu.Lock()
	s.cmd, s.exit, s.base = cmd, exit, base
	s.mu.Unlock()
	return nil
}

// Restart kills the server and starts it again, as after ErrHung.
func (s *Server) Restart() error {
	s.Close()
	return s.start()
}

// Close stops the server.
func (s *Server) Close() {
	s.mu.Lock()
	cmd, exit := s.cmd, s.exit
	s.cmd = nil
	s.mu.Unlock()
	if cmd != nil {
		cmd.Process.Kill()
		<-exit
	}
}

// The kernel kills the server when the thread that started it ends, not the process (the
// parent-death signal is per thread), so every start happens on one thread kept for that.
var (
	spawn     = make(chan func())
	spawnOnce sync.Once
)

func onSpawnThread(start func() error) error {
	spawnOnce.Do(func() {
		go func() {
			runtime.LockOSThread() // and never unlocked: the thread lives as long as the process
			for f := range spawn {
				f()
			}
		}()
	})
	var err error
	done := make(chan struct{})
	spawn <- func() { err = start(); close(done) }
	<-done
	return err
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Speak makes text as one take and calls out with each chunk of audio, float samples at the
// voice's rate, as it comes. Cancelling ctx cuts the take off. It returns ErrHung if audio
// stopped coming, before the first or partway, or the server went away, before the take or
// during it, and ctx's error if it was cut off.
func (s *Server) Speak(ctx context.Context, text string, out func([]float32)) error {
	s.mu.Lock()
	base, exit := s.base, s.exit
	s.mu.Unlock()
	select {
	case <-exit:
		return fmt.Errorf("%w: the server has exited", ErrHung)
	default:
	}
	body := map[string]any{"model": "m", "input": text, "stream": true, "stream_format": "sse",
		"response_format": "pcm", "options": s.voice.Options}
	if s.voice.Ref != "" {
		body["voice_ref"] = s.voice.Ref
		body["reference_text"] = s.voice.RefText
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// No audio for a while, or the server gone: hung. Either ends the request.
	var isHung atomic.Bool
	hang := func() { isHung.Store(true); cancel() }
	alive := make(chan struct{}, 1)
	go func() {
		stall := time.NewTimer(s.voice.Stall)
		defer stall.Stop()
		for {
			select {
			case <-alive:
				stall.Reset(s.voice.Stall)
			case <-stall.C:
				hang()
				return
			case <-exit:
				hang()
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/audio/speech", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		err = fmt.Errorf("%w: %v", ErrHung, err) // the server isn't there
	} else {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			return fmt.Errorf("audio.cpp: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
		}
		err = readEvents(resp.Body, s.voice.Rate, func(samples []float32) {
			select {
			case alive <- struct{}{}:
			default:
			}
			out(samples)
		})
	}
	switch {
	case parent.Err() != nil:
		return parent.Err() // cut off
	case isHung.Load():
		return ErrHung
	}
	return err
}

// readEvents reads the server's server-sent events, `data: {...}` lines, until `[DONE]`:
// `speech.audio.delta` carries base64 PCM (16-bit, little-endian, mono).
func readEvents(r io.Reader, rate int, out func([]float32)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	done := false
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[5:])
		if string(payload) == "[DONE]" {
			return nil
		}
		var event struct {
			Type       string `json:"type"`
			Audio      string `json:"audio"`
			SampleRate int    `json:"sample_rate"`
			Error      any    `json:"error"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("audio.cpp: an event that isn't JSON: %.80s", payload)
		}
		switch event.Type {
		case "speech.audio.delta":
			if event.SampleRate != 0 && event.SampleRate != rate {
				return fmt.Errorf("audio.cpp speaks at %d Hz, not the %d expected", event.SampleRate, rate)
			}
			pcm, err := base64.StdEncoding.DecodeString(event.Audio)
			if err != nil {
				return fmt.Errorf("audio.cpp: audio that isn't base64: %v", err)
			}
			samples := make([]float32, len(pcm)/2)
			for i := range samples {
				samples[i] = float32(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
			}
			if len(samples) > 0 {
				out(samples)
			}
		case "speech.audio.done":
			done = true
		default: // text that comes with the audio, from models that write it, is ignored
			if event.Error != nil || strings.Contains(event.Type, "error") {
				return fmt.Errorf("audio.cpp: %.300s", payload)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrHung, err) // the server went away mid-take, as a reset connection
	}
	if !done {
		return fmt.Errorf("%w: the stream ended before the take did", ErrHung)
	}
	return nil
}
