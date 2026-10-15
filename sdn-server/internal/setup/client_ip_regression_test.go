package setup

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// SEC-062: the setup lane keyed its rate limit and audit IP on the leftmost
// X-Forwarded-For entry, which the client writes.
func TestSEC062_SetupClientIPIsTheProxyAppendedEntry(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/setup", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.50")
	if got := getClientIP(r); got != "203.0.113.50" {
		t.Fatalf("getClientIP = %q, want the proxy-appended 203.0.113.50", got)
	}
	r.RemoteAddr = "198.51.100.7:40000"
	if got := getClientIP(r); got != "198.51.100.7" {
		t.Fatalf("getClientIP from a remote peer = %q, want its own address", got)
	}
}
