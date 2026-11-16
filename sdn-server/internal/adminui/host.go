package adminui

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Host is an SPA file host for the built SDN admin client: a request naming a
// file of the build gets that file, and any other GET or HEAD without a file
// extension gets index.html with the runtime config injected.
type Host struct {
	buildDir  string
	indexPath string
	files     http.Handler
}

// NewHost returns an SPA file host for the built SDN admin client.
func NewHost(buildDir string) (*Host, error) {
	buildDir = strings.TrimSpace(buildDir)
	if buildDir == "" {
		return nil, fmt.Errorf("admin ui path is empty")
	}

	indexPath := filepath.Join(buildDir, "index.html")
	if st, err := os.Stat(indexPath); err != nil {
		return nil, fmt.Errorf("admin ui path %q: missing index.html: %w", buildDir, err)
	} else if st.IsDir() {
		return nil, fmt.Errorf("admin ui path %q: index.html is a directory", buildDir)
	}

	return &Host{
		buildDir:  buildDir,
		indexPath: indexPath,
		files:     http.FileServer(http.Dir(buildDir)),
	}, nil
}

// IsFile reports whether urlPath, relative to where the host is mounted, names
// a file of the build (not a directory).
func (h *Host) IsFile(urlPath string) bool {
	clean := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if clean == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(h.buildDir, filepath.FromSlash(clean)))
	return err == nil && !st.IsDir()
}

// ServeHTTP implements http.Handler.
func (h *Host) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.IsFile(r.URL.Path) {
		w.Header().Set("Cache-Control", "public, max-age=1800")
		h.files.ServeHTTP(w, r)
		return
	}

	if ext := path.Ext(r.URL.Path); ext != "" && r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	indexHTML, err := os.ReadFile(h.indexPath)
	if err != nil {
		http.Error(w, "admin ui unavailable", http.StatusServiceUnavailable)
		return
	}
	indexHTML = injectRuntimeConfig(indexHTML)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(indexHTML)
	}
}

func injectRuntimeConfig(html []byte) []byte {
	configScript := []byte(`<script>window.__SDN_CONFIG__={apiBase:"/api/v1",serverBaseUrl:window.location.origin,ipfsDashboardUrl:"/webui/"};</script>`)
	if bytes.Contains(html, []byte("window.__SDN_CONFIG__")) {
		return html
	}
	if idx := bytes.Index(html, []byte("</head>")); idx >= 0 {
		result := make([]byte, 0, len(html)+len(configScript))
		result = append(result, html[:idx]...)
		result = append(result, configScript...)
		result = append(result, html[idx:]...)
		return result
	}
	return append(configScript, html...)
}
