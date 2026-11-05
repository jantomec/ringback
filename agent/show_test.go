package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A PDF of one blank page, as small as Quick Look reads.
const blankPDF = "%PDF-1.1\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
	"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>endobj\ntrailer<</Root 1 0 R>>\n"

var pngSignature = "\x89PNG\r\n\x1a\n"

// What the fake hub took to show, part by part: each part's bytes, and "<part> type".
type shownUploads struct {
	mu     sync.Mutex
	got    []map[string]string
	refuse string
}

func (s *shownUploads) serve(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, `{"error": "needs multipart"}`, http.StatusBadRequest)
		return
	}
	upload := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}
		data, _ := io.ReadAll(part)
		upload[part.FormName()] = string(data)
		upload[part.FormName()+" type"] = part.Header.Get("Content-Type")
	}
	s.mu.Lock()
	s.got = append(s.got, upload)
	refuse := s.refuse
	s.mu.Unlock()
	if refuse != "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": refuse})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": "shown-1", "phones": []string{"shown shown-1 → test phone: APNs 200"}})
}

func (s *shownUploads) last(t *testing.T) (map[string]string, map[string]any) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.got) == 0 {
		t.Fatal("the hub got nothing")
	}
	upload := s.got[len(s.got)-1]
	var meta map[string]any
	json.Unmarshal([]byte(upload["meta"]), &meta)
	return upload, meta
}

func writePNG(t *testing.T, path string, size int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7919 % 251) // noise, so it doesn't compress away
	}
	img.Set(0, 0, color.White)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}

// A session shows you a PDF, an image and a large image: each reaches the hub with what it is,
// the PDF with Quick Look's picture of it and the large image with a smaller copy; what can't
// be shown, and what the hub refuses, are said to the session.
func TestShow(t *testing.T) {
	hub := newFakeHub(t)
	uploads := &shownUploads{}
	hub.show = uploads.serve
	a := startAgent(t, hub, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "plan.pdf"), []byte(blankPDF), 0o644)
	writePNG(t, filepath.Join(dir, "small.png"), 32)
	writePNG(t, filepath.Join(dir, "large.png"), 600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644)

	status, answer := a.mod("POST", "/sessions/s-show/show", map[string]any{
		"path": "plan.pdf", "cwd": dir, "project": "demo", "caption": "  The plan, page one  "})
	if status != 200 || answer["id"] != "shown-1" {
		t.Fatalf("a PDF: %d %v", status, answer)
	}
	upload, meta := uploads.last(t)
	if meta["kind"] != "pdf" || meta["name"] != "plan.pdf" || meta["session_id"] != "s-show" || meta["project"] != "demo" ||
		meta["caption"] != "The plan, page one" {
		t.Errorf("the PDF's meta: %v", meta)
	}
	if upload["file"] != blankPDF || upload["file type"] != "application/pdf" {
		t.Errorf("the PDF itself: %q %s", prefix(upload["file"], 20), upload["file type"])
	}
	if !strings.HasPrefix(upload["poster"], pngSignature) || upload["poster type"] != "image/png" {
		t.Errorf("the PDF's poster isn't a PNG: %q %s", prefix(upload["poster"], 8), upload["poster type"])
	}
	if !strings.HasPrefix(upload["thumb"], "\xff\xd8\xff") || upload["thumb type"] != "image/jpeg" {
		t.Errorf("the PDF's thumbnail isn't a JPEG: %q %s", prefix(upload["thumb"], 4), upload["thumb type"])
	}
	a.waitLog("session s-show: shows you plan.pdf", time.Second)

	status, answer = a.mod("POST", "/sessions/s-show/show", map[string]any{"path": filepath.Join(dir, "small.png"), "project": "demo"})
	if upload, meta := uploads.last(t); status != 200 || meta["kind"] != "image" || upload["file type"] != "image/png" || upload["poster"] != "" {
		t.Errorf("a small image goes as it is, with no poster: %d %v %v", status, answer, meta)
	}

	saved := posterOver
	posterOver = 1000
	defer func() { posterOver = saved }()
	status, _ = a.mod("POST", "/sessions/s-show/show", map[string]any{"path": filepath.Join(dir, "large.png"), "project": "demo"})
	upload, _ = uploads.last(t)
	if status != 200 || !strings.HasPrefix(upload["poster"], "\xff\xd8\xff") || upload["poster type"] != "image/jpeg" {
		t.Errorf("a large image's poster isn't a JPEG: %d %q %s", status, prefix(upload["poster"], 4), upload["poster type"])
	}

	before := len(uploads.got)
	for what, c := range map[string]struct {
		body map[string]any
		want string
	}{
		"a text file":    {map[string]any{"path": filepath.Join(dir, "notes.txt"), "project": "demo"}, "not .txt"},
		"a missing file": {map[string]any{"path": "gone.png", "cwd": dir, "project": "demo"}, "can't read"},
		"a folder":       {map[string]any{"path": dir + "/", "project": "demo"}, "not files without an extension"},
		"no path":        {map[string]any{"project": "demo"}, "needs path and project"},
	} {
		if status, answer := a.mod("POST", "/sessions/s-show/show", c.body); status != 400 || !strings.Contains(answer["error"].(string), c.want) {
			t.Errorf("%s: %d %v", what, status, answer)
		}
	}
	if len(uploads.got) != before {
		t.Errorf("what can't be shown reached the hub")
	}

	uploads.mu.Lock()
	uploads.refuse = "too big: at most 100 MB"
	uploads.mu.Unlock()
	status, answer = a.mod("POST", "/sessions/s-show/show", map[string]any{"path": filepath.Join(dir, "small.png"), "project": "demo"})
	if status != 502 || answer["error"] != "the hub answered 400: too big: at most 100 MB" {
		t.Errorf("the hub refuses it: %d %v", status, answer)
	}
	if !bytes.Contains([]byte(strings.Join(a.log(), "\n")), []byte("shows you small.png")) {
		t.Errorf("the log doesn't say what was shown: %v", a.log())
	}
}

// A model stays on the Mac: the hub gets the renderer's picture of it and its facts, not the
// file; a model the renderer can't open is said to the session.
func TestShowModel(t *testing.T) {
	hub := newFakeHub(t)
	uploads := &shownUploads{}
	hub.show = uploads.serve
	a := startAgent(t, hub, "")
	dir := t.TempDir()
	// A stand-in renderer: a PNG, and the facts, as ringback-render's still writes them.
	fake := filepath.Join(dir, "ringback-render")
	writePNG(t, filepath.Join(dir, "rendered.png"), 64)
	os.WriteFile(fake, []byte(`#!/bin/sh
case "$2" in *broken*) echo "can't open $2: not a USD file" >&2; exit 1;; esac
cp "`+filepath.Join(dir, "rendered.png")+`" "$3"
echo '{"animated": true, "frames": 48, "fps": 24, "start": 1, "end": 48, "up": "Z"}'
`), 0o755)
	saved := renderer
	renderer = fake
	defer func() { renderer = saved }()
	os.WriteFile(filepath.Join(dir, "bracket.usdc"), []byte("PXR-USDC"), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.glb"), []byte("nope"), 0o644)

	status, answer := a.mod("POST", "/sessions/s-model/show", map[string]any{"path": "bracket.usdc", "cwd": dir, "project": "demo"})
	if status != 200 {
		t.Fatalf("a model: %d %v", status, answer)
	}
	upload, meta := uploads.last(t)
	if meta["kind"] != "model" || meta["name"] != "bracket.usdc" {
		t.Errorf("the model's meta: %v", meta)
	}
	if facts, _ := meta["model"].(map[string]any); facts["frames"] != float64(48) || facts["up"] != "Z" || facts["animated"] != true {
		t.Errorf("the model's facts: %v", meta["model"])
	}
	if _, sent := upload["file"]; sent {
		t.Errorf("the model itself went to the hub")
	}
	if !strings.HasPrefix(upload["poster"], pngSignature) || upload["poster type"] != "image/png" {
		t.Errorf("the model's picture: %q %s", prefix(upload["poster"], 8), upload["poster type"])
	}

	status, answer = a.mod("POST", "/sessions/s-model/show", map[string]any{"path": "broken.glb", "cwd": dir, "project": "demo"})
	if status != 400 || answer["error"] != "can't render broken.glb: can't open "+filepath.Join(dir, "broken.glb")+": not a USD file" {
		t.Errorf("a model it can't render: %d %v", status, answer)
	}

	// With no renderer, the session hears what the computer lacks.
	renderer = ""
	status, answer = a.mod("POST", "/sessions/s-model/show", map[string]any{"path": "bracket.usdc", "cwd": dir, "project": "demo"})
	if why, _ := answer["error"].(string); status != 400 || !strings.Contains(why, "can't render 3D models: it has") ||
		!strings.Contains(why, "render/README.md") {
		t.Errorf("no renderer: %d %v", status, answer)
	}
}
