package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestIdentityCRUDAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	ts := NewTokenStore("")
	if err := ts.SetUsersFile(path); err != nil {
		t.Fatal(err)
	}

	id, err := ts.Create("meabed", "Mohamed", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if info, ok := ts.Authenticate(id.Token); !ok || info.Namespace != "meabed" || info.Role != RoleAdmin {
		t.Fatalf("created identity not authenticatable: %+v ok=%v", info, ok)
	}

	if err := ts.Update(id.Token, "acme", "Mo", RoleUser); err != nil {
		t.Fatal(err)
	}
	if info, _ := ts.Authenticate(id.Token); info.Namespace != "acme" || info.Role != RoleUser {
		t.Fatalf("update not applied: %+v", info)
	}

	rot, err := ts.Rotate(id.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ts.Authenticate(id.Token); ok {
		t.Fatal("old token should be invalid after rotate")
	}
	if _, ok := ts.Authenticate(rot.Token); !ok {
		t.Fatal("rotated token should authenticate")
	}

	// Persisted across a reload.
	ts2 := NewTokenStore("")
	if err := ts2.SetUsersFile(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := ts2.Authenticate(rot.Token); !ok {
		t.Fatal("rotated token should survive reload from disk")
	}

	if err := ts2.Delete(rot.Token); err != nil {
		t.Fatal(err)
	}
	if _, ok := ts2.Authenticate(rot.Token); ok {
		t.Fatal("deleted token should be invalid")
	}
}

func TestCreateRequiresUsersFile(t *testing.T) {
	ts := NewTokenStore("inline") // no users file configured
	if _, err := ts.Create("ns", "l", RoleUser); err == nil {
		t.Fatal("Create without a users file should error")
	}
}

func TestAdminAuthGate(t *testing.T) {
	s := newTestServer("ur.link", "")
	_ = s.tokens.SetUsersFile(filepath.Join(t.TempDir(), "u.json"))
	admin, _ := s.tokens.Create("", "root", RoleAdmin)
	user, _ := s.tokens.Create("meabed", "u", RoleUser)
	h := s.adminMux()

	// No token -> 401.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://admin.ur.link/api/users", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token = %d, want 401", rec.Code)
	}
	// User token -> 401 (needs admin).
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://admin.ur.link/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+user.Token)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("user-token = %d, want 401", rec.Code)
	}
	// Admin token -> 200.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://admin.ur.link/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+admin.Token)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin-token = %d, want 200", rec.Code)
	}
}
