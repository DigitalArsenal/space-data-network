package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/adminaddr"
	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// The dial-address rules moved to internal/adminaddr when the in-daemon
// update-signal lane needed the same answers (the daemon hands its own admin
// URL to the helper that will stop and health-check it). These stay as thin
// delegations so every existing call site — and every future one that reaches
// for the familiar name — resolves to the one implementation.

// DialAddrForListenAddr converts an admin LISTEN (bind) address into an address
// the local CLI can actually DIAL. See adminaddr.DialAddrForListenAddr for the
// rules and the 2026-07-28 owner incident that produced them.
func DialAddrForListenAddr(addr string) string { return adminaddr.DialAddrForListenAddr(addr) }

// ExpectedCertHostFor reports the hostname a TLS certificate must cover for a
// dial address, so a failure can name it.
func ExpectedCertHostFor(dialAddr string) string { return adminaddr.ExpectedCertHostFor(dialAddr) }

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
