// Package web renders the tunnel admin console and per-namespace status pages
// with templ + HTMX. Assets are embedded so the binary stays single-file. The
// package defines its own view-model structs (UserRow/ServiceRow/...) so the
// server package can map onto them without an import cycle.
package web

import (
	_ "embed"
	"net/http"
	"strconv"
)

//go:embed static/app.css
var appCSS string

//go:embed static/htmx.min.js
var htmxJS []byte

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Static serves embedded assets under /_static/.
func Static(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/_static/htmx.min.js" {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(htmxJS)
		return
	}
	http.NotFound(w, r)
}

// AdminConsole renders the admin console page.
func AdminConsole(w http.ResponseWriter, r *http.Request, v AdminView) {
	_ = adminConsole(v).Render(r.Context(), w)
}

// Hub renders a namespace's status page.
func Hub(w http.ResponseWriter, r *http.Request, v HubView) {
	_ = hubPage(v).Render(r.Context(), w)
}

// Login renders the token sign-in page.
func Login(w http.ResponseWriter, r *http.Request, title, action, errMsg string) {
	_ = loginPage(title, action, errMsg).Render(r.Context(), w)
}

// ServicesPartial renders just the services table (HTMX live-refresh target).
func ServicesPartial(w http.ResponseWriter, r *http.Request, services []ServiceRow, showNS bool) {
	_ = servicesTable(services, showNS).Render(r.Context(), w)
}
