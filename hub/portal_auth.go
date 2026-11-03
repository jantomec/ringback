package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// The portal's login: a user name and password, then a one-time passcode from an
// authenticator app (TOTP, RFC 6238). Passkeys wait until the portal has a name and HTTPS
// ("The hub's portal" and "Security" in docs/design.md).

// The portal's account: portal-user.json in the state folder, readable only by the hub's
// user. `ringback-hub portal-user NAME` makes it, or replaces it, which ends every session
// signed in to the one before.
var accountPath = filepath.Join(stateDir, "portal-user.json")

type portalAccount struct {
	Name       string `json:"name"`
	Salt       string `json:"salt"`        // base64
	Iterations int    `json:"iterations"`  // of PBKDF2-SHA256
	Hash       string `json:"hash"`        // base64, 32 bytes
	TOTPSecret string `json:"totp_secret"` // base32, without padding
	Created    string `json:"created"`
	// LastStep is the time step of the last passcode taken: none at or before it is taken
	// again, so a code works once (RFC 6238, 5.2).
	LastStep int64 `json:"last_step,omitempty"`
}

const (
	passwordIterations = 600_000
	minPassword        = 10
	totpPeriod         = 30
	totpDigits         = 6
)

var (
	accountMu sync.Mutex // reading and writing the account, and taking a passcode
	b32       = base32.StdEncoding.WithPadding(base32.NoPadding)
)

func loadAccount() *portalAccount {
	data, err := os.ReadFile(accountPath)
	if err != nil {
		return nil
	}
	var a portalAccount
	if json.Unmarshal(data, &a) != nil || a.Name == "" || a.Iterations < 1 {
		return nil
	}
	return &a
}

// saveAccount writes the account atomically, readable only by this user.
func saveAccount(a *portalAccount) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	tmp := accountPath + ".new"
	_ = os.Remove(tmp)
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, accountPath)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand doesn't fail on the systems the hub runs on
	}
	return b
}

func hashPassword(password string, salt []byte, iterations int) []byte {
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		panic(err) // only for a key length SHA-256 can't make
	}
	return key
}

// newAccount makes an account with a random salt and passcode secret.
func newAccount(name, password string) *portalAccount {
	salt, secret := randomBytes(16), randomBytes(20)
	return &portalAccount{
		Name: name, Salt: base64.StdEncoding.EncodeToString(salt), Iterations: passwordIterations,
		Hash:       base64.StdEncoding.EncodeToString(hashPassword(password, salt, passwordIterations)),
		TOTPSecret: b32.EncodeToString(secret), Created: isoNow(),
	}
}

// checkPassword says whether a name and password are the account's. It always hashes, and
// compares both in constant time, so how long it takes says nothing about the name.
func (a *portalAccount) checkPassword(name, password string) bool {
	salt, err1 := base64.StdEncoding.DecodeString(a.Salt)
	want, err2 := base64.StdEncoding.DecodeString(a.Hash)
	got := hashPassword(password, salt, a.Iterations)
	nameOK := subtle.ConstantTimeCompare([]byte(name), []byte(a.Name))
	passwordOK := subtle.ConstantTimeCompare(got, want)
	return err1 == nil && err2 == nil && nameOK&passwordOK == 1
}

// totp is the passcode for a time step (RFC 6238 over RFC 4226's HOTP, with HMAC-SHA-1).
func totp(secret []byte, step int64, digits int) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, code%mod)
}

// codeStep is the time step an account's passcode is right for, among the steps around t (one
// either side, for clocks that differ a little), or -1.
func codeStep(a *portalAccount, code string, t time.Time) (int64, error) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	secret, err := b32.DecodeString(strings.ToUpper(a.TOTPSecret))
	if err != nil {
		return -1, err
	}
	current := t.Unix() / totpPeriod
	var matched int64 = -1
	for step := current - 1; step <= current+1; step++ {
		if subtle.ConstantTimeCompare([]byte(code), []byte(totp(secret, step, totpDigits))) == 1 {
			matched = step
		}
	}
	return matched, nil
}

// takeCode checks a passcode and, if it's right and newer than the last one taken, takes it:
// no code works twice. reused says it was right but already taken, or older than the last.
func takeCode(code string, t time.Time) (ok, reused bool, err error) {
	accountMu.Lock()
	defer accountMu.Unlock()
	a := loadAccount()
	if a == nil {
		return false, false, errors.New("no account")
	}
	matched, err := codeStep(a, code, t)
	switch {
	case err != nil:
		return false, false, err
	case matched < 0:
		return false, false, nil
	case matched <= a.LastStep:
		return false, true, nil
	}
	a.LastStep = matched
	return true, false, saveAccount(a)
}

// otpauthURI is the link an authenticator app takes the account from. Spaces are %20, not
// +, which some apps would show.
func otpauthURI(a *portalAccount) string {
	return "otpauth://totp/" + url.PathEscape("ringback hub:"+a.Name) + "?secret=" + a.TOTPSecret +
		"&issuer=" + url.PathEscape("ringback hub") + fmt.Sprintf("&algorithm=SHA1&digits=%d&period=%d", totpDigits, totpPeriod)
}

// createPortalUser is `ringback-hub portal-user NAME`: it asks for the password twice,
// without showing it, prints what an authenticator app needs, and keeps the account once a
// passcode from the app matches, so it can't end with an account whose key you didn't get.
func createPortalUser(name string) {
	if n := utf8.RuneCountInString(name); n < 1 || n > 64 || strings.ContainsAny(name, " \t\r\n:") {
		fail("A user name is 1 to 64 characters, without spaces or colons.")
	}
	in := bufio.NewReader(os.Stdin)
	password := askPassword("Password: ", in)
	if utf8.RuneCountInString(password) < minPassword {
		fail(fmt.Sprintf("Use a password of at least %d characters.", minPassword))
	}
	if askPassword("Again: ", in) != password {
		fail("The two passwords differ; nothing was changed.")
	}
	a := newAccount(name, password)
	key := a.TOTPSecret
	var groups []string
	for len(key) > 4 {
		groups, key = append(groups, key[:4]), key[4:]
	}
	groups = append(groups, key)
	fmt.Printf(`Add the account %s to an authenticator app for its passcodes, with this link (open
it on the device with the app) or the setup key:

  %s

  setup key: %s   (time-based, 6 digits, every 30 s)

`, a.Name, otpauthURI(a), strings.Join(groups, " "))
	for tries := 1; ; tries++ {
		fmt.Fprint(os.Stderr, "The passcode the app shows, to check it: ")
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			fail("\nNo passcode given; nothing was changed.")
		}
		step, err := codeStep(a, line, now())
		if err != nil {
			fail("Can't check the passcode: " + err.Error())
		}
		if step >= 0 {
			a.LastStep = step
			break
		}
		if tries == 3 {
			fail("None of three passcodes matched; nothing was changed. Check the clocks of the phone and this computer, and run it again.")
		}
		fmt.Fprintln(os.Stderr, "That passcode doesn't match; try the one the app shows now.")
	}
	accountMu.Lock()
	before := loadAccount()
	err := saveAccount(a)
	accountMu.Unlock()
	if err != nil {
		fail("Can't write " + accountPath + ": " + err.Error())
	}
	if before != nil {
		fmt.Printf("It replaces the account %s; whoever was signed in to it is signed out.\n", before.Name)
	}
	address := "http://<the hub's address>/portal/"
	if data, err := os.ReadFile(configPath); err == nil && len(data) > 0 {
		if listen := loadConfig()["listen"]; listen != "" {
			address = "http://" + listen + "/portal/"
		}
	}
	fmt.Printf("The portal's account is %s. Sign in at %s\n", a.Name, address)
}

// askPassword reads a line from the terminal without echoing it (stty), or from whatever
// stdin is, when it isn't a terminal.
func askPassword(prompt string, in *bufio.Reader) string {
	fmt.Fprint(os.Stderr, prompt)
	stty := func(arg string) bool {
		cmd := exec.Command("stty", arg)
		cmd.Stdin = os.Stdin
		return cmd.Run() == nil
	}
	hidden := stty("-echo")
	if hidden {
		interrupted := make(chan os.Signal, 1)
		signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
		done := make(chan struct{})
		defer func() {
			signal.Stop(interrupted)
			close(done)
			stty("echo")
			fmt.Fprintln(os.Stderr)
		}()
		go func() {
			select {
			case <-interrupted:
				stty("echo")
				fmt.Fprintln(os.Stderr)
				os.Exit(1)
			case <-done:
			}
		}()
	}
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		fail("\nNo password given.")
	}
	return strings.TrimRight(line, "\r\n")
}

// Signing in. Everything lives in memory, so a restart signs everyone out.
const (
	sessionCookie = "ringback_session"
	loginCookie   = "ringback_login" // a login past its password, waiting for the passcode
	formCookie    = "ringback_form"  // the login form's CSRF token, before there's a session
	sessionIdle   = 12 * time.Hour
	loginWithin   = 5 * time.Minute
	maxFailures   = 5
	refuseFor     = time.Minute
	// refreshHeader marks the pages' own refreshes, which don't count as use: a page left
	// open still signs you out after sessionIdle.
	refreshHeader = "X-Portal-Refresh"
)

// portalSession is one signed-in browser: the account it signed in to (a new account ends
// it), its CSRF token, and when it was last used. A pending login is one past its password,
// with until as its deadline.
type portalSession struct {
	name, account string // the account's name and salt, which a new account changes
	csrf          string
	used, until   time.Time
}

type loginFailures struct {
	count         int
	last, refused time.Time
}

var (
	portalMu       sync.Mutex
	portalSessions = map[string]*portalSession{} // by the SHA-256 of their ID
	pendingLogins  = map[string]*portalSession{}
	failures       = map[string]*loginFailures{} // by address
)

func newID() string { return base64.RawURLEncoding.EncodeToString(randomBytes(32)) }

// keyOf is how a session is kept: by its ID's hash, so finding it compares no secret.
func keyOf(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/portal", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func clearCookie(w http.ResponseWriter, name string) { setCookie(w, name, "", -1) }

func cookie(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// signedIn is the request's session, if it has one that's in use and of the current account;
// a page's own refresh doesn't count as use.
func signedIn(r *http.Request, a *portalAccount) *portalSession {
	id := cookie(r, sessionCookie)
	if id == "" || a == nil {
		return nil
	}
	portalMu.Lock()
	defer portalMu.Unlock()
	t := now()
	for key, s := range portalSessions {
		if t.Sub(s.used) > sessionIdle {
			delete(portalSessions, key)
		}
	}
	s := portalSessions[keyOf(id)]
	if s == nil || s.name != a.Name || s.account != a.Salt {
		return nil
	}
	if r.Header.Get(refreshHeader) == "" {
		s.used = t
	}
	return s
}

// pendingLogin is the request's login waiting for its passcode, if it's still in time.
func pendingLogin(r *http.Request, a *portalAccount) *portalSession {
	id := cookie(r, loginCookie)
	if id == "" || a == nil {
		return nil
	}
	portalMu.Lock()
	defer portalMu.Unlock()
	t := now()
	for key, s := range pendingLogins {
		if t.After(s.until) {
			delete(pendingLogins, key)
		}
	}
	s := pendingLogins[keyOf(id)]
	if s == nil || s.name != a.Name || s.account != a.Salt {
		return nil
	}
	return s
}

// startLogin keeps a login past its password until its passcode comes, at most loginWithin.
func startLogin(w http.ResponseWriter, a *portalAccount) {
	id := newID()
	portalMu.Lock()
	pendingLogins[keyOf(id)] = &portalSession{name: a.Name, account: a.Salt, csrf: newID(), until: now().Add(loginWithin)}
	portalMu.Unlock()
	setCookie(w, loginCookie, id, int(loginWithin.Seconds()))
}

// finishLogin swaps the pending login for a session, with a new ID.
func finishLogin(w http.ResponseWriter, r *http.Request, a *portalAccount) {
	id := newID()
	portalMu.Lock()
	delete(pendingLogins, keyOf(cookie(r, loginCookie)))
	portalSessions[keyOf(id)] = &portalSession{name: a.Name, account: a.Salt, csrf: newID(), used: now()}
	portalMu.Unlock()
	clearCookie(w, loginCookie)
	clearCookie(w, formCookie)
	setCookie(w, sessionCookie, id, 0)
}

func signOut(w http.ResponseWriter, r *http.Request) {
	portalMu.Lock()
	delete(portalSessions, keyOf(cookie(r, sessionCookie)))
	portalMu.Unlock()
	clearCookie(w, sessionCookie)
}

// formToken is the login form's CSRF token: one in a cookie, which the form carries back.
func formToken(w http.ResponseWriter, r *http.Request) string {
	if token := cookie(r, formCookie); len(token) == 43 {
		return token
	}
	token := newID()
	setCookie(w, formCookie, token, 0)
	return token
}

// sameToken compares a form's CSRF token with the one it should carry, in constant time.
func sameToken(r *http.Request, want string) bool {
	got := r.PostFormValue("csrf")
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// address is where a request comes from, without its port.
func address(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// addressRefused says whether an address is refused for now, after maxFailures failed logins.
func addressRefused(addr string) bool {
	portalMu.Lock()
	defer portalMu.Unlock()
	t := now()
	for a, f := range failures {
		if t.After(f.refused) && t.Sub(f.last) > time.Hour {
			delete(failures, a)
		}
	}
	f := failures[addr]
	return f != nil && t.Before(f.refused)
}

// failedLogin counts a failed login from an address, and logs it; the fifth in a row refuses
// the address for refuseFor.
func failedLogin(addr, why string) {
	portalMu.Lock()
	f := failures[addr]
	if f == nil {
		f = &loginFailures{}
		failures[addr] = f
	}
	f.count++
	f.last = now()
	count := f.count
	if count >= maxFailures {
		f.count, f.refused = 0, now().Add(refuseFor)
	}
	portalMu.Unlock()
	log(fmt.Sprintf("portal: a failed login from %s: %s (%d in a row)", addr, why, count))
	if count >= maxFailures {
		log("portal: " + addr + " is refused for a minute")
	}
}

func loggedIn(addr string) {
	portalMu.Lock()
	delete(failures, addr)
	portalMu.Unlock()
}
