package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/appmanifest"
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
// identity, and media type come from the APP record itself.
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
