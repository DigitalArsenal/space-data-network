package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/appmanifest"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
)

// appSurfaceCSP is applied uniformly to pages decoded from embedded SDS $APP
// records. It permits only the document's own inline resources and same-origin
// API traffic; hosts do not know application-specific routes or assets.
const appSurfaceCSP = "default-src 'self'; " +
	"base-uri 'none'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"form-action 'none'; " +
	"script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self' data:; " +
	"worker-src 'self' blob:; " +
	"connect-src 'self'"

//go:embed embedded/apps.json embedded/*.app
var embeddedAppsFS embed.FS

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

// embeddedAppSpec is host metadata only: a stable mount slug and an opaque SDS
// $APP payload. The host decodes the record with the same resolver used for any
// installed app; it does not embed or route an application's HTML directly.
type embeddedAppSpec struct {
	slug   string
	record []byte
	root   bool
}

func embeddedAppSpecs() ([]embeddedAppSpec, error) {
	var config struct {
		Apps []struct {
			Slug   string `json:"slug"`
			Record string `json:"record"`
			Root   bool   `json:"root"`
		} `json:"apps"`
	}
	data, err := embeddedAppsFS.ReadFile("embedded/apps.json")
	if err != nil {
		return nil, fmt.Errorf("embedded apps: read config: %w", err)
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("embedded apps: parse config: %w", err)
	}
	if len(config.Apps) != 1 {
		return nil, fmt.Errorf("embedded apps: config must declare exactly one current app")
	}
	entry := config.Apps[0]
	if entry.Slug == "" || entry.Record == "" || strings.Contains(entry.Record, "/") || !strings.HasSuffix(entry.Record, ".app") || !entry.Root {
		return nil, fmt.Errorf("embedded apps: invalid current app configuration")
	}
	record, err := embeddedAppsFS.ReadFile("embedded/" + entry.Record)
	if err != nil {
		return nil, fmt.Errorf("embedded apps: read %q: %w", entry.Record, err)
	}
	return []embeddedAppSpec{{slug: entry.Slug, record: record, root: entry.Root}}, nil
}

func embeddedAppManifest(slug string) (*appmanifest.AppManifest, error) {
	specs, err := embeddedAppSpecs()
	if err != nil {
		return nil, err
	}
	for _, spec := range specs {
		if spec.slug != slug {
			continue
		}
		manifest, err := appmanifest.FromAPP(spec.record)
		if err != nil {
			return nil, fmt.Errorf("embedded app %q: decode $APP: %w", slug, err)
		}
		return manifest, nil
	}
	return nil, fmt.Errorf("embedded app %q is not registered", slug)
}

func primaryEmbeddedAppSlug() (string, error) {
	specs, err := embeddedAppSpecs()
	if err != nil {
		return "", err
	}
	for _, spec := range specs {
		if spec.root {
			return spec.slug, nil
		}
	}
	return "", fmt.Errorf("embedded apps: no root app")
}

// makeEmbeddedAppSurfaceHandler mounts one resolved $APP entry page as a
// homepage. The only host policy is HTTP method/path handling; page content,
// identity, and media type come from the APP record itself. The isolated
// wallet callback (an exact, read-only static route used by the external
// wallet presenter's opener handshake) is served ahead of the app page.
func makeEmbeddedAppSurfaceHandler(slug string) (http.Handler, error) {
	manifest, err := embeddedAppManifest(slug)
	if err != nil {
		return nil, err
	}
	resolution, err := manifest.Resolve()
	if err != nil {
		return nil, fmt.Errorf("embedded app %q: resolve: %w", slug, err)
	}
	if resolution.EntryPage == nil {
		return nil, fmt.Errorf("embedded app %q has no entry page", slug)
	}
	page, err := resolution.EntryPage.DecodedContent()
	if err != nil {
		return nil, fmt.Errorf("embedded app %q: decode entry page: %w", slug, err)
	}
	app := resolvedApp{slug: slug, id: manifest.ID, name: manifest.Name, version: manifest.Version, description: manifest.Description, mediaType: strings.TrimSpace(resolution.EntryPage.MediaType), page: page}
	if app.mediaType == "" {
		app.mediaType = "text/html"
	}
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
		serveAppPage(w, r, app)
	}), nil
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
// The primary route "/" always serves the embedded root SDS $APP; the mode
// gates only the legacy wallet/login development surfaces.
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
