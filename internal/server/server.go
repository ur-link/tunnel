// Package server implements the tunnel edge: a public reverse proxy plus a
// WebSocket control plane that multiplexes inbound requests to connected
// clients over yamux.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ur-link/tunnel/internal/config"
)

// Server ties together the control plane, the public edge, and metrics.
type Server struct {
	cfg     *config.Server
	log     *slog.Logger
	reg     *registry
	tokens  *TokenStore
	store   *serviceStore
	metrics *metrics
}

// New constructs a Server from resolved config.
func New(cfg *config.Server, log *slog.Logger) *Server {
	s := &Server{
		cfg:     cfg,
		log:     log,
		reg:     newRegistry(),
		tokens:  NewTokenStore(cfg.TokensRaw),
		store:   newServiceStore(cfg.StateFile),
		metrics: newMetrics(),
	}
	if err := s.tokens.SetUsersFile(cfg.UsersFile); err != nil {
		log.Warn("users file load failed", "file", cfg.UsersFile, "err", err)
	}
	return s
}

// Run starts all listeners and blocks until ctx is cancelled or a listener
// fails. The control, edge, and metrics planes run on independent listeners so
// they can be routed separately (e.g. by Traefik).
func (s *Server) Run(ctx context.Context) error {
	if s.tokens.IsEphemeral() {
		s.log.Warn("no auth tokens configured — generated an ephemeral token",
			"token", s.tokens.EphemeralTok,
			"hint", "set TUNNEL_TOKENS / --tokens / tokens_file for stable auth")
	}

	errc := make(chan error, 4)
	var servers []*http.Server

	control := s.newHTTPServer(s.cfg.ControlAddr, s.controlMux(), ctx)
	metricsSrv := s.newHTTPServer(s.cfg.MetricsAddr, s.metricsMux(), ctx)
	servers = append(servers, control, metricsSrv)
	go serve(control, "control", s.cfg.ControlAddr, errc, s.log)
	go serve(metricsSrv, "metrics", s.cfg.MetricsAddr, errc, s.log)
	go s.watchTokens(ctx) // hot-reload identities without dropping connections

	// The edge listeners depend on the TLS mode.
	switch s.cfg.TLSMode {
	case "off":
		// Behind a TLS-terminating proxy: serve the edge in plain HTTP.
		httpSrv := s.newHTTPServer(s.cfg.HTTPAddr, s.edgeHandler(), ctx)
		servers = append(servers, httpSrv)
		go serve(httpSrv, "edge-http", s.cfg.HTTPAddr, errc, s.log)

	case "acme":
		// Standalone Let's Encrypt: HTTPS edge + :80 handles ACME HTTP-01 and
		// redirects all other traffic to HTTPS.
		mgr := s.acmeManager()
		httpsSrv := s.newHTTPServer(s.cfg.HTTPSAddr, s.edgeHandler(), ctx)
		httpsSrv.TLSConfig = mgr.TLSConfig()
		httpSrv := s.newHTTPServer(s.cfg.HTTPAddr, mgr.HTTPHandler(redirectToHTTPS()), ctx)
		servers = append(servers, httpsSrv, httpSrv)
		go serveTLS(httpsSrv, "edge-https", s.cfg.HTTPSAddr, errc, s.log)
		go serve(httpSrv, "edge-http", s.cfg.HTTPAddr, errc, s.log)

	case "file":
		// Mounted certificate (e.g. a wildcard cert from your own DNS-01 tooling):
		// HTTPS edge + :80 redirects to HTTPS.
		tlsCfg, err := s.fileTLSConfig()
		if err != nil {
			return err
		}
		httpsSrv := s.newHTTPServer(s.cfg.HTTPSAddr, s.edgeHandler(), ctx)
		httpsSrv.TLSConfig = tlsCfg
		httpSrv := s.newHTTPServer(s.cfg.HTTPAddr, redirectToHTTPS(), ctx)
		servers = append(servers, httpsSrv, httpSrv)
		go serveTLS(httpsSrv, "edge-https", s.cfg.HTTPSAddr, errc, s.log)
		go serve(httpSrv, "edge-http", s.cfg.HTTPAddr, errc, s.log)

	case "dns":
		// DNS-01 wildcard certs (lego): one *.<domain> cert (or per-namespace
		// *.<ns>.<domain> when nested), issued on demand and renewed in place.
		mgr, err := newDNSCertMgr(s.cfg, s.log)
		if err != nil {
			return fmt.Errorf("dns acme: %w", err)
		}
		httpsSrv := s.newHTTPServer(s.cfg.HTTPSAddr, s.edgeHandler(), ctx)
		httpsSrv.TLSConfig = mgr.TLSConfig()
		httpSrv := s.newHTTPServer(s.cfg.HTTPAddr, redirectToHTTPS(), ctx)
		servers = append(servers, httpsSrv, httpSrv)
		go serveTLS(httpsSrv, "edge-https", s.cfg.HTTPSAddr, errc, s.log)
		go serve(httpSrv, "edge-http", s.cfg.HTTPAddr, errc, s.log)
	}

	s.log.Info("tunnel server started",
		"domain", s.cfg.Domain,
		"control", s.cfg.ControlAddr,
		"http", s.cfg.HTTPAddr,
		"https", s.cfg.HTTPSAddr,
		"metrics", s.cfg.MetricsAddr,
		"tls_mode", s.cfg.TLSMode,
	)

	select {
	case <-ctx.Done():
		s.shutdown(servers...)
		return ctx.Err()
	case err := <-errc:
		s.shutdown(servers...)
		return err
	}
}

// redirectToHTTPS sends any plaintext request to its https:// equivalent (308,
// preserving method/body). Used on :80 in acme/file modes.
func redirectToHTTPS() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := "https://" + r.Host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})
}

// newHTTPServer builds an http.Server whose connections inherit ctx, with a
// ReadHeaderTimeout and IdleTimeout but deliberately NO WriteTimeout (a hard
// write deadline would sever long-lived SSE/WebSocket streams).
func (s *Server) newHTTPServer(addr string, h http.Handler, ctx context.Context) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: s.cfg.ReadHeaderTimeout,
		IdleTimeout:       s.cfg.IdleTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}

func (s *Server) shutdown(srvs ...*http.Server) {
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range srvs {
		if srv != nil {
			_ = srv.Shutdown(sctx)
		}
	}
}

// edgeRoute strips the port and base domain from a request host, returning the
// subdomain part ("" if not under the domain) and the normalised full host
// (used as the registry key).
func (s *Server) edgeRoute(host string) (sub, full string) {
	host = strings.ToLower(host)
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	suffix := "." + strings.ToLower(s.cfg.Domain)
	if !strings.HasSuffix(host, suffix) {
		return "", host
	}
	return strings.TrimSuffix(host, suffix), host
}

// reserveName picks and atomically reserves a hostname for a client and returns
// the chosen (host, slug). For a namespaced token the host is
// "<slug>-<namespace>.<domain>" (single-level, one *.<domain> wildcard) — or
// "<slug>.<namespace>.<domain>" when nested subdomains are enabled (needs a
// per-namespace wildcard). Non-namespaced tokens get "<slug>.<domain>".
func (s *Server) reserveName(requested string, info *TokenInfo) (host, slug string, ok bool) {
	hostFor := func(label string) string {
		switch {
		case info.Namespace == "":
			return label + "." + s.cfg.Domain
		case s.cfg.NestedSubdomains:
			return label + "." + info.Namespace + "." + s.cfg.Domain
		default:
			return label + "-" + info.Namespace + "." + s.cfg.Domain
		}
	}
	if name := sanitizeLabel(requested); name != "" && s.tokens.NameAllowed(info, name) {
		if h := hostFor(name); s.reg.reserve(h) {
			return h, name, true
		}
		// Requested name is taken; fall through to a random assignment.
	}
	for i := 0; i < 16; i++ {
		label := randomName(s.cfg.RandomNameLen)
		if h := hostFor(label); s.reg.reserve(h) {
			return h, label, true
		}
	}
	return "", "", false
}

// watchTokens hot-reloads the tokens file when it changes on disk, so adding or
// revoking identities never requires a restart. Active tunnels live in the
// registry (independent of the token store), so reloading drops no connections.
func (s *Server) watchTokens(ctx context.Context) {
	if s.cfg.TokensFile == "" || s.cfg.ReloadInterval <= 0 {
		return
	}
	last := modTime(s.cfg.TokensFile)
	t := time.NewTicker(s.cfg.ReloadInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if m := modTime(s.cfg.TokensFile); m != last {
				last = m
				b, err := os.ReadFile(s.cfg.TokensFile)
				if err != nil {
					s.log.Warn("tokens reload: read failed", "err", err)
					continue
				}
				if s.tokens.Reload(string(b)) {
					s.log.Info("tokens reloaded", "file", s.cfg.TokensFile)
				}
			}
		}
	}
}

func serve(srv *http.Server, name, addr string, errc chan<- error, log *slog.Logger) {
	log.Debug("listener starting", "name", name, "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errc <- fmt.Errorf("%s listener (%s): %w", name, addr, err)
	}
}

func serveTLS(srv *http.Server, name, addr string, errc chan<- error, log *slog.Logger) {
	log.Debug("tls listener starting", "name", name, "addr", addr)
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errc <- fmt.Errorf("%s listener (%s): %w", name, addr, err)
	}
}
