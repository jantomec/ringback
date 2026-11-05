package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"
	"time"
)

// upload is what an agent sends to show something: meta, then each part's name, type and bytes.
type upload struct {
	meta  map[string]any
	parts [][3]string
}

func (u upload) post(t *testing.T, secret string) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if u.meta != nil {
		data, _ := json.Marshal(u.meta)
		part, _ := writer.CreateFormField("meta")
		part.Write(data)
	}
	for _, p := range u.parts {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="`+p[0]+`"; filename="x"`)
		header.Set("Content-Type", p[1])
		part, _ := writer.CreatePart(header)
		part.Write([]byte(p[2]))
	}
	writer.Close()
	r := httptest.NewRequest(http.MethodPost, "/show", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	w := httptest.NewRecorder()
	showFile(w, r)
	var answer map[string]any
	json.Unmarshal(w.Body.Bytes(), &answer)
	return w.Code, answer
}

func fetchShown(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	shownFile(w, httptest.NewRequest(http.MethodPost, "/shown/file", strings.NewReader(body)))
	return w
}

func shownMeta(kind, name string) map[string]any {
	return map[string]any{"session_id": "session-shows", "project": "demo", "kind": kind, "name": name, "caption": "  The render, v3  "}
}

// A session's image, PDF and model reach a paired phone: kept, listed newest first, and fetched
// by its key, each part as it came; nothing without the agent's token or the phone's key.
func TestShowAndFetch(t *testing.T) {
	os.Remove(devicesPath)
	os.RemoveAll(shownDir())
	defer os.Remove(devicesPath)
	defer os.Remove(pairingPath)
	defer os.RemoveAll(shownDir())
	key, err := pairWith(t, makeCode(t), "shown-token")
	if err != nil {
		t.Fatal(err)
	}

	thumb := [3]string{"thumb", "image/jpeg", "a thumbnail"}
	image := upload{shownMeta("image", "render.PNG"), [][3]string{{"file", "image/png", "png bytes"}, thumb}}
	if status, _ := image.post(t, ""); status != http.StatusUnauthorized {
		t.Errorf("without the agent's token: %d", status)
	}
	if status, _ := image.post(t, "wrong"); status != http.StatusUnauthorized {
		t.Errorf("with a wrong token: %d", status)
	}
	status, answer := image.post(t, "agent-secret-5678")
	imageID := str(answer, "id")
	if status != http.StatusOK || imageID == "" {
		t.Fatalf("an image: %d %v", status, answer)
	}
	if phones, _ := answer["phones"].([]any); len(phones) != 1 || !strings.Contains(phones[0].(string), "no notification token") {
		t.Errorf("its notification, to a phone without a notification token: %v", answer["phones"])
	}

	for what, bad := range map[string]upload{
		"a PDF without its poster":   {shownMeta("pdf", "plan.pdf"), [][3]string{{"file", "application/pdf", "%PDF"}, thumb}},
		"a clip without its file":    {shownMeta("video", "run.mov"), [][3]string{{"poster", "image/png", "png"}, thumb}},
		"an image without its thumb": {shownMeta("image", "a.png"), [][3]string{{"file", "image/png", "png"}}},
		"no meta":                    {nil, [][3]string{{"file", "image/png", "png"}, thumb}},
		"an unknown kind":            {shownMeta("song", "a.mp3"), [][3]string{{"file", "audio/mpeg", "mp3"}, thumb}},
		"nothing at all":             {nil, nil},
	} {
		if status, answer := bad.post(t, "agent-secret-5678"); status != http.StatusBadRequest {
			t.Errorf("%s: %d %v", what, status, answer)
		}
	}

	advance(time.Second)
	model := upload{shownMeta("model", "bracket.usdc"), [][3]string{{"poster", "image/png", "model poster"}, thumb}}
	model.meta["model"] = map[string]any{"start": 1, "end": 48, "fps": 24}
	status, answer = model.post(t, "agent-secret-5678")
	modelID := str(answer, "id")
	if status != http.StatusOK || modelID == "" {
		t.Fatalf("a model's poster: %d %v", status, answer)
	}

	list := func(key string) (int, string) {
		return runEndpoint(map[string]endpoint{"/shown": shownList}, `{"token": "shown-token", "key": "`+key+`"}`)
	}
	if status, _ := list(strings.Repeat("0", 64)); status != http.StatusUnauthorized {
		t.Errorf("the list without the phone's key: %d", status)
	}
	status, listed := list(key)
	var shown struct{ Shown []map[string]any }
	json.Unmarshal([]byte(listed), &shown)
	if status != http.StatusOK || len(shown.Shown) != 2 || shown.Shown[0]["id"] != modelID || shown.Shown[1]["id"] != imageID {
		t.Fatalf("the list, newest first: %d %s", status, listed)
	}
	if strings.Contains(listed, `"path"`) || strings.Contains(listed, `"by"`) {
		t.Errorf("the list shows where files are kept, or which Mac: %s", listed)
	}
	if first := shown.Shown[1]; first["caption"] != "The render, v3" || first["kind"] != "image" || first["session_id"] != "session-shows" {
		t.Errorf("the image, as listed: %v", first)
	}
	if m, _ := shown.Shown[0]["model"].(map[string]any); m["end"] != float64(48) {
		t.Errorf("the model's frames: %v", shown.Shown[0])
	}

	w := fetchShown(t, `{"token": "shown-token", "key": "`+key+`", "id": "`+imageID+`", "part": "file"}`)
	if w.Code != http.StatusOK || w.Body.String() != "png bytes" || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("the image's file: %d %q %s", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	w = fetchShown(t, `{"token": "shown-token", "key": "`+key+`", "id": "`+modelID+`", "part": "poster"}`)
	if w.Code != http.StatusOK || w.Body.String() != "model poster" {
		t.Errorf("the model's poster: %d %q", w.Code, w.Body.String())
	}
	w = fetchShown(t, `{"token": "shown-token", "key": "`+key+`", "id": "`+imageID+`", "part": "thumb"}`)
	if w.Code != http.StatusOK || w.Body.String() != "a thumbnail" || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("the image's thumbnail: %d %q", w.Code, w.Body.String())
	}
	for what, body := range map[string]string{
		"without the key":     `{"token": "shown-token", "id": "` + imageID + `", "part": "file"}`,
		"an image's poster":   `{"token": "shown-token", "key": "` + key + `", "id": "` + imageID + `", "part": "poster"}`,
		"a model's file":      `{"token": "shown-token", "key": "` + key + `", "id": "` + modelID + `", "part": "file"}`,
		"a path, not an id":   `{"token": "shown-token", "key": "` + key + `", "id": "../parts.json", "part": "file"}`,
		"its meta":            `{"token": "shown-token", "key": "` + key + `", "id": "` + imageID + `", "part": "meta"}`,
		"what was never kept": `{"token": "shown-token", "key": "` + key + `", "id": "nothing", "part": "file"}`,
	} {
		if w := fetchShown(t, body); w.Code == http.StatusOK {
			t.Errorf("%s: %d %q", what, w.Code, w.Body.String())
		}
	}

	// A week later, it's forgotten.
	saved := now()
	defer setClock(saved)
	setClock(time.Now().Add(shownKept + time.Hour))
	if _, listed := list(key); !strings.Contains(listed, `"shown":[]`) {
		t.Errorf("after shownKept: %s", listed)
	}
	if entries, _ := os.ReadDir(shownDir()); len(entries) != 0 {
		t.Errorf("its files are still kept: %v", entries)
	}
}

// runEndpoint runs an endpoint on a JSON body, as the server does.
func runEndpoint(endpoints map[string]endpoint, body string) (int, string) {
	var path string
	for p := range endpoints {
		path = p
	}
	return answer(endpoints, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
}
