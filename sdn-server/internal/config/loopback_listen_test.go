package config

import (
	"errors"
	"net"
	"testing"
)

// The lane carries UNAUTHENTICATED writes and nginx on the prod host has a
// catch-all `location / -> 127.0.0.1:8443` with no /api/ block, so every /api/**
// route on the proxied listener is internet-reachable. The lane's only protection
// is that its socket is bound to loopback and nginx has no location block for it.
// This test asserts the bind is 127.0.0.1 and never 0.0.0.0.
func TestListenLoopbackBindsLoopbackOnly(t *testing.T) {
	listener, err := ListenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenLoopback(127.0.0.1:0) = %v, want a listener", err)
	}
	defer listener.Close()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is %T, want *net.TCPAddr", listener.Addr())
	}
	if !tcpAddr.IP.IsLoopback() {
		t.Fatalf("bound to %s, want a loopback address", tcpAddr)
	}
	if tcpAddr.IP.IsUnspecified() || tcpAddr.IP.String() == "0.0.0.0" || tcpAddr.IP.String() == "::" {
		t.Fatalf("bound to the wildcard address %s — the lane would be internet-reachable", tcpAddr)
	}
	if got := tcpAddr.IP.String(); got != "127.0.0.1" {
		t.Fatalf("bound to %s, want 127.0.0.1", got)
	}
}

// A non-loopback lane address must be refused outright — never bound and served.
func TestListenLoopbackRefusesPublicBind(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0", ":0", "localhost:0"} {
		t.Run(addr, func(t *testing.T) {
			listener, err := ListenLoopback(addr)
			if err == nil {
				listener.Close()
				t.Fatalf("ListenLoopback(%q) bound a socket; want a refusal", addr)
			}
			if !errors.Is(err, ErrListenAddrNotLoopback) {
				t.Fatalf("error should wrap ErrListenAddrNotLoopback, got: %v", err)
			}
		})
	}
}

// The local publish lane carries unauthenticated writes, so its listen address is
// a security boundary: anything that is not a literal loopback IP must be a fatal
// config error rather than a silent public bind.
func TestValidateLoopbackListenAddr(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{"ipv4 loopback", "127.0.0.1:5011", false},
		{"ipv4 loopback alternate", "127.0.0.2:5011", false},
		{"ipv6 loopback", "[::1]:5011", false},

		{"empty", "", true},
		{"all interfaces v4", "0.0.0.0:5011", true},
		{"all interfaces v6", "[::]:5011", true},
		{"no host binds everything", ":5011", true},
		{"routable ip", "203.0.113.9:5011", true},
		{"private lan ip", "10.0.0.5:5011", true},
		{"hostname is not a boundary", "localhost:5011", true},
		{"public hostname", "sdn.spaceaware.io:5011", true},
		{"no port", "127.0.0.1", true},
		{"empty port", "127.0.0.1:", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLoopbackListenAddr(tc.addr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateLoopbackListenAddr(%q) = nil, want error", tc.addr)
				}
				if !errors.Is(err, ErrListenAddrNotLoopback) {
					t.Fatalf("error should wrap ErrListenAddrNotLoopback, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateLoopbackListenAddr(%q) = %v, want nil", tc.addr, err)
			}
		})
	}
}
