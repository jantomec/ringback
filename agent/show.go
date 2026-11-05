package main

// What a session shows you (docs/design.md, "Showing you things"): its mod's show tool gives
// the agent a file, which it sends to the hub (POST /show, as it links) with what it is and,
// for what a notification can't show as it is, a poster (posterOf). The hub keeps them and notifies the phone, whose notification shows the image, the
// clip or the poster; the app opens the file.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// showKinds are the files a session can show, by extension, and their content types.
var showKinds = map[string][2]string{
	".png": {"image", "image/png"}, ".jpg": {"image", "image/jpeg"}, ".jpeg": {"image", "image/jpeg"},
	".gif": {"image", "image/gif"}, ".heic": {"image", "image/heic"}, ".webp": {"image", "image/webp"},
	".pdf": {"pdf", "application/pdf"},
	".mp4": {"video", "video/mp4"}, ".mov": {"video", "video/quicktime"}, ".m4v": {"video", "video/x-m4v"},
	// A model stays on this Mac: the hub gets its picture (posterOf).
	".usd": {"model", ""}, ".usda": {"model", ""}, ".usdc": {"model", ""}, ".usdz": {"model", ""},
	".glb": {"model", ""}, ".gltf": {"model", ""}, ".stl": {"model", ""},
}

// renderer renders a model's picture: ringback-render, with OpenUSD's Storm (render/README.md),
// or on a Mac without it, Apple's fallback, ringback-render-apple (render/apple); "" if this
// computer has neither.
var renderer string

// findRenderer sets renderer: the config's `render`, or else the first of them beside the agent
// or on the PATH.
func findRenderer(config map[string]string) {
	if path := config["render"]; path != "" {
		renderer = expand(path)
		return
	}
	names := []string{"ringback-render"}
	if runtime.GOOS == "darwin" {
		names = append(names, "ringback-render-apple")
	}
	self, _ := os.Executable()
	for _, name := range names {
		if beside := filepath.Join(filepath.Dir(self), name); self != "" && fileExists(beside) {
			renderer = beside
			return
		}
		if found, err := exec.LookPath(name); err == nil {
			renderer = found
			return
		}
	}
}

// noRenderer says why this computer can't render a model, and what it needs.
func noRenderer() string {
	text := "this computer can't render 3D models: it has no ringback-render, which renders them with OpenUSD's Storm (render/README.md)"
	if runtime.GOOS == "darwin" {
		text = "this Mac can't render 3D models: it has neither ringback-render, which renders them with OpenUSD's Storm " +
			"(render/README.md), nor Apple's fallback, ringback-render-apple (render/build.sh apple)"
	}
	return text
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// posterOver: a larger image gets a poster, a smaller copy, since a notification shows images
// of at most 10 MB.
var posterOver int64 = 8 << 20

// attachable are the images a notification shows as they are; others get a poster.
var attachable = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true}

const (
	// showMax is the largest file the hub takes.
	showMax = 100 << 20
	// showWait is how long the session waits to hear the file reached the hub; a larger one
	// goes on in the background.
	showWait = 25 * time.Second
)

// showRequest is what a session shows: the file, absolute or in its folder (cwd), and a caption.
type showRequest struct {
	Session, Project, Cwd, Path, Caption string
}

// errShow is a problem with what a session asked to show, which it's told.
type errShow string

func (e errShow) Error() string { return string(e) }

// show sends what a session shows you to the hub. It answers once the hub has it, with its id
// and a line per phone notified, or after showWait, saying it's still on its way.
func show(link *hubLink, request showRequest, log func(string)) (map[string]any, error) {
	path := request.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(request.Cwd, path)
	}
	ext := strings.ToLower(filepath.Ext(path))
	kind, known := showKinds[ext]
	if !known {
		return nil, errShow("ringback shows images (PNG, JPEG, GIF, HEIC, WebP), PDFs, clips (MP4, MOV, M4V) and 3D models " +
			"(USD, USDA, USDC, USDZ, GLB, glTF, STL), not " + or(ext, "files without an extension"))
	}
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return nil, errShow("can't read " + path + ": " + err.Error())
	case info.IsDir():
		return nil, errShow(path + " is a folder")
	case info.Size() > showMax && kind[0] != "model":
		return nil, errShow(fmt.Sprintf("%s is %d MB; ringback shows files up to %d MB", filepath.Base(path), info.Size()>>20, showMax>>20))
	}
	if link == nil {
		return nil, errNotLinked
	}
	meta := map[string]any{
		"session_id": request.Session, "project": request.Project, "kind": kind[0], "name": filepath.Base(path),
		"caption": strings.TrimSpace(request.Caption),
	}
	done := make(chan error, 1)
	var answer map[string]any
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		var err error
		answer, err = link.upload(ctx, meta, path, kind[1], info.Size())
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return nil, err
		}
		log("session " + prefix(request.Session, 8) + ": shows you " + filepath.Base(path))
		return answer, nil
	case <-time.After(showWait):
		go func() {
			if err := <-done; err != nil {
				log("session " + prefix(request.Session, 8) + ": couldn't show you " + filepath.Base(path) + ": " + err.Error())
				return
			}
			log("session " + prefix(request.Session, 8) + ": shows you " + filepath.Base(path))
		}()
		return map[string]any{"ok": true, "sending": true}, nil
	}
}

// upload sends a file the hub shows the phone, with what it is (meta), a poster if it needs
// one, and a thumbnail, and answers what the hub said.
func (h *hubLink) upload(ctx context.Context, meta map[string]any, path, contentType string, size int64) (map[string]any, error) {
	dir, err := os.MkdirTemp("", "ringback-show-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	poster, err := posterOf(ctx, path, meta["kind"].(string), size, dir)
	if err != nil {
		return nil, errShow(err.Error())
	}
	// A small picture of each, for the app's lists: of its poster, or of the image itself.
	thumb := filepath.Join(dir, "thumb.jpg")
	from := or(poster, path)
	if out, err := exec.CommandContext(ctx, "sips", "-Z", "480", "-s", "format", "jpeg", from, "--out", thumb).CombinedOutput(); err != nil {
		return nil, errShow(fmt.Sprintf("can't make a small picture of %s: %s", filepath.Base(path), strings.TrimSpace(string(out))))
	}
	model := ""
	if meta["kind"] == "model" {
		// Its facts, which the renderer wrote beside its picture; the file stays here.
		model = path
		var facts map[string]any
		if data, err := os.ReadFile(poster + ".json"); err == nil && json.Unmarshal(data, &facts) == nil {
			meta["model"] = facts
		}
		path = ""
	}
	body, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		writer.CloseWithError(writeShown(form, meta, path, contentType, poster, thumb))
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.httpURL("/show"), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+h.token)
	client := h.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("can't reach the hub at %s: %v", h.addr, err)
	}
	defer response.Body.Close()
	var answer map[string]any
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&answer)
	if response.StatusCode != http.StatusOK {
		why, _ := answer["error"].(string)
		return nil, fmt.Errorf("the hub answered %d: %s", response.StatusCode, or(why, "no reason"))
	}
	// Where the model is, for its live view (view.go).
	if id, _ := answer["id"].(string); model != "" && id != "" {
		shownModels.remember(id, model)
	}
	return answer, nil
}

// writeShown writes the upload's parts: meta, then the file, the poster and the thumbnail.
func writeShown(form *multipart.Writer, meta map[string]any, path, contentType, poster, thumb string) error {
	data, _ := json.Marshal(meta)
	part, err := form.CreateFormField("meta")
	if err == nil {
		_, err = part.Write(data)
	}
	if err != nil {
		return err
	}
	var files [][3]string
	if path != "" {
		files = append(files, [3]string{"file", path, contentType})
	}
	if poster != "" {
		posterType := "image/png"
		if strings.HasSuffix(poster, ".jpg") {
			posterType = "image/jpeg"
		}
		files = append(files, [3]string{"poster", poster, posterType})
	}
	files = append(files, [3]string{"thumb", thumb, "image/jpeg"})
	for _, f := range files {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, f[0], filepath.Base(f[1])))
		header.Set("Content-Type", f[2])
		part, err := form.CreatePart(header)
		if err != nil {
			return err
		}
		file, err := os.Open(f[1])
		if err != nil {
			return err
		}
		_, err = io.Copy(part, file)
		file.Close()
		if err != nil {
			return err
		}
	}
	return form.Close()
}

// posterOf makes a file's poster in dir: Quick Look's thumbnail of a PDF's first page or a
// clip, or a JPEG of a large image or one a notification can't show (HEIC, WebP); "" if it
// needs none. A notification shows only PNG, JPEG and GIF images, up to 10 MB, and the app
// opens the rest; a clip, which it could show, has a poster too, since the phone's
// notification has 30 s to fetch what it shows.
func posterOf(ctx context.Context, path, kind string, size int64, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch {
	case kind == "pdf" || kind == "video":
		out, err := exec.CommandContext(ctx, "qlmanage", "-t", "-s", "1200", "-o", dir, path).CombinedOutput()
		poster := filepath.Join(dir, filepath.Base(path)+".png")
		if _, statErr := os.Stat(poster); err != nil || statErr != nil {
			return "", fmt.Errorf("can't make a picture of %s: %s", filepath.Base(path), or(strings.TrimSpace(string(out)), fmt.Sprint(err)))
		}
		return poster, nil
	case kind == "model" && renderer == "":
		return "", errors.New(noRenderer())
	case kind == "model":
		// Rendering a large scene takes its loading first, so it has longer.
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		poster := filepath.Join(dir, "poster.png")
		var stderr strings.Builder
		command := exec.CommandContext(ctx, renderer, "still", path, poster)
		command.Stderr = &stderr
		facts, err := command.Output()
		if err != nil {
			return "", fmt.Errorf("can't render %s: %s", filepath.Base(path), or(lastLine(stderr.String()), err.Error()))
		}
		if err := os.WriteFile(poster+".json", facts, 0o600); err != nil {
			return "", err
		}
		return poster, nil
	case kind == "image" && (size > posterOver || !attachable[strings.ToLower(filepath.Ext(path))]):
		poster := filepath.Join(dir, "poster.jpg")
		out, err := exec.CommandContext(ctx, "sips", "-Z", "2048", "-s", "format", "jpeg", path, "--out", poster).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("can't make a smaller copy of %s: %s", filepath.Base(path), strings.TrimSpace(string(out)))
		}
		return poster, nil
	}
	return "", nil
}

// lastLine is the last line of a program's error output, which says why it failed.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// httpURL is the hub's address for a request over HTTP or HTTPS, as the link is over WS or WSS.
func (h *hubLink) httpURL(path string) string {
	return "http" + strings.TrimPrefix(strings.TrimSuffix(h.url, "/agent"), "ws") + path
}
