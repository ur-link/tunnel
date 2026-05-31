// Package proto defines the tiny handshake spoken over the control connection
// before it is handed to yamux.
//
// Wire framing is deliberately minimal: a 4-byte big-endian length prefix
// followed by a JSON payload. Exactly one Register (client→server) and one
// Ready/Error (server→client) message are exchanged, after which both sides
// wrap the same net.Conn in yamux and the framing below is never used again.
package proto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// ControlPath is the WebSocket endpoint clients connect to on the server's
// control listener.
const ControlPath = "/_tunnel/connect"

// maxFrame caps a handshake frame so a malformed/hostile peer can't make us
// allocate unbounded memory. Handshake payloads are a few hundred bytes.
const maxFrame = 64 * 1024

// Register is sent by the client immediately after the WebSocket is
// established. The auth token travels in the HTTP Authorization header, not
// here, so it never lands in this struct.
type Register struct {
	// Name is the requested subdomain. Empty means "assign me a random one".
	Name string `json:"name"`
	// HostHeader, if set, is the Host header the server should present to the
	// local app (e.g. "localhost:3000"). Empty means forward the public host.
	HostHeader string `json:"host_header,omitempty"`
	// ClientVersion is informational, for logs/metrics.
	ClientVersion string `json:"client_version,omitempty"`
}

// Response is the server's single reply to a Register. On success OK is true
// and Hostname/URL are populated; on failure OK is false and Error explains why.
type Response struct {
	OK       bool   `json:"ok"`
	Hostname string `json:"hostname,omitempty"` // e.g. myapp.tunnel.example.com
	URL      string `json:"url,omitempty"`      // e.g. https://myapp.tunnel.example.com
	Error    string `json:"error,omitempty"`
}

// WriteMsg length-prefixes and writes a JSON message to w.
func WriteMsg(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal handshake: %w", err)
	}
	if len(payload) > maxFrame {
		return fmt.Errorf("handshake frame too large: %d bytes", len(payload))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

// ReadMsg reads one length-prefixed JSON message from r into v.
func ReadMsg(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrame {
		return fmt.Errorf("handshake frame too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, v)
}
