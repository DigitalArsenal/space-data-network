package main

// SpaceAware UI serving (SDN_SPACEAWARE_UI_LOOP.md, packaging hard rule
// 2026-07-06): the SpaceAware UI ships INSIDE this binary as a single
// self-contained HTML artifact (all JS/CSS/fonts inlined) and is served from
// memory — never from a dist folder on disk. The artifact is produced by
// sdn-js `npm run build:spaceaware` (Vite build + single-file inliner,
// scripts/build-spaceaware-single-file.mjs) and committed at
// embedded/spaceaware_app.html.
//
// The route set below must stay in sync with
// sdn-js/ui/src/spaceaware/router.ts (SPACEAWARE_ROUTES). Note on /login:
// when HD-wallet auth is enabled, the auth handler registers an exact
// "/login" mux pattern which takes precedence over the "/" frontend surface,
// so the existing wallet login page keeps winning until loop task U1.2
// deliberately replaces it. On nodes without auth routes, /login serves the
// SpaceAware UI.

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed embedded/spaceaware_app.html
var spaceAwareAppHTML []byte

// isSpaceAwareUIPath reports whether the request path belongs to the
// SpaceAware UI route skeleton (U0.1): /login, /console(/{node,peers,groups,
// data,channels,conjunction}), /orbital, /gantt, /bmc2(/f1…f6).
func isSpaceAwareUIPath(requestPath string) bool {
	p := strings.TrimSuffix(requestPath, "/")
	if p == "" {
		return false
	}
	switch p {
	case "/login", "/console", "/orbital", "/gantt", "/bmc2":
		return true
	}
	if view, ok := strings.CutPrefix(p, "/console/"); ok {
		switch view {
		case "node", "peers", "groups", "data", "channels", "conjunction":
			return true
		}
		return false
	}
	if mode, ok := strings.CutPrefix(p, "/bmc2/"); ok {
		switch mode {
		case "f1", "f2", "f3", "f4", "f5", "f6":
			return true
		}
		return false
	}
	return false
}

// serveSpaceAwareUI serves the embedded single-file SpaceAware artifact from
// memory with the same runtime-config injection and cross-origin-isolation
// headers as the disk-backed frontend handler (COOP/COEP are required for
// SharedArrayBuffer → OrbPro SGP4 on /orbital and /gantt).
func serveSpaceAwareUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(injectFrontendConfig(spaceAwareAppHTML))
	}
}
