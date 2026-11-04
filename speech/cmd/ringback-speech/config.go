package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type config struct {
	path     string
	hub      string // the hub's speech link, ws://…/speech or wss://…/speech (linkURL)
	ca       string // a CA to trust for the hub's certificate, besides the system's (hubClient)
	client   *http.Client
	token    string
	name     string
	provider string // "cuda" or "cpu", for Parakeet and Kokoro
	threads  int    // CPU threads per model
	// How long the worker stays with no stream, no speech and no message before it exits,
	// freeing the GPU and the memory; a new one loads the models for the next call.
	idle time.Duration
	// The models, relative to the config file's folder.
	silero, smartTurn, parakeet, kokoro, espeak string
}

// defaultConfig is speech.toml in the install directory, the folder above the binary's.
func defaultConfig() string {
	exe, err := os.Executable()
	if err != nil {
		return "speech.toml"
	}
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "speech.toml")
}

// idleTime reads the idle time, in seconds.
func idleTime(v string) (time.Duration, error) {
	secs, err := strconv.ParseFloat(v, 64)
	if err != nil || secs <= 0 {
		return 0, fmt.Errorf("idle must be a number of seconds, not %q", v)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// loadConfig reads speech.toml: `[section]` headers, `key = "value"` lines and `#` comments,
// as the hub's hub.toml.
func loadConfig(path string) (config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config{}, fmt.Errorf("no config at %s; copy speech/speech.example.toml there", path)
	}
	values := map[string]string{}
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ") + "."
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			if end := strings.Index(value[1:], `"`); end >= 0 {
				value = value[1 : end+1]
			}
		} else if hash := strings.Index(value, "#"); hash >= 0 {
			value = strings.TrimSpace(value[:hash])
		}
		values[section+key] = value
	}
	dir := filepath.Dir(path)
	rel := func(key, fallback string) string {
		v := values[key]
		if v == "" {
			v = fallback
		}
		if v == "" || filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(dir, v)
	}
	c := config{
		path:      path,
		hub:       linkURL(values["hub"]),
		ca:        rel("ca", ""),
		token:     values["token"],
		name:      values["name"],
		provider:  values["provider"],
		silero:    rel("models.silero", "models/silero-vad-v5.onnx"),
		smartTurn: rel("models.smart_turn", "models/smart-turn-v3.2-cpu.onnx"),
		parakeet:  rel("models.parakeet", "models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-fp16"),
		kokoro:    rel("models.kokoro", "models/kokoro-multi-lang-v1_0"),
		espeak:    rel("models.espeak", ""),
	}
	if c.name == "" {
		c.name = "speech node"
	}
	if c.provider == "" {
		c.provider = "cuda"
	}
	c.threads, _ = strconv.Atoi(values["threads"])
	if c.threads <= 0 {
		c.threads = 2
	}
	c.idle = 60 * time.Second
	if v := values["idle"]; v != "" {
		if c.idle, err = idleTime(v); err != nil {
			return config{}, fmt.Errorf("%s: %v", path, err)
		}
	}
	return c, nil
}
