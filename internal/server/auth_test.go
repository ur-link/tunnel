package server

import "testing"

func TestTokenStoreAuthAndReservations(t *testing.T) {
	ts := NewTokenStore("tokA:foo|bar, tokB ,# comment\n tokC")

	infoA, ok := ts.Authenticate("tokA")
	if !ok {
		t.Fatal("tokA should authenticate")
	}
	if _, ok := ts.Authenticate("nope"); ok {
		t.Fatal("unknown token must not authenticate")
	}
	if _, ok := ts.Authenticate(""); ok {
		t.Fatal("empty token must not authenticate")
	}

	infoB, _ := ts.Authenticate("tokB")

	// foo is reserved by A: only A may use it.
	if !ts.NameAllowed(infoA, "foo") {
		t.Error("A should be allowed its reserved name foo")
	}
	if ts.NameAllowed(infoB, "foo") {
		t.Error("B must not use A's reserved name foo")
	}
	// Unreserved names are first-come-first-served.
	if !ts.NameAllowed(infoB, "whatever") {
		t.Error("unreserved name should be allowed")
	}
	if ts.IsEphemeral() {
		t.Error("configured store must not be ephemeral")
	}
}

func TestTokenStoreEphemeral(t *testing.T) {
	ts := NewTokenStore("   ")
	if !ts.IsEphemeral() {
		t.Fatal("empty config should yield an ephemeral token")
	}
	if _, ok := ts.Authenticate(ts.EphemeralTok); !ok {
		t.Fatal("ephemeral token should authenticate")
	}
}
