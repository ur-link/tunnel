package server

import (
	"context"
	"crypto/tls"
	"fmt"

	"golang.org/x/crypto/acme/autocert"
)

// acmeTLSConfig returns a *tls.Config that obtains a Let's Encrypt certificate
// on demand (TLS-ALPN-01) for any host that currently has a live tunnel. Gating
// issuance on registered hosts prevents attackers from triggering unbounded
// cert requests for arbitrary subdomains.
func (s *Server) acmeTLSConfig() *tls.Config {
	m := &autocert.Manager{
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
	return m.TLSConfig()
}
