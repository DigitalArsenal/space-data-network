package main

import (
	"net/http"
	"path"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/adminui"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// makeAdminSurfaceHandler serves /admin, the surface reserved for admin and
// auth flows (Agents.md, Product Surfaces).
//
// adminUI (the hosted admin build or the legacy inline admin UI) is reached
// only for a path isResource accepts, with the /admin prefix stripped and,
// with require_auth, behind an Admin session. isResource names what that UI
// serves that is not one of its pages: hostedAdminResource or
// (*peers.AdminUI).IsAPIPath. Any other GET or HEAD goes to the dashboard at
// "/" (owner 2026-11-15: "/admin/: have it serve the same dashboard as /, so
// you can't land on the old one"), and any other request is not found, so no
// address or method opens the older admin UI.
func makeAdminSurfaceHandler(adminUI http.Handler, isResource func(urlPath string) bool, requireAuth bool, authHandler *auth.Handler) http.Handler {
	subtree := http.StripPrefix("/admin", adminUI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/admin/") && r.URL.Path != "/admin" {
			http.NotFound(w, r)
			return
		}
		if !isResource(strings.TrimPrefix(r.URL.Path, "/admin")) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.NotFound(w, r)
				return
			}
			target := "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		if !requireAuth {
			subtree.ServeHTTP(w, r)
			return
		}
		if authHandler == nil {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		authHandler.RequireAuth(peers.Admin, subtree.ServeHTTP)(w, r)
	})
}

// hostedAdminResource returns what the hosted admin build serves that is not a
// page: a file of the build other than an HTML document. For a path naming no
// file the host answered its index.html (or a 404, if the path had a file
// extension).
func hostedAdminResource(host *adminui.Host) func(urlPath string) bool {
	return func(urlPath string) bool {
		switch strings.ToLower(path.Ext(path.Clean("/" + urlPath))) {
		case ".html", ".htm":
			return false
		}
		return host.IsFile(urlPath)
	}
}
