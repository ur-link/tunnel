package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// edgeHandler serves public traffic. The leading host label decides routing:
//   - "admin"          -> admin console / API
//   - a known namespace -> that user's hub (status page / API)
//   - anything else     -> a tunnel (registry lookup)
func (s *Server) edgeHandler() http.Handler {
	admin := s.adminMux()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := s.hostToName(r.Host)
		switch {
		case name == "":
			s.writeEdgeIndex(w, http.StatusNotFound, fmt.Sprintf("no tunnel for host %q", r.Host))
		case name == "admin":
			admin.ServeHTTP(w, r)
		case s.tokens.Namespaces()[name]:
			s.handleHub(w, r, name)
		default:
			sess, ok := s.reg.lookup(name + "." + s.cfg.Domain)
			if !ok {
				s.writeEdgeIndex(w, http.StatusBadGateway, fmt.Sprintf("tunnel %q is not connected", name))
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
