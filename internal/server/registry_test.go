package server

import "testing"

func TestRegistryReserveAttachRelease(t *testing.T) {
	r := newRegistry()
	const host = "app.tunnel.example.com"

	if !r.reserve(host) {
		t.Fatal("first reserve should succeed")
	}
	if r.reserve(host) {
		t.Fatal("second reserve must fail (name held)")
	}
	// A reserved-but-unattached host is not routable.
	if _, ok := r.lookup(host); ok {
		t.Fatal("placeholder must not be returned by lookup")
	}
	if r.count() != 0 {
		t.Fatal("placeholder should not count as a live client")
	}

	sess := &Session{host: host}
	r.attach(host, sess)
	got, ok := r.lookup(host)
	if !ok || got != sess {
		t.Fatal("attached session should be returned by lookup")
	}
	if r.count() != 1 {
		t.Fatalf("count = %d, want 1", r.count())
	}

	// Release by a different session must not evict.
	r.release(host, &Session{host: host})
	if _, ok := r.lookup(host); !ok {
		t.Fatal("release by wrong session must not evict")
	}
	r.release(host, sess)
	if _, ok := r.lookup(host); ok {
		t.Fatal("release by owner should evict")
	}
}
