package main

// SDN apps launcher + per-app serving (App 2 packet A2.10 — OWNER DIRECTIVE
// 2026-07-13 "App 2 must be servable and selectable from the UI, same as the
// conjunction app").
//
// This surface is mounted at "/apps/" on the admin mux (main.go), ahead of the
// "/" primary-UI surface, so it is available in BOTH conjunction (shipped
// default) and spaceaware (dev) UI modes and never shadows the conjunction app
// at "/". Two routes:
//
//   - GET /apps/            -> the launcher: a tiny, self-contained, generated
//     HTML page listing every APP record the daemon serves (name/version/
//     description + a link per app). House style (square corners, uppercase
//     labels, monospace), zero external requests, and the conjunction-grade
//     header set (COOP/COEP/CSP no-store).
//   - GET /apps/<appId>/    -> the app's decoded inline UI page
//     (decode(record.Pages[entry].Content)) with the conjunction-grade header
//     set and the same __SDN_CONFIG__ injection serveConjunctionUI performs, so
//     an app's page reads window.__SDN_CONFIG__.apiBase the same way at "/apps/
//     <id>/" as the conjunction app does at "/".
//
// The app set is DATA, not structure: appServingSpecs() is a slice of
// {slug, builder} pairs consuming the internal/appmanifest record/embed API
// (NewConjunctionApp over the daemon's embedded conjunction artifact;
// NewSupplementalOMMApp over appmanifest's embedded status board). Adding App 3
// is one more entry here — the launcher and the per-app router iterate the set
// and never branch per app. The App 2 status board bytes are consumed ONLY via
// appmanifest.SupplementalOMMBoardHTML()/the record (the board file itself is
// owned by a concurrent worker; this code never reads or embeds it directly).
//
// Anonymous by construction: these are public app pages, mounted without a
// RequireAuth wrapper. The apps' own data sources are the same anonymous-safe
// gateway surfaces the conjunction app uses (isPublicReadAPIPath); the admin API
// wall is untouched.

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/appmanifest"
)

// appServingSpec is one app the daemon serves under /apps/. slug is the
// <appId> path segment (/apps/<slug>/), matching genapprecord's -app names;
// build turns the app's serving artifact bytes into its canonical APP record via
// the internal/appmanifest builder.
type appServingSpec struct {
	slug  string
	build func() (*appmanifest.AppManifest, error)
}

// appServingSpecs returns the data-driven registry of apps served under /apps/.
// This is the ONE place the app set is enumerated; the launcher and per-app
// router consume it and never hardcode individual apps. Records are built from
// the same single sources of truth the drift gates verify:
//   - conjunction: the daemon's embedded serving artifact (conjunctionAppHTML,
//     cmd/spacedatanetwork/embedded/conjunction_app.html).
//   - supplemental-omm: appmanifest's embedded status board, consumed via the
//     record/embed API (SupplementalOMMBoardHTML) — never read directly here.
func appServingSpecs() []appServingSpec {
	return []appServingSpec{
		{
			slug: "conjunction",
			build: func() (*appmanifest.AppManifest, error) {
				return appmanifest.NewConjunctionApp(conjunctionAppHTML)
			},
		},
		{
			slug: "supplemental-omm",
			build: func() (*appmanifest.AppManifest, error) {
				return appmanifest.NewSupplementalOMMApp(appmanifest.SupplementalOMMBoardHTML())
			},
		},
	}
}

// resolvedApp is one launcher/serving-ready app: the URL slug, the record's
// launcher metadata, the decoded entry-page bytes, and the page media type.
type resolvedApp struct {
	slug        string
	id          string
	name        string
	version     string
	description string
	mediaType   string
	page        []byte // decode(record.Pages[entry].Content)
}

// resolveServedApps builds and resolves every APP record in the registry,
// decoding each entry page. An error here means an internal embed/record is
// malformed (the appmanifest drift-gate tests already guarantee they are not),
// so the caller logs and skips mounting the surface rather than serving a
// half-built launcher.
func resolveServedApps() ([]resolvedApp, error) {
	specs := appServingSpecs()
	out := make([]resolvedApp, 0, len(specs))
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if spec.slug == "" {
			return nil, fmt.Errorf("apps: registry entry has an empty slug")
		}
		if seen[spec.slug] {
			return nil, fmt.Errorf("apps: duplicate app slug %q", spec.slug)
		}
		seen[spec.slug] = true

		manifest, err := spec.build()
		if err != nil {
			return nil, fmt.Errorf("apps: build %q record: %w", spec.slug, err)
		}
		resolution, err := manifest.Resolve()
		if err != nil {
			return nil, fmt.Errorf("apps: resolve %q record: %w", spec.slug, err)
		}
		if resolution.EntryPage == nil {
			return nil, fmt.Errorf("apps: %q record has no entry UI page to serve", spec.slug)
		}
		page, err := resolution.EntryPage.DecodedContent()
		if err != nil {
			return nil, fmt.Errorf("apps: decode %q entry page: %w", spec.slug, err)
		}
		mediaType := strings.TrimSpace(resolution.EntryPage.MediaType)
		if mediaType == "" {
			mediaType = "text/html"
		}
		out = append(out, resolvedApp{
			slug:        spec.slug,
			id:          manifest.ID,
			name:        manifest.Name,
			version:     manifest.Version,
			description: manifest.Description,
			mediaType:   mediaType,
			page:        page,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("apps: no apps in the serving registry")
	}
	return out, nil
}

// makeAppsHandler builds the "/apps/" subtree handler: the launcher at "/apps/"
// and each app's page at "/apps/<slug>/". Records/pages are resolved once at
// construction (they are derived from embedded artifacts, deterministic); a
// build error is returned so the caller can log and skip mounting.
func makeAppsHandler() (http.Handler, error) {
	apps, err := resolveServedApps()
	if err != nil {
		return nil, err
	}
	bySlug := make(map[string]resolvedApp, len(apps))
	for _, a := range apps {
		bySlug[a.slug] = a
	}
	launcher, err := renderAppsLauncher(apps)
	if err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// r.URL.Path is within the /apps subtree (ServeMux longest-prefix match).
		// "/apps" (no trailing slash) is redirected to "/apps/" by ServeMux before
		// reaching here; handle it defensively anyway.
		rest := strings.TrimPrefix(r.URL.Path, "/apps/")
		if r.URL.Path == "/apps" {
			rest = ""
		}
		if rest == "" {
			serveAppLauncher(w, r, launcher)
			return
		}

		// /apps/<slug>[/<tail>]. The app page lives exactly at /apps/<slug>/ (or
		// /apps/<slug>); any deeper path is unknown (pages are self-contained
		// inline, no sub-assets).
		slug, tail, _ := strings.Cut(rest, "/")
		app, ok := bySlug[slug]
		if !ok || tail != "" {
			http.NotFound(w, r)
			return
		}
		serveAppPage(w, r, app)
	}), nil
}

// appsCSP preserves the self-hosted policy for launcher/supplemental app pages.
// The conjunction shell has one additional wallet connection destination, so
// sharing its policy here would unnecessarily widen unrelated app surfaces.
const appsCSP = "default-src 'self'; " +
	"base-uri 'none'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"form-action 'none'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'"

// setAppSurfaceHeaders applies the cross-origin-isolation, isolated app CSP,
// and no-store header set shared by the launcher and every supplemental page.
func setAppSurfaceHeaders(w http.ResponseWriter) {
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
	w.Header().Set("Content-Security-Policy", appsCSP)
	w.Header().Set("Cache-Control", "no-store")
}

// serveAppLauncher writes the generated launcher page with the app-surface
// header set. The launcher makes no API calls, so it carries no __SDN_CONFIG__.
func serveAppLauncher(w http.ResponseWriter, r *http.Request, launcher []byte) {
	setAppSurfaceHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(launcher)
	}
}

// serveAppPage serves an app's decoded inline UI page with the app-surface
// header set and the same __SDN_CONFIG__ injection serveConjunctionUI performs
// (so the page reads window.__SDN_CONFIG__.apiBase = "/api/v1"). The served
// bytes are injectFrontendConfig(decode(record.Pages[entry].Content)); the
// record's decoded CONTENT byte-equals its checked-in serving artifact (the
// appmanifest drift gate), so this reads the app through the record at request
// time and never snapshots page content.
func serveAppPage(w http.ResponseWriter, r *http.Request, app resolvedApp) {
	setAppSurfaceHeaders(w)
	contentType := app.mediaType
	if strings.HasPrefix(contentType, "text/") && !strings.Contains(contentType, "charset") {
		contentType += "; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(injectFrontendConfig(app.page))
	}
}

// appsLauncherData is the template model for the launcher page.
type appsLauncherData struct {
	Apps  []appsLauncherEntry
	Count int
}

type appsLauncherEntry struct {
	Slug        string
	ID          string
	Name        string
	Version     string
	Description string
}

// appsLauncherTemplate is the self-contained launcher page: dark house style
// (square corners, uppercase labels, system monospace — no vendored/CDN fonts),
// zero external requests, no scripts. Each app card is a same-origin link to
// /apps/<slug>/.
var appsLauncherTemplate = template.Must(template.New("apps-launcher").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>SDN APPS</title>
<style>
  *{box-sizing:border-box;border-radius:0}
  html,body{margin:0;padding:0}
  body{
    background:#05070b;color:#c9d4e0;
    font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace;
    font-size:14px;line-height:1.5;min-height:100vh;
    padding:48px 24px;
  }
  .apps{max-width:960px;margin:0 auto}
  .apps-head{border-bottom:1px solid #1e2a38;padding-bottom:20px;margin-bottom:28px}
  .kicker{font-size:11px;letter-spacing:0.28em;text-transform:uppercase;color:#4f6274;margin-bottom:10px}
  h1{margin:0;font-size:34px;letter-spacing:0.16em;text-transform:uppercase;color:#eaf2fb;font-weight:700}
  .sub{margin-top:8px;font-size:12px;letter-spacing:0.18em;text-transform:uppercase;color:#6b7a8d}
  .grid{list-style:none;margin:0;padding:0;display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:16px}
  .card a{
    display:block;height:100%;text-decoration:none;color:inherit;
    background:#0b131e;border:1px solid #1e2a38;padding:20px;
    transition:border-color .12s ease,background .12s ease;
  }
  .card a:hover,.card a:focus{border-color:#3d8fbf;background:#0e1826;outline:none}
  .card-name{font-size:16px;letter-spacing:0.10em;text-transform:uppercase;color:#8fd6ff;font-weight:700}
  .card-ver{margin-top:4px;font-size:11px;letter-spacing:0.14em;text-transform:uppercase;color:#4f6274}
  .card-desc{margin:14px 0 0;font-size:12.5px;line-height:1.55;color:#9fb0c2}
  .card-id{margin-top:16px;font-size:10.5px;letter-spacing:0.08em;color:#3d4c5c;word-break:break-all}
  .apps-foot{margin-top:32px;padding-top:18px;border-top:1px solid #1e2a38;font-size:11px;letter-spacing:0.16em;text-transform:uppercase;color:#4f6274}
</style>
</head>
<body>
<main class="apps">
  <header class="apps-head">
    <div class="kicker">Space Data Network</div>
    <h1>Apps</h1>
    <div class="sub">Select an application</div>
  </header>
  <ul class="grid">
    {{range .Apps}}
    <li class="card">
      <a href="/apps/{{.Slug}}/" title="Open {{.Name}}">
        <div class="card-name">{{.Name}}</div>
        <div class="card-ver">Version {{.Version}}</div>
        <p class="card-desc">{{.Description}}</p>
        <div class="card-id">{{.ID}}</div>
      </a>
    </li>
    {{end}}
  </ul>
  <footer class="apps-foot">{{.Count}} application{{if ne .Count 1}}s{{end}} &middot; served from this node</footer>
</main>
</body>
</html>
`))

// renderAppsLauncher renders the launcher page for the given resolved apps.
func renderAppsLauncher(apps []resolvedApp) ([]byte, error) {
	entries := make([]appsLauncherEntry, 0, len(apps))
	for _, a := range apps {
		entries = append(entries, appsLauncherEntry{
			Slug:        a.slug,
			ID:          a.id,
			Name:        a.name,
			Version:     a.version,
			Description: a.description,
		})
	}
	var buf bytes.Buffer
	if err := appsLauncherTemplate.Execute(&buf, appsLauncherData{Apps: entries, Count: len(entries)}); err != nil {
		return nil, fmt.Errorf("apps: render launcher: %w", err)
	}
	return buf.Bytes(), nil
}
