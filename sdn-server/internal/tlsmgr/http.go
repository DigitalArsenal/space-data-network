package tlsmgr

import (
	"net"
	"net/http"
	"strings"
)

func NewRedirectHandler(httpsBase string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/acme-challenge/" || strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			http.NotFound(w, r)
			return
		}

		target := redirectTarget(r, httpsBase)
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})
}

func (m *Manager) HTTPHandler(httpsAddr string) http.Handler {
	redirect := NewRedirectHandler(httpsAddr)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.RLock()
		acmeMgr := m.acmeManager
		m.mu.RUnlock()

		if acmeMgr == nil {
			redirect.ServeHTTP(w, r)
			return
		}
		acmeMgr.HTTPHandler(redirect).ServeHTTP(w, r)
	})
}

func redirectTarget(r *http.Request, httpsAddr string) string {
	if strings.HasPrefix(httpsAddr, "https://") {
		return strings.TrimRight(httpsAddr, "/") + r.URL.RequestURI()
	}
	host, port := splitHostPortLoose(httpsAddr)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host, _ = splitHostPortLoose(r.Host)
		if host == "" {
			host = r.Host
		}
	}
	if port == "" || port == "443" {
		return "https://" + host + r.URL.RequestURI()
	}
	return "https://" + net.JoinHostPort(host, port) + r.URL.RequestURI()
}

func splitHostPortLoose(value string) (string, string) {
	if value == "" {
		return "", ""
	}
	host, port, err := net.SplitHostPort(value)
	if err == nil {
		return host, port
	}
	return value, ""
}
