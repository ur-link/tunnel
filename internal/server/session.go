package server

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/prometheus/client_golang/prometheus"
)

// Session is one connected client. Every inbound public request for the
// session's hostname is proxied over a fresh yamux stream to the client, which
// relays it to its local app. The reverse proxy itself is the same battle-
// tested net/http/httputil core used by portless-tailscale-proxy — the only
// change is that DialContext opens a yamux stream instead of dialing a TCP port.
type Session struct {
	mux        *yamux.Session
	host       string // public hostname, e.g. myapp.tunnel.example.com
	url        string // public URL, e.g. https://myapp.tunnel.example.com
	hostHeader string // Host presented to the local app ("" => public host)
	label      string // token label, for logs/status
	createdAt  time.Time

	activeStreams atomic.Int64
	requests      atomic.Int64

	proxy   *httputil.ReverseProxy
	metrics *metrics
	log     *slog.Logger
}

// newSession builds a Session and its bound reverse proxy.
func newSession(m *yamux.Session, host, url, hostHeader, label string, mt *metrics, log *slog.Logger) *Session {
	s := &Session{
		mux:        m,
		host:       host,
		url:        url,
		hostHeader: hostHeader,
		label:      label,
		createdAt:  time.Now(),
		metrics:    mt,
		log:        log,
	}

	transport := &http.Transport{
		// Open a yamux stream instead of dialing TCP. addr is ignored: there is
		// exactly one upstream (the client) per session.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			stream, err := s.mux.Open()
			if err != nil {
				return nil, err
			}
			s.activeStreams.Add(1)
			mt.activeStreams.Inc()
			return &countConn{
				Conn: stream,
				in:   mt.bytesIn,
				out:  mt.bytesOut,
				onClose: func() {
					s.activeStreams.Add(-1)
					mt.activeStreams.Dec()
				},
			}, nil
		},
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
		// No HTTP/2 over the raw stream — the local app speaks HTTP/1.1.
		ForceAttemptHTTP2: false,
	}

	s.proxy = &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1, // flush immediately: SSE / streaming / chunked
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = s.host // only used for connection-pool keying
			pr.SetXForwarded()
			// The public edge is always reached over https (standalone ACME, or
			// the upstream proxy terminates TLS), so advertise that to the app.
			pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
			if pr.Out.Header.Get("X-Forwarded-Proto") == "" {
				pr.Out.Header.Set("X-Forwarded-Proto", "https")
			}
			if s.hostHeader != "" {
				pr.Out.Host = s.hostHeader
			} else {
				pr.Out.Host = pr.In.Host
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.log.Warn("upstream error", "host", s.host, "path", r.URL.Path, "err", err)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "tunnel: upstream error: %v\n", err)
		},
	}
	return s
}

// ServeHTTP proxies one request to the client, recording status and timing.
func (s *Session) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.proxy.ServeHTTP(rec, r)
	s.metrics.observeStatus(rec.status)
	s.log.Info("request",
		"host", s.host,
		"method", r.Method,
		"path", r.URL.Path,
		"status", rec.status,
		"dur", time.Since(start).Round(time.Millisecond).String(),
	)
}

// Close tears down the underlying yamux session.
func (s *Session) Close() error { return s.mux.Close() }

// statusRecorder captures the response status while preserving streaming
// (Flush) and WebSocket (Hijack) support — both essential for long-lived
// connections through the proxy.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hj.Hijack()
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// countConn counts bytes proxied to/from the local app and runs onClose exactly
// once when the stream closes (to keep the active-stream gauge accurate).
type countConn struct {
	net.Conn
	in      prometheus.Counter
	out     prometheus.Counter
	onClose func()
	closed  atomic.Bool
}

func (c *countConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.in.Add(float64(n))
	}
	return n, err
}

func (c *countConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.out.Add(float64(n))
	}
	return n, err
}

func (c *countConn) Close() error {
	if c.closed.CompareAndSwap(false, true) && c.onClose != nil {
		c.onClose()
	}
	return c.Conn.Close()
}
