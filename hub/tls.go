package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// servingTLS says whether the hub serves HTTPS and WSS ([tls] in hub.toml), so the portal's
// cookies are marked Secure.
var servingTLS bool

// tlsConfig is the hub's TLS, nil without [tls] in hub.toml: a certificate chain and its key,
// as PEM files, such as an ACME client writes them. The files are read again whenever they
// change, so a renewal needs no restart.
func tlsConfig(config map[string]string) (*tls.Config, error) {
	certFile, keyFile := expand(config["tls.cert"]), expand(config["tls.key"])
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, errors.New("[tls] needs both cert and key")
	}
	files := &certFiles{cert: certFile, key: keyFile}
	if _, err := files.get(); err != nil {
		return nil, err
	}
	// HTTP/1.1 only, which the WebSocket links upgrade from.
	return &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return files.get() }}, nil
}

// certFiles is the certificate in a pair of files, read again when either changes. A
// renewal writes them one at a time, so a pair that doesn't match keeps the certificate
// before it until the other file is written too.
type certFiles struct {
	cert, key string

	mu             sync.Mutex
	loaded         *tls.Certificate
	certAt, keyAt  time.Time // the files' times when last read
	expiryReported time.Time
}

func (c *certFiles) get() (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	certInfo, certErr := os.Stat(c.cert)
	keyInfo, keyErr := os.Stat(c.key)
	if err := errors.Join(certErr, keyErr); err != nil {
		if c.loaded != nil {
			return c.loaded, nil
		}
		return nil, err
	}
	if certInfo.ModTime().Equal(c.certAt) && keyInfo.ModTime().Equal(c.keyAt) {
		c.reportExpiry()
		return c.loaded, nil
	}
	c.certAt, c.keyAt = certInfo.ModTime(), keyInfo.ModTime()
	pair, err := tls.LoadX509KeyPair(c.cert, c.key)
	switch {
	case err != nil && c.loaded == nil:
		return nil, fmt.Errorf("can't read the certificate: %v", err)
	case err != nil:
		log("TLS: the certificate's files changed but can't be read (" + err.Error() + "); the one before stays")
		return c.loaded, nil
	}
	c.loaded = &pair
	log("TLS: " + describeCert(pair.Leaf))
	c.reportExpiry()
	return c.loaded, nil
}

// reportExpiry logs, once a day, a certificate that has run out or runs out within a week,
// as when its renewal has stopped.
func (c *certFiles) reportExpiry() {
	left := time.Until(c.loaded.Leaf.NotAfter)
	if left > 7*24*time.Hour || time.Since(c.expiryReported) < 24*time.Hour {
		return
	}
	c.expiryReported = time.Now()
	if left <= 0 {
		log("TLS: the certificate ran out on " + c.loaded.Leaf.NotAfter.Local().Format("2006-01-02") + ": renew it")
		return
	}
	log(fmt.Sprintf("TLS: the certificate runs out in %.0f days: check that its renewal runs", left.Hours()/24))
}

// describeCert is a certificate's names and when it runs out, for the log.
func describeCert(leaf *x509.Certificate) string {
	names := append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) == 0 {
		names = []string{leaf.Subject.CommonName}
	}
	return "certificate for " + strings.Join(names, ", ") + ", valid until " + leaf.NotAfter.Local().Format("2006-01-02")
}
