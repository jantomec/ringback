package main

// Pairing a Mac's agent or a speech node, as a phone pairs: a one-time code made for that
// kind of part, on the portal's Setup page or with `ringback-hub pair mac` (or `node`), which
// the part trades at /pair-link for a key of its own (`ringback-agent pair`, `ringback-speech
// pair`). It links with that key instead of the shared token, and the hub keeps only the
// key's hash, with the name the part paired as. Forgetting the part on the portal drops its
// key. The tokens in hub.toml still work beside the keys.

import (
	"crypto/subtle"
	"fmt"
	"path/filepath"
	"sync"
	"unicode/utf8"
)

// partKeysPath keeps each paired part's key hash, by kind and name: readable only by the
// hub's user.
var partKeysPath = filepath.Join(stateDir, "parts.json")

// partKinds are the parts that pair, as their links name them, and as people do.
var partKinds = map[string]string{"agent": "Mac", "speech": "speech node"}

type partKey struct {
	Key    string `json:"key"` // its hash
	Paired string `json:"paired"`
}

var partKeysMu sync.Mutex

func loadPartKeys() map[string]map[string]partKey {
	keys := map[string]map[string]partKey{}
	readJSON(partKeysPath, &keys)
	for kind := range partKinds {
		if keys[kind] == nil {
			keys[kind] = map[string]partKey{}
		}
	}
	return keys
}

// pairPart: a Mac's agent or a speech node trades a pairing code made for its kind for a key:
// {"code", "kind", "name"} answers {"key"}. Pairing again under the same name replaces the key
// before. A code that's wrong, used, expired or for another kind: 403.
func pairPart(body map[string]any) (map[string]any, error) {
	kind, name := str(body, "kind"), str(body, "name")
	if partKinds[kind] == "" || name == "" || utf8.RuneCountInString(name) > 64 {
		return nil, errBadRequest
	}
	taken, err := takePairingCode(str(body, "code"), kind)
	if err != nil {
		return nil, err
	}
	if !taken {
		log("pairing refused for the " + partKinds[kind] + " " + name + ": a code that isn't valid")
		return nil, errPairingCode
	}
	key, hash, err := newPhoneKey()
	if err != nil {
		return nil, err
	}
	partKeysMu.Lock()
	defer partKeysMu.Unlock()
	keys := loadPartKeys()
	_, again := keys[kind][name]
	keys[kind][name] = partKey{Key: hash, Paired: isoNow()}
	if err := writePrivateJSON(partKeysPath, keys); err != nil {
		return nil, err
	}
	how := "paired"
	if again {
		how = "paired again, its key before no longer works"
	}
	log(fmt.Sprintf("the %s %s: %s", partKinds[kind], name, how))
	return map[string]any{"ok": true, "key": key}, nil
}

// pairedPart is the name a part of that kind paired as with key; false if no part has it.
func pairedPart(kind, key string) (string, bool) {
	if key == "" {
		return "", false
	}
	hash := hashOf(key)
	partKeysMu.Lock()
	defer partKeysMu.Unlock()
	found := ""
	for name, k := range loadPartKeys()[kind] {
		if subtle.ConstantTimeCompare([]byte(k.Key), []byte(hash)) == 1 {
			found = name
		}
	}
	return found, found != ""
}

// forgetPartKey drops a part's key, so it can't link again until it pairs again.
func forgetPartKey(kind, name string) error {
	partKeysMu.Lock()
	defer partKeysMu.Unlock()
	keys := loadPartKeys()
	if _, ok := keys[kind][name]; !ok {
		return nil
	}
	delete(keys[kind], name)
	return writePrivateJSON(partKeysPath, keys)
}

// partCommand is what to run on a Mac or a speech node to pair it with code.
func partCommand(kind, code string) string {
	program := "~/.local/bin/ringback-agent"
	if kind == "speech" {
		program = "~/ringback-speech/bin/ringback-speech"
	}
	return program + " pair " + hubURL() + " " + code
}
