package main

// Root-surface serving and the auth/login surface configuration.
//
// UI CLEAN SLATE (owner ruling 2026-07-24): every embedded UI application —
// the $APP registry, launcher, per-app pages, and their build pipelines —
// has been removed from this repo pending the owner's new UI codebase. The
// primary route "/" serves a minimal placeholder; the isolated wallet
// callback (auth machinery, not UI content) remains. New user-facing
// surfaces are Iris's (ui-oracle) domain and land only with the owner's
// codebase.

import (
	_ "embed"
	"net/http"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
)

//go:embed embedded/wallet_callback.html
var walletCallbackHTML []byte

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

// placeholderCSP locks the placeholder page to itself: no scripts, no
// external fetches, nothing to misuse while the real UI is absent.
const placeholderCSP = "default-src 'none'; " +
	"style-src 'unsafe-inline'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'; " +
	"form-action 'none'"

// placeholderHTML is the single self-contained interim page served at "/"
// (owner directive 2026-07-24: the SPACE DATA NETWORK wordmark, green on
// textured black) until the new UI codebase lands.
//
//go:embed embedded/placeholder.html
var placeholderHTML []byte

// makeRootPlaceholderHandler serves the minimal placeholder at "/" and the
// wallet callback routes; everything else under the root surface 404s.
func makeRootPlaceholderHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWalletCallbackPath(r.URL.Path) {
			serveWalletCallback(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", placeholderCSP)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(placeholderHTML)
		}
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

// uiMode selects which auth/login surface configuration the daemon runs with.
// The mode gates only the legacy wallet/login development surfaces.
type uiMode int

const (
	// uiModeConjunction is the SHIPPED default: no legacy login route, the
	// external isolated wallet presenter owns authorization.
	uiModeConjunction uiMode = iota
	// uiModeSpaceAware enables the legacy wallet login development surfaces —
	// dev / re-enablement only, never the shipped default.
	uiModeSpaceAware
)

func (m uiMode) String() string {
	if m == uiModeSpaceAware {
		return "spaceaware"
	}
	return "conjunction"
}

// resolveUIMode reads SDN_UI_MODE and returns the UI serving mode. Anything
// other than an explicit "spaceaware"/"full" opt-in yields the shipped
// default (empty, unset, "conjunction", or an unrecognized value).
func resolveUIMode() uiMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SDN_UI_MODE"))) {
	case "spaceaware", "full", "spaceaware-full", "dev":
		return uiModeSpaceAware
	default:
		return uiModeConjunction
	}
}

// legacyWalletUIPathForMode strips legacy wallet configuration from the
// shipped server. This value is passed into auth.Handler as well as the
// optional static mount, so /api/auth/status and routing cannot disagree.
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
