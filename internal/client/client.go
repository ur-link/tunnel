// Package client implements the tunnel client: it maintains a single persistent
// WebSocket to the server, accepts multiplexed yamux streams (one per inbound
// public request), and relays each to the configured local address.
package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/ur-link/tunnel/internal/config"
	"github.com/ur-link/tunnel/internal/mux"
	"github.com/ur-link/tunnel/internal/proto"
)

// Version is stamped into the Register message for server-side logging.
var Version = "dev"

// Client relays public requests to a local address over a tunnel.
type Client struct {
	cfg *config.Client
	log *slog.Logger
	// bufPool supplies 32 KiB copy buffers for the bidirectional relay.
	bufPool sync.Pool
}

// New constructs a client.
func New(cfg *config.Client, log *slog.Logger) *Client {
	c := &Client{cfg: cfg, log: log}
	c.bufPool.New = func() any { b := make([]byte, 32*1024); return &b }
	return c
}

// Run connects and serves until ctx is cancelled, reconnecting with exponential
// backoff. Unrecoverable errors (bad auth) stop the loop.
func (c *Client) Run(ctx context.Context) error {
	const baseBackoff = 500 * time.Millisecond
	backoff := baseBackoff

	for {
		err := c.connectAndServe(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var fatal *fatalError
		if errors.As(err, &fatal) {
			return fatal
		}
		c.log.Warn("disconnected; reconnecting", "err", err, "in", backoff.String())

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > c.cfg.MaxBackoff {
			backoff = c.cfg.MaxBackoff
		}
	}
}

// fatalError marks an error that should stop reconnection attempts.
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }

// connectAndServe runs one connection: dial, handshake, then serve streams
// until the session closes.
func (c *Client) connectAndServe(ctx context.Context) error {
	url := strings.TrimRight(c.cfg.Server, "/") + proto.ControlPath

	opts := &websocket.DialOptions{HTTPHeader: http.Header{}}
	opts.HTTPHeader.Set("Authorization", "Bearer "+c.cfg.Token)
	if c.cfg.Insecure {
		opts.HTTPClient = &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // opt-in --insecure
		}}
	}

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	wsConn, resp, err := websocket.Dial(dialCtx, url, opts)
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return &fatalError{fmt.Errorf("authentication failed (401): check --token")}
		}
		return fmt.Errorf("dial %s: %w", url, err)
	}
	defer wsConn.CloseNow()

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()
	nc := mux.WrapConn(connCtx, wsConn)

	// Handshake: send Register, read Response.
	reg := proto.Register{Name: c.cfg.Name, HostHeader: c.cfg.HostHeader, ClientVersion: Version}
	_ = nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := proto.WriteMsg(nc, reg); err != nil {
		return fmt.Errorf("send register: %w", err)
	}
	_ = nc.SetWriteDeadline(time.Time{})

	_ = nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	var resp2 proto.Response
	if err := proto.ReadMsg(nc, &resp2); err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	_ = nc.SetReadDeadline(time.Time{})
	if !resp2.OK {
		return &fatalError{fmt.Errorf("server rejected tunnel: %s", resp2.Error)}
	}

	session, err := mux.Client(nc, 30*time.Second, 1<<20)
	if err != nil {
		return fmt.Errorf("yamux client: %w", err)
	}
	defer session.Close()

	c.log.Info("tunnel established",
		"url", resp2.URL, "forwarding_to", c.cfg.Target)
	fmt.Printf("\n  ➜  %s  →  %s\n\n", resp2.URL, c.cfg.Target)

	// Accept streams until the session dies.
	for {
		stream, err := session.Accept()
		if err != nil {
			return fmt.Errorf("session closed: %w", err)
		}
		go c.handleStream(stream)
	}
}

// handleStream relays one yamux stream to a fresh connection to the local app.
func (c *Client) handleStream(stream net.Conn) {
	defer stream.Close()

	local, err := net.Dial("tcp", c.cfg.Target)
	if err != nil {
		c.log.Warn("local dial failed", "target", c.cfg.Target, "err", err)
		return
	}
	defer local.Close()

	// Pipe both directions; when either side finishes, tearing down both
	// connections unblocks the other copy.
	var once sync.Once
	stop := func() { stream.Close(); local.Close() }

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.copy(local, stream); once.Do(stop) }()
	go func() { defer wg.Done(); c.copy(stream, local); once.Do(stop) }()
	wg.Wait()
}

// copy relays from src to dst using a pooled buffer.
func (c *Client) copy(dst io.Writer, src io.Reader) {
	bufp := c.bufPool.Get().(*[]byte)
	defer c.bufPool.Put(bufp)
	_, _ = io.CopyBuffer(dst, src, *bufp)
}
