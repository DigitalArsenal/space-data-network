package update

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The update shutdown control is loopback-only, and its handler is the only
// gate on a require_auth:false node. Behind a reverse proxy on this box every
// internet client arrives from 127.0.0.1, so a forwarding header must refuse
// before the request is even read (SEC-062 local-only re-check).
func TestControlShutdownRefusesAProxiedLoopbackRequest(t *testing.T) {
	called := false
	h := NewControlHandler(ControlHandlerOptions{BundleRoot: t.TempDir(), Shutdown: func() { called = true }})
	for _, header := range []string{"X-Forwarded-For", "Forwarded", "X-Real-IP", "X-Forwarded-Proto", "Via"} {
		r := httptest.NewRequest(http.MethodPost, ControlShutdownPath, strings.NewReader(`{"token":"x","bundleRoot":"/"}`))
		r.RemoteAddr = "127.0.0.1:40000"
		r.Header.Set(header, "203.0.113.9")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s present: status %d, want 403", header, w.Code)
		}
	}
	if called {
		t.Fatalf("shutdown ran for a proxied request")
	}
}
