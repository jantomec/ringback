package main

// The caller's name: the folder the session started in (its project, as its mod says), and
// the session's title, "ringback · Whole-turn transcription", so two sessions in one project
// ring apart. The title is the session's in Claude Code: the name you gave it with /rename, or
// else the one Claude Code wrote, as its transcript keeps them. Claude Code writes them again
// as the session goes on, so the last of each is near the file's end, where they're read.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// titleMost is the most of a title the caller's name keeps, in characters.
const titleMost = 60

// titleTails are how much of a transcript's end is read for its title, in turn: a title is
// usually within its last 40 KB, even in a transcript of 100 MB.
var titleTails = []int64{256 << 10, 4 << 20}

// titleRecheck is how often a session's title is looked at again, at most.
const titleRecheck = 10 * time.Second

// callerName joins a session's project and its title, unless it has none, or it's the
// project's name again.
func callerName(project, title string) string {
	title = strings.Join(strings.Fields(title), " ")
	if utf8.RuneCountInString(title) > titleMost {
		title = strings.TrimSpace(string([]rune(title)[:titleMost])) + "…"
	}
	if title == "" || strings.EqualFold(title, project) {
		return project
	}
	return project + " · " + title
}

// titles are the sessions' titles as last read, and when.
type titles struct {
	mu    sync.Mutex
	known map[string]*titleRead
}

type titleRead struct {
	path    string
	size    int64
	mod     time.Time
	checked time.Time
	title   string
}

// of is the session's title, "" if its transcript can't be found or has none yet.
func (t *titles) of(session string) string {
	if session == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.known == nil {
		t.known = map[string]*titleRead{}
	}
	r := t.known[session]
	if r != nil && time.Since(r.checked) < titleRecheck {
		return r.title
	}
	if r == nil {
		r = &titleRead{}
		t.known[session] = r
	}
	r.checked = time.Now()
	if r.path == "" {
		r.path = transcriptOf(session)
	}
	info, err := os.Stat(r.path)
	if err != nil {
		r.path = ""
		return r.title
	}
	if info.Size() == r.size && info.ModTime().Equal(r.mod) {
		return r.title
	}
	r.size, r.mod = info.Size(), info.ModTime()
	if title, ok := readTitle(r.path, info.Size()); ok {
		r.title = title
	}
	return r.title
}

// transcriptOf is where Claude Code keeps the session's transcript: in the folder of its
// projects named for where it started, by its ID. "" if there's none.
func transcriptOf(session string) string {
	config := os.Getenv("CLAUDE_CONFIG_DIR")
	if config == "" {
		config = filepath.Join(homeDir(), ".claude")
	}
	if strings.ContainsAny(session, `/\*?[`) {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(config, "projects", "*", session+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// readTitle reads a transcript's end for its title: the last name you gave the session, or
// else the last title Claude Code wrote. False if neither is there.
func readTitle(path string, size int64) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	for _, tail := range titleTails {
		from := max(0, size-tail)
		data := make([]byte, size-from)
		if _, err := f.ReadAt(data, from); err != nil && err != io.EOF {
			return "", false
		}
		custom, ai := "", ""
		for _, line := range bytes.Split(data, []byte("\n")) {
			if !bytes.Contains(line, []byte(`-title"`)) {
				continue
			}
			var entry struct{ Type, CustomTitle, AiTitle string }
			if json.Unmarshal(line, &entry) != nil {
				continue // the first line may be cut
			}
			switch entry.Type {
			case "custom-title":
				custom = entry.CustomTitle
			case "ai-title":
				ai = entry.AiTitle
			}
		}
		if custom != "" {
			return custom, true
		}
		if ai != "" {
			return ai, true
		}
		if from == 0 {
			break
		}
	}
	return "", false
}
