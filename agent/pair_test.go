package main

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pairing writes the hub's address and the key at the config's top, keeping the rest, and
// makes a config that isn't there; readable only by you.
func TestSetConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	os.WriteFile(path, []byte("# The hub.\nhub = \"10.0.0.1:8787\"  # its address\ntoken = \"old\"\n\n[section]\nkey = \"not this one\"\n"), 0o644)
	if err := setConfig(path, [][2]string{{"hub", "https://hub.example.com:8787"}, {"key", "k1"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := "# The hub.\nhub = \"https://hub.example.com:8787\"  # its address\ntoken = \"old\"\nkey = \"k1\"\n\n[section]\nkey = \"not this one\"\n"
	check(t, "the hub replaced with its comment, the key before the section, the rest kept", string(data) == want, "\n"+string(data))
	config, _ := loadConfig(path)
	check(t, "and read back", config["hub"] == "https://hub.example.com:8787" && config["key"] == "k1" &&
		config["token"] == "old" && config["section.key"] == "not this one", config)
	setConfig(path, [][2]string{{"key", "k2"}})
	config, _ = loadConfig(path)
	check(t, "pairing again replaces the key", config["key"] == "k2", config)
	info, _ := os.Stat(path)
	check(t, "readable only by you", info.Mode().Perm() == 0o600, info.Mode())

	made := filepath.Join(dir, "new", "agent.toml")
	check(t, "a config that isn't there is made", setConfig(made, [][2]string{{"hub", "10.0.0.1:8787"}, {"key", "k"}}) == nil)
	data, _ = os.ReadFile(made)
	check(t, "with the two", string(data) == "hub = \"10.0.0.1:8787\"\nkey = \"k\"\n", string(data))
}

// The code goes to the hub's /pair-link, by any address agent.toml takes, and over TLS with a
// CA of your own; a code the hub refuses says why.
func TestPairWithHub(t *testing.T) {
	var got []map[string]any
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		if r.Method != http.MethodPost || r.URL.Path != "/pair-link" || body["code"] != "good" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"ok": false, "error": "pairing code not valid"}`))
			return
		}
		w.Write([]byte(`{"ok": true, "key": "the-key"}`))
	})
	hub := httptest.NewServer(handler)
	defer hub.Close()
	address := strings.TrimPrefix(hub.URL, "http://")
	for _, addr := range []string{address, hub.URL, "ws://" + address + "/agent"} {
		key, err := pairWithHub(addr, "good", "studio", "")
		check(t, "paired at "+addr, key == "the-key" && err == nil, key, err)
	}
	check(t, "as an agent, by its name", got[0]["kind"] == "agent" && got[0]["name"] == "studio", got[0])
	_, err := pairWithHub(address, "used", "studio", "")
	check(t, "a refused code says why", err != nil && strings.Contains(err.Error(), "the hub refused the code"), err)
	_, err = pairWithHub("127.0.0.1:1", "good", "studio", "")
	check(t, "a hub it can't reach says so", err != nil && strings.Contains(err.Error(), "can't reach the hub"), err)

	secure := httptest.NewTLSServer(handler)
	defer secure.Close()
	_, err = pairWithHub(secure.URL, "good", "studio", "")
	check(t, "over TLS, a certificate it doesn't trust fails", err != nil && strings.Contains(err.Error(), "certificate"), err)
	ca := filepath.Join(t.TempDir(), "hub-ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}), 0o600)
	key, err := pairWithHub(secure.URL, "good", "studio", ca)
	check(t, "and pairs with a CA of your own", key == "the-key" && err == nil, key, err)
}
