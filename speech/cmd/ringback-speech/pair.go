package main

// Pairing this speech node with the hub: `ringback-speech pair HUB CODE` trades a one-time code,
// from the hub's portal (Setup, speech node) or `ringback-hub pair node`, for the node's own key,
// and writes the hub's address and the key into speech.toml. The node then links with the key
// instead of the hub's shared token ("Security" in docs/design.md). As the agent's (agent/pair.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// pairNode pairs the node whose settings are at path (c, if they could be read). Unnamed, it
// pairs by its host name, so two nodes don't take each other's key.
func pairNode(c config, path, hub, code string) {
	name := c.name
	if name == "" || name == "speech node" {
		if host, err := os.Hostname(); err == nil && host != "" {
			name = host
		}
	}
	key, err := pairWithHub(hub, code, name, c.ca)
	if err != nil {
		fail("Not paired: " + err.Error())
	}
	if err := setConfig(path, [][2]string{{"hub", hub}, {"key", key}}); err != nil {
		fail("Paired, but can't write " + path + ": " + err.Error())
	}
	fmt.Printf("Paired with the hub at %s as %s; its key is in %s.\n", hub, name, path)
	fmt.Println("Now `ringback-speech install` (again, if it runs already) to link with it.")
}

// pairWithHub trades code at the hub's /pair-link for this node's key. hub is the hub's
// address as speech.toml takes it; ca, a CA to trust for its certificate.
func pairWithHub(hub, code, name, ca string) (string, error) {
	base := hub
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q isn't the hub's address", hub)
	}
	u.Scheme = strings.Replace(u.Scheme, "ws", "http", 1) // ws:// and wss:// too
	u.Path = "/pair-link"
	client, err := hubClient(ca)
	if err != nil {
		return "", fmt.Errorf("can't read the hub's CA: %v", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	body, _ := json.Marshal(map[string]any{"code": code, "kind": "speech", "name": name})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("can't reach the hub at %s: %v", u.Host, err)
	}
	defer response.Body.Close()
	var answer map[string]any
	json.NewDecoder(response.Body).Decode(&answer)
	key, _ := answer["key"].(string)
	switch {
	case response.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("the hub refused the code: it's wrong, used, made for something else, or more than 10 minutes old")
	case response.StatusCode != http.StatusOK || key == "":
		return "", fmt.Errorf("the hub answered %s", response.Status)
	}
	return key, nil
}

// settingLine is a `key = value` line, with the comment after its value if it has one.
var settingLine = regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+)\s*=\s*(?:"[^"]*"|[^#]*?)(\s+#.*)?$`)

// setConfig sets settings at a config file's top, before any [section], as `key = "value"`
// lines: each replaces the line that set it, keeping its comment, or comes before the first
// section. A file that isn't there is made. It's left readable only by you, since it holds a key.
func setConfig(path string, settings [][2]string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(data) == 0 {
		lines = nil
	}
	for _, s := range settings {
		line := fmt.Sprintf("%s = %q", s[0], s[1])
		top := len(lines)
		done := false
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "[") {
				top = i
				break
			}
			if m := settingLine.FindStringSubmatch(l); m != nil && m[1] == s[0] {
				lines[i], done = line+m[2], true
				break
			}
		}
		if !done {
			// After the last setting, before the blank lines above the section.
			for top > 0 && strings.TrimSpace(lines[top-1]) == "" {
				top--
			}
			lines = append(lines[:top], append([]string{line}, lines[top:]...)...)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
