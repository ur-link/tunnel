package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ur-link/tunnel/internal/web"
)

const (
	adminCookie = "tn_admin"
	hubCookie   = "tn_hub"
)

// tokenFromRequest reads the auth token from a cookie (browser) or the
// Authorization bearer / ?token= (API/CLI).
func tokenFromRequest(r *http.Request, cookie string) string {
	if c, err := r.Cookie(cookie); err == nil && c.Value != "" {
		return c.Value
	}
	return bearerToken(r)
}

func setAuthCookie(w http.ResponseWriter, name, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600,
	})
}

// serviceRows maps persisted records (+ live registry overlay) to view rows.
func (s *Server) serviceRows(namespace string) []web.ServiceRow {
	live := s.reg.snapshot()
	recs := s.store.list(namespace)
	rows := make([]web.ServiceRow, 0, len(recs))
	for _, rec := range recs {
		row := web.ServiceRow{
			Host: rec.Host, URL: rec.URL, Namespace: rec.Namespace, Online: rec.Online,
			LastSeen: humanSince(rec.LastSeen),
		}
		if sess, ok := live[rec.Host]; ok {
			row.Online = true
			row.Requests = sess.requests.Load()
			row.ActiveStreams = sess.activeStreams.Load()
			row.LastSeen = "now"
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *Server) adminView() web.AdminView {
	ids := s.tokens.List()
	users := make([]web.UserRow, 0, len(ids))
	for _, id := range ids {
		users = append(users, web.UserRow{Token: id.Token, Namespace: id.Namespace, Label: id.Label, Role: id.Role})
	}
	return web.AdminView{Domain: s.cfg.Domain, Users: users, Services: s.serviceRows("")}
}

// --- admin web handlers (cookie-authenticated browser UI) ---

func (s *Server) adminAuthed(r *http.Request) bool {
	info, ok := s.tokens.Authenticate(tokenFromRequest(r, adminCookie))
	return ok && info.Role == RoleAdmin
}

func (s *Server) adminHome(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthed(r) {
		web.Login(w, r, "tunnel admin", "/login", "")
		return
	}
	web.AdminConsole(w, r, s.adminView())
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	if info, ok := s.tokens.Authenticate(token); ok && info.Role == RoleAdmin {
		setAuthCookie(w, adminCookie, token)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	web.Login(w, r, "tunnel admin", "/login", "Invalid token, or not an admin token.")
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) adminWebCreate(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthed(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	_, _ = s.tokens.Create(r.FormValue("namespace"), r.FormValue("label"), r.FormValue("role"))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) adminWebRotate(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthed(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	_, _ = s.tokens.Rotate(r.FormValue("token"))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) adminWebDelete(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthed(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	_ = s.tokens.Delete(r.FormValue("token"))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) adminPartialServices(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	web.ServicesPartial(w, r, s.serviceRows(""), true)
}

// --- hub web handlers (per-namespace status page) ---

// hubLogin authenticates a namespace token from the login form and sets the
// hub cookie (used by both subdomain and path routing modes).
func (s *Server) hubLogin(w http.ResponseWriter, r *http.Request, namespace string) {
	token := r.FormValue("token")
	if info, ok := s.tokens.Authenticate(token); ok && (info.Namespace == namespace || info.Role == RoleAdmin) {
		setAuthCookie(w, hubCookie, token)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	web.Login(w, r, namespace, "/login", "Invalid token for this namespace.")
}

func (s *Server) hubAuthed(r *http.Request, namespace string) bool {
	info, ok := s.tokens.Authenticate(tokenFromRequest(r, hubCookie))
	return ok && (info.Namespace == namespace || info.Role == RoleAdmin)
}

// handleHubWeb serves the browser status page + login + live partial for a
// namespace hub. API JSON lives in handleHub (control/automation).
func (s *Server) handleHubWeb(w http.ResponseWriter, r *http.Request, namespace string) {
	switch {
	case r.URL.Path == "/_static/htmx.min.js":
		web.Static(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/login":
		s.hubLogin(w, r, namespace)
	case r.URL.Path == "/partials/services":
		if !s.hubAuthed(r, namespace) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		web.ServicesPartial(w, r, s.serviceRows(namespace), false)
	default:
		if !s.hubAuthed(r, namespace) {
			web.Login(w, r, namespace, "/login", "")
			return
		}
		web.Hub(w, r, web.HubView{Namespace: namespace, Domain: s.cfg.Domain, Services: s.serviceRows(namespace)})
	}
}

// humanSince renders a coarse relative time for the status pages.
func humanSince(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m ago"
	case d < 24*time.Hour:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h ago"
	default:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d ago"
	}
}
