package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// THE OWNER'S FAILURE. host-01 binds admin on 0.0.0.0:443; the CLI built
// https://0.0.0.0:443 and TLS died with "certificate is valid for 127.0.0.1,
// not 0.0.0.0". A wildcard bind is not a destination.
func TestWildcardBindIsNeverDialed(t *testing.T) {
	for name, tc := range map[string]struct{ listen, want string }{
		"host shape (owner's)":  {"0.0.0.0:443", "127.0.0.1:443"},
		"container shape":       {"0.0.0.0:5001", "127.0.0.1:5001"},
		"ipv6 wildcard":         {"[::]:443", "127.0.0.1:443"},
		"ipv6 wildcard no bkts": {"::", "127.0.0.1"},
		"port only":             {":5001", "127.0.0.1:5001"},
		"bare wildcard":         {"0.0.0.0", "127.0.0.1"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := DialAddrForListenAddr(tc.listen); got != tc.want {
				t.Errorf("DialAddrForListenAddr(%q) = %q, want %q", tc.listen, got, tc.want)
			}
		})
	}
}

// A concrete bind is the operator's intent and must survive untouched —
// silently rewriting it to loopback would aim the CLI at a different node.
func TestConcreteBindIsNotRewritten(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1:5001",
		"10.0.0.5:443",
		"192.168.1.20:5001",
		"sdn.spaceaware.io:443",
		"[::1]:443",
	} {
		if got := DialAddrForListenAddr(addr); got != addr {
			t.Errorf("DialAddrForListenAddr(%q) = %q, want it unchanged", addr, got)
		}
	}
}

// End to end through the URL builder the client actually uses.
func TestAdminURLMapsWildcardBind(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.ListenAddr = "0.0.0.0:443"
	cfg.Admin.TLSEnabled = true

	got := adminURL(cfg)
	if strings.Contains(got, "0.0.0.0") {
		t.Fatalf("adminURL = %q; a wildcard bind must never appear in a dial URL", got)
	}
	if got != "https://127.0.0.1:443/" {
		t.Errorf("adminURL = %q, want https://127.0.0.1:443/", got)
	}
}

func TestAdminURLKeepsConcreteBind(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.ListenAddr = "10.0.0.5:5001"
	if got := adminURL(cfg); !strings.Contains(got, "10.0.0.5:5001") {
		t.Errorf("adminURL = %q, want the operator's own host preserved", got)
	}
}

// The error must name the host the cert has to cover; the owner's original
// message never said what the CLI should have dialed.
func TestCertHostHintNamesExpectedHost(t *testing.T) {
	tlsErr := errors.New("tls: failed to verify certificate: x509: certificate is valid for 127.0.0.1, not 0.0.0.0")
	hint := certHostHint("https://127.0.0.1:443", tlsErr)
	if !strings.Contains(hint, "127.0.0.1") {
		t.Errorf("hint %q does not name the expected certificate host", hint)
	}

	if got := certHostHint("https://127.0.0.1:443", errors.New("connection refused")); got != "" {
		t.Errorf("non-TLS failure produced a TLS hint: %q", got)
	}
	if got := certHostHint("https://127.0.0.1:443", nil); got != "" {
		t.Errorf("nil error produced a hint: %q", got)
	}
}
