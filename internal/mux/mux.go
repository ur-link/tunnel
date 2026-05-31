// Package mux bridges a WebSocket connection to a yamux session.
//
// The client opens a single WebSocket to the server (so L7 proxies like
// Traefik/nginx pass it natively). That WebSocket is wrapped as a net.Conn and
// handed to yamux, which multiplexes one logical stream per inbound public
// request. yamux lets either peer open streams, so the server opens a stream
// per request (Session.Open) and the client accepts them (Session.Accept) —
// there is no per-request connection setup, which is what keeps thousands of
// concurrent and idle-but-open connections cheap.
package mux

import (
	"context"
	"io"
	"net"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// minWindow is yamux's required floor for MaxStreamWindowSize.
const minWindow = 256 * 1024

// WrapConn adapts a *websocket.Conn into a net.Conn suitable for yamux. The
// read limit is disabled because tunneled payloads (uploads, downloads,
// streaming) can be arbitrarily large.
func WrapConn(ctx context.Context, c *websocket.Conn) net.Conn {
	c.SetReadLimit(-1)
	return websocket.NetConn(ctx, c, websocket.MessageBinary)
}

// config returns a yamux config tuned for long-lived tunnels.
func config(keepAlive time.Duration, window int) *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	if keepAlive <= 0 {
		keepAlive = 30 * time.Second
	}
	cfg.KeepAliveInterval = keepAlive
	if window < minWindow {
		window = minWindow
	}
	cfg.MaxStreamWindowSize = uint32(window)
	// yamux's own framing logs are noise for us; surface nothing by default.
	cfg.LogOutput = io.Discard
	return cfg
}

// Server creates the yamux server end of the session (used by tunnel server).
func Server(conn net.Conn, keepAlive time.Duration, window int) (*yamux.Session, error) {
	return yamux.Server(conn, config(keepAlive, window))
}

// Client creates the yamux client end of the session (used by tunnel client).
func Client(conn net.Conn, keepAlive time.Duration, window int) (*yamux.Session, error) {
	return yamux.Client(conn, config(keepAlive, window))
}
