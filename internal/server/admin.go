package server

import (
	"encoding/json"
	"net/http"

	"github.com/ur-link/tunnel/internal/web"
)

// staticHandler serves embedded UI assets (/_static/...).
func staticHandler(w http.ResponseWriter, r *http.Request) { web.Static(w, r) }

// writeJSON writes v as an indented JSON response with the given status.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// adminMux serves host admin.<domain>: the web console (cookie auth) plus a
// JSON API under /api (admin-role Bearer or admin cookie).
func (s *Server) adminMux() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/users", s.adminListUsers)
	api.HandleFunc("POST /api/users", s.adminCreateUser)
	api.HandleFunc("PATCH /api/users/{token}", s.adminUpdateUser)
	api.HandleFunc("DELETE /api/users/{token}", s.adminDeleteUser)
	api.HandleFunc("POST /api/users/{token}/rotate", s.adminRotateUser)
	api.HandleFunc("GET /api/services", s.adminListServices)

	m := http.NewServeMux()
	m.Handle("/api/", s.requireRole(RoleAdmin, api))
	m.HandleFunc("/_static/", staticHandler)
	m.HandleFunc("POST /login", s.adminLogin)
	m.HandleFunc("POST /logout", s.adminLogout)
	m.HandleFunc("POST /users", s.adminWebCreate)
	m.HandleFunc("POST /users/rotate", s.adminWebRotate)
	m.HandleFunc("POST /users/delete", s.adminWebDelete)
	m.HandleFunc("GET /partials/services", s.adminPartialServices)
	m.HandleFunc("GET /{$}", s.adminHome)
	return m
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

// handleHub serves a user's namespace hub at <namespace>.<domain>. The JSON API
// (/api/services, Bearer or hub cookie) is for CLI/automation; everything else
// is the browser status page (cookie-authenticated) in handleHubWeb.
func (s *Server) handleHub(w http.ResponseWriter, r *http.Request, namespace string) {
	if r.URL.Path == "/api/services" {
		info, ok := s.tokens.Authenticate(tokenFromRequest(r, hubCookie))
		if !ok || (info.Namespace != namespace && info.Role != RoleAdmin) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized for namespace " + namespace})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"namespace": namespace, "services": s.store.list(namespace)})
		return
	}
	s.handleHubWeb(w, r, namespace)
}
