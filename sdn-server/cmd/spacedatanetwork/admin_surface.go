package main

import (
	"net/http"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// makeAdminSurfaceHandler serves /admin, the surface reserved for admin and
// auth flows (Agents.md, Product Surfaces). adminUI is the hosted admin build
// or the legacy inline admin UI, mounted with the /admin prefix stripped; with
// require_auth every request needs an Admin session.
func makeAdminSurfaceHandler(adminUI http.Handler, requireAuth bool, authHandler *auth.Handler) http.Handler {
	subtree := http.StripPrefix("/admin", adminUI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin" {
			http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/admin/") {
			http.NotFound(w, r)
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
