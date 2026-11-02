package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"ringback/speech/internal/engine"
	"ringback/speech/internal/hearing"
	"ringback/speech/internal/speaking"
	"ringback/speech/internal/wav"
)

const (
	voice     = 3     // af_heart in kokoro-multi-lang-v1_0
	speakRate = 24000 // Kokoro's sample rate
	// At most 0.25 s of audio per binary message, 12 KB, well under the 32 KB a WebSocket
	// library reads by default (coder/websocket's limit refused 1 s messages).
	chunkFrames      = speakRate / 4
	smartTurnThreads = 1 // as Pipecat runs it
)

// models are loaded once and shared by every stream.
type models struct {
	silero *engine.Silero
	turn   *engine.SmartTurn
	asr    *engine.Parakeet
	tts    *engine.Kokoro
	g2p    *speaking.G2P
	espeak bool
}

// loadModels loads what hearing and speaking need, and runs each once so the first call
// doesn't wait for that.
func loadModels(c config, hear, speak bool) (*models, error) {
	m := &models{}
	var err error
	t := time.Now()
	if hear {
		if m.silero, err = engine.OpenSilero(c.silero); err != nil {
			return nil, err
		}
		if m.turn, err = engine.OpenSmartTurn(c.smartTurn, smartTurnThreads); err != nil {
			return nil, err
		}
		if m.asr, err = engine.OpenParakeet(c.parakeet, c.provider, c.threads); err != nil {
			return nil, err
		}
		m.asr.Transcribe(make([]float32, hearing.Rate))
		m.asr.Transcribe(make([]float32, 3*hearing.Rate))
		if _, err := m.turn.Run(make([]float32, 80*800)); err != nil {
			return nil, err
		}
		if _, err := m.silero.Run(make([]float32, 576), make([]float32, 256)); err != nil {
			return nil, err
		}
	}
	if speak {
		lex, err := speaking.LoadLexicon(filepath.Join(c.kokoro, "lexicon-us-en.txt"))
		if err != nil {
			return nil, err
		}
		vocab, err := speaking.LoadVocab(filepath.Join(c.kokoro, "tokens.txt"))
		if err != nil {
			return nil, err
		}
		var fallback func(string) string
		if c.espeak != "" {
			e, err := engine.OpenEspeak(c.espeak)
			if err != nil {
				logf("espeak-ng didn't load (%v): words in no lexicon are spelled", err)
			} else {
				m.espeak = true
				fallback = func(w string) string { return speaking.FromEspeak(e.IPA(w)) }
			}
		}
		m.g2p = speaking.New(lex, vocab, fallback)
		if m.tts, err = engine.OpenKokoro(c.kokoro, c.provider, c.threads, voice); err != nil {
			return nil, err
		}
		for _, chunk := range m.g2p.Chunks("Hello, the voice is ready.") {
			if _, err := m.tts.Speak(chunk); err != nil {
				return nil, err
			}
		}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	logf("models loaded in %.1f s (%s); Go's own memory %d MB", time.Since(t).Seconds(), c.provider, ms.Sys>>20)
	return m, nil
}

// node hears and speaks for the hub's streams, in the worker process (work.go).
type node struct {
	m    *models
	sink atomic.Pointer[sink] // the pipe to the link process
	gpu  *gpu

	mu      sync.Mutex
	streams map[uint32]*hearStream
}

func newNode(m *models) *node {
	n := &node{m: m, streams: map[uint32]*hearStream{}}
	n.gpu = newGPU(n)
	go n.gpu.run()
	return n
}

// sink sends JSON and binary messages to the hub, through the link process.
type sink struct {
	text func(msg any)
	bin  func(data []byte)
}

func (n *node) out(msg any) {
	if s := n.sink.Load(); s != nil {
		s.text(msg)
	}
}

func (n *node) bin(data []byte) {
	if s := n.sink.Load(); s != nil {
		s.bin(data)
	}
}

// --- Hearing

type hearStream struct {
	id   uint32
	in   chan any // []int16 audio, or transcribed
	done chan struct{}
	s    *hearing.Stream
	n    *node
	stop sync.Once
}

type transcribed struct {
	id   int
	text string
	took float64
}

func (n *node) hear(id uint32, sensitivity, gate float64) {
	n.endHearing(id)
	params := hearing.Thresholds(sensitivity, gate)
	h := &hearStream{id: id, in: make(chan any, 2048), done: make(chan struct{}), n: n}
	h.s = hearing.NewStream(h, n.m.silero, n.m.turn, params)
	n.mu.Lock()
	n.streams[id] = h
	n.mu.Unlock()
	logf("stream %d: hearing, sensitivity %.2f, background filter %.2f: confidence %v, %v s of speech, volume %v",
		id, sensitivity, gate, params.Confidence, params.StartSecs, params.MinVolume)
	go h.run()
}

func (n *node) endHearing(id uint32) {
	n.mu.Lock()
	h := n.streams[id]
	delete(n.streams, id)
	n.mu.Unlock()
	if h != nil {
		h.end()
		logf("stream %d: hearing ended", id)
	}
}

func (n *node) audio(id uint32, samples []int16) {
	n.mu.Lock()
	h := n.streams[id]
	n.mu.Unlock()
	if h == nil {
		return
	}
	select {
	case h.in <- samples:
	case <-h.done:
	default:
		logf("stream %d: hearing is behind, a frame is dropped", id)
	}
}

func (h *hearStream) end() { h.stop.Do(func() { close(h.done) }) }

func (h *hearStream) run() {
	idle := time.NewTimer(time.Duration(hearing.IdleTimeout * float64(time.Second)))
	defer idle.Stop()
	for {
		select {
		case <-h.done:
			return
		case msg := <-h.in:
			switch m := msg.(type) {
			case []int16:
				h.s.Audio(m)
				idle.Reset(time.Duration(hearing.IdleTimeout * float64(time.Second)))
			case transcribed:
				h.s.Transcribed(m.id, m.text, m.took)
			}
			if err := h.s.Err(); err != nil {
				logf("stream %d: a model failed: %v", h.id, err)
			}
		case <-idle.C:
			h.s.Idle()
			idle.Reset(time.Duration(hearing.IdleTimeout * float64(time.Second)))
		}
	}
}

func (h *hearStream) SpeechStarted() {
	h.n.out(map[string]any{"type": "speech_started", "stream": h.id})
}

func (h *hearStream) SpeechStopped() {
	h.n.out(map[string]any{"type": "speech_stopped", "stream": h.id})
}

func (h *hearStream) Turn(text string, secs, took float64) {
	p, d := h.s.LastTurn()
	logf("stream %d: you (%.1f s, heard in %.3f s; Smart Turn %.2f in %.3f s): %q", h.id, secs, took, p, d.Seconds(), text)
	h.n.out(map[string]any{"type": "turn", "stream": h.id, "text": text, "secs": round3(secs), "took": round3(took)})
}

func (h *hearStream) Transcribe(id int, samples []float32) {
	asked := time.Now()
	h.n.gpu.hearing(func() {
		text := h.n.m.asr.Transcribe(samples)
		select {
		case h.in <- transcribed{id, text, time.Since(asked).Seconds()}:
		case <-h.done:
		}
	})
}

// --- Speaking

func (n *node) speak(stream, number uint32, text string) {
	n.gpu.speak(&piece{stream: stream, number: number, text: text, chunks: n.m.g2p.Chunks(text)})
}

func (n *node) cancel(stream uint32) {
	if dropped := n.gpu.cancel(stream); dropped > 0 {
		logf("stream %d: speech cancelled, %d pieces dropped", stream, dropped)
	}
}

func (n *node) sendAudio(stream, number uint32, samples []float32) {
	pcm := wav.FromFloat(samples)
	for len(pcm) > 0 {
		k := min(len(pcm), chunkFrames)
		b := make([]byte, 8+2*k)
		binary.BigEndian.PutUint32(b, stream)
		binary.BigEndian.PutUint32(b[4:], number)
		for i, s := range pcm[:k] {
			binary.LittleEndian.PutUint16(b[8+2*i:], uint16(s))
		}
		n.bin(b)
		pcm = pcm[k:]
	}
}

// busy says whether a stream is open or speech is still to be made.
func (n *node) busy() bool {
	n.mu.Lock()
	streams := len(n.streams)
	n.mu.Unlock()
	return streams > 0 || n.gpu.busy()
}

// --- The GPU: one worker, so hearing and speaking take turns, as in the Python voice. Your
// words go first; speech goes a chunk (about a sentence) at a time, so your words wait at
// most for one chunk.

type piece struct {
	stream, number uint32
	text           string
	chunks         []string
	epoch          uint64 // the stream's cancellations when it was asked for
	next           int
	samples        int
	took           time.Duration
}

type gpu struct {
	n      *node
	mu     sync.Mutex
	wake   chan struct{}
	asr    []func()
	pieces []*piece
	epochs map[uint32]uint64
}

func newGPU(n *node) *gpu {
	return &gpu{n: n, wake: make(chan struct{}, 1), epochs: map[uint32]uint64{}}
}

func (g *gpu) signal() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

func (g *gpu) hearing(job func()) {
	g.mu.Lock()
	g.asr = append(g.asr, job)
	g.mu.Unlock()
	g.signal()
}

func (g *gpu) speak(p *piece) {
	g.mu.Lock()
	p.epoch = g.epochs[p.stream]
	g.pieces = append(g.pieces, p)
	g.mu.Unlock()
	g.signal()
}

func (g *gpu) cancel(stream uint32) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.epochs[stream]++
	kept := g.pieces[:0]
	for _, p := range g.pieces {
		if p.stream != stream {
			kept = append(kept, p)
		}
	}
	dropped := len(g.pieces) - len(kept)
	g.pieces = kept
	return dropped
}

func (g *gpu) busy() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pieces) > 0 || len(g.asr) > 0
}

func (g *gpu) run() {
	runtime.LockOSThread() // the models' CUDA work stays on one thread
	for {
		g.mu.Lock()
		var job func()
		switch {
		case len(g.asr) > 0:
			job = g.asr[0]
			g.asr = g.asr[1:]
		case len(g.pieces) > 0:
			p := g.pieces[0]
			job = func() { g.speakChunk(p) }
		}
		g.mu.Unlock()
		if job == nil {
			<-g.wake
			continue
		}
		job()
	}
}

// speakChunk makes the next chunk of the first piece and sends it, unless the piece was
// cancelled meanwhile; after the last chunk it sends `spoken`.
func (g *gpu) speakChunk(p *piece) {
	var audio []float32
	if p.next < len(p.chunks) {
		t := time.Now()
		var err error
		audio, err = g.n.m.tts.Speak(p.chunks[p.next])
		p.took += time.Since(t)
		if err != nil {
			logf("stream %d piece %d: %v", p.stream, p.number, err)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.epochs[p.stream] != p.epoch {
		return // cancelled; it's no longer in the queue
	}
	if len(audio) > 0 {
		g.n.sendAudio(p.stream, p.number, audio)
		p.samples += len(audio)
	}
	p.next++
	if p.next < len(p.chunks) {
		return
	}
	g.pieces = g.pieces[1:]
	secs := float64(p.samples) / speakRate
	logf("stream %d: voice piece %d (%.1f s, made in %.2f s): %q", p.stream, p.number, secs, p.took.Seconds(), p.text)
	g.n.out(map[string]any{"type": "spoken", "stream": p.stream, "piece": p.number, "secs": round3(secs), "took": round3(p.took.Seconds())})
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func logf(format string, args ...any) {
	fmt.Printf("%s  %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}
