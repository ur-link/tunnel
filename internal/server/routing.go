package server

import (
	"net/http"
	"strings"

	"github.com/ur-link/tunnel/internal/web"
)

// routeCookie pins a browser to the last service it opened (path routing mode),
// so an app's prefix-less asset/API requests still reach the right service.
const routeCookie = "tn_route"

// reservedSlugs are subdomain/path labels the edge owns; a service can't claim
// them (they'd shadow the admin host or the hub framework paths).
var reservedSlugs = map[string]bool{
	"admin": true, "login": true, "logout": true, "api": true,
	"partials": true, "_static": true, "_tunnel": true, "healthz": true, "connect": true,
}

func reservedSlug(s string) bool { return reservedSlugs[strings.ToLower(s)] }

// splitFirstSegment splits "/seg/rest..." into ("seg", "/rest...").
func splitFirstSegment(p string) (seg, rest string) {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", "/"
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], "/" + p[i+1:]
	}
	return p, "/"
}

// handlePathNamespace serves "<namespace>.<domain>" in path routing mode:
//   - reserved framework paths (login / partials / api / static) → hub UI/API;
//   - "/<slug>/..." where slug is a live service → proxy it (prefix stripped,
//     affinity cookie set);
//   - a prefix-less request with an affinity cookie → the last service (full path);
//   - otherwise the namespace status page (auth-gated).
func (s *Server) handlePathNamespace(w http.ResponseWriter, r *http.Request, ns string) {
	host := ns + "." + s.cfg.Domain

	switch {
	case r.URL.Path == "/_static/htmx.min.js":
		web.Static(w, r)
		return
	case r.Method == http.MethodPost && r.URL.Path == "/login":
		s.hubLogin(w, r, ns)
		return
	case r.URL.Path == "/partials/services":
		if !s.hubAuthed(r, ns) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		web.ServicesPartial(w, r, s.serviceRows(ns), false)
		return
	case r.URL.Path == "/api/services":
		s.handleHub(w, r, ns) // JSON (Bearer/cookie), shared with subdomain mode
		return
	}

	// Service routing by first path segment.
	if seg, rest := splitFirstSegment(r.URL.Path); seg != "" && !reservedSlug(seg) {
		if sess, ok := s.reg.lookup(host + "/" + seg); ok {
			http.SetCookie(w, &http.Cookie{Name: routeCookie, Value: seg, Path: "/", SameSite: http.SameSiteLaxMode})
			r.URL.Path = rest
			r.Header.Set("X-Forwarded-Prefix", "/"+seg)
			sess.ServeHTTP(w, r)
			return
		}
	}

	// Prefix-less / unmatched: follow the affinity cookie, forwarding the full path.
	if c, err := r.Cookie(routeCookie); err == nil && c.Value != "" {
		if sess, ok := s.reg.lookup(host + "/" + c.Value); ok {
			sess.ServeHTTP(w, r)
			return
		}
	}

	// Root or no match → namespace status page (auth-gated).
	if !s.hubAuthed(r, ns) {
		web.Login(w, r, ns, "/login", "")
		return
	}
	web.Hub(w, r, web.HubView{Namespace: ns, Domain: s.cfg.Domain, Services: s.serviceRows(ns)})
}
