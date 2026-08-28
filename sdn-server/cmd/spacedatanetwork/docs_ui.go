package main

// The node ships its own documentation (owner 2026-08-28): the rendered
// guides and the combined PDF are built by docs/build-docs.mjs, emitted into
// embedded/docs/, go:embed'ed here, and served same-origin at /docs/ — so the
// instructions a node shows are the instructions for the exact version it
// runs, and they update with every release. Zero external origin: the pages
// are fully self-contained (inline styles, no scripts, same-origin links).

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed embedded/docs
var embeddedDocs embed.FS

// docsPageCSP: the guides are static documents — no scripts, inline styles
// only, links to sibling pages and the PDF.
const docsPageCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// makeDocsHandler serves /docs/ from the embedded documentation set.
// "/docs" and "/docs/" land on the first guide; every other path must name an
// embedded file exactly (no traversal — the embed FS holds only the docs).
func makeDocsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/docs")
		name = strings.TrimPrefix(name, "/")
		if name == "" || name == "index.html" {
			name = "server-overview.html"
		}
		if strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}
		body, err := fs.ReadFile(embeddedDocs, path.Join("embedded/docs", name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case strings.HasSuffix(name, ".html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", docsPageCSP)
		case strings.HasSuffix(name, ".pdf"):
			w.Header().Set("Content-Type", "application/pdf")
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}
