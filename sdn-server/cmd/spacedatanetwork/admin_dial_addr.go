package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/config"
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

// certAnchorHint names the daemon certificate the CLI trusted and the name it
// verified against, so a mismatch reports BOTH what was tried and what failed.
func certAnchorHint(certPath, serverName string, err error) string {
	if err == nil || certPath == "" {
		return ""
	}
	msg := err.Error()
	if !strings.Contains(msg, "x509:") && !strings.Contains(msg, "certificate") {
		return ""
	}
	if serverName != "" {
		return fmt.Sprintf(" [TLS anchor: trusted the daemon certificate at %s, verified as %q]",
			certPath, serverName)
	}
	return fmt.Sprintf(" [TLS anchor: trusted the daemon certificate at %s]", certPath)
}

// daemonTLSConfig anchors TLS verification to the certificate THE DAEMON'S OWN
// CONFIG declares, for the local-CLI case only.
//
// OWNER, 2026-07-28, after the dial fix landed: the CLI reached
// https://127.0.0.1:443 correctly and then failed with
//
//	x509: certificate signed by unknown authority
//
// because the sidecar serves an origin/self-signed certificate that no system
// root signs. The system root pool is the wrong question to ask about our own
// node: we are not authenticating a stranger on the internet, we are checking
// that the loopback socket is the daemon whose config we just read. That config
// names the exact certificate the daemon serves, so those bytes — and nothing
// wider — are the correct trust anchor.
//
// Hard limits, deliberately:
//   - InsecureSkipVerify is NEVER set. Verification still happens; only the
//     anchor changes, so a wrong or tampered cert still fails.
//   - Applies ONLY when the config came from the running daemon or a system
//     location (Resolution.IsOwnDaemonConfig). A -c/SDN_CONFIG path could
//     describe another node, and must not get to choose our trust anchor.
//   - NEVER applies to a --session-token/SDN_SESSION_TOKEN target: that is an
//     operator pointing at a remote node, where system roots are the right and
//     only anchor.
//
// Returns nil when no daemon certificate is configured or readable, leaving the
// default system-root behaviour untouched.
func daemonTLSConfig(cfg *config.Config, res config.Resolution) (*tls.Config, string, error) {
	if cfg == nil || !res.IsOwnDaemonConfig() {
		return nil, "", nil
	}
	certPath := daemonCertPath(cfg)
	if certPath == "" {
		return nil, "", nil
	}
	pem, err := os.ReadFile(certPath)
	if err != nil {
		// Not fatal: fall back to system roots and let the dial report the
		// real problem, rather than turning an unreadable file into a hard stop.
		return nil, certPath, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, certPath, fmt.Errorf(
			"admin TLS certificate %s contains no usable PEM certificate", certPath)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, certPath, nil
}

// daemonCertPath returns the certificate the daemon serves: the explicit
// tls_cert_file when set, else the managed-TLS material under tls_cache_dir.
func daemonCertPath(cfg *config.Config) string {
	if p := strings.TrimSpace(cfg.Admin.TLSCertFile); p != "" {
		return p
	}
	dir := strings.TrimSpace(cfg.Admin.TLSCacheDir)
	if dir == "" {
		return ""
	}
	for _, name := range []string{"cert.pem", "fullchain.pem", "origin.crt", "bootstrap.crt"} {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// serverNameForCert picks the name to present in SNI/verification.
//
// We dial loopback, but the daemon's certificate may carry only its public
// domain (an origin cert for sdn.spaceaware.io has no 127.0.0.1 SAN). Verifying
// loopback against such a cert fails on the NAME even with the right anchor. So
// if the cert does not cover the dial host, present a name it does cover: the
// connection still goes to loopback, and the identity check still runs against
// the daemon's real certificate. Returns "" to leave ServerName as the dial host.
func serverNameForCert(certPath, dialHost string) string {
	if certPath == "" || dialHost == "" {
		return ""
	}
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return ""
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	if leaf.VerifyHostname(dialHost) == nil {
		return "" // the cert already covers what we dial
	}
	for _, name := range leaf.DNSNames {
		if strings.TrimSpace(name) != "" {
			return name
		}
	}
	if strings.TrimSpace(leaf.Subject.CommonName) != "" {
		return leaf.Subject.CommonName
	}
	return ""
}
