package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Transcript is a call's transcript, in the calling session's project, for the session to
// read. It's written under `.ringback/calls/` as the call goes (see "Call transcripts" in
// docs/design.md). The folder holds its own `.gitignore` of `*`, so git ignores it without a
// change to the project's.
type Transcript struct {
	Path     string
	Relative string // from the project, as the session reads it
}

func newTranscript(project, subject, briefing string) (*Transcript, error) {
	calls := filepath.Join(project, ".ringback", "calls")
	if err := os.MkdirAll(calls, 0o755); err != nil {
		return nil, err
	}
	ignore := filepath.Join(project, ".ringback", ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	name := now.Format("2006-01-02-150405") + ".md"
	t := &Transcript{Path: filepath.Join(calls, name), Relative: ".ringback/calls/" + name}
	if subject == "" {
		subject = "no subject"
	}
	header := fmt.Sprintf("# Call: %s\n\n%s\n\n## The briefing the voice had\n\n%s\n\n## The call\n\n"+
		"The developer's words are as speech recognition heard them.\n\n",
		subject, now.Format("2006-01-02 15:04"), strings.TrimSpace(briefing))
	if err := os.WriteFile(t.Path, []byte(header), 0o644); err != nil {
		return nil, err
	}
	return t, nil
}

// add writes a line of the call, if it says anything.
func (t *Transcript) add(who, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	f, err := os.OpenFile(t.Path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "**%s:** %s\n\n", who, text)
}
