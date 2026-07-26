package main

// Root-surface serving and the auth/login surface configuration.
//
// The primary route "/" serves the SDN Node Status $APP homepage — ONE
// self-contained file built by sdn-js/dashboard (build-dashboard.mjs) from the
// SpaceAware-Student-UI design components, go:embed'ed here. It reads the
// node's public read-only /ws/status feed exclusively. The self-hosted display
// + mono web fonts it references are served same-origin at /fonts/*.woff2. The
// isolated wallet callback (auth machinery, not UI content) is unchanged, and
// so is the 404 behavior for every other root-surface path. New user-facing
// surfaces are Iris's (ui-oracle) domain.

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
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

// placeholderHTML is the self-contained wordmark page kept on disk as the
// graceful fallback served at "/" when the dashboard artifact has not been
// built into the binary (embedded/dashboard.html empty).
//
//go:embed embedded/placeholder.html
var placeholderHTML []byte

// dashboardHTML is the built SDN Node Status $APP homepage: ONE self-contained
// file emitted by sdn-js/dashboard/build-dashboard.mjs (design components +
// theme tokens, fed by the /ws/status $NST feed). It is the "/" content.
//
//go:embed embedded/dashboard.html
var dashboardHTML []byte

// dashboardCSPRaw is the appSurfaceCSP-grade policy for the dashboard, emitted
// by build-dashboard.mjs alongside the html so the inline-script hash in
// script-src always matches the shipped bytes.
//
//go:embed embedded/dashboard.csp
var dashboardCSPRaw string

// dashboardFonts holds the self-hosted display + mono web fonts (Chakra Petch +
// IBM Plex Mono) served same-origin at /fonts/*.woff2, so the dashboard's
// @font-face rules resolve without any external Google Fonts request.
//
//go:embed embedded/fonts/*.woff2
var dashboardFonts embed.FS

// dashboardCSP returns the trimmed, build-generated CSP for the "/" dashboard.
func dashboardCSP() string { return strings.TrimSpace(dashboardCSPRaw) }

// makeRootHandler serves the SDN Node Status dashboard at "/" (and
// "/index.html") with its build-generated CSP, the isolated wallet callback
// routes unchanged, and a 404 for every other root-surface path. If the
// dashboard artifact was not built into the binary it falls back to the
// self-contained wordmark placeholder.
func makeRootHandler() http.Handler {
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
		body := dashboardHTML
		csp := dashboardCSP()
		if len(body) == 0 || csp == "" {
			// Dashboard artifact absent — serve the wordmark placeholder.
			body = placeholderHTML
			csp = placeholderCSP
		}
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}

// makeFontsHandler serves the self-hosted dashboard web fonts at
// /fonts/<name>.woff2 (GET/HEAD only, public, immutable). These are the exact
// same-origin font paths the built dashboard's @font-face rules reference.
func makeFontsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/fonts/")
		if name == "" || strings.Contains(name, "/") || !strings.HasSuffix(name, ".woff2") {
			http.NotFound(w, r)
			return
		}
		body, err := fs.ReadFile(dashboardFonts, path.Join("embedded/fonts", name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}

// embeddingAssetExts allow-lists the file types the /embedding/* surface may
// serve: the sentence-embedding model, its tokenizer vocab, and the
// onnxruntime-web runtime artifacts the dashboard's semantic search loads
// same-origin. Everything else (and any nested path) 404s.
var embeddingAssetExts = map[string]string{
	".onnx": "application/octet-stream",
	".txt":  "text/plain; charset=utf-8",
	".wasm": "application/wasm",
	".mjs":  "text/javascript; charset=utf-8",
}

// makeEmbeddingHandler serves the semantic-search assets at
// /embedding/<name> from the configured assets dir (GET/HEAD only, flat
// namespace, extension allow-list). Fail-open like the geoip mmdb: an empty
// dir path or missing files simply 404 and the dashboard keeps substring
// search. Responses are cacheable for a day — operators replace assets by
// re-running deployment/embedding/fetch-model.sh, not live-swapping.
func makeEmbeddingHandler(assetsDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/embedding/")
		mediaType, allowed := embeddingAssetExts[strings.ToLower(path.Ext(name))]
		if assetsDir == "" || name == "" || !allowed ||
			strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.HasPrefix(name, ".") {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(filepath.Join(assetsDir, name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, info.ModTime(), f)
	})
}

// identitySource feeds the anonymous /identity/* download surface with the
// node's own artifacts and observed-peer lookups. Kept as closures so this
// file stays free of node wiring.
type identitySource struct {
	SelfID    string
	SelfVCard func() (string, error)
	SelfEPM   func() []byte
	PeerVCard func(peerID string) (string, bool)
	PeerEPM   func(peerID string) ([]byte, bool)
}

// makeIdentityHandler serves node identity artifacts for the status
// dashboard's node modal: /identity/<peerId>.vcf (the vCard, importable by
// phones) and /identity/<peerId>.epm (the peer's serialized EPM record —
// signed per the owner rule with the signing key derived at the signing path
// published in the vCard's sign/xpub email aliases). This is the same
// public read-only surface class as /ws/status: every vCard served here
// already streams in the feed's VCARD field, and EPM records are what peers
// publish to the network. GET/HEAD only; unknown ids/extensions 404.
func makeIdentityHandler(src identitySource) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/identity/")
		id, ext, ok := strings.Cut(name, ".")
		if !ok || id == "" || strings.ContainsAny(id, "/\\.") {
			http.NotFound(w, r)
			return
		}
		var body []byte
		var contentType, filename string
		switch ext {
		case "vcf":
			var card string
			if id == src.SelfID {
				if src.SelfVCard != nil {
					card, _ = src.SelfVCard()
				}
			} else if src.PeerVCard != nil {
				card, _ = src.PeerVCard(id)
			}
			if strings.TrimSpace(card) == "" {
				http.NotFound(w, r)
				return
			}
			body = []byte(card)
			contentType = "text/vcard; charset=utf-8"
			filename = id + ".vcf"
		case "epm":
			var epmBytes []byte
			if id == src.SelfID {
				if src.SelfEPM != nil {
					epmBytes = src.SelfEPM()
				}
			} else if src.PeerEPM != nil {
				epmBytes, _ = src.PeerEPM(id)
			}
			if len(epmBytes) == 0 {
				http.NotFound(w, r)
				return
			}
			body = epmBytes
			contentType = "application/x-flatbuffers"
			filename = id + ".epm"
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
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
