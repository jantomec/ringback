package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// selfSigned is a certificate for name and its key, as PEM.
func selfSigned(t *testing.T, name string, serial int64, days int) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name},
		DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Duration(days) * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// The certificate is read again when its files change, and a renewal half written keeps the
// one before.
func TestCertFiles(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "key.pem")
	write := func(path string, data []byte, at time.Time) {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(path, at, at)
	}
	serial := func(files *certFiles) int64 {
		got, err := files.get()
		if err != nil {
			t.Fatal(err)
		}
		return got.Leaf.SerialNumber.Int64()
	}

	if _, err := tlsConfig(map[string]string{"tls.cert": certFile}); err == nil {
		t.Error("a cert without its key is refused")
	}
	if _, err := tlsConfig(map[string]string{"tls.cert": certFile, "tls.key": keyFile}); err == nil {
		t.Error("files that aren't there are refused")
	}
	if config, err := tlsConfig(map[string]string{}); config != nil || err != nil {
		t.Error("no [tls]: plain HTTP", config, err)
	}

	start := time.Now().Add(-time.Hour)
	cert1, key1 := selfSigned(t, "hub.example.com", 1, 90)
	write(certFile, cert1, start)
	write(keyFile, key1, start)
	config, err := tlsConfig(map[string]string{"tls.cert": certFile, "tls.key": keyFile})
	if err != nil || config.MinVersion != 0x0303 {
		t.Fatal("the certificate is served, TLS 1.2 or later", err)
	}
	files := &certFiles{cert: certFile, key: keyFile}
	if serial(files) != 1 {
		t.Error("the first certificate")
	}

	cert2, key2 := selfSigned(t, "hub.example.com", 2, 90)
	write(certFile, cert2, start.Add(time.Minute))
	if serial(files) != 1 {
		t.Error("a new certificate without its key yet keeps the one before")
	}
	write(keyFile, key2, start.Add(time.Minute))
	if serial(files) != 2 {
		t.Error("with its key, the renewal is served")
	}
	os.Remove(certFile)
	if serial(files) != 2 {
		t.Error("files gone keep the one served")
	}
}

func TestDescribeCert(t *testing.T) {
	certPEM, _ := selfSigned(t, "hub.example.com", 1, 30)
	block, _ := pem.Decode(certPEM)
	leaf, _ := x509.ParseCertificate(block.Bytes)
	want := "certificate for hub.example.com, valid until " + leaf.NotAfter.Local().Format("2006-01-02")
	if got := describeCert(leaf); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A certificate that runs out within a week is logged, as when its renewal has stopped.
func TestCertRunsOut(t *testing.T) {
	dir := t.TempDir()
	files := &certFiles{cert: filepath.Join(dir, "fullchain.pem"), key: filepath.Join(dir, "key.pem")}
	certPEM, keyPEM := selfSigned(t, "hub.example.com", 1, 3)
	os.WriteFile(files.cert, certPEM, 0o600)
	os.WriteFile(files.key, keyPEM, 0o600)
	if _, err := files.get(); err != nil {
		t.Fatal(err)
	}
	tail := logTail()
	if last := tail[len(tail)-1]; !strings.HasSuffix(last, "TLS: the certificate runs out in 3 days: check that its renewal runs") {
		t.Errorf("logged %q", last)
	}
	files.get()
	if again := logTail(); again[len(again)-1] != tail[len(tail)-1] || len(again) != len(tail) {
		t.Error("logged again within the day")
	}
}

// With a URL or TLS, the setup page gives each part the hub's URL; plain, its listen address.
func TestSetupAddresses(t *testing.T) {
	before, _ := os.ReadFile(configPath)
	defer os.WriteFile(configPath, before, 0o600)
	tlsFiles := "[tls]\ncert = \"/c.pem\"\nkey = \"/k.pem\"\n"
	for _, c := range []struct{ config, phone, agent, speech string }{
		{"listen = \"127.0.0.1:18899\"\n", "127.0.0.1:18899", "127.0.0.1:18899", "ws://127.0.0.1:18899/speech"},
		{"listen = \"0.0.0.0:8787\"\nurl = \"https://hub.example.com:8787/\"\n" + tlsFiles,
			"https://hub.example.com:8787", "https://hub.example.com:8787", "wss://hub.example.com:8787/speech"},
		{"listen = \"10.0.0.1:8787\"\n" + tlsFiles, "https://10.0.0.1:8787", "https://10.0.0.1:8787", "wss://10.0.0.1:8787/speech"},
	} {
		os.WriteFile(configPath, []byte(c.config), 0o600)
		v := setupData()
		if v.PhoneHub != c.phone || v.AgentHub != c.agent || v.SpeechHub != c.speech {
			t.Errorf("%q: got %q %q %q", c.config, v.PhoneHub, v.AgentHub, v.SpeechHub)
		}
	}
}

// Over TLS, the portal's cookies are only sent over TLS.
func TestSecureCookie(t *testing.T) {
	defer func() { servingTLS = false }()
	for _, serving := range []bool{false, true} {
		servingTLS = serving
		w := httptest.NewRecorder()
		setCookie(w, "s", "v", 60)
		if got := strings.Contains(w.Header().Get("Set-Cookie"), "; Secure"); got != serving {
			t.Errorf("serving TLS %v: %s", serving, w.Header().Get("Set-Cookie"))
		}
	}
}
