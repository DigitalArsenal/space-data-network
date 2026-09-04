package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// SEC-05: a remote peer cannot choose its rate-limit bucket by writing
// X-Forwarded-For; only a loopback reverse proxy's forwarding headers count.
func TestClientIPHonoursForwardingHeadersOnlyFromLoopback(t *testing.T) {
	remote := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	remote.RemoteAddr = "203.0.113.7:51000"
	remote.Header.Set("X-Forwarded-For", "10.0.0.1")
	if got := clientIP(remote); got != "203.0.113.7" {
		t.Fatalf("remote peer with spoofed X-Forwarded-For keyed as %q, want its own address", got)
	}

	proxied := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	proxied.RemoteAddr = "127.0.0.1:40000"
	proxied.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.2")
	if got := clientIP(proxied); got != "198.51.100.9" {
		t.Fatalf("loopback proxy forwarding keyed as %q, want the original client", got)
	}

	realIP := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	realIP.RemoteAddr = "[::1]:40000"
	realIP.Header.Set("X-Real-IP", "198.51.100.10")
	if got := clientIP(realIP); got != "198.51.100.10" {
		t.Fatalf("X-Real-IP from loopback keyed as %q", got)
	}

	direct := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	direct.RemoteAddr = "192.0.2.4:1234"
	if got := clientIP(direct); got != "192.0.2.4" {
		t.Fatalf("direct peer keyed as %q", got)
	}
}
