// Package sdnui is the self-contained static operator console for a live SDN
// node — Phase 8 of the kubo rebase. It is the visual companion to the
// read-only JSON API in kubo/sdn/sdnapi: a plain HTML/CSS/vanilla-JS single
// page, embedded into the binary with go:embed (the same "ship it inside the
// node" pattern flatsqlrt uses for its wasm engine) and served by the sdnapi
// plugin on the SAME loopback listener as the API.
//
// # Self-contained, offline, same-origin only
//
// The assets make NO external-origin request: no CDN, no web font fetch, no
// remote image, no analytics. Every data call the page makes is a same-origin
// GET to /sdn/v1/* on the node that served the page. That is what lets a node
// serve its own console with no network egress, and it is asserted by the
// package test (the embedded assets are grepped for any foreign http(s):// URL).
//
// # Read-only, GET-only
//
// Like the API it fronts, this surface is read-only. Handler serves exactly
// three assets by exact path (/, /styles.css, /app.js) to GET requests;
// everything else — unknown path or non-GET method — is a plain 404.
package sdnui

import (
	"embed"
	"io/fs"
	"net/http"
)

// Assets holds the embedded static console (index.html, styles.css, app.js).
// It is exported so provenance/self-containment checks (e.g. the package test
// that forbids external-origin URLs) can walk exactly what ships in the binary.
//
//go:embed assets/index.html assets/styles.css assets/app.js
var Assets embed.FS

// asset is one embedded file plus the Content-Type it is served with.
type asset struct {
	path        string
	contentType string
}

// routeAssets maps a request path to its embedded file. Only these exact paths
// are served; there is no directory listing and no path traversal.
var routeAssets = map[string]asset{
	"/":           {path: "assets/index.html", contentType: "text/html; charset=utf-8"},
	"/styles.css": {path: "assets/styles.css", contentType: "text/css; charset=utf-8"},
	"/app.js":     {path: "assets/app.js", contentType: "text/javascript; charset=utf-8"},
}

type uiHandler struct{}

// Handler returns the static SDN console handler. It serves the embedded app
// shell at "/" and its two assets, GET-only; any other path or method is 404.
func Handler() http.Handler { return uiHandler{} }

func (uiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	a, ok := routeAssets[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := fs.ReadFile(Assets, a.path)
	if err != nil {
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", a.contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// IndexHTML returns the embedded app-shell HTML bytes (handy for callers that
// want to inline or verify the shell without going through the HTTP handler).
func IndexHTML() ([]byte, error) { return fs.ReadFile(Assets, "assets/index.html") }
