package server

import (
	"crypto/rand"
	"strings"
	"sync"
)

// registry maps a tunnel hostname (e.g. "myapp.tunnel.example.com") to the live
// client session serving it. A nil value is a reservation placeholder: the name
// is held (so concurrent clients can't both claim it) but no session is attached
// yet. lookup only returns attached (non-nil) sessions. Safe for concurrent use.
type registry struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func newRegistry() *registry {
	return &registry{sessions: map[string]*Session{}}
}

// reserve atomically holds host with a placeholder. Returns false if host is
// already reserved or attached.
func (r *registry) reserve(host string) bool {
	host = strings.ToLower(host)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sessions[host]; exists {
		return false
	}
	r.sessions[host] = nil
	return true
}

// attach binds a live session to a previously reserved host.
func (r *registry) attach(host string, s *Session) {
	host = strings.ToLower(host)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[host] = s
}

// release removes a reservation/session for host only if it currently holds the
// placeholder or s (guards against a reconnecting client evicting its successor).
func (r *registry) release(host string, s *Session) {
	host = strings.ToLower(host)
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.sessions[host]; ok && (cur == nil || cur == s) {
		delete(r.sessions, host)
	}
}

// lookup returns the attached session for host, if any (placeholders excluded).
func (r *registry) lookup(host string) (*Session, bool) {
	host = strings.ToLower(host)
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[host]
	return s, ok && s != nil
}

// snapshot returns a copy of the attached host->session map for status/metrics.
func (r *registry) snapshot() map[string]*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*Session, len(r.sessions))
	for h, s := range r.sessions {
		if s != nil {
			out[h] = s
		}
	}
	return out
}

// count returns the number of attached (live) sessions.
func (r *registry) count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, s := range r.sessions {
		if s != nil {
			n++
		}
	}
	return n
}

// nameAlphabet excludes ambiguous characters (0/o, 1/l/i) for readable slugs.
const nameAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// randomName returns a random DNS-safe slug of length n.
func randomName(n int) string {
	if n < 1 {
		n = 8
	}
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = nameAlphabet[int(b[i])%len(nameAlphabet)]
	}
	return string(b)
}
