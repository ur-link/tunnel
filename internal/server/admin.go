package server

import (
	"encoding/json"
	"net/http"
)

// writeJSON writes v as an indented JSON response with the given status.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// adminMux serves the admin API (identity CRUD), gated to admin-role tokens.
// Mounted on the edge at host admin.<domain>.
func (s *Server) adminMux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/users", s.adminListUsers)
	m.HandleFunc("POST /api/users", s.adminCreateUser)
	m.HandleFunc("PATCH /api/users/{token}", s.adminUpdateUser)
	m.HandleFunc("DELETE /api/users/{token}", s.adminDeleteUser)
	m.HandleFunc("POST /api/users/{token}/rotate", s.adminRotateUser)
	m.HandleFunc("GET /api/services", s.adminListServices)
	m.HandleFunc("/", s.adminIndex)
	return s.requireRole(RoleAdmin, m)
}

// requireRole wraps a handler with Bearer-token auth requiring the given role.
func (s *Server) requireRole(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info, ok := s.tokens.Authenticate(bearerToken(r))
		if !ok || (role == RoleAdmin && info.Role != RoleAdmin) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) adminListUsers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"users": s.tokens.List()})
}

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	var body Identity
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	id, err := s.tokens.Create(body.Namespace, body.Label, body.Role)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.log.Info("admin: user created", "namespace", id.Namespace, "role", id.Role)
	writeJSON(w, http.StatusCreated, id)
}

func (s *Server) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	var body Identity
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if err := s.tokens.Update(r.PathValue("token"), body.Namespace, body.Label, body.Role); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	if err := s.tokens.Delete(r.PathValue("token")); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) adminRotateUser(w http.ResponseWriter, r *http.Request) {
	id, err := s.tokens.Rotate(r.PathValue("token"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, id)
}

func (s *Server) adminListServices(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"services": s.store.list("")})
}

// adminIndex is a placeholder until the templ admin console (Phase 3) lands.
func (s *Server) adminIndex(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"service": "tunnel admin", "domain": s.cfg.Domain,
		"hint": "API: /api/users, /api/services (admin Bearer token). Web console: coming soon.",
	})
}

// handleHub serves a user's namespace hub at <namespace>.<domain>: the status
// API now, the templ status page later. Auth: the namespace's own token (or an
// admin token), via Bearer or ?token=.
func (s *Server) handleHub(w http.ResponseWriter, r *http.Request, namespace string) {
	info, ok := s.tokens.Authenticate(bearerToken(r))
	if !ok || (info.Namespace != namespace && info.Role != RoleAdmin) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized for namespace " + namespace})
		return
	}
	switch r.URL.Path {
	case "/api/services":
		writeJSON(w, http.StatusOK, map[string]any{
			"namespace": namespace, "services": s.store.list(namespace),
		})
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"namespace": namespace, "services": s.store.list(namespace),
			"hint": "status page UI coming soon",
		})
	}
}
