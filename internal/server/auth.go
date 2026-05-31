package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"strings"
)

// TokenInfo describes one authorized client token.
type TokenInfo struct {
	Token    string
	Label    string
	Reserved map[string]bool // subdomains this token is allowed to claim/own
}

// TokenStore authenticates client tokens and enforces reserved-name ownership.
//
// A name reserved by any token may only be used by that token; a name reserved
// by nobody is first-come-first-served. The zero value is not usable — build
// one with NewTokenStore.
type TokenStore struct {
	tokens       map[string]*TokenInfo
	reservedBy   map[string]string // name -> owning token
	ephemeral    bool              // true if we generated a token (none configured)
	EphemeralTok string
}

// NewTokenStore parses the token text (inline comma-separated or file newline-
// separated). Each entry is "token" or "token:name1|name2". Lines starting with
// '#' are comments. If the result is empty, a single ephemeral token is
// generated so the server is usable out of the box (it is logged at startup).
func NewTokenStore(raw string) *TokenStore {
	ts := &TokenStore{
		tokens:     map[string]*TokenInfo{},
		reservedBy: map[string]string{},
	}

	// Accept both commas and newlines as entry separators.
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	for _, entry := range fields {
		entry = strings.TrimSpace(entry)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		tok, names := entry, ""
		if i := strings.IndexByte(entry, ':'); i >= 0 {
			tok, names = strings.TrimSpace(entry[:i]), entry[i+1:]
		}
		if tok == "" {
			continue
		}
		info := &TokenInfo{Token: tok, Reserved: map[string]bool{}}
		for _, n := range strings.Split(names, "|") {
			n = strings.ToLower(strings.TrimSpace(n))
			if n == "" {
				continue
			}
			info.Reserved[n] = true
			ts.reservedBy[n] = tok
		}
		ts.tokens[tok] = info
	}

	if len(ts.tokens) == 0 {
		tok := randomToken()
		ts.tokens[tok] = &TokenInfo{Token: tok, Label: "ephemeral", Reserved: map[string]bool{}}
		ts.ephemeral = true
		ts.EphemeralTok = tok
	}
	return ts
}

// IsEphemeral reports whether the store auto-generated its token.
func (ts *TokenStore) IsEphemeral() bool { return ts.ephemeral }

// Authenticate returns the TokenInfo for a token using a constant-time compare
// against each known token (the set is small; this avoids leaking which prefix
// matched via timing).
func (ts *TokenStore) Authenticate(token string) (*TokenInfo, bool) {
	if token == "" {
		return nil, false
	}
	for _, info := range ts.tokens {
		if subtle.ConstantTimeCompare([]byte(info.Token), []byte(token)) == 1 {
			return info, true
		}
	}
	return nil, false
}

// NameAllowed reports whether info may claim subdomain name. A name reserved by
// another token is denied; an unreserved name is allowed.
func (ts *TokenStore) NameAllowed(info *TokenInfo, name string) bool {
	name = strings.ToLower(name)
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
