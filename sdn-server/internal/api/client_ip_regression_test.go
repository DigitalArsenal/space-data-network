package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// SEC-062: behind the local reverse proxy (nginx $proxy_add_x_forwarded_for)
// the leftmost X-Forwarded-For entry is whatever the client sent, so taking it
// gave every request a fresh rate-limit bucket for the price of a header.
func TestSEC062_ForgedForwardedForGetsNoFreshBucket(t *testing.T) {
	seen := map[string]bool{}
	for _, forged := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "garbage"} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
		r.RemoteAddr = "127.0.0.1:40000"
		r.Header.Set("X-Forwarded-For", forged+", 203.0.113.50")
		seen[clientIP(r)] = true
	}
	if len(seen) != 1 || !seen["203.0.113.50"] {
		t.Fatalf("forged leftmost entries keyed as %v, want only the proxy-appended 203.0.113.50", seen)
	}
}

// The dataset-updates lanes are loopback-only; every forwarding header, not
// only the four the lane used to list, means a proxy is in front of them.
func TestSEC062_LocalOnlyLanesRefuseEveryForwardingHeader(t *testing.T) {
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto", "Via", "CF-Connecting-IP", "True-Client-IP"} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/publish", nil)
		r.RemoteAddr = "127.0.0.1:40000"
		r.Header.Set(h, "203.0.113.9")
		if err := requireLoopbackClient(r); err == nil {
			t.Fatalf("%s present: the local-only lane admitted a proxied request", h)
		}
	}
	for _, remote := range []string{"[::ffff:203.0.113.9]:1", "[fe80::1%lo0]:1", "203.0.113.9:1"} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/publish", nil)
		r.RemoteAddr = remote
		if err := requireLoopbackClient(r); err == nil {
			t.Fatalf("%s: the local-only lane admitted a non-loopback peer", remote)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/publish", nil)
	r.RemoteAddr = "[::ffff:127.0.0.1]:40000"
	if err := requireLoopbackClient(r); err != nil {
		t.Fatalf("an IPv4-mapped loopback peer was refused: %v", err)
	}
}
