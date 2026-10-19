package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// TestCheckSameOriginHost locks in gap B10.3(b): the pubsub bridge used to
// accept every Origin unconditionally (CheckOrigin: func(r) bool { return
// true }), which allowed a cross-origin page to ride a browser's session
// cookie into a WebSocket connection (cross-site WebSocket hijacking). The
// replacement, checkSameOriginHost, must reject any Origin whose host
// differs from the request's Host, while still admitting non-browser
// clients that omit Origin entirely (matching gorilla/websocket's own
// documented secure default).
func TestCheckSameOriginHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"no origin header is allowed", "sdn.example:8443", "", true},
		{"same host and port is allowed", "sdn.example:8443", "https://sdn.example:8443", true},
		{"same host default https port is allowed", "sdn.example", "https://sdn.example", true},
		{"different host is rejected", "sdn.example:8443", "https://evil.example:8443", false},
		{"different port is rejected", "sdn.example:8443", "https://sdn.example:9443", false},
		{"malformed origin is rejected", "sdn.example:8443", "://not a url", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if got := checkSameOriginHost(req); got != tc.want {
				t.Fatalf("checkSameOriginHost(host=%q, origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}

// TestWSHandlerRejectsCrossOriginUpgrade dials a real WebSocket handshake
// against a live WSHandler with an Origin header naming a different host
// than the test server. The upgrade must fail (no 101 Switching Protocols).
func TestWSHandlerRejectsCrossOriginUpgrade(t *testing.T) {
	t.Parallel()

	handler := NewWSHandler(nil, nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	header := http.Header{}
	header.Set("Origin", "http://evil.example")

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("cross-origin WebSocket dial succeeded, want rejection")
	}
	if resp == nil {
		t.Fatal("expected an HTTP response on dial failure")
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		t.Fatalf("cross-origin dial got 101 Switching Protocols, want a non-upgrade response")
	}
}

// TestWSHandlerAllowsSameOriginSubscribeRoundTrip dials a real WebSocket
// handshake with an Origin header matching the test server's own host, then
// exercises the existing subscribe/publish round trip end to end to confirm
// the CheckOrigin tightening did not break legitimate same-origin use.
