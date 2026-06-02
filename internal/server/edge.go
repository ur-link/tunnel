package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// edgeHandler serves public traffic. The leading host label decides routing:
//   - "admin"          -> admin console / API
//   - a known namespace -> that user's hub (status page / API)
//   - anything else     -> a tunnel (registry lookup)
func (s *Server) edgeHandler() http.Handler {
	admin := s.adminMux()
	control := s.controlMux()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub, full := s.edgeRoute(r.Host)
		switch {
		case sub == "":
			s.writeEdgeIndex(w, http.StatusNotFound, fmt.Sprintf("no tunnel for host %q", r.Host))
		case s.cfg.ControlHost != "" && sub == s.cfg.ControlHost:
			// Serve the control plane on the edge too, so clients reach it over the
			// edge's TLS at wss://<control-host>.<domain> (single port, works behind
			// Cloudflare-proxied / any L7 that only forwards :80/:443).
			control.ServeHTTP(w, r)
		case sub == "admin":
			admin.ServeHTTP(w, r)
		case !strings.Contains(sub, ".") && s.tokens.Namespaces()[sub]:
			// Bare namespace label -> that user's hub. In path mode the hub host
			// also serves the namespace's services at /<slug>/.
			if s.cfg.RoutingMode == "path" {
				s.handlePathNamespace(w, r, sub)
			} else {
				s.handleHub(w, r, sub)
			}
		default:
			// A service: <slug>-<ns>.<domain> (flat) or <slug>.<ns>.<domain> (nested).
			sess, ok := s.reg.lookup(full)
			if !ok {
				s.writeEdgeIndex(w, http.StatusBadGateway, fmt.Sprintf("tunnel %q is not connected", sub))
				return
			}
			sess.ServeHTTP(w, r)
		}
	})
}

// writeEdgeIndex returns a plain-text message for unrouteable requests.
func (s *Server) writeEdgeIndex(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "tunnel: %s\n", msg)
}

// metricsMux serves Prometheus metrics and the JSON status API.
func (s *Server) metricsMux() http.Handler {
	m := http.NewServeMux()
	m.Handle("/metrics", s.metrics.handler())
	m.HandleFunc("/_tunnel/status", s.handleStatus)
	m.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return m
}

// tunnelStatus is one entry in the status API.
type tunnelStatus struct {
	Host          string    `json:"host"`
	Namespace     string    `json:"namespace,omitempty"`
	URL           string    `json:"url"`
	Label         string    `json:"label,omitempty"`
	Online        bool      `json:"online"`
	ActiveStreams int64     `json:"active_streams"`
	Requests      int64     `json:"requests"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
}

// statusResponse is the JSON body of /_tunnel/status.
type statusResponse struct {
	Domain  string         `json:"domain"`
	Clients int            `json:"clients"` // live sessions
	Tunnels []tunnelStatus `json:"tunnels"` // all known services (online + offline)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	// ?namespace=foo restricts the listing (used by per-user status pages).
	ns := r.URL.Query().Get("namespace")
	live := s.reg.snapshot()
	out := statusResponse{Domain: s.cfg.Domain, Clients: len(live)}
	for _, rec := range s.store.list(ns) {
		st := tunnelStatus{
			Host: rec.Host, Namespace: rec.Namespace, URL: "https://" + rec.Host,
			Label: rec.Label, Online: rec.Online, FirstSeen: rec.FirstSeen, LastSeen: rec.LastSeen,
		}
		if sess, ok := live[rec.Host]; ok {
			st.Online = true
			st.ActiveStreams = sess.activeStreams.Load()
			st.Requests = sess.requests.Load()
		}
		out.Tunnels = append(out.Tunnels, st)
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
