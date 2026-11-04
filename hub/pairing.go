package main

// Pairing: a phone proves it's yours with a one-time code, which the portal makes for you once
// you've signed in ("Pair this phone"), or `ringback-hub pair` on the hub's machine. Opened on
// the phone, the code's link (ringback://pair) gives the app the hub's address and the code,
// which the app trades at /pair for a key of its own. The hub keeps only the key's hash, with
// the phone, and each request that names the phone by its push token must carry the key: the
// push token alone isn't a secret, since anything on the network can make one up.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

var (
	errNotPaired   = errors.New("not paired")
	errPairingCode = errors.New("pairing code not valid")
)

// pairingPath keeps the codes not yet used, by their hashes, so a code `ringback-hub pair`
// makes works for the hub that runs: readable only by the hub's user.
var pairingPath = filepath.Join(stateDir, "pairing.json")

// pairingValid is how long a code lasts.
const pairingValid = 10 * time.Minute

type pendingCode struct {
	Hash    string    `json:"hash"`
	Expires time.Time `json:"expires"`
	// What it pairs: a phone (""), a Mac's agent ("agent") or a speech node ("speech").
	For string `json:"for,omitempty"`
}

// pendingCodes are the codes still valid at t, read from pairingPath.
func pendingCodes(t time.Time) []pendingCode {
	var codes, valid []pendingCode
	readJSON(pairingPath, &codes)
	for _, c := range codes {
		if t.Before(c.Expires) {
			valid = append(valid, c)
		}
	}
	return valid
}

func savePendingCodes(codes []pendingCode) error {
	if codes == nil {
		codes = []pendingCode{}
	}
	return writePrivateJSON(pairingPath, codes)
}

// newPairingCode makes a one-time code that pairs a phone (kind "") or a part of that kind
// (partKinds), valid for pairingValid. The caller holds stateMu (or is the `pair` command).
func newPairingCode(kind string) (code string, expires time.Time, err error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", time.Time{}, err
	}
	code = base64.RawURLEncoding.EncodeToString(random)
	t := now()
	expires = t.Add(pairingValid)
	codes := append(pendingCodes(t), pendingCode{Hash: hashOf(code), Expires: expires, For: kind})
	return code, expires, savePendingCodes(codes)
}

// takePairingCode uses a code up: true if it was one of the valid ones, for that kind. The
// caller holds stateMu.
func takePairingCode(code, kind string) (bool, error) {
	if code == "" {
		return false, nil
	}
	hash := hashOf(code)
	var kept []pendingCode
	found := false
	for _, c := range pendingCodes(now()) {
		if !found && subtle.ConstantTimeCompare([]byte(c.Hash), []byte(hash)) == 1 && c.For == kind {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	if !found {
		return false, nil
	}
	return true, savePendingCodes(kept)
}

// pairingLink opens Ringback on the phone, with the hub's address as the phone reaches it and
// the code.
func pairingLink(code string) string {
	return "ringback://pair?hub=" + url.QueryEscape(hubURL()) + "&code=" + url.QueryEscape(code)
}

// newPhoneKey is a phone's key, and the hash the hub keeps of it.
func newPhoneKey() (key, hash string, err error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", "", err
	}
	key = hex.EncodeToString(random)
	return key, hashOf(key), nil
}

func hashOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// keyMatches says whether key is the one whose hash the phone's entry keeps.
func keyMatches(device map[string]string, key string) bool {
	return key != "" && device["key"] != "" &&
		subtle.ConstantTimeCompare([]byte(hashOf(key)), []byte(device["key"])) == 1
}

// pairedDevices are the phones that paired: the ones calls ring, the hold counts and pushes go
// to. A phone registered before pairing, with no key, waits to pair. The caller holds stateMu.
func pairedDevices() map[string]map[string]string {
	paired := map[string]map[string]string{}
	for token, device := range loadDevices() {
		if device["key"] != "" {
			paired[token] = device
		}
	}
	return paired
}

// pairedPhone is the phone a request names by its push token, if it carries that phone's key;
// errNotPaired otherwise.
func pairedPhone(body map[string]any) (string, map[string]string, error) {
	token, key := str(body, "token"), str(body, "key")
	device, known := loadDevices()[token]
	if token == "" || !known || !keyMatches(device, key) {
		return "", nil, errNotPaired
	}
	return token, device, nil
}

// movePairedPhone finds the phone a key belongs to, and if its push token has changed (APNs
// can give it a new one), keeps it under the new token. Returns whether there's such a phone.
// The caller holds stateMu.
func movePairedPhone(token, key string) (bool, error) {
	if token == "" || key == "" {
		return false, nil
	}
	devices := loadDevices()
	for old, device := range devices {
		if !keyMatches(device, key) {
			continue
		}
		if old == token {
			return true, nil
		}
		delete(devices, old)
		devices[token] = device
		log(fmt.Sprintf("%s: push token …%s, was …%s", orDefault(device["device"], "a phone"), suffix(token, 8), suffix(old, 8)))
		return true, saveDevices(devices)
	}
	return false, nil
}

// pair: a phone trades a pairing code for its key, with what /register takes: {"code",
// "token", "environment", "device", "notification_token", "on_device", work hours}. A valid
// code makes the phone a key, which the hub keeps only as a hash, registers the phone and
// answers {"key"}. A code that's wrong, used or expired: 403.
func pair(body map[string]any) (map[string]any, error) {
	token := str(body, "token")
	if _, valid := workHoursFields(body); token == "" || !valid {
		return nil, errBadRequest
	}
	device := orDefault(str(body, "device"), "a phone")
	taken, err := takePairingCode(str(body, "code"), "")
	if err != nil {
		return nil, err
	}
	if !taken {
		log("pairing refused for " + device + ", token …" + suffix(token, 8) + ": a code that isn't valid")
		return nil, errPairingCode
	}
	key, hash, err := newPhoneKey()
	if err != nil {
		return nil, err
	}
	// Registered as /register would: a phone new to the hub joins a hold you set. An entry it
	// had (its hold, its hours) stays.
	if err := keepPhone(body); err != nil {
		return nil, err
	}
	devices := loadDevices()
	devices[token]["key"] = hash
	if err := saveDevices(devices); err != nil {
		return nil, err
	}
	log("paired " + device + ", token …" + suffix(token, 8))
	return map[string]any{"ok": true, "key": key}, nil
}

// writePrivateJSON writes as writeJSON does, readable only by the hub's user.
func writePrivateJSON(path string, value any) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
