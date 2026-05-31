package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/meabed/tunnel/internal/mux"
	"github.com/meabed/tunnel/internal/proto"
)

// controlMux returns the handler for the control listener.
func (s *Server) controlMux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc(proto.ControlPath, s.handleControl)
	m.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return m
}

// handleControl authenticates a client, upgrades to WebSocket, performs the
// register/response handshake, then wraps the connection in yamux and serves
// the session until it closes.
func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	info, ok := s.tokens.Authenticate(bearerToken(r))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled, // payloads are already framed/binary
	})
	if err != nil {
		s.log.Debug("websocket accept failed", "err", err)
		return
	}
	// ctx bounds the NetConn lifetime; cancelled when the session ends.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer c.CloseNow()

	nc := mux.WrapConn(ctx, c)

	// 1. Read the client's Register (bounded by a handshake deadline).
	_ = nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	var reg proto.Register
	if err := proto.ReadMsg(nc, &reg); err != nil {
		s.log.Debug("read register failed", "err", err)
		return
	}
	_ = nc.SetReadDeadline(time.Time{}) // clear deadline for the long-lived session

	// 2. Reserve a hostname (honors permitted requested name, else random).
	host, ok := s.reserveName(reg.Name, info)
	if !ok {
		_ = proto.WriteMsg(nc, proto.Response{OK: false, Error: "could not assign a subdomain"})
		return
	}
	url := "https://" + host

	// 3. Tell the client it's live, THEN hand the wire to yamux.
	if err := proto.WriteMsg(nc, proto.Response{OK: true, Hostname: host, URL: url}); err != nil {
		s.reg.release(host, nil)
		return
	}

	session, err := mux.Server(nc, s.cfg.YamuxKeepAlive, s.cfg.YamuxWindow)
	if err != nil {
		s.reg.release(host, nil)
		s.log.Warn("yamux server init failed", "host", host, "err", err)
		return
	}

	sess := newSession(session, host, url, reg.HostHeader, info.Label, s.metrics, s.log)
	s.reg.attach(host, sess)
	s.metrics.activeClients.Inc()
	s.log.Info("tunnel registered", "host", host, "label", info.Label, "client_version", reg.ClientVersion)

	// 4. Serve until the session dies, then clean up.
	<-session.CloseChan()
	cancel()
	s.reg.release(host, sess)
	s.metrics.activeClients.Dec()
	s.log.Info("tunnel closed", "host", host)
}

// bearerToken extracts the auth token from the Authorization header
// ("Bearer x" or bare), falling back to the ?token= query parameter.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return r.URL.Query().Get("token")
}
