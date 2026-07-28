package main

import (
	"fmt"
	"net"
	"strings"
)

// DialAddrForListenAddr converts an admin LISTEN (bind) address into an address
// the local CLI can actually DIAL.
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
// a wrong dial target and a TLS name mismatch, which is exactly what the owner
// hit on the one command he needed to work.
//
// So: an unspecified/wildcard host is rewritten to loopback, port preserved.
// Loopback is right for the local-CLI case specifically — the CLI is running ON
// the host, a wildcard listener is by definition listening on loopback too, and
// loopback is the name the daemon's self-signed cert actually carries. Both
// 0.0.0.0 and :: map to 127.0.0.1: a Go dual-stack listener on :: accepts IPv4
// loopback, and 127.0.0.1 is the SAN the cert has, so this is the one choice
// that satisfies the socket AND the certificate.
//
// A concrete host (10.0.0.5:443, example.com:443) is NEVER rewritten — the
// operator meant that host, and silently redirecting it to loopback would point
// the CLI at a different node, the exact class of bug the resolver work exists
// to end.
func DialAddrForListenAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return addr
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port (or malformed). A bare wildcard is still undialable.
		if isUnspecifiedHost(addr) {
			return "127.0.0.1"
		}
		return addr
	}
	if isUnspecifiedHost(host) {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

// isUnspecifiedHost reports whether a host is a wildcard/unspecified bind:
// empty (":443"), 0.0.0.0, ::, or the bracketed [::] spelling.
func isUnspecifiedHost(host string) bool {
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

// certHostHint appends the hostname the daemon's certificate must cover, but
// only when the failure is actually a TLS name mismatch.
//
// The owner's error read "certificate is valid for 127.0.0.1, not 0.0.0.0",
// which states what the cert has and what was attempted but not what the CLI
// should have been dialing. When the names disagree, say the expected host
// outright so the next operator does not have to reason it out.
func certHostHint(baseURL string, err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if !strings.Contains(msg, "certificate is valid for") &&
		!strings.Contains(msg, "x509:") {
		return ""
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	trimmed = strings.TrimSuffix(trimmed, "/")
	host := ExpectedCertHostFor(trimmed)
	if host == "" {
		return ""
	}
	return fmt.Sprintf(" [TLS: the daemon certificate must be valid for %q; "+
		"a wildcard bind such as 0.0.0.0 is never dialable and no certificate can cover it]", host)
}
