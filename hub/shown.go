package main

// What a session shows you (docs/design.md, "Showing you things"): an image, a PDF, a short
// clip, a still of a 3D model, or text it sends you (Markdown, with its start as plain text). The session's agent uploads it (POST /show, with its link's
// key or token): what it is, then the file, and a poster, an image of it, for a PDF, a clip
// and a model; a model's file stays on its Mac. The hub keeps them for shownKept, and pushes
// each phone a notification from the session, under its project's name as its calls ring,
// whose service extension fetches the file or the poster (/shown/file) to show with it. The
// app lists what's kept (/shown) and opens it.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// shownMax is the largest file a session can show, posterMax the largest poster, and
	// thumbMax the largest thumbnail.
	shownMax  = 100 << 20
	posterMax = 10 << 20
	thumbMax  = 1 << 20
	// textMax is the most text a session can send.
	textMax = 1 << 20
)

// shownKept is how long what's shown is kept. RINGBACK_SHOWN_KEPT sets fewer seconds, for the
// checks.
var shownKept = envSeconds("RINGBACK_SHOWN_KEPT", 7*24*3600)

// shownMu guards the shown files, each a folder of the shown directory: meta.json, the file
// and the poster.
var shownMu sync.Mutex

var shownKinds = map[string]bool{"image": true, "pdf": true, "video": true, "model": true, "text": true}

func shownDir() string { return filepath.Join(stateDir, "shown") }

var errNotShown = errors.New("not shown")

// showFile takes what a session shows you from its agent, as multipart: "meta" first
// ({"session_id", "project", "kind", "name", "caption"}, a model's "model": its frames, and
// text's "preview": its start as plain text, and "url" if it's only a web link), then "file", "poster" and "thumb", each with its
// Content-Type. A model has no file, and an image may have no poster; each has a thumbnail,
// for the app's lists, but text, which has no picture at all. It keeps
// them, notifies every phone, and answers the id and a line per phone.
func showFile(w http.ResponseWriter, r *http.Request) {
	agent, _, allowed := partAllowed("agent", r)
	if !allowed {
		phoneJSON(w, http.StatusUnauthorized, map[string]any{"error": "not allowed"})
		return
	}
	if r.Method != http.MethodPost {
		phoneJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "needs POST"})
		return
	}
	withDeadline(w, 5*time.Minute)
	r.Body = http.MaxBytesReader(w, r.Body, shownMax+posterMax+1<<20)
	reader, err := r.MultipartReader()
	if err != nil {
		phoneJSON(w, http.StatusBadRequest, map[string]any{"error": "needs multipart"})
		return
	}
	id := newUUID()
	dir := filepath.Join(shownDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log("can't keep what's shown: " + err.Error())
		phoneJSON(w, http.StatusInternalServerError, map[string]any{"error": "can't keep it"})
		return
	}
	meta, problem := readShown(reader, dir)
	if problem == "" {
		meta["id"], meta["at"], meta["by"] = id, isoNow(), agent
		var data []byte
		data, err = json.Marshal(meta)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o600)
		}
		if err != nil {
			problem = "can't keep it"
			log("can't keep what's shown: " + err.Error())
		}
	}
	if problem != "" {
		_ = os.RemoveAll(dir)
		phoneJSON(w, http.StatusBadRequest, map[string]any{"error": problem})
		return
	}
	forgetOldShown()
	log("session " + prefix(str(meta, "session_id"), 8) + ": shows you " + str(meta, "kind") + " " + str(meta, "name"))
	lines := notifyShown(r.Context(), meta)
	for _, line := range lines {
		log(line)
	}
	phoneJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "phones": lines})
}

// readShown reads an upload's parts into dir, and what it is; a problem if it isn't whole.
func readShown(reader *multipart.Reader, dir string) (map[string]any, string) {
	var meta map[string]any
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "can't read it: " + err.Error()
		}
		switch part.FormName() {
		case "meta":
			data, _ := io.ReadAll(io.LimitReader(part, 64<<10))
			if json.Unmarshal(data, &meta) != nil || meta == nil {
				return nil, "meta isn't JSON"
			}
			if str(meta, "session_id") == "" || str(meta, "project") == "" || str(meta, "name") == "" || !shownKinds[str(meta, "kind")] {
				return nil, "meta needs session_id, project, name and kind (image, pdf, video, model or text)"
			}
			meta["caption"] = prefix(strings.TrimSpace(str(meta, "caption")), 300)
			if meta["kind"] == "text" {
				meta["preview"] = prefix(strings.TrimSpace(str(meta, "preview")), 400)
			}
			// Text that's only a link has its address, which a tap opens: a web one only.
			if url := str(meta, "url"); meta["kind"] != "text" || len(url) > 2048 ||
				!(strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")) {
				delete(meta, "url")
			}
		case "file", "poster", "thumb":
			if meta == nil {
				return nil, "meta comes first"
			}
			name, limit := "file"+extension(str(meta, "name")), int64(shownMax)
			if meta["kind"] == "text" {
				limit = textMax
			}
			switch part.FormName() {
			case "poster":
				name, limit = "poster", posterMax
			case "thumb":
				name, limit = "thumb", thumbMax
			}
			size, err := saveLimited(filepath.Join(dir, name), part, limit)
			if err != nil {
				return nil, err.Error()
			}
			meta[part.FormName()] = map[string]any{"path": name, "size": size, "type": orDefault(part.Header.Get("Content-Type"), "application/octet-stream")}
		}
	}
	switch {
	case meta == nil:
		return nil, "needs meta"
	case meta["file"] == nil && meta["kind"] != "model":
		return nil, "needs the file"
	case meta["poster"] == nil && meta["kind"] != "image" && meta["kind"] != "text":
		return nil, "needs a poster"
	case meta["thumb"] == nil && meta["kind"] != "text":
		return nil, "needs a thumbnail"
	}
	return meta, ""
}

// extension is a file name's extension, as it's kept: ".png", or none if it's odd.
func extension(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if len(ext) < 2 || len(ext) > 8 || strings.Trim(ext[1:], "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		return ""
	}
	return ext
}

func saveLimited(path string, from io.Reader, limit int64) (int64, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, errors.New("can't keep it")
	}
	size, err := io.Copy(file, io.LimitReader(from, limit+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	switch {
	case err != nil:
		return 0, errors.New("can't read it: " + err.Error())
	case size > limit:
		return 0, errors.New("too big: at most " + strconv.Itoa(int(limit>>20)) + " MB")
	}
	return size, nil
}

// notifyShown sends every phone a notification of what a session shows you: from its project,
// as its calls ring, with the caption, grouped by session. The app's service extension
// attaches its poster, or the image itself if it has none (part, mutable-content); text has
// neither, and shows its caption and its start, and if it's only a link, its address, which
// tapping it opens. APNs keeps it while the file is kept, for a
// phone that's off meanwhile. One line per phone.
func notifyShown(ctx context.Context, meta map[string]any) []string {
	return notifyPhones(ctx, "shown "+prefix(str(meta, "id"), 8), shownPayload(meta), now().Add(shownKept))
}

// shownPayload is the notification of what a session shows you.
func shownPayload(meta map[string]any) map[string]any {
	body := str(meta, "caption")
	if meta["kind"] == "text" {
		body = strings.TrimSpace(body + "\n" + str(meta, "preview"))
	}
	if body == "" {
		body = str(meta, "name")
	}
	payload := map[string]any{
		"aps": map[string]any{
			"alert":              map[string]string{"title": str(meta, "project"), "body": body},
			"sound":              "default",
			"mutable-content":    1,
			"thread-id":          str(meta, "session_id"),
			"interruption-level": "active",
		},
		"shown": str(meta, "id"), "session_id": str(meta, "session_id"), "kind": str(meta, "kind"), "part": "file",
	}
	switch {
	case meta["kind"] == "text":
		payload["part"] = ""
		if url := str(meta, "url"); url != "" {
			payload["url"] = url
		}
	case meta["poster"] != nil:
		payload["part"] = "poster"
	}
	return payload
}

// listShown is what's kept, newest first, as the app lists it. The caller holds shownMu.
func listShown() []map[string]any {
	entries, _ := os.ReadDir(shownDir())
	shown := []map[string]any{}
	for _, entry := range entries {
		var meta map[string]any
		readJSON(filepath.Join(shownDir(), entry.Name(), "meta.json"), &meta)
		if meta != nil && str(meta, "id") == entry.Name() {
			shown = append(shown, meta)
		}
	}
	sort.Slice(shown, func(i, j int) bool { return str(shown[i], "at") > str(shown[j], "at") })
	return shown
}

// forgetOldShown removes what was shown more than shownKept ago, and uploads left unfinished.
func forgetOldShown() {
	shownMu.Lock()
	defer shownMu.Unlock()
	entries, _ := os.ReadDir(shownDir())
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || now().Sub(info.ModTime()) < shownKept {
			continue
		}
		if err := os.RemoveAll(filepath.Join(shownDir(), entry.Name())); err == nil {
			log("shown " + prefix(entry.Name(), 8) + ": forgotten, after " + strconv.Itoa(int(shownKept.Hours()/24)) + " days")
		}
	}
}

// shownList is what's kept, for a paired phone: {"shown": [...]}, newest first, each with its
// id, session_id, project, kind, name, caption, at, and its file's and poster's size and type.
func shownList(body map[string]any) (map[string]any, error) {
	if _, _, err := pairedPhone(body); err != nil {
		return nil, err
	}
	forgetOldShown()
	shownMu.Lock()
	defer shownMu.Unlock()
	shown := listShown()
	for _, meta := range shown {
		for _, part := range []string{"file", "poster", "thumb"} {
			if about, ok := meta[part].(map[string]any); ok {
				delete(about, "path")
			}
		}
		delete(meta, "by")
	}
	return map[string]any{"shown": shown}, nil
}

// shownFile sends a paired phone a shown file, its poster or its thumbnail: {"token", "key",
// "id", "part"}, part "file", "poster" or "thumb". 404 if it isn't kept, or hasn't that part.
func shownFile(w http.ResponseWriter, r *http.Request) {
	body, valid := readBody(r)
	if !valid {
		phoneJSON(w, http.StatusBadRequest, map[string]any{"error": "needs id and part"})
		return
	}
	stateMu.Lock()
	_, _, err := pairedPhone(body)
	stateMu.Unlock()
	if err != nil {
		phoneJSON(w, http.StatusUnauthorized, map[string]any{"error": "not paired"})
		return
	}
	path, kind, err := shownPart(str(body, "id"), str(body, "part"))
	if err != nil {
		phoneJSON(w, http.StatusNotFound, map[string]any{"error": "not kept"})
		return
	}
	file, err := os.Open(path)
	if err != nil {
		phoneJSON(w, http.StatusNotFound, map[string]any{"error": "not kept"})
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		phoneJSON(w, http.StatusNotFound, map[string]any{"error": "not kept"})
		return
	}
	w.Header().Set("Content-Type", kind)
	http.ServeContent(w, r, "", info.ModTime(), file)
}

// shownPart is where a shown file's part is kept, and its type.
func shownPart(id, part string) (string, string, error) {
	if id == "" || strings.ContainsAny(id, "/\\.") || (part != "file" && part != "poster" && part != "thumb") {
		return "", "", errNotShown
	}
	shownMu.Lock()
	defer shownMu.Unlock()
	var meta map[string]any
	readJSON(filepath.Join(shownDir(), id, "meta.json"), &meta)
	about, ok := meta[part].(map[string]any)
	if !ok || str(about, "path") == "" {
		return "", "", errNotShown
	}
	return filepath.Join(shownDir(), id, filepath.Base(str(about, "path"))), str(about, "type"), nil
}
