package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// edgeHandler serves public traffic: it maps the request Host to a tunnel and
// proxies it to the owning client session.
func (s *Server) edgeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := s.hostToName(r.Host)
		if name == "" {
			s.writeEdgeIndex(w, http.StatusNotFound, fmt.Sprintf("no tunnel for host %q", r.Host))
			return
		}
		sess, ok := s.reg.lookup(name + "." + s.cfg.Domain)
		if !ok {
			s.writeEdgeIndex(w, http.StatusBadGateway, fmt.Sprintf("tunnel %q is not connected", name))
			return
		}
		sess.ServeHTTP(w, r)
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
	Host          string `json:"host"`
	URL           string `json:"url"`
	Label         string `json:"label,omitempty"`
	ActiveStreams int64  `json:"active_streams"`
	Requests      int64  `json:"requests"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// statusResponse is the JSON body of /_tunnel/status.
type statusResponse struct {
	Domain  string         `json:"domain"`
	Clients int            `json:"clients"`
	Tunnels []tunnelStatus `json:"tunnels"`
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := s.reg.snapshot()
	out := statusResponse{Domain: s.cfg.Domain, Clients: len(snap)}
	for _, sess := range snap {
		out.Tunnels = append(out.Tunnels, tunnelStatus{
			Host:          sess.host,
			URL:           sess.url,
			Label:         sess.label,
			ActiveStreams: sess.activeStreams.Load(),
			Requests:      sess.requests.Load(),
			UptimeSeconds: int64(time.Since(sess.createdAt).Seconds()),
		})
	}
	sort.Slice(out.Tunnels, func(i, j int) bool { return out.Tunnels[i].Host < out.Tunnels[j].Host })

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
