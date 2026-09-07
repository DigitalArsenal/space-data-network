package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

type moduleApplications interface {
	ModuleApplication(string) ([]byte, error)
	ModuleApplicationArtifact(string) ([]byte, error)
	ModuleApplicationPage(string) ([]byte, error)
}

func (h *CoreAPIHandler) registerModuleApplicationRoutes(mux *http.ServeMux) {
	apps, ok := h.publisher.(moduleApplications)
	if !ok {
		return
	}
	for _, kind := range []string{"app", "artifact", "app/page"} {
		kind := kind
		mux.HandleFunc("/api/v1/modules/{moduleID}/"+kind, h.withRL(h.requireAdminStrict(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var payload []byte
			var err error
			if kind == "app" {
				payload, err = apps.ModuleApplication(r.PathValue("moduleID"))
			} else if kind == "app/page" {
				payload, err = apps.ModuleApplicationPage(r.PathValue("moduleID"))
			} else {
				payload, err = apps.ModuleApplicationArtifact(r.PathValue("moduleID"))
			}
			if err != nil {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			if kind == "app" {
				w.Header().Set("Content-Type", "application/x-flatbuffers; schema=APP")
			} else if kind == "app/page" {
				digest := sha256.Sum256(payload)
				if r.URL.Query().Get("sha256") != hex.EncodeToString(digest[:]) {
					writeError(w, http.StatusConflict, "The application changed. Reopen it from Modules.")
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("X-Frame-Options", "SAMEORIGIN")
				w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
				w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
				// A separate response prevents the dashboard's script hashes from
				// blocking this independently verified page. Both CSP and the iframe
				// impose an opaque origin; all node access uses the bounded host bridge.
				w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline' 'wasm-unsafe-eval'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts allow-forms allow-downloads")
				w.Header().Set("Referrer-Policy", "no-referrer")
			} else {
				w.Header().Set("Content-Type", "application/wasm")
			}
			_, _ = w.Write(payload)
		})))
	}
}
