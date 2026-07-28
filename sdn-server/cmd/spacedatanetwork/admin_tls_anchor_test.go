package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// writeSelfSignedCert emits a cert/key pair covering the given hosts, standing
// in for the origin/self-signed certificate the sidecar actually serves.
func writeSelfSignedCert(t *testing.T, dir string, dnsNames []string, ips []string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "sdn-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              dnsNames,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	for _, ip := range ips {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP(ip))
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPath = filepath.Join(dir, "origin.crt")
	keyPath = filepath.Join(dir, "origin.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

func daemonResolution() config.Resolution {
	return config.Resolution{Path: "/etc/space-data-network/config.yaml",
		Source: config.ConfigSource("running daemon (pid 1234)"), Exists: true}
}

// THE OWNER'S SECOND FAILURE: dial was right, but verification died with
// "x509: certificate signed by unknown authority" because no system root signs
// the daemon's origin cert. Anchoring to the configured cert must fix it — and
// must actually complete a TLS handshake, not merely build a pool.
func TestDaemonCertAnchorVerifiesRealHandshake(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedCert(t, dir, nil, []string{"127.0.0.1"})

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load pair: %v", err)
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	srv.StartTLS()
	defer srv.Close()

	cfg := config.Default()
	cfg.Admin.TLSEnabled = true
	cfg.Admin.TLSCertFile = certPath

	tlsCfg, gotPath, err := daemonTLSConfig(cfg, daemonResolution())
	if err != nil {
		t.Fatalf("daemonTLSConfig: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("no TLS config produced for the daemon's own cert")
	}
	if gotPath != certPath {
		t.Errorf("cert path = %q, want %q", gotPath, certPath)
	}
	if tlsCfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify was set — verification must remain ON")
	}

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}, Timeout: 10 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("handshake against the daemon cert failed: %v", err)
	}
	resp.Body.Close()
}

// A cert that is NOT the daemon's must still fail — the anchor narrows trust,
// it does not disable it.
func TestForeignCertStillFails(t *testing.T) {
	serverDir, clientDir := t.TempDir(), t.TempDir()
	srvCert, srvKey := writeSelfSignedCert(t, serverDir, nil, []string{"127.0.0.1"})
	otherCert, _ := writeSelfSignedCert(t, clientDir, nil, []string{"127.0.0.1"})

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	pair, err := tls.LoadX509KeyPair(srvCert, srvKey)
	if err != nil {
		t.Fatalf("load pair: %v", err)
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	srv.StartTLS()
	defer srv.Close()

	cfg := config.Default()
	cfg.Admin.TLSCertFile = otherCert // anchored to the WRONG cert
	tlsCfg, _, err := daemonTLSConfig(cfg, daemonResolution())
	if err != nil || tlsCfg == nil {
		t.Fatalf("daemonTLSConfig: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}, Timeout: 10 * time.Second}
	if _, err := client.Get(srv.URL); err == nil {
		t.Fatal("a foreign certificate verified successfully — trust was widened, not narrowed")
	}
}

// A -c / SDN_CONFIG path must NOT get to choose the trust anchor: it can point
// at a config describing someone else's node.
func TestOperatorSuppliedConfigDoesNotAnchorTrust(t *testing.T) {
	dir := t.TempDir()
	certPath, _ := writeSelfSignedCert(t, dir, nil, []string{"127.0.0.1"})
	cfg := config.Default()
	cfg.Admin.TLSCertFile = certPath

	for _, src := range []config.ConfigSource{config.SourceFlag, config.SourceEnv, config.SourceHome} {
		res := config.Resolution{Source: src, Exists: true}
		if res.IsOwnDaemonConfig() {
			t.Errorf("%q must not count as the daemon's own config", src)
		}
		tlsCfg, _, err := daemonTLSConfig(cfg, res)
		if err != nil {
			t.Fatalf("daemonTLSConfig: %v", err)
		}
		if tlsCfg != nil {
			t.Errorf("%q produced a trust anchor; system roots must be kept", src)
		}
	}
}

// Both local tiers DO qualify.
func TestLocalTiersAnchorTrust(t *testing.T) {
	if !(config.Resolution{Source: config.SourceSystem}).IsOwnDaemonConfig() {
		t.Error("system tier must count as the daemon's own config")
	}
	if !(config.Resolution{Source: config.ConfigSource("running daemon (pid 77)")}).IsOwnDaemonConfig() {
		t.Error("running-daemon tier must count as the daemon's own config")
	}
}

// CONTAINER SHAPE: managed TLS with no explicit tls_cert_file — the material
// lives under tls_cache_dir.
func TestContainerShapeFindsCertInCacheDir(t *testing.T) {
	dir := t.TempDir()
	certPath, _ := writeSelfSignedCert(t, dir, nil, []string{"127.0.0.1"})
	if err := os.Rename(certPath, filepath.Join(dir, "cert.pem")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	cfg := config.Default()
	cfg.Admin.TLSCertFile = ""
	cfg.Admin.TLSCacheDir = dir

	tlsCfg, gotPath, err := daemonTLSConfig(cfg, daemonResolution())
	if err != nil {
		t.Fatalf("daemonTLSConfig: %v", err)
	}
	if tlsCfg == nil {
		t.Fatal("managed-TLS material under tls_cache_dir was not found")
	}
	if filepath.Base(gotPath) != "cert.pem" {
		t.Errorf("cert path = %q, want the cache-dir cert.pem", gotPath)
	}
}

// A cert carrying ONLY the public domain: we still dial loopback, but must
// verify under a name the cert actually has.
func TestServerNameFallsBackToCertSAN(t *testing.T) {
	dir := t.TempDir()
	certPath, _ := writeSelfSignedCert(t, dir, []string{"sdn.spaceaware.io"}, nil)
	if got := serverNameForCert(certPath, "127.0.0.1"); got != "sdn.spaceaware.io" {
		t.Errorf("serverName = %q, want the cert's own SAN sdn.spaceaware.io", got)
	}
}

// When the cert already covers the dial host, do not override ServerName.
func TestServerNameEmptyWhenCertCoversDialHost(t *testing.T) {
	dir := t.TempDir()
	certPath, _ := writeSelfSignedCert(t, dir, nil, []string{"127.0.0.1"})
	if got := serverNameForCert(certPath, "127.0.0.1"); got != "" {
		t.Errorf("serverName = %q, want empty (cert already covers the dial host)", got)
	}
}

// No cert configured: leave the default system-root behaviour alone.
func TestNoConfiguredCertLeavesSystemRoots(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.TLSCertFile = ""
	cfg.Admin.TLSCacheDir = ""
	tlsCfg, path, err := daemonTLSConfig(cfg, daemonResolution())
	if err != nil || tlsCfg != nil || path != "" {
		t.Errorf("expected no anchor; got tlsCfg=%v path=%q err=%v", tlsCfg != nil, path, err)
	}
}

// The failure must name the cert that was trusted and the name verified.
func TestCertAnchorHintNamesBoth(t *testing.T) {
	err := errX509("x509: certificate is valid for sdn.spaceaware.io, not 127.0.0.1")
	hint := certAnchorHint("/etc/spacedatanetwork/tls/origin.crt", "sdn.spaceaware.io", err)
	for _, want := range []string{"/etc/spacedatanetwork/tls/origin.crt", "sdn.spaceaware.io"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q missing %q", hint, want)
		}
	}
	if got := certAnchorHint("", "", err); got != "" {
		t.Errorf("no anchor configured should produce no hint, got %q", got)
	}
}

type errX509 string

func (e errX509) Error() string { return string(e) }
