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
	"bytes"
	_ "embed"
	"net/http"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
)

//go:embed embedded/conjunction_app.html
var conjunctionAppHTML []byte

//go:embed embedded/wallet_callback.html
var walletCallbackHTML []byte

// conjunctionCSP is the Content-Security-Policy served with the conjunction UI
// (C3). The conjunction artifact is a single self-contained document — all
// JS/CSS inline and all fonts inlined as data: URIs. Its network calls are
// same-origin GETs to /api/v1/* plus the one reviewed wallet origin used by the
// typed public presenter.
//
//   - connect-src permits self and exactly https://wallet.spacedatanetwork.org.
//     No wildcard, alternate host, websocket, or blob destination is allowed.
//   - default/img/font/object/base-uri lock every other fetch to self (+ data:
//     for the inlined woff2 fonts and any inline images).
//   - frame-ancestors 'none' + form-action 'none' add clickjacking / form-
//     hijack protection; the conjunction app has no <form> and is never framed.
//   - script-src/style-src carry 'unsafe-inline' because the packaging hard
//     rule inlines the single module script (plus the serve-time __SDN_CONFIG__
//     script) and Svelte emits inline style="" attributes throughout; a
//     nonce/hash pass over the embedded artifact is the noted future hardening.
//     No wasm ships (C1), so no 'wasm-unsafe-eval'; no
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
	"connect-src 'self' https://wallet.spacedatanetwork.org"

// walletCallbackCSP isolates the generated callback document from every
// application capability except loading its immutable helper from the reviewed
// static asset origin. The helper completes the opener handshake using browser
// primitives; it does not need a connect-src destination.
const walletCallbackCSP = "default-src 'none'; " +
	"script-src https://static.spacedatanetwork.org; " +
	"style-src 'none'; " +
	"connect-src 'none'; " +
	"img-src 'none'; " +
	"font-src 'none'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'; " +
	"form-action 'none'"

// appsLauncherLink is a serve-time-injected, fully self-contained affordance
// that surfaces the /apps/ launcher on the served conjunction UI (A2.10 item 3:
// "surface an APPS link on the conjunction page WITHOUT rebuilding the sdn-js
// artifact"). It is a single same-origin anchor with an inline style attribute —
// allowed by conjunctionCSP's style-src 'unsafe-inline' — carrying no script and
// making no network request until clicked, so it preserves the zero-external-
// request / console-clean posture. It is injected before </body> at serve time
// (via injectAppsLauncherLink); the embedded conjunction artifact and the App 1
// APP-record drift gate are untouched (they cover the embed bytes, not the
// serve-time bytes). House style: uppercase, square corners, monospace, no arrow
// glyph, with a title tooltip.
const appsLauncherLink = `<a href="/apps/" title="Open the SDN apps launcher" ` +
	`style="position:fixed;bottom:12px;right:12px;z-index:2147483000;` +
	`font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:11px;` +
	`font-weight:600;letter-spacing:0.14em;text-transform:uppercase;color:#8fd6ff;` +
	`background:#0a0e14;border:1px solid #2a3a4a;padding:7px 12px;text-decoration:none;">APPS</a>`

// injectAppsLauncherLink inserts the /apps/ launcher affordance before the final
// </body> of the served conjunction document (appending to the end if no </body>
// is present). It operates on serve-time bytes only.
func injectAppsLauncherLink(html []byte) []byte {
	link := []byte(appsLauncherLink)
	if idx := bytes.LastIndex(html, []byte("</body>")); idx >= 0 {
		out := make([]byte, 0, len(html)+len(link))
		out = append(out, html[:idx]...)
		out = append(out, link...)
		out = append(out, html[idx:]...)
		return out
	}
	return append(html, link...)
}

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

// legacyWalletUIPathForMode strips legacy wallet configuration from the
// shipped conjunction server. This value is passed into auth.Handler as well
// as the optional static mount, so /api/auth/status and routing cannot disagree.
func legacyWalletUIPathForMode(mode uiMode, configuredPath string) string {
	if mode != uiModeSpaceAware {
		return ""
	}
	return strings.TrimSpace(configuredPath)
}

// registerLegacyWalletStaticFiles mounts the generic wallet bundle only for
// the explicitly selected SpaceAware development mode.
func registerLegacyWalletStaticFiles(mux *http.ServeMux, mode uiMode, configuredPath string) (string, bool) {
	walletUIPath := legacyWalletUIPathForMode(mode, configuredPath)
	if walletUIPath == "" {
		return "", false
	}
	serveRoot := auth.WalletUIStaticRoot(walletUIPath)
	if serveRoot == "" {
		serveRoot = walletUIPath
	}
	mux.Handle("/wallet-ui/", http.StripPrefix("/wallet-ui/", http.FileServer(http.Dir(serveRoot))))
	return serveRoot, true
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
		if isWalletCallbackPath(r.URL.Path) {
			serveWalletCallback(w, r)
			return
		}
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

func isWalletCallbackPath(requestPath string) bool {
	return requestPath == "/wallet/callback" ||
		requestPath == "/wallet/callback/" ||
		requestPath == "/wallet-callback.html"
}

// serveWalletCallback serves the pinner-generated callback document without
// mutation. It is deliberately separate from runtime-config injection and all
// SPA fallbacks: the wallet helper validates this callback URI exactly.
func serveWalletCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", walletCallbackCSP)
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(walletCallbackHTML)
	}
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
		// Inject the /apps/ launcher affordance (A2.10 item 3) at serve time, on
		// top of the __SDN_CONFIG__ injection, without touching the embedded
		// artifact or its drift gate.
		_, _ = w.Write(injectAppsLauncherLink(injectFrontendConfig(conjunctionAppHTML)))
	}
}
