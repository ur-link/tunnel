package server

import (
	"path/filepath"
	"testing"

	"github.com/ur-link/tunnel/internal/config"
)

func newTestServer(domain, tokens string) *Server {
	return &Server{
		cfg:    &config.Server{Domain: domain, RandomNameLen: 8},
		reg:    newRegistry(),
		tokens: NewTokenStore(tokens),
		store:  newServiceStore(""),
	}
}

func TestReserveNameNamespaced(t *testing.T) {
	s := newTestServer("ur.link", "tok@meabed")
	info, _ := s.tokens.Authenticate("tok")
	if info.Namespace != "meabed" {
		t.Fatalf("namespace = %q, want meabed", info.Namespace)
	}
	host, slug, ok := s.reserveName("web", info)
	if !ok || host != "web-meabed.ur.link" || slug != "web" {
		t.Fatalf("got host=%q slug=%q ok=%v, want web-meabed.ur.link/web", host, slug, ok)
	}
	// Same name again collides -> random slug, still in the namespace.
	host2, _, ok := s.reserveName("web", info)
	if !ok || host2 == host || filepath.Ext(host2) == "" {
		t.Fatalf("expected a distinct namespaced host, got %q", host2)
	}
	if got := host2[len(host2)-len("-meabed.ur.link"):]; got != "-meabed.ur.link" {
		t.Fatalf("random fallback %q not in namespace", host2)
	}
}

func TestReserveNameLegacyFlat(t *testing.T) {
	s := newTestServer("ur.link", "plain")
	info, _ := s.tokens.Authenticate("plain")
	host, slug, ok := s.reserveName("api", info)
	if !ok || host != "api.ur.link" || slug != "api" {
		t.Fatalf("got host=%q slug=%q, want api.ur.link/api", host, slug)
	}
}

func TestTokenNamespaceParse(t *testing.T) {
	ts := NewTokenStore("tok@meabed:web|api")
	info, ok := ts.Authenticate("tok")
	if !ok || info.Namespace != "meabed" || !info.Reserved["web"] || !info.Reserved["api"] {
		t.Fatalf("parse failed: %+v ok=%v", info, ok)
	}
}

func TestTokenStoreReload(t *testing.T) {
	ts := NewTokenStore("a@ns1")
	if _, ok := ts.Authenticate("a"); !ok {
		t.Fatal("a should authenticate before reload")
	}
	if !ts.Reload("b@ns2, c") {
		t.Fatal("reload should report a change")
	}
	if _, ok := ts.Authenticate("a"); ok {
		t.Fatal("a should be gone after reload")
	}
	info, ok := ts.Authenticate("b")
	if !ok || info.Namespace != "ns2" {
		t.Fatalf("b should authenticate with ns2, got %+v", info)
	}
	// Empty/partial read must not wipe the identity set.
	if ts.Reload("   ") {
		t.Fatal("empty reload should be ignored")
	}
	if _, ok := ts.Authenticate("b"); !ok {
		t.Fatal("b should survive an ignored empty reload")
	}
}

func TestServiceStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s1 := newServiceStore(path)
	s1.markOnline("meabed", "web", "web-meabed.ur.link", "Web")
	if recs := s1.list("meabed"); len(recs) != 1 || !recs[0].Online {
		t.Fatalf("expected 1 online record, got %+v", recs)
	}
	s1.markOffline("web-meabed.ur.link")

	// A fresh store loads persisted records as offline.
	s2 := newServiceStore(path)
	recs := s2.list("")
	if len(recs) != 1 || recs[0].Online || recs[0].Host != "web-meabed.ur.link" {
		t.Fatalf("reload mismatch: %+v", recs)
	}
	// Namespace filter excludes other namespaces.
	if got := s2.list("other"); len(got) != 0 {
		t.Fatalf("namespace filter leaked: %+v", got)
	}
}
