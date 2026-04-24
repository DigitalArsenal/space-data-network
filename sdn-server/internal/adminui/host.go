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

// NewHost returns an SPA file host for the built SDN admin client.
func NewHost(buildDir string) (http.Handler, error) {
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

	fs := http.FileServer(http.Dir(buildDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		clean := path.Clean("/" + r.URL.Path)
		clean = strings.TrimPrefix(clean, "/")
		if clean != "" {
			full := filepath.Join(buildDir, filepath.FromSlash(clean))
			if st, err := os.Stat(full); err == nil && !st.IsDir() {
				w.Header().Set("Cache-Control", "public, max-age=1800")
				fs.ServeHTTP(w, r)
				return
			}
		}

		if ext := path.Ext(r.URL.Path); ext != "" && r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		indexHTML, err := os.ReadFile(indexPath)
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
	}), nil
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
