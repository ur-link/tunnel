package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"sync"

	"golang.org/x/crypto/acme/autocert"
)

// acmeManager builds the autocert manager used in tls-mode=acme. It obtains a
// Let's Encrypt certificate on demand (TLS-ALPN-01 on :443, or HTTP-01 served by
// the manager's HTTPHandler on :80) for any host that currently has a live
// tunnel. Gating issuance on registered hosts prevents attackers from triggering
// unbounded cert requests for arbitrary subdomains.
//
// autocert does NOT support DNS-01 or wildcard certificates. For a wildcard /
// DNS-01 cert, obtain it externally (Caddy, certbot, acme.sh, lego, …) and run
// tls-mode=file, or terminate TLS at a proxy and run tls-mode=off.
func (s *Server) acmeManager() *autocert.Manager {
	return &autocert.Manager{
		Prompt: autocert.AcceptTOS,
		Cache:  autocert.DirCache(s.cfg.TLSCacheDir),
		Email:  s.cfg.TLSACMEEmail,
		HostPolicy: func(_ context.Context, host string) error {
			if _, ok := s.reg.lookup(host); ok {
				return nil
			}
			return fmt.Errorf("acme: host %q has no active tunnel", host)
		},
	}
}

// fileTLSConfig builds a *tls.Config for tls-mode=file that serves a certificate
// loaded from disk (e.g. a wildcard cert you obtained via DNS-01 with your own
// tooling and mounted into the container). The cert is hot-reloaded when the
// files change on disk, so renewals are picked up without a restart.
func (s *Server) fileTLSConfig() (*tls.Config, error) {
	r := &certReloader{certFile: s.cfg.TLSCertFile, keyFile: s.cfg.TLSKeyFile}
	if _, err := r.load(); err != nil { // fail fast on a bad/missing cert at startup
		return nil, fmt.Errorf("load tls cert: %w", err)
	}
	return &tls.Config{GetCertificate: r.GetCertificate, MinVersion: tls.VersionTLS12}, nil
}

// certReloader caches a tls.Certificate and reloads it from disk when either the
// cert or key file's modification time changes.
type certReloader struct {
	certFile, keyFile string

	mu      sync.RWMutex
	cert    *tls.Certificate
	modCert int64
	modKey  int64
}

func (r *certReloader) load() (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return nil, err
	}
	mc, mk := modTime(r.certFile), modTime(r.keyFile)
	r.mu.Lock()
	r.cert, r.modCert, r.modKey = &cert, mc, mk
	r.mu.Unlock()
	return &cert, nil
}

func (r *certReloader) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.RLock()
	cur, mc, mk := r.cert, r.modCert, r.modKey
	r.mu.RUnlock()
	// Reload if either file changed on disk (cert renewal).
	if modTime(r.certFile) != mc || modTime(r.keyFile) != mk {
		if reloaded, err := r.load(); err == nil {
			return reloaded, nil
		}
		// On reload error keep serving the last good cert.
	}
	return cur, nil
}

func modTime(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().UnixNano()
	}
	return 0
}
