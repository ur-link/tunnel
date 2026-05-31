package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Identity is the JSON shape of a managed user in the users file and the
// admin API. The token is omitted from list responses by the caller when needed.
type Identity struct {
	Token     string `json:"token"`
	Namespace string `json:"namespace,omitempty"`
	Label     string `json:"label,omitempty"`
	Role      string `json:"role,omitempty"`
}

// SetUsersFile points the store at a writable JSON identity file and loads it.
// File-backed identities are "managed" (admin-editable) and override inline ones.
func (ts *TokenStore) SetUsersFile(path string) error {
	ts.usersFile = path
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // created on first write
		}
		return fmt.Errorf("read users file: %w", err)
	}
	var ids []Identity
	if err := json.Unmarshal(b, &ids); err != nil {
		return fmt.Errorf("parse users file: %w", err)
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, id := range ids {
		if id.Token == "" {
			continue
		}
		role := id.Role
		if role == "" {
			role = RoleUser
		}
		ts.tokens[id.Token] = &TokenInfo{
			Token: id.Token, Namespace: strings.ToLower(id.Namespace), Label: id.Label,
			Role: role, Reserved: map[string]bool{}, Managed: true,
		}
		ts.ephemeral = false
	}
	return nil
}

// List returns all identities, sorted by namespace then label.
func (ts *TokenStore) List() []Identity {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]Identity, 0, len(ts.tokens))
	for _, info := range ts.tokens {
		out = append(out, Identity{Token: info.Token, Namespace: info.Namespace, Label: info.Label, Role: info.Role})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// Create adds a new managed identity with a freshly-generated token.
func (ts *TokenStore) Create(namespace, label, role string) (Identity, error) {
	if ts.usersFile == "" {
		return Identity{}, fmt.Errorf("admin store not configured: set --users-file / TUNNEL_USERS_FILE")
	}
	if role == "" {
		role = RoleUser
	}
	if role != RoleUser && role != RoleAdmin {
		return Identity{}, fmt.Errorf("invalid role %q (want user|admin)", role)
	}
	tok := randomToken()
	ts.mu.Lock()
	ts.tokens[tok] = &TokenInfo{
		Token: tok, Namespace: strings.ToLower(namespace), Label: label,
		Role: role, Reserved: map[string]bool{}, Managed: true,
	}
	ts.ephemeral = false
	ts.mu.Unlock()
	if err := ts.persistUsers(); err != nil {
		return Identity{}, err
	}
	return Identity{Token: tok, Namespace: strings.ToLower(namespace), Label: label, Role: role}, nil
}

// Update modifies a managed identity's namespace/label/role.
func (ts *TokenStore) Update(token, namespace, label, role string) error {
	ts.mu.Lock()
	info, ok := ts.tokens[token]
	if !ok || !info.Managed {
		ts.mu.Unlock()
		return fmt.Errorf("no managed identity for that token")
	}
	if role != "" {
		if role != RoleUser && role != RoleAdmin {
			ts.mu.Unlock()
			return fmt.Errorf("invalid role %q", role)
		}
		info.Role = role
	}
	info.Namespace, info.Label = strings.ToLower(namespace), label
	ts.mu.Unlock()
	return ts.persistUsers()
}

// Delete removes a managed identity.
func (ts *TokenStore) Delete(token string) error {
	ts.mu.Lock()
	info, ok := ts.tokens[token]
	if !ok || !info.Managed {
		ts.mu.Unlock()
		return fmt.Errorf("no managed identity for that token")
	}
	delete(ts.tokens, token)
	ts.mu.Unlock()
	return ts.persistUsers()
}

// Rotate replaces a managed identity's token, preserving its namespace/label/role.
func (ts *TokenStore) Rotate(token string) (Identity, error) {
	ts.mu.Lock()
	info, ok := ts.tokens[token]
	if !ok || !info.Managed {
		ts.mu.Unlock()
		return Identity{}, fmt.Errorf("no managed identity for that token")
	}
	newTok := randomToken()
	delete(ts.tokens, token)
	info.Token = newTok
	ts.tokens[newTok] = info
	ns, label, role := info.Namespace, info.Label, info.Role
	ts.mu.Unlock()
	if err := ts.persistUsers(); err != nil {
		return Identity{}, err
	}
	return Identity{Token: newTok, Namespace: ns, Label: label, Role: role}, nil
}

// persistUsers atomically writes the managed identities to the users file.
func (ts *TokenStore) persistUsers() error {
	if ts.usersFile == "" {
		return fmt.Errorf("admin store not configured")
	}
	ts.mu.RLock()
	ids := make([]Identity, 0)
	for _, info := range ts.tokens {
		if info.Managed {
			ids = append(ids, Identity{Token: info.Token, Namespace: info.Namespace, Label: info.Label, Role: info.Role})
		}
	}
	ts.mu.RUnlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i].Token < ids[j].Token })

	b, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(ts.usersFile), 0o755)
	tmp := ts.usersFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ts.usersFile)
}

// Namespaces returns the set of distinct namespaces (for hub routing).
func (ts *TokenStore) Namespaces() map[string]bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := map[string]bool{}
	for _, info := range ts.tokens {
		if info.Namespace != "" {
			out[info.Namespace] = true
		}
	}
	return out
}
