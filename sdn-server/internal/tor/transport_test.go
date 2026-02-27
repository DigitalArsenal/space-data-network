package tor

import (
	"net"
	"net/http"
	"testing"
)

func TestNewTorTransportCreatesTransport(t *testing.T) {
	// We can create a transport pointing at any address without actually
	// connecting; the SOCKS5 dial only happens on request.
	tr, err := NewTorTransport("127.0.0.1:9050")
	if err != nil {
		t.Fatalf("NewTorTransport failed: %v", err)
	}
	if tr == nil {
		t.Fatal("expected non-nil transport")
	}
	if tr.DialContext == nil {
		t.Fatal("expected DialContext to be set")
	}
	// Proxy should be nil — we bypass env proxy and dial SOCKS directly.
	if tr.Proxy != nil {
		t.Fatal("expected Proxy to be nil for direct SOCKS dialing")
	}
}

func TestNewFallbackTransportClearnet(t *testing.T) {
	rt, err := NewFallbackTransport("127.0.0.1:9050", OutboundClearnet)
	if err != nil {
		t.Fatalf("clearnet mode failed: %v", err)
	}
	if rt != http.DefaultTransport {
		t.Fatal("clearnet mode should return http.DefaultTransport")
	}
}

func TestNewFallbackTransportTorOnly(t *testing.T) {
	rt, err := NewFallbackTransport("127.0.0.1:9050", OutboundTorOnly)
	if err != nil {
		t.Fatalf("tor-only mode failed: %v", err)
	}
	if rt == nil {
		t.Fatal("expected non-nil transport")
	}
	// Should be a raw *http.Transport, not a fallbackTransport.
	if _, ok := rt.(*http.Transport); !ok {
		t.Fatalf("tor-only should return *http.Transport, got %T", rt)
	}
}

func TestNewFallbackTransportTorPreferred(t *testing.T) {
	rt, err := NewFallbackTransport("127.0.0.1:9050", OutboundTorPreferred)
	if err != nil {
		t.Fatalf("tor-preferred mode failed: %v", err)
	}
	if rt == nil {
		t.Fatal("expected non-nil transport")
	}
	// Should be a fallbackTransport.
	if _, ok := rt.(*fallbackTransport); !ok {
		t.Fatalf("tor-preferred should return *fallbackTransport, got %T", rt)
	}
}

func TestNewFallbackTransportUnknownMode(t *testing.T) {
	_, err := NewFallbackTransport("127.0.0.1:9050", OutboundMode("bogus"))
	if err == nil {
		t.Fatal("expected error for unknown outbound mode")
	}
}

func TestRuntimeTransport_NilRuntime(t *testing.T) {
	var r *Runtime
	tr, err := r.Transport()
	if err != nil {
		t.Fatalf("nil runtime should not error: %v", err)
	}
	if tr != nil {
		t.Fatal("nil runtime should return nil transport")
	}
}

func TestRuntimeTransport_EmptyProxy(t *testing.T) {
	r := &Runtime{proxyURL: ""}
	tr, err := r.Transport()
	if err != nil {
		t.Fatalf("empty proxy should not error: %v", err)
	}
	if tr != nil {
		t.Fatal("empty proxy should return nil transport")
	}
}

func TestRuntimeTransport_ValidProxy(t *testing.T) {
	r := &Runtime{proxyURL: "socks5h://127.0.0.1:9050"}
	tr, err := r.Transport()
	if err != nil {
		t.Fatalf("valid proxy failed: %v", err)
	}
	if tr == nil {
		t.Fatal("expected non-nil transport")
	}
}

func TestIsDialError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"net.OpError", &net.OpError{Op: "dial", Err: &net.DNSError{}}, true},
		{"net.DNSError", &net.DNSError{Err: "no such host"}, true},
		{"generic error", http.ErrAbortHandler, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDialError(tt.err)
			if got != tt.want {
				t.Errorf("isDialError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestOutboundModeConstants(t *testing.T) {
	// Verify the string values match what will appear in YAML config.
	if OutboundTorOnly != "tor-only" {
		t.Fatalf("OutboundTorOnly = %q", OutboundTorOnly)
	}
	if OutboundTorPreferred != "tor-preferred" {
		t.Fatalf("OutboundTorPreferred = %q", OutboundTorPreferred)
	}
	if OutboundClearnet != "clearnet" {
		t.Fatalf("OutboundClearnet = %q", OutboundClearnet)
	}
}
