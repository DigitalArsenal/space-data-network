package main

import (
	"net/http"
	"path"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// makeAdminSurfaceHandler serves /admin, the surface reserved for admin and
// auth flows (Agents.md, Product Surfaces).
//
// A page request under /admin goes to the dashboard at "/" (owner
// 2026-11-15: "/admin/: have it serve the same dashboard as /, so you can't
// land on the old one"), so no address opens the older admin UI any more.
// Everything else is unchanged: adminUI (the hosted admin build or the legacy
// inline admin UI) still serves its files and the legacy /admin/api/ routes,
// with the /admin prefix stripped and, with require_auth, behind an Admin
// session.
func makeAdminSurfaceHandler(adminUI http.Handler, requireAuth bool, authHandler *auth.Handler) http.Handler {
	subtree := http.StripPrefix("/admin", adminUI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/admin/") && r.URL.Path != "/admin" {
			http.NotFound(w, r)
			return
		}
		if isAdminPageRequest(r) {
			target := "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		if r.URL.Path == "/admin" {
			http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
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

// isAdminPageRequest reports whether r asks for an admin page: a GET or HEAD
// of /admin, /admin/ or a path whose last segment names no file, which the
// hosted admin UI answered with its index.html. A file (/admin/assets/…,
// /admin/favicon.ico), the legacy admin API (/admin/api/…) and every other
// method are not pages.
func isAdminPageRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	if p == "/admin" || p == "/admin/" {
		return true
	}
	if p == "/admin/api" || strings.HasPrefix(p, "/admin/api/") {
		return false
	}
	return path.Ext(p) == ""
}
