package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var apnsHosts = map[string]string{
	"sandbox":    "https://api.sandbox.push.apple.com",
	"production": "https://api.push.apple.com",
}

type pushKind int

const (
	voip       pushKind = iota // rings the phone, through PushKit; goes to the VoIP token
	background                 // wakes the app for a moment, showing nothing; goes to the notification token
	alert                      // an ordinary notification, shown and sounding as your Focus allows
)

// apnsHTTP is one client for every push: Go speaks HTTP/2 to APNs over TLS by itself, and
// keeps the connection.
var apnsHTTP = &http.Client{Timeout: 10 * time.Second}

// tokenTTL is how long a provider token is used. APNs refuses a new one more often than
// every 20 minutes on a connection, and an hour-old one has expired. (The Swift switchboard
// made a new token for every push.)
const tokenTTL = 50 * time.Minute

type apnsToken struct {
	bearer string
	made   time.Time
	keyID  string
}

var (
	tokensMu sync.Mutex
	tokens   = map[string]apnsToken{} // by environment
)

// apnsSetupError is why the hub can't push to an environment.
type apnsSetupError struct{ text string }

func (e apnsSetupError) Error() string { return e.text }

// apnsClient is what a push needs for an environment, from the config: the bundle ID and a
// provider token, made at most every tokenTTL.
func apnsClient(environment string, config map[string]string) (bundleID, bearer string, err error) {
	team, bundle := config["apns.team_id"], config["apns.bundle_id"]
	keyID, keyFile := config["apns."+environment+".key_id"], config["apns."+environment+".key_file"]
	if team == "" || bundle == "" || keyID == "" || keyFile == "" {
		return "", "", apnsSetupError{"no [apns." + environment + "] key in the config"}
	}
	tokensMu.Lock()
	defer tokensMu.Unlock()
	if t, ok := tokens[environment]; ok && t.keyID == keyID && now().Sub(t.made) < tokenTTL {
		return bundle, t.bearer, nil
	}
	bearer, err = providerToken(team, keyID, expand(keyFile))
	if err != nil {
		return "", "", apnsSetupError{fmt.Sprintf("can't sign with the APNs key %s: %v", keyFile, err)}
	}
	tokens[environment] = apnsToken{bearer, now(), keyID}
	return bundle, bearer, nil
}

// readPushKey reads a .p8 key, as Apple gives it: an EC private key in PKCS #8, in PEM.
func readPushKey(keyFile string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("not a PEM key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an EC key")
	}
	return key, nil
}

// providerToken is an ES256 JWT signed with the .p8 key.
func providerToken(teamID, keyID, keyFile string) (string, error) {
	key, err := readPushKey(keyFile)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID})
	claims, _ := json.Marshal(map[string]any{"iss": teamID, "iat": now().Unix()})
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return signing + "." + enc.EncodeToString(signature), nil
}

// push sends one push; it returns APNs' status and its reason if it refused. It expires at
// once, so a phone that's offline doesn't get it later, unless it's kept until expiresAt.
func push(ctx context.Context, config map[string]string, deviceToken, environment string, kind pushKind, expiresAt time.Time, payload any) (int, string, error) {
	bundleID, bearer, err := apnsClient(environment, config)
	if err != nil {
		return 0, "", err
	}
	host, ok := apnsHosts[environment]
	if !ok {
		return 0, "", fmt.Errorf("unknown APNs environment %q", environment)
	}
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, "POST", host+"/3/device/"+deviceToken, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	topic, kindName, priority := bundleID, "alert", "10"
	switch kind {
	case voip:
		topic, kindName = bundleID+".voip", "voip"
	case background:
		kindName, priority = "background", "5" // a background push must be 5
	}
	expiration := "0"
	if !expiresAt.IsZero() {
		expiration = strconv.FormatInt(expiresAt.Unix(), 10)
	}
	request.Header.Set("authorization", "bearer "+bearer)
	request.Header.Set("apns-topic", topic)
	request.Header.Set("apns-push-type", kindName)
	request.Header.Set("apns-priority", priority)
	request.Header.Set("apns-expiration", expiration)
	response, err := apnsHTTP.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	var answer struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(response.Body).Decode(&answer)
	return response.StatusCode, answer.Reason, nil
}

// The last push to each phone, by the token it went to (its VoIP token or its notification
// token), for the app's status: whether the hub can still reach it through APNs.
var (
	pushesMu sync.Mutex
	pushes   = map[string]pushResult{}
)

type pushResult struct {
	at     time.Time
	status int
	reason string
	err    error
}

func (p pushResult) ok() bool { return p.err == nil && p.status == 200 }

// problem is what a failed push says about how calls reach the phone, for the app's banner:
// "key" (Apple refused the hub's key, or it has none), "phone" (Apple no longer knows this
// phone's registration), "network" (the hub couldn't reach Apple), or "apple" (anything else
// Apple refused).
func (p pushResult) problem() string {
	var setup apnsSetupError
	switch {
	case errors.As(p.err, &setup):
		return "key"
	case p.err != nil:
		return "network"
	case p.status == 403 && strings.HasSuffix(p.reason, "ProviderToken"), p.reason == "BadCertificate", p.reason == "BadCertificateEnvironment":
		return "key"
	case p.reason == "BadDeviceToken", p.reason == "Unregistered", p.reason == "DeviceTokenNotForTopic":
		return "phone"
	}
	return "apple"
}

func recordPush(token string, status int, reason string, err error) {
	pushesMu.Lock()
	defer pushesMu.Unlock()
	pushes[token] = pushResult{now(), status, reason, err}
}

// registeredAgain: the phone registered, which mends a push that failed because Apple no
// longer knew it ("phone"); other failures stay until a push gets through.
func registeredAgain(tokens ...string) {
	pushesMu.Lock()
	defer pushesMu.Unlock()
	for _, token := range tokens {
		if last, ok := pushes[token]; ok && !last.ok() && last.problem() == "phone" {
			delete(pushes, token)
		}
	}
}

// lastPush is the latest push to a phone, to either of its tokens; false if none since the
// hub started.
func lastPush(device map[string]string, token string) (pushResult, bool) {
	pushesMu.Lock()
	defer pushesMu.Unlock()
	last, found := pushes[token]
	if other, ok := pushes[device["notification_token"]]; ok && (!found || other.at.After(last.at)) {
		last, found = other, true
	}
	return last, found
}

// devicesByName are the registered phones sorted by name, as the lines about them are.
func devicesByName(devices map[string]map[string]string) []map[string]string {
	list := make([]map[string]string, 0, len(devices))
	for _, d := range devices {
		list = append(list, d)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i]["device"] < list[j]["device"] })
	return list
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// outcome is a push's result as a line: "APNs 200", "APNs 400 BadDeviceToken", or the error.
func outcome(status int, reason string, err error) string {
	if err != nil {
		var setup apnsSetupError
		if errors.As(err, &setup) {
			return setup.text
		}
		return err.Error()
	}
	return strings.TrimSpace(fmt.Sprintf("APNs %d %s", status, reason))
}

// ring rings every registered phone once, as call callID, or a new call, from session if a
// voice server named it. One line per phone.
func ring(ctx context.Context, project, subject, voice, callID, session string) []string {
	config := loadConfig()
	if callID == "" {
		callID = newUUID()
	}
	stateMu.Lock()
	devices := loadDevices()
	stateMu.Unlock()
	tokens := make([]string, 0, len(devices))
	for token := range devices {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	var lines []string
	for _, token := range tokens {
		device := devices[token]
		payload := map[string]string{"call_id": callID, "project": project}
		for key, value := range map[string]string{"subject": subject, "voice": voice, "session_id": session} {
			if value != "" {
				payload[key] = value
			}
		}
		status, reason, err := push(ctx, config, token, orDefault(device["environment"], "sandbox"), voip, time.Time{}, payload)
		recordPush(token, status, reason, err)
		lines = append(lines, "call "+prefix(callID, 8)+" → "+orDefault(device["device"], "a phone")+": "+outcome(status, reason, err))
		if err == nil && (reason == "BadDeviceToken" || reason == "Unregistered") {
			stateMu.Lock()
			_ = forgetDevice(token)
			stateMu.Unlock()
			lines = append(lines, "  token no longer valid; forgotten — reopen the app to register again")
		}
	}
	return lines
}

// notifyQuestion tells every phone that a held call has a question, with one ordinary
// notification to its notification token: "<project> has a question", and the call's
// subject. Its interruption level is the default (active), so your Focus decides whether it
// shows and sounds. Tapping it opens the app, which lists the call. APNs keeps it until the
// call would ring anyway (until), for a phone that's off meanwhile. One line per phone.
func notifyQuestion(ctx context.Context, callID, project, subject string, until time.Time) []string {
	return notifyWaiting(ctx, callID, "question", project+" has a question", orDefault(subject, "It waits until you take calls."), until)
}

// notifyNoSpeech tells every phone, as notifyQuestion does, that a call waits because no
// speech node is linked: it can't be talked through, so it doesn't ring.
func notifyNoSpeech(ctx context.Context, callID, project, subject string) []string {
	body := "The speech computer is offline, so the call waits. It rings once it's back."
	if subject != "" {
		body = subject + ". " + body
	}
	return notifyWaiting(ctx, callID, "no speech", project+" wants to talk", body, time.Time{})
}

// notifyWaiting sends every phone one ordinary notification about a waiting call. One line per
// phone, saying what it's about (what).
func notifyWaiting(ctx context.Context, callID, what, title, body string, until time.Time) []string {
	config := loadConfig()
	stateMu.Lock()
	devices := devicesByName(loadDevices())
	stateMu.Unlock()
	payload := map[string]any{"aps": map[string]any{
		"alert":              map[string]string{"title": title, "body": body},
		"sound":              "default",
		"interruption-level": "active",
	}}
	var lines []string
	for _, device := range devices {
		line := "call " + prefix(callID, 8) + ": " + what + " → " + orDefault(device["device"], "a phone")
		token, ok := device["notification_token"]
		if !ok {
			lines = append(lines, line+": no notification token")
			continue
		}
		status, reason, err := push(ctx, config, token, orDefault(device["environment"], "sandbox"), alert, until, payload)
		recordPush(token, status, reason, err)
		lines = append(lines, line+": "+outcome(status, reason, err))
	}
	return lines
}

// askPhonesToCheck asks every phone that gave a notification token to check, with a
// background push. The app wakes for a moment and calls /check over Nebula, the way a call
// reaches the Mac. If it can't, it warns you itself, so nothing here needs to hear back. One
// line per phone.
func askPhonesToCheck(ctx context.Context) []string {
	config := loadConfig()
	stateMu.Lock()
	devices := devicesByName(loadDevices())
	stateMu.Unlock()
	payload := map[string]any{"aps": map[string]any{"content-available": 1}, "check": "nebula"}
	var lines []string
	for _, device := range devices {
		token, ok := device["notification_token"]
		if !ok {
			continue
		}
		status, reason, err := push(ctx, config, token, orDefault(device["environment"], "sandbox"), background, time.Time{}, payload)
		recordPush(token, status, reason, err)
		lines = append(lines, "check → "+orDefault(device["device"], "a phone")+": "+outcome(status, reason, err))
	}
	return lines
}

// checkPeriodically asks the phones to check every 30 min, for as long as the hub serves.
// It logs only what went wrong; `devices` shows when each phone last checked in. iOS
// delivers an app at most a few background pushes an hour.
func checkPeriodically() {
	go func() {
		for {
			time.Sleep(30 * time.Minute)
			for _, line := range askPhonesToCheck(context.Background()) {
				if !strings.HasSuffix(line, "APNs 200") {
					log(line)
				}
			}
		}
	}()
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
