// The ringback speech node: hears and speaks for the hub's calls, on a machine with the
// horsepower (Silero, Smart Turn v3, Parakeet v2 and Kokoro, on CUDA or a CPU). It dials out
// to the hub over a WebSocket (docs/protocol.md, "The speech node"); see "The parts" in
// docs/design.md.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ringback/speech/internal/hearing"
	"ringback/speech/internal/wav"
)

const usage = `usage: ringback-speech [--config FILE] install | uninstall | serve | say TEXT OUT.wav
       | phonemes TEXT | hear FILE.wav | listen [--sensitivity S] [--gate G] FILE.wav...

  install   run the node as a service (a systemd user unit): now, at boot, and again
            whenever it stops; again after building a new binary, to restart it;
            uninstall stops it and takes it away
  serve     dial the hub and hear and speak for its calls: the models load in a worker
            process when a call is coming, which exits when it has been idle
  say       speak TEXT as the voice does, into a WAV file (24 kHz)
  phonemes  print the phonemes the voice would speak for TEXT, chunk by chunk
  hear      print Parakeet's words for a WAV file (16 kHz, one stretch of speech)
  listen    hear WAV files as a call does, each its own call, with the app's interruption
            sensitivity and background filter (0 to 1; 0.5 each by default): print what's
            heard as JSON lines (tools/short-answers)

  --config FILE  the settings (default: speech.toml in the install directory, the folder
                 above the binary's); the hub's address, the token and the idle time in it
                 can be overridden with --hub URL, --token TOKEN and --idle SECONDS`

func main() {
	args := os.Args[1:]
	cfgPath, hub, token, idle := defaultConfig(), "", "", ""
	for len(args) >= 2 && strings.HasPrefix(args[0], "--") {
		switch args[0] {
		case "--config":
			cfgPath = args[1]
		case "--hub":
			hub = args[1]
		case "--token":
			token = args[1]
		case "--idle":
			idle = args[1]
		default:
			fail(usage)
		}
		args = args[2:]
	}
	if len(args) == 0 {
		fail(usage)
	}
	if args[0] == "uninstall" && len(args) == 1 {
		uninstallService()
		return
	}
	c, err := loadConfig(cfgPath)
	if err != nil {
		fail(err.Error())
	}
	if hub != "" {
		c.hub = hub
	}
	if token != "" {
		c.token = token
	}
	if idle != "" {
		if c.idle, err = idleTime(idle); err != nil {
			fail(err.Error())
		}
	}

	switch {
	case args[0] == "install" && len(args) == 1:
		installService(c)
	case args[0] == "serve" && len(args) == 1:
		if c.hub == "" || c.token == "" {
			fail("No hub or token in " + c.path + ".")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		r := newRelay(c)
		serve(ctx, c, r)
		r.wait()
	case args[0] == "work" && len(args) == 1: // started by serve, which it talks to on stdin and stdout
		work(c)
	case args[0] == "say" && len(args) == 3:
		m, err := loadModels(c, false, true)
		if err != nil {
			fail(err.Error())
		}
		var all []float32
		t := time.Now()
		for i, chunk := range m.g2p.Chunks(args[1]) {
			audio, err := m.tts.Speak(chunk)
			if err != nil {
				fail(err.Error())
			}
			if i == 0 {
				fmt.Printf("first chunk in %.0f ms\n", time.Since(t).Seconds()*1000)
			}
			all = append(all, audio...)
		}
		fmt.Printf("%.2f s of audio in %.0f ms\n", float64(len(all))/speakRate, time.Since(t).Seconds()*1000)
		if err := wav.Write(args[2], wav.FromFloat(all), speakRate); err != nil {
			fail(err.Error())
		}
	case args[0] == "phonemes" && len(args) == 2:
		m, err := loadModels(c, false, true)
		if err != nil {
			fail(err.Error())
		}
		for _, chunk := range m.g2p.Chunks(args[1]) {
			fmt.Println(chunk)
		}
	case args[0] == "hear" && len(args) == 2:
		samples, rate, err := wav.Read(args[1])
		if err != nil {
			fail(err.Error())
		}
		samples = wav.Resample(samples, rate, hearing.Rate)
		m, err := loadModels(c, true, false)
		if err != nil {
			fail(err.Error())
		}
		audio := make([]float32, len(samples))
		for i, s := range samples {
			audio[i] = float32(s) / 32768
		}
		t := time.Now()
		text := m.asr.Transcribe(audio)
		fmt.Printf("%.0f ms: %s\n", time.Since(t).Seconds()*1000, text)
	case args[0] == "listen" && len(args) >= 2:
		listen(c, args[1:])
	default:
		fail(usage)
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
