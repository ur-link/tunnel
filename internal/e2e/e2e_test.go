// Package e2e exercises the whole data path in-process: a local app, the tunnel
// server (edge + control), and the tunnel client, proving that plain HTTP, SSE
// streaming, and WebSocket upgrades all round-trip through a yamux stream.
package e2e

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/meabed/tunnel/internal/client"
	"github.com/meabed/tunnel/internal/config"
	"github.com/meabed/tunnel/internal/logging"
	"github.com/meabed/tunnel/internal/server"
)

const (
	testDomain = "test.local"
	testName   = "app"
	testToken  = "testtoken"
)

var testHost = testName + "." + testDomain

func TestTunnelEndToEnd(t *testing.T) {
	echoAddr := startLocalApp(t)
	edgeAddr, ctrlAddr := startServerAndClient(t, echoAddr)

	t.Run("http", func(t *testing.T) {
		body, status := mustGet(t, edgeAddr, "/echo?q=1", testHost)
		if status != 200 {
			t.Fatalf("status = %d", status)
		}
		if !strings.Contains(body, "GET /echo?q=1") {
			t.Fatalf("unexpected echo body: %q", body)
		}
		// The app must see the public host advertised via X-Forwarded-Host.
		if !strings.Contains(body, "x-forwarded-host="+testHost) {
			t.Fatalf("missing/incorrect X-Forwarded-Host in: %q", body)
		}
	})

	t.Run("sse_streaming", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://"+edgeAddr+"/sse", nil)
		req.Host = testHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		sc := bufio.NewScanner(resp.Body)
		var got []string
		deadline := time.Now().Add(5 * time.Second)
		for sc.Scan() && time.Now().Before(deadline) {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "data:") {
				got = append(got, line)
				if len(got) == 3 {
					break
				}
			}
		}
		if len(got) != 3 {
			t.Fatalf("expected 3 SSE events, got %v", got)
		}
	})

	t.Run("websocket_echo", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, _, err := websocket.Dial(ctx, "ws://"+edgeAddr+"/ws", &websocket.DialOptions{
			Host: testHost,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer c.CloseNow()

		if err := c.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
			t.Fatal(err)
		}
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ != websocket.MessageText || string(data) != "ping" {
			t.Fatalf("ws echo = %q (%v), want ping", data, typ)
		}
		c.Close(websocket.StatusNormalClosure, "")
	})

	t.Run("concurrency", func(t *testing.T) {
		const n = 50
		var wg sync.WaitGroup
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				body, status := mustGet(t, edgeAddr, fmt.Sprintf("/echo?i=%d", i), testHost)
				if status != 200 || !strings.Contains(body, fmt.Sprintf("i=%d", i)) {
					errs <- fmt.Errorf("req %d: status %d body %q", i, status, body)
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
	})

	t.Run("unknown_host_502_or_404", func(t *testing.T) {
		_, status := mustGet(t, edgeAddr, "/", "nope."+testDomain)
		if status != http.StatusBadGateway {
			t.Fatalf("unconnected tunnel status = %d, want 502", status)
		}
		_, status = mustGet(t, edgeAddr, "/", "elsewhere.example.org")
		if status != http.StatusNotFound {
			t.Fatalf("foreign host status = %d, want 404", status)
		}
	})

	_ = ctrlAddr
}

// startLocalApp starts the "local service" the client forwards to: an echo
// endpoint, an SSE stream, and a WebSocket echo. Returns its address.
func startLocalApp(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s\n", r.Method, r.URL.RequestURI())
		fmt.Fprintf(w, "host=%s\n", r.Host)
		fmt.Fprintf(w, "x-forwarded-host=%s\n", r.Header.Get("X-Forwarded-Host"))
		fmt.Fprintf(w, "x-forwarded-proto=%s\n", r.Header.Get("X-Forwarded-Proto"))
	})

	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flush", 500)
			return
		}
		for i := 1; i <= 3; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			fl.Flush()
			time.Sleep(30 * time.Millisecond)
		}
	})

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		_ = c.Write(ctx, typ, data)
	})

	srv := &http.Server{Handler: mux}
	ln := mustListen(t)
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// startServerAndClient boots the tunnel server and a client forwarding to
// echoAddr, then waits until the tunnel is routable. Returns (edgeAddr, ctrlAddr).
func startServerAndClient(t *testing.T, echoAddr string) (string, string) {
	t.Helper()
	log := logging.New("error", "text")

	edgeAddr := freeAddr(t)
	ctrlAddr := freeAddr(t)
	metricsAddr := freeAddr(t)

	scfg := &config.Server{
		Domain:            testDomain,
		HTTPAddr:          edgeAddr,
		ControlAddr:       ctrlAddr,
		MetricsAddr:       metricsAddr,
		TLSMode:           "off",
		TokensRaw:         testToken + ":" + testName,
		RandomNameLen:     8,
		YamuxKeepAlive:    30 * time.Second,
		YamuxWindow:       1 << 20,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		LogLevel:          "error",
		LogFormat:         "text",
	}
	sctx, scancel := context.WithCancel(context.Background())
	go func() { _ = server.New(scfg, log).Run(sctx) }()
	t.Cleanup(scancel)
	waitHTTP(t, "http://"+ctrlAddr+"/healthz")

	ccfg := &config.Client{
		Server:     "ws://" + ctrlAddr,
		Token:      testToken,
		Name:       testName,
		Target:     echoAddr,
		MaxBackoff: 2 * time.Second,
		LogLevel:   "error",
		LogFormat:  "text",
	}
	cctx, ccancel := context.WithCancel(context.Background())
	go func() { _ = client.New(ccfg, log).Run(cctx) }()
	t.Cleanup(ccancel)

	// Wait until the edge routes the tunnel (client connected + registered).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, status := mustGet(t, edgeAddr, "/echo", testHost); status == 200 {
			return edgeAddr, ctrlAddr
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("tunnel never became routable")
	return edgeAddr, ctrlAddr
}

func mustGet(t *testing.T, addr, path, host string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.StatusCode
}

func waitHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server at %s never came up", url)
}

func mustListen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// freeAddr returns a likely-free 127.0.0.1 address. There is an inherent race
// between closing and rebinding, acceptable for tests.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
