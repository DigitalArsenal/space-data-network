package api

import (
	"fmt"
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

	// The proxy appends the address it saw; everything to its left is what
	// the client sent, so the client is the RIGHTMOST entry (SEC-062).
	proxied := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	proxied.RemoteAddr = "127.0.0.1:40000"
	proxied.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.2")
	if got := clientIP(proxied); got != "10.0.0.2" {
		t.Fatalf("loopback proxy forwarding keyed as %q, want the proxy-appended 10.0.0.2", got)
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

// One IPv6 subscriber holds a whole /64. Keyed per address, it had 2^64 fresh
// write buckets (security audit 2026-10-15, round 2 note on SEC-062). Uses
// only APIs on origin/main.
func TestR2_WriteRateLimitBucketsIPv6By64(t *testing.T) {
	rl := newRateLimiter()
	limited := false
	for i := 0; i < writeLimitPerMin+5; i++ {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/publish", nil)
		r.RemoteAddr = fmt.Sprintf("[2001:db8:7:9::%x]:443", i+1)
		if !rl.Allow(httptest.NewRecorder(), r) {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("%d writes from one /64 were never rate limited", writeLimitPerMin+5)
	}
	// A different /64 is a different client.
	r := httptest.NewRequest(http.MethodPost, "/api/v1/publish", nil)
	r.RemoteAddr = "[2001:db8:7:a::1]:443"
	if !rl.Allow(httptest.NewRecorder(), r) {
		t.Fatalf("a neighbouring /64 shared the bucket")
	}
}
