package main

// Conjunction-only UI serving (SDN_SPACEAWARE_UI_LOOP.md Phase C, task C2 —
// OWNER DIRECTIVE 2026-07-11 "ship with the conjunction app ONLY").
//
// The daemon ships the standalone CONJUNCTION app as THE user interface. It is
// a single self-contained HTML artifact (all JS/CSS/fonts inlined, NO hd-wallet
// wasm — C1) produced by sdn-js `npm run build:conjunction`
// (scripts/build-conjunction-single-file.mjs) and committed at
// embedded/conjunction_app.html (sibling of the full-app spaceaware_app.html,
// which stays committed and dormant — the descoped SpaceAware screens are NOT
// deleted, they simply stop being served in the shipped configuration).
//
// Which UI the daemon serves is chosen by resolveUIMode() (SDN_UI_MODE env
// var, default = conjunction). This keeps the dev workflow for the full
// SpaceAware app working (SDN_UI_MODE=spaceaware) without deleting its build
// target, while the SHIPPED default serves conjunction-only:
//
//   - conjunction (default): the conjunction app is served at the primary UI
//     route "/"; the descoped SpaceAware screens (/console/*, /orbital,
//     /gantt, /bmc2/*, and the SpaceAware /login screen) return 404 — code
//     stays, serving stops. Deep links use the ?group= query string, which is
//     preserved on "/", so no path-based routes are needed.
//   - spaceaware: the full SpaceAware app (login/console/orbital/gantt/bmc2)
//     is served at its route skeleton exactly as it was through Phase U — for
//     local development and future re-enablement only.
//
// The conjunction app's data sources (/api/v1/peers, /api/v1/channels,
// /api/v1/stats, /api/v1/data/health) are all anonymous-safe public read
// endpoints (isPublicReadAPIPath), so no session flow ships with this UI; the
// admin API wall (adminSecurityMiddleware + RequireAuth) is unchanged.

import (
	_ "embed"
	"net/http"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
)

//go:embed embedded/conjunction_app.html
var conjunctionAppHTML []byte

// conjunctionCSP is the Content-Security-Policy served with the conjunction UI
// (C3). The conjunction artifact is a single self-contained document — all
// JS/CSS inline, all fonts inlined as data: URIs, and its only network calls
// are same-origin GETs to /api/v1/* — so this policy enforces that
// fully-self-hosted posture at the browser layer, complementing the build-time
// forbidden-host audit and the runtime zero-external-request verification.
//
//   - connect-src 'self' is the load-bearing directive: it blocks any
//     exfiltration / third-party beacon the single-file bundle would otherwise
//     be able to make. The app only fetches /api/v1/{peers,channels,stats} +
//     /api/v1/data/health, all same-origin.
//   - default/img/font/object/base-uri lock every other fetch to self (+ data:
//     for the inlined woff2 fonts and any inline images).
//   - frame-ancestors 'none' + form-action 'none' add clickjacking / form-
//     hijack protection; the conjunction app has no <form> and is never framed.
//   - script-src/style-src carry 'unsafe-inline' because the packaging hard
//     rule inlines the single module script (plus the serve-time __SDN_CONFIG__
//     script) and Svelte emits inline style="" attributes throughout; a
//     nonce/hash pass over the embedded artifact is the noted future hardening,
//     but 'unsafe-inline' here does NOT weaken the exfiltration protection that
//     connect-src provides. No wasm ships (C1), so no 'wasm-unsafe-eval'; no
//     workers/blob:, so no worker-src.
const conjunctionCSP = "default-src 'self'; " +
	"base-uri 'none'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"form-action 'none'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'"

// uiMode selects which embedded UI the daemon serves at the primary route.
type uiMode int

const (
	// uiModeConjunction is the SHIPPED default: the conjunction-only app at "/",
	// with every descoped SpaceAware screen 404'd.
	uiModeConjunction uiMode = iota
	// uiModeSpaceAware serves the full SpaceAware app route skeleton — dev /
	// re-enablement only, never the shipped default.
	uiModeSpaceAware
)

func (m uiMode) String() string {
	if m == uiModeSpaceAware {
		return "spaceaware"
	}
	return "conjunction"
}

// resolveUIMode reads SDN_UI_MODE and returns the UI serving mode. Anything
// other than an explicit "spaceaware"/"full" opt-in yields the conjunction-only
// shipped default (empty, unset, "conjunction", or an unrecognized value).
func resolveUIMode() uiMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SDN_UI_MODE"))) {
	case "spaceaware", "full", "spaceaware-full", "dev":
		return uiModeSpaceAware
	default:
		return uiModeConjunction
	}
}

// makeUISurfaceHandler builds the "/" catch-all surface handler for the chosen
// UI mode. In the SHIPPED conjunction default it serves the conjunction app at
// the primary route and 404s the descoped SpaceAware screens; in spaceaware
// (dev) mode it delegates to the unchanged full-app makeFrontendSurfaceHandler.
func makeUISurfaceHandler(frontendHandler http.Handler, authHandler *auth.Handler, requireAuth bool, mode uiMode) http.Handler {
	if mode == uiModeConjunction {
		return makeConjunctionSurfaceHandler(frontendHandler)
	}
	return makeFrontendSurfaceHandler(frontendHandler, authHandler, requireAuth)
}

// makeConjunctionSurfaceHandler serves the conjunction-only ship (C2). The
// conjunction app is THE UI at the primary route "/"; deep links use the
// ?group= query string (preserved on "/"), so no path-based routes are needed.
// Every descoped SpaceAware screen (/console/*, /orbital, /gantt, /bmc2/*, the
// SpaceAware /login screen) returns 404 — its code stays committed and dormant,
// only its serving stops. Anything else (assets, /favicon fallback, unknown)
// falls through to the disk frontend handler and its own 404s.
func makeConjunctionSurfaceHandler(frontendHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			serveConjunctionUI(w, r)
			return
		}
		if isSpaceAwareUIPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		frontendHandler.ServeHTTP(w, r)
	})
}

// serveConjunctionUI serves the embedded single-file conjunction artifact from
// memory with the same runtime-config injection and cross-origin-isolation
// headers as the disk-backed frontend handler (COOP/COEP mirror the full-app
// surface for parity; the conjunction app is fully self-contained so they do
// not gate any subresource).
func serveConjunctionUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
	w.Header().Set("Content-Security-Policy", conjunctionCSP)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(injectFrontendConfig(conjunctionAppHTML))
	}
}
