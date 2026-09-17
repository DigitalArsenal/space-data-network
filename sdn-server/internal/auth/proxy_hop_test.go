package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A loopback RemoteAddr proves the LAST HOP was local, not that the request
// was. With a reverse proxy on the same machine — which is the deployed shape,
// nginx terminating TLS on the node's own box — every remote caller on the
// internet arrives from 127.0.0.1, and a gate that reads only RemoteAddr hands
// each of them whatever loopback was supposed to earn.
func TestRequestCrossedAProxyRefusesForwardedRequests(t *testing.T) {
	for _, header := range []string{
		"X-Forwarded-For",
		"X-Real-Ip",
		"Forwarded",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/anything", nil)
		r.RemoteAddr = "127.0.0.1:54321" // what nginx makes every caller look like
		r.Header.Set(header, "203.0.113.7")
		if !requestCrossedAProxy(r) {
			t.Errorf("%s present but the request was not treated as proxied", header)
		}
	}
}

// The headers must never GRANT anything — they are attacker-controlled — so
// their absence is the only case that may proceed, and that still has to pass
// the RemoteAddr check afterwards.
func TestRequestCrossedAProxyAllowsDirectRequests(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/anything", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	if requestCrossedAProxy(r) {
		t.Error("a request with no forwarding headers was treated as proxied")
	}
	if requestCrossedAProxy(nil) {
		t.Error("nil request must not report as proxied")
	}
}

// dev_auto_admin mints a REAL Admin session, so this is the highest-value
// instance of the gate: armed, loopback RemoteAddr, but forwarded — it must
// return nil rather than an Admin session.
func TestDevAutoAdminRefusesProxiedLoopbackRequest(t *testing.T) {
	h := &Handler{devAutoAdmin: true}
	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	if s := h.devAutoAdminSession(r); s != nil {
		t.Fatal("a proxied request was auto-admitted as Admin")
	}
}
