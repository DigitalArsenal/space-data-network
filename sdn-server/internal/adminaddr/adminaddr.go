// Package adminaddr turns the daemon's admin BIND address into an address that
// can actually be DIALLED, and into the URL a local process uses to reach its
// own daemon.
//
// It was extracted from cmd/spacedatanetwork/admin_dial_addr.go verbatim (which
// now delegates here) when the in-daemon update-signal lane needed the same
// answer: the daemon must hand its own admin URL to the helper that will stop
// and health-check it. Two implementations of "which address is my daemon" is
// how a box ends up with a CLI that can reach it and an updater that cannot —
// which is precisely the failure this package's own case law is about.
package adminaddr

import (
	"fmt"
	"net"
	"strings"
)

// DialAddrForListenAddr converts an admin LISTEN (bind) address into an address
// a local client can actually DIAL.
//
// OWNER, 2026-07-28, running `sudo spacedatanetwork accounts list` on host-01:
// the resolver did its job — it found the running daemon and printed the config
// provenance — and then the client tried to reach
//
//	https://0.0.0.0:443
//
// and died with: certificate is valid for 127.0.0.1, not 0.0.0.0.
//
// The bug is a category error: 0.0.0.0 is a BIND address meaning "every
// interface", never a destination. Nothing serves 0.0.0.0; no certificate can
// be valid for it. Passing a wildcard bind straight into a URL guarantees both
// a wrong dial target and a TLS name mismatch.
//
// So: an unspecified/wildcard host is rewritten to loopback, port preserved.
// Both 0.0.0.0 and :: map to 127.0.0.1: a Go dual-stack listener on :: accepts
// IPv4 loopback, and 127.0.0.1 is the SAN a self-signed daemon cert carries, so
// this is the one choice that satisfies the socket AND the certificate.
//
// A concrete host (10.0.0.5:443, example.com:443) is NEVER rewritten — the
// operator meant that host, and silently redirecting it to loopback would point
// the client at a different node.
func DialAddrForListenAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port (or malformed). A bare wildcard is still undialable.
		if IsUnspecifiedHost(addr) {
			return "127.0.0.1"
		}
		return addr
	}
	if IsUnspecifiedHost(host) {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

// IsUnspecifiedHost reports whether a host is a wildcard/unspecified bind:
// empty (":443"), 0.0.0.0, ::, or the bracketed [::] spelling.
func IsUnspecifiedHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return true
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if host == "" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsUnspecified()
	}
	return false
}

// ExpectedCertHostFor reports the hostname a TLS certificate must cover for a
// dial address, so a failure can name it instead of leaving the operator to
// infer it from a raw x509 error.
func ExpectedCertHostFor(dialAddr string) string {
	if host, _, err := net.SplitHostPort(strings.TrimSpace(dialAddr)); err == nil {
		return host
	}
	return strings.TrimSpace(dialAddr)
}

// LocalAdminURL builds the base URL a process on this box uses to reach its own
// daemon's admin surface. listenAddr is the config's bind address; tlsEnabled
// selects the scheme.
func LocalAdminURL(listenAddr string, tlsEnabled bool) string {
	addr := "127.0.0.1:5001"
	if trimmed := strings.TrimSpace(listenAddr); trimmed != "" {
		addr = DialAddrForListenAddr(trimmed)
	}
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/", scheme, addr)
}
