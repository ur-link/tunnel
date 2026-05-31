package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"strings"
	"sync"
)

// Role of an identity.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// TokenInfo describes one authorized client token / identity.
type TokenInfo struct {
	Token     string
	Label     string
	Namespace string          // user namespace; services become <slug>-<namespace>
	Role      string          // RoleUser | RoleAdmin
	Reserved  map[string]bool // explicit reserved subdomains (legacy / non-namespaced)
	Managed   bool            // true if backed by the writable users file (admin-editable)
}

// TokenStore authenticates tokens, resolves their namespace, and enforces
// reserved-name ownership. Identities come from two sources, merged: the inline
// TokensRaw string (legacy/bootstrap) and an optional structured users file
// (which can also set role). Build one with NewTokenStore.
type TokenStore struct {
	mu           sync.RWMutex
	tokens       map[string]*TokenInfo
	reservedBy   map[string]string // name -> owning token (non-namespaced reservations)
	usersFile    string            // writable JSON identity store (admin CRUD target)
	ephemeral    bool
	EphemeralTok string
}

// NewTokenStore parses the inline token text. Entry grammar (comma/newline
// separated, '#' comments):
//
//	token[@namespace][:reserved1|reserved2]
//
// e.g. "abc@meabed", "xyz@acme:web|api", or legacy "tok:foo|bar".
// If the store ends up empty, an ephemeral admin token is generated so the
// service is reachable out of the box (logged at startup).
func NewTokenStore(raw string) *TokenStore {
	ts := &TokenStore{
		tokens:     map[string]*TokenInfo{},
		reservedBy: map[string]string{},
	}

	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	for _, entry := range fields {
		entry = strings.TrimSpace(entry)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		info := parseInlineToken(entry)
		if info == nil {
			continue
		}
		ts.addLocked(info)
	}

	if len(ts.tokens) == 0 {
		tok := randomToken()
		ts.addLocked(&TokenInfo{Token: tok, Label: "ephemeral", Role: RoleAdmin, Reserved: map[string]bool{}})
		ts.ephemeral = true
		ts.EphemeralTok = tok
	}
	return ts
}

// parseInlineToken parses "token[@namespace][:reserved1|reserved2]".
func parseInlineToken(entry string) *TokenInfo {
	tokenPart, names := entry, ""
	if i := strings.IndexByte(entry, ':'); i >= 0 {
		tokenPart, names = strings.TrimSpace(entry[:i]), entry[i+1:]
	}
	tok, ns := tokenPart, ""
	if i := strings.IndexByte(tokenPart, '@'); i >= 0 {
		tok, ns = strings.TrimSpace(tokenPart[:i]), strings.ToLower(strings.TrimSpace(tokenPart[i+1:]))
	}
	if tok == "" {
		return nil
	}
	info := &TokenInfo{Token: tok, Namespace: ns, Role: RoleUser, Reserved: map[string]bool{}}
	for _, n := range strings.Split(names, "|") {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			info.Reserved[n] = true
		}
	}
	return info
}

// addLocked inserts an identity and indexes its non-namespaced reservations.
// Caller need not hold the lock during construction (NewTokenStore is single-
// threaded); the mutex guards later CRUD.
func (ts *TokenStore) addLocked(info *TokenInfo) {
	if info.Reserved == nil {
		info.Reserved = map[string]bool{}
	}
	if info.Role == "" {
		info.Role = RoleUser
	}
	ts.tokens[info.Token] = info
	for name := range info.Reserved {
		ts.reservedBy[name] = info.Token
	}
}

// IsEphemeral reports whether the store auto-generated its token.
func (ts *TokenStore) IsEphemeral() bool { return ts.ephemeral }

// Reload atomically replaces the identity set from freshly-read inline token
// text. Used by the file watcher for hot-reload: it swaps the maps under the
// write lock, so in-flight Authenticate calls see a consistent view and — since
// active tunnels live in the registry, not here — no connections are dropped.
// An empty parse result is ignored (keeps the current set) to avoid locking
// everyone out on a truncated/partial write.
func (ts *TokenStore) Reload(raw string) (changed bool) {
	next := map[string]*TokenInfo{}
	reserved := map[string]string{}
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	for _, entry := range fields {
		entry = strings.TrimSpace(entry)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		if info := parseInlineToken(entry); info != nil {
			if info.Role == "" {
				info.Role = RoleUser
			}
			next[info.Token] = info
			for name := range info.Reserved {
				reserved[name] = info.Token
			}
		}
	}
	if len(next) == 0 {
		return false // refuse to wipe the identity set on an empty/partial read
	}
	ts.mu.Lock()
	ts.tokens, ts.reservedBy, ts.ephemeral, ts.EphemeralTok = next, reserved, false, ""
	ts.mu.Unlock()
	return true
}

// Authenticate returns the identity for a token using a constant-time compare.
func (ts *TokenStore) Authenticate(token string) (*TokenInfo, bool) {
	if token == "" {
		return nil, false
	}
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	for _, info := range ts.tokens {
		if subtle.ConstantTimeCompare([]byte(info.Token), []byte(token)) == 1 {
			return info, true
		}
	}
	return nil, false
}

// NameAllowed reports whether info may claim subdomain label name (used for
// non-namespaced, explicitly-requested names). A name reserved by another token
// is denied; an unreserved name is allowed. Namespaced tokens are always allowed
// their own names because the namespace suffix already scopes ownership.
func (ts *TokenStore) NameAllowed(info *TokenInfo, name string) bool {
	if info.Namespace != "" {
		return true
	}
	name = strings.ToLower(name)
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	owner, reserved := ts.reservedBy[name]
	if !reserved {
		return true
	}
	return owner == info.Token
}

// randomToken returns a 160-bit base32 token (no padding, lowercase).
func randomToken() string {
	var b [20]byte
	_, _ = rand.Read(b[:])
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}
