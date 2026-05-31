package server

import (
	"path/filepath"
	"strings"
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
	res, ok := s.reserveName("web", info)
	if !ok || res.Host != "web-meabed.ur.link" || res.Slug != "web" {
		t.Fatalf("got %+v ok=%v, want web-meabed.ur.link/web", res, ok)
	}
	// Same name again collides -> random slug, still in the namespace.
	res2, ok := s.reserveName("web", info)
	if !ok || res2.Host == res.Host || !strings.HasSuffix(res2.Host, "-meabed.ur.link") {
		t.Fatalf("expected a distinct namespaced host, got %q", res2.Host)
	}
}

func TestReserveNameLegacyFlat(t *testing.T) {
	s := newTestServer("ur.link", "plain")
	info, _ := s.tokens.Authenticate("plain")
	res, ok := s.reserveName("site", info)
	if !ok || res.Host != "site.ur.link" || res.Slug != "site" {
		t.Fatalf("got %+v, want site.ur.link/site", res)
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
	s1.markOnline("web-meabed.ur.link", "meabed", "web", "web-meabed.ur.link", "https://web-meabed.ur.link", "Web")
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

func TestWildcardFor(t *testing.T) {
	flat := &dnsCertMgr{domain: "ur.link", nested: false}
	if got := flat.wildcardFor("web-meabed.ur.link"); got != "*.ur.link" {
		t.Errorf("flat wildcard = %q, want *.ur.link", got)
	}
	nested := &dnsCertMgr{domain: "ur.link", nested: true}
	if got := nested.wildcardFor("web.meabed.ur.link"); got != "*.meabed.ur.link" {
		t.Errorf("nested wildcard = %q, want *.meabed.ur.link", got)
	}
	if got := nested.wildcardFor("meabed.ur.link"); got != "*.ur.link" {
		t.Errorf("hub wildcard = %q, want *.ur.link", got)
	}
}

func TestReserveNameNested(t *testing.T) {
	s := newTestServer("ur.link", "tok@meabed")
	s.cfg.NestedSubdomains = true
	info, _ := s.tokens.Authenticate("tok")
	res, ok := s.reserveName("web", info)
	if !ok || res.Host != "web.meabed.ur.link" || res.Slug != "web" {
		t.Fatalf("nested reserve = %+v, want web.meabed.ur.link/web", res)
	}
}

func TestReserveNamePathMode(t *testing.T) {
	s := newTestServer("ur.link", "tok@meabed")
	s.cfg.RoutingMode = "path"
	info, _ := s.tokens.Authenticate("tok")
	res, ok := s.reserveName("web", info)
	if !ok || res.Host != "meabed.ur.link" || res.Key != "meabed.ur.link/web" || res.URL != "https://meabed.ur.link/web/" {
		t.Fatalf("path reserve = %+v, want host meabed.ur.link key meabed.ur.link/web", res)
	}
}
