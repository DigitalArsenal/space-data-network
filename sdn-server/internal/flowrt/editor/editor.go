// Package editor embeds the SDN visual runtime editor into the Go binary and
// provides an HTTP handler that serves it with the required API endpoints.
package editor

import (
	"embed"
	"encoding/json"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
)

//go:embed dist
var editorFS embed.FS

// Handler returns an http.Handler that serves the embedded SDN runtime editor
// and implements the backend API the editor expects. All routes are served
// under basePath (e.g. "/flow-editor").
func Handler(basePath string, mgr *flowrt.FlowManager) http.Handler {
	// Strip the dist/ prefix so files are at the root
	sub, _ := fs.Sub(editorFS, "dist")
	fileServer := http.FileServer(http.FS(sub))

	basePath = strings.TrimRight(basePath, "/")

	mux := http.NewServeMux()

	// --- Editor API routes ---

	mux.HandleFunc(basePath+"/api/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{
			"title":       "SDN Runtime Editor",
			"engineLabel": "Space Data Network",
			"api": map[string]string{
				"bootstrapUrl":       basePath + "/api/bootstrap",
				"compilePreviewUrl":  basePath + "/api/compile-preview",
				"runtimeStatusUrl":   basePath + "/api/runtime-status",
				"runtimeArtifactUrl": basePath + "/api/runtime-artifact",
				"runtimeSettingsUrl": basePath + "/api/runtime-settings",
				"archivesUrl":        basePath + "/api/archives",
				"downloadWasmUrl":    basePath + "/api/download/wasm",
			},
		})
	})

	mux.HandleFunc(basePath+"/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, map[string]interface{}{
			"version":            "0.3.9",
			"user":               map[string]interface{}{"anonymous": false, "username": "sdn-runtime", "permissions": "*"},
			"httpNodeRoot":       "/",
			"paletteCategories":  []string{"common", "function", "network", "sequence", "parser", "storage"},
			"editorTheme":        map[string]interface{}{},
			"flowEncryptionType": "system",
		})
	})

	mux.HandleFunc(basePath+"/settings/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeJSON(w, map[string]interface{}{})
	})

	mux.HandleFunc(basePath+"/theme", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{
			"header": map[string]interface{}{
				"title": "SDN Runtime Editor",
				"url":   "https://github.com/DigitalArsenal/space-data-network",
			},
		})
	})

	mux.HandleFunc(basePath+"/nodes", func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		if strings.Contains(accept, "text/html") {
			// Return HTML with node configurations — served from registry
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<!-- Node configurations loaded from capabilities -->"))
			return
		}
		// Return JSON node set metadata
		writeJSON(w, []interface{}{})
	})

	mux.HandleFunc(basePath+"/nodes/messages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{})
	})

	mux.HandleFunc(basePath+"/icons", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []interface{}{})
	})

	mux.HandleFunc(basePath+"/locales/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{})
	})

	mux.HandleFunc(basePath+"/plugins", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []interface{}{})
	})

	mux.HandleFunc(basePath+"/plugins/messages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{})
	})

	// Flow management
	mux.HandleFunc(basePath+"/flows", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, map[string]interface{}{
				"rev":   "initial",
				"flows": []interface{}{},
			})
		case http.MethodPost:
			writeJSON(w, map[string]interface{}{
				"rev":            "updated",
				"compileId":      nil,
				"restartPending": false,
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc(basePath+"/flows/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"state": "start"})
	})

	// Compilation & runtime
	mux.HandleFunc(basePath+"/api/compile-preview", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"status": "idle"})
	})

	mux.HandleFunc(basePath+"/api/runtime-status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"status": "running"})
	})

	mux.HandleFunc(basePath+"/api/runtime-artifact", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, nil)
	})

	mux.HandleFunc(basePath+"/api/runtime-settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{})
	})

	mux.HandleFunc(basePath+"/api/archives", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []interface{}{})
	})

	// Capabilities endpoint — what host-provided nodes are available
	mux.HandleFunc(basePath+"/api/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, flowrt.AvailableCapabilities())
	})

	// Debug endpoints (no-op stubs)
	mux.HandleFunc(basePath+"/debug/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(basePath+"/inject/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Static asset serving — catch-all
	mux.HandleFunc(basePath+"/", func(w http.ResponseWriter, r *http.Request) {
		// Strip base path to get the file path
		filePath := strings.TrimPrefix(r.URL.Path, basePath)
		if filePath == "" || filePath == "/" {
			filePath = "/index.html"
		}
		filePath = strings.TrimPrefix(filePath, "/")

		// Try to serve from embedded FS
		f, err := sub.Open(filePath)
		if err != nil {
			// Fallback to index.html for SPA routing
			filePath = "index.html"
		} else {
			f.Close()
		}

		// Set content type from extension
		ext := filepath.Ext(filePath)
		if ct := mime.TypeByExtension(ext); ct != "" {
			w.Header().Set("Content-Type", ct)
		}

		r.URL.Path = "/" + filePath
		fileServer.ServeHTTP(w, r)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers for editor
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Node-RED-API-Version, Node-RED-Deployment-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		mux.ServeHTTP(w, r)
	})
}

// RegisterEditor registers the editor handler on the given mux if enabled.
func RegisterEditor(mux *http.ServeMux, basePath string, mgr *flowrt.FlowManager) {
	handler := Handler(basePath, mgr)

	// Register both with and without trailing slash
	mux.Handle(basePath+"/", handler)
	if !strings.HasSuffix(basePath, "/") {
		mux.Handle(basePath, http.RedirectHandler(basePath+"/", http.StatusPermanentRedirect))
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if v == nil {
		w.Write([]byte("null"))
		return
	}
	json.NewEncoder(w).Encode(v)
}

// Ensure mime types are registered for common editor assets
func init() {
	mime.AddExtensionType(".js", "application/javascript")
	mime.AddExtensionType(".mjs", "application/javascript")
	mime.AddExtensionType(".css", "text/css")
	mime.AddExtensionType(".svg", "image/svg+xml")
	mime.AddExtensionType(".woff2", "font/woff2")
	mime.AddExtensionType(".woff", "font/woff")
	mime.AddExtensionType(".ttf", "font/ttf")
	mime.AddExtensionType(".ico", "image/x-icon")
	mime.AddExtensionType(".map", "application/json")
}

// Ignore unused import
var _ = path.Join
