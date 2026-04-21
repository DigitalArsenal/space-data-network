# HTTPS Bootstrap And Managed ACME Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add native HTTPS bootstrap and explicit-hostname ACME management to `sdn-server`, including self-signed first-boot certificates, signed node-identity proof extensions, secure cookies under TLS, and a login/admin surface that exposes TLS trust state and bootstrap certificate download.

**Architecture:** Introduce a dedicated `sdn-server/internal/tlsmgr` package that owns bootstrap certificate generation, proof-extension encoding, active-certificate selection, and managed-host persistence. Refactor `cmd/spacedatanetwork/main.go` to run HTTPS through a `tls.Config.GetCertificate` selector and pair it with an HTTP port-80 challenge/redirect server. Extend `internal/auth/login_page.go` and the admin API/UI so operators can inspect TLS state, download the bootstrap cert, configure explicit hostnames, and trigger ACME issuance while existing session cookies become strictly `Secure` whenever native TLS is active.

**Tech Stack:** Go `crypto/x509`, `crypto/tls`, `encoding/asn1`, `golang.org/x/crypto/acme/autocert`, existing `sdn-server/internal/auth`, `sdn-js` hosted UI, Go `httptest`, Vitest, Chrome MCP verification.

---

## File Structure

### New files

- `sdn-server/internal/tlsmgr/bootstrap.go`
  - bootstrap certificate generation/loading and persistence
- `sdn-server/internal/tlsmgr/binding.go`
  - custom ASN.1 extension encoding, signature creation, and verification helpers
- `sdn-server/internal/tlsmgr/manager.go`
  - runtime selection between bootstrap and managed certs, hostname state, ACME setup
- `sdn-server/internal/tlsmgr/http.go`
  - HTTP-01/challenge handler and HTTP-to-HTTPS redirect handler
- `sdn-server/internal/tlsmgr/status.go`
  - serializable TLS status model for login/admin surfaces
- `sdn-server/internal/tlsmgr/bootstrap_test.go`
- `sdn-server/internal/tlsmgr/binding_test.go`
- `sdn-server/internal/tlsmgr/manager_test.go`
- `sdn-server/internal/tlsmgr/http_test.go`
- `sdn-server/internal/api/tls.go`
  - admin TLS/hostname API
- `sdn-server/internal/api/tls_test.go`
- `sdn-js/src/ui/runtime/tls-settings.ts`
  - client-side fetch + render helpers for TLS settings
- `sdn-js/src/ui/runtime/tls-settings.test.ts`

### Modified files

- `sdn-server/internal/config/config.go`
  - new `tls_mode`, `tls_hosts`, `tls_cache_dir`, `http_challenge_addr`
- `sdn-server/cmd/spacedatanetwork/main.go`
  - native TLS startup rewrite and dual listener wiring
- `sdn-server/cmd/spacedatanetwork/main_test.go`
  - HTTPS redirect, cert-selection, and bootstrap download tests
- `sdn-server/internal/auth/handler.go`
  - expose TLS status/bootstrap cert handlers
- `sdn-server/internal/auth/login_page.go`
  - render TLS trust block
- `sdn-server/internal/auth/handler_test.go`
  - login page trust block and bootstrap download assertions
- `sdn-server/internal/auth/middleware.go`
  - no behavior change beyond using secure-cookie logic from TLS mode
- `sdn-server/internal/auth/sessions.go`
  - no schema change expected, but validate session-cookie compatibility
- `scripts/admin-dev.sh`
  - boot local dev with managed/bootstrap TLS instead of HTTP
- `scripts/dev-local.sh`
  - print HTTPS URLs and bootstrap certificate path/download
- `config/dev.yaml`
- `config/dev-docker.yaml`
- `config/full-vm.yaml`
- `config/full-docker.yaml`
- `README.md`

---

### Task 1: Extend config and add a managed TLS runtime model

**Files:**
- Create: `sdn-server/internal/tlsmgr/manager.go`
- Test: `sdn-server/internal/tlsmgr/manager_test.go`
- Modify: `sdn-server/internal/config/config.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`

- [ ] **Step 1: Write the failing config/manager tests**

Add a new test file:

```go
package tlsmgr

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

func TestConfigTLSMode_BackfillsManagedModeFromLegacyTLSEnabled(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.TLSEnabled = true
	cfg.Admin.TLSCertFile = ""
	cfg.Admin.TLSKeyFile = ""

	if got := cfg.Admin.EffectiveTLSMode(); got != "managed" {
		t.Fatalf("EffectiveTLSMode() = %q, want %q", got, "managed")
	}
}

func TestConfigTLSMode_BackfillsStaticModeWhenLegacyFilesPresent(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.TLSEnabled = true
	cfg.Admin.TLSCertFile = "/tmp/cert.pem"
	cfg.Admin.TLSKeyFile = "/tmp/key.pem"

	if got := cfg.Admin.EffectiveTLSMode(); got != "static" {
		t.Fatalf("EffectiveTLSMode() = %q, want %q", got, "static")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
../scripts/go-with-wasmedge.sh test ./internal/tlsmgr -run 'TestConfigTLSMode' -count=1
```

Expected: FAIL with compile errors because `internal/tlsmgr` and `AdminConfig.EffectiveTLSMode` do not exist yet.

- [ ] **Step 3: Add config fields and compatibility helpers**

Update `sdn-server/internal/config/config.go` so `AdminConfig` grows the new fields and helper:

```go
type AdminConfig struct {
	Enabled           bool     `yaml:"enabled"`
	ListenAddr        string   `yaml:"listen_addr"`
	HTTPChallengeAddr string   `yaml:"http_challenge_addr"`
	RequireAuth       bool     `yaml:"require_auth"`
	SessionExpiry     string   `yaml:"session_expiry"`
	TOTPRequired      bool     `yaml:"totp_required"`
	TLSMode           string   `yaml:"tls_mode"`
	TLSEnabled        bool     `yaml:"tls_enabled"`
	TLSCertFile       string   `yaml:"tls_cert_file"`
	TLSKeyFile        string   `yaml:"tls_key_file"`
	TLSCacheDir       string   `yaml:"tls_cache_dir"`
	TLSHosts          []string `yaml:"tls_hosts"`
	FrontendPath      string   `yaml:"frontend_path"`
	AdminUIPath       string   `yaml:"admin_ui_path"`
	HomepageFile      string   `yaml:"homepage_file"`
	WebuiPath         string   `yaml:"webui_path"`
	IPFSAPIURL        string   `yaml:"ipfs_api_url"`
	IPFSGatewayURL    string   `yaml:"ipfs_gateway_url"`
	WalletUIPath      string   `yaml:"wallet_ui_path"`
	TrustedProxy      string   `yaml:"trusted_proxy"`
}

func (a AdminConfig) EffectiveTLSMode() string {
	mode := strings.ToLower(strings.TrimSpace(a.TLSMode))
	if mode != "" {
		return mode
	}
	if !a.TLSEnabled {
		return "disabled"
	}
	if strings.TrimSpace(a.TLSCertFile) != "" && strings.TrimSpace(a.TLSKeyFile) != "" {
		return "static"
	}
	return "managed"
}
```

Set defaults:

```go
Admin: AdminConfig{
	Enabled:           true,
	ListenAddr:        "127.0.0.1:5001",
	HTTPChallengeAddr: "127.0.0.1:5080",
	RequireAuth:       true,
	SessionExpiry:     "24h",
	TLSMode:           "",
	TLSEnabled:        false,
	TLSCacheDir:       "",
	TLSHosts:          nil,
},
```

- [ ] **Step 4: Add a minimal manager scaffold**

Create `sdn-server/internal/tlsmgr/manager.go`:

```go
package tlsmgr

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

type Manager struct {
	mode string
}

func New(cfg config.AdminConfig) (*Manager, error) {
	mode := cfg.EffectiveTLSMode()
	switch mode {
	case "disabled", "static", "managed":
		return &Manager{mode: mode}, nil
	default:
		return nil, fmt.Errorf("unsupported tls mode %q", mode)
	}
}

func (m *Manager) Mode() string {
	if m == nil {
		return "disabled"
	}
	return strings.TrimSpace(m.mode)
}

func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
../scripts/go-with-wasmedge.sh test ./internal/tlsmgr -run 'TestConfigTLSMode' -count=1
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/config/config.go sdn-server/internal/tlsmgr/manager.go sdn-server/internal/tlsmgr/manager_test.go
git commit -m "feat: add managed TLS config model"
```

### Task 2: Generate and verify bootstrap certificates with SDN proof extensions

**Files:**
- Create: `sdn-server/internal/tlsmgr/bootstrap.go`
- Create: `sdn-server/internal/tlsmgr/binding.go`
- Test: `sdn-server/internal/tlsmgr/bootstrap_test.go`
- Test: `sdn-server/internal/tlsmgr/binding_test.go`
- Modify: `sdn-server/internal/wasm/hdwallet_identity.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`

- [ ] **Step 1: Write the failing bootstrap and proof tests**

Create `sdn-server/internal/tlsmgr/bootstrap_test.go`:

```go
package tlsmgr

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateBootstrapCert_PersistsStableIdentity(t *testing.T) {
	dir := t.TempDir()
	mgr := &Manager{mode: "managed"}

	first, err := mgr.loadOrCreateBootstrapCert(dir, BootstrapIdentityInput{
		PeerID:                      "12D3KooWTestPeer",
		EncryptionPath:              "m/44'/0'/0'/1'/0'",
		EncryptionX25519PublicKey:   []byte("12345678901234567890123456789012"),
		EncryptionProofEd25519Seed:  make([]byte, 32),
	})
	if err != nil {
		t.Fatalf("loadOrCreateBootstrapCert() first error = %v", err)
	}

	second, err := mgr.loadOrCreateBootstrapCert(dir, BootstrapIdentityInput{
		PeerID:                      "12D3KooWTestPeer",
		EncryptionPath:              "m/44'/0'/0'/1'/0'",
		EncryptionX25519PublicKey:   []byte("12345678901234567890123456789012"),
		EncryptionProofEd25519Seed:  make([]byte, 32),
	})
	if err != nil {
		t.Fatalf("loadOrCreateBootstrapCert() second error = %v", err)
	}

	if first.Leaf.SerialNumber.Cmp(second.Leaf.SerialNumber) != 0 {
		t.Fatalf("serial changed across reload")
	}

	raw, err := os.ReadFile(filepath.Join(dir, "bootstrap-cert.pem"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("pem.Decode() returned nil")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}
}
```

Create `sdn-server/internal/tlsmgr/binding_test.go`:

```go
package tlsmgr

import "testing"

func TestEncodeAndVerifyBootstrapBinding_RoundTrips(t *testing.T) {
	input := BootstrapBindingInput{
		PeerID:                    "12D3KooWTestPeer",
		EncryptionPath:            "m/44'/0'/0'/1'/0'",
		EncryptionX25519PublicKey: make([]byte, 32),
		ProofEd25519Seed:          make([]byte, 32),
		TLSSPKISHA256:             make([]byte, 32),
	}

	ext, err := EncodeBootstrapBinding(input)
	if err != nil {
		t.Fatalf("EncodeBootstrapBinding() error = %v", err)
	}

	if _, err := VerifyBootstrapBinding(ext.Value, input.TLSSPKISHA256); err != nil {
		t.Fatalf("VerifyBootstrapBinding() error = %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
../scripts/go-with-wasmedge.sh test ./internal/tlsmgr -run 'TestLoadOrCreateBootstrapCert|TestEncodeAndVerifyBootstrapBinding' -count=1
```

Expected: FAIL because the bootstrap helpers and binding types do not exist yet.

- [ ] **Step 3: Implement binding encoding and verification**

Create `sdn-server/internal/tlsmgr/binding.go` with the custom OID and signature helpers:

```go
package tlsmgr

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
)

var bootstrapBindingOID = asn1.ObjectIdentifier{1, 3, 112, 4, 57, 10, 1}

type bootstrapBindingASN1 struct {
	Version                         int
	PeerID                          string `asn1:"optional,utf8"`
	EncryptionPath                  string `asn1:"utf8"`
	EncryptionX25519PublicKey       []byte
	EncryptionProofEd25519PublicKey []byte
	TLSSPKISHA256                   []byte
	SignatureAlgorithm              asn1.ObjectIdentifier
	Signature                       []byte
}

type BootstrapBindingInput struct {
	PeerID                    string
	EncryptionPath            string
	EncryptionX25519PublicKey []byte
	ProofEd25519Seed          []byte
	TLSSPKISHA256             []byte
}

func EncodeBootstrapBinding(input BootstrapBindingInput) (pkix.Extension, error) {
	pub := ed25519.NewKeyFromSeed(input.ProofEd25519Seed).Public().(ed25519.PublicKey)
	message := bootstrapBindingMessage(input.PeerID, input.EncryptionPath, input.EncryptionX25519PublicKey, pub, input.TLSSPKISHA256)
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(input.ProofEd25519Seed), message)

	payload, err := asn1.Marshal(bootstrapBindingASN1{
		Version:                         1,
		PeerID:                          input.PeerID,
		EncryptionPath:                  input.EncryptionPath,
		EncryptionX25519PublicKey:       input.EncryptionX25519PublicKey,
		EncryptionProofEd25519PublicKey: pub,
		TLSSPKISHA256:                   input.TLSSPKISHA256,
		SignatureAlgorithm:              asn1.ObjectIdentifier{1, 3, 101, 112},
		Signature:                       sig,
	})
	if err != nil {
		return pkix.Extension{}, err
	}

	return pkix.Extension{Id: bootstrapBindingOID, Critical: false, Value: payload}, nil
}

func VerifyBootstrapBinding(raw []byte, wantSPKIHash []byte) (*bootstrapBindingASN1, error) {
	var binding bootstrapBindingASN1
	if _, err := asn1.Unmarshal(raw, &binding); err != nil {
		return nil, err
	}
	if !equalBytes(binding.TLSSPKISHA256, wantSPKIHash) {
		return nil, fmt.Errorf("tls spki hash mismatch")
	}
	msg := bootstrapBindingMessage(binding.PeerID, binding.EncryptionPath, binding.EncryptionX25519PublicKey, binding.EncryptionProofEd25519PublicKey, binding.TLSSPKISHA256)
	if !ed25519.Verify(ed25519.PublicKey(binding.EncryptionProofEd25519PublicKey), msg, binding.Signature) {
		return nil, fmt.Errorf("invalid bootstrap binding signature")
	}
	return &binding, nil
}

func bootstrapBindingMessage(peerID, path string, x25519Pub, ed25519Pub, spkiHash []byte) []byte {
	h := sha256.New()
	h.Write([]byte("SDN TLS BOOTSTRAP BINDING V1"))
	h.Write([]byte(peerID))
	h.Write([]byte(path))
	h.Write(x25519Pub)
	h.Write(ed25519Pub)
	h.Write(spkiHash)
	return h.Sum(nil)
}
```

- [ ] **Step 4: Implement bootstrap cert generation and persistence**

Create `sdn-server/internal/tlsmgr/bootstrap.go`:

```go
package tlsmgr

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

type BootstrapIdentityInput struct {
	PeerID                     string
	EncryptionPath             string
	EncryptionX25519PublicKey  []byte
	EncryptionProofEd25519Seed []byte
}

func (m *Manager) loadOrCreateBootstrapCert(dir string, identity BootstrapIdentityInput) (*tls.Certificate, error) {
	certPath := filepath.Join(dir, "bootstrap-cert.pem")
	keyPath := filepath.Join(dir, "bootstrap-key.pem")
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
		return &cert, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName:         "Space Data Network Node",
			Organization:       []string{"Space Data Network"},
			OrganizationalUnit: []string{"Bootstrap TLS"},
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(5 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	spkiDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, err
	}
	spkiHash := sha256.Sum256(spkiDER)
	ext, err := EncodeBootstrapBinding(BootstrapBindingInput{
		PeerID:                    identity.PeerID,
		EncryptionPath:            identity.EncryptionPath,
		EncryptionX25519PublicKey: identity.EncryptionX25519PublicKey,
		ProofEd25519Seed:          identity.EncryptionProofEd25519Seed,
		TLSSPKISHA256:             spkiHash[:],
	})
	if err != nil {
		return nil, err
	}
	template.ExtraExtensions = []pkix.Extension{ext}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, err
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	return &cert, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
../scripts/go-with-wasmedge.sh test ./internal/tlsmgr -run 'TestLoadOrCreateBootstrapCert|TestEncodeAndVerifyBootstrapBinding' -count=1
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/tlsmgr/bootstrap.go sdn-server/internal/tlsmgr/binding.go sdn-server/internal/tlsmgr/bootstrap_test.go sdn-server/internal/tlsmgr/binding_test.go
git commit -m "feat: add bootstrap TLS certificate generation"
```

### Task 3: Refactor server startup to enforce HTTPS, secure cookies, and HTTP challenge redirects

**Files:**
- Create: `sdn-server/internal/tlsmgr/http.go`
- Test: `sdn-server/internal/tlsmgr/http_test.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main_test.go`
- Modify: `sdn-server/internal/auth/handler.go`
- Modify: `sdn-server/internal/auth/login_page.go`

- [ ] **Step 1: Write the failing redirect and secure-cookie tests**

Add to `sdn-server/cmd/spacedatanetwork/main_test.go`:

```go
func TestManagedTLSRedirectsHTTPRootToHTTPS(t *testing.T) {
	redirect := tlsmgr.NewRedirectHandler("https://sdn.example")
	req := httptest.NewRequest(http.MethodGet, "http://sdn.example/", nil)
	rec := httptest.NewRecorder()

	redirect.ServeHTTP(rec, req)

	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusPermanentRedirect)
	}
	if got := rec.Header().Get("Location"); got != "https://sdn.example/" {
		t.Fatalf("Location = %q, want %q", got, "https://sdn.example/")
	}
}
```

Add to `sdn-server/internal/auth/handler_test.go`:

```go
func TestSessionCookieIsSecureWhenNativeTLSActive(t *testing.T) {
	h := newTestHandler(t)
	h.cookieSecureOverride = ptrBool(true)

	rec := httptest.NewRecorder()
	h.setSessionCookie(rec, "token", time.Now().Add(time.Hour))

	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || !cookies[0].Secure {
		t.Fatalf("expected secure session cookie")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork ./internal/auth -run 'TestManagedTLSRedirectsHTTPRootToHTTPS|TestSessionCookieIsSecureWhenNativeTLSActive' -count=1
```

Expected: FAIL because the redirect handler and secure override hook do not exist yet.

- [ ] **Step 3: Add HTTP redirect/challenge helpers**

Create `sdn-server/internal/tlsmgr/http.go`:

```go
package tlsmgr

import (
	"net/http"
	"strings"
)

func NewRedirectHandler(httpsBase string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/acme-challenge/" || strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			http.NotFound(w, r)
			return
		}
		target := httpsBase + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})
}
```

- [ ] **Step 4: Refactor `main.go` to use managed TLS**

Replace the current `ListenAndServeTLS(adminCertFile, adminKeyFile)` branch with manager-driven certificate selection:

```go
tlsManager, err := tlsmgr.New(cfg.Admin)
if err != nil {
	return fmt.Errorf("configure tls manager: %w", err)
}

httpsServer := &http.Server{
	Addr:    adminAddr,
	Handler: securedMux,
	TLSConfig: func() *tls.Config {
		cfg := tlsManager.TLSConfig()
		cfg.GetCertificate = tlsManager.GetCertificate
		return cfg
	}(),
}

go func() {
	if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		log.Warnf("HTTPS server error: %v", err)
	}
}()
```

Add the port-80 listener in managed mode:

```go
if tlsManager.Mode() == "managed" {
	httpServer := &http.Server{
		Addr:    cfg.Admin.HTTPChallengeAddr,
		Handler: tlsManager.HTTPHandler(adminAddr),
	}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Warnf("HTTP challenge server error: %v", err)
		}
	}()
}
```

- [ ] **Step 5: Make secure-cookie behavior explicit under native TLS**

Update `internal/auth/handler.go`:

```go
type Handler struct {
	userStore             *UserStore
	sessionStore          *SessionStore
	sessionExpiry         time.Duration
	trustedProxy          string
	walletUIPath          string
	tlsManager            *tlsmgr.Manager
	cookieSecureOverride *bool
}

func (h *Handler) cookieSecure(r *http.Request) bool {
	if h.cookieSecureOverride != nil {
		return *h.cookieSecureOverride
	}
	return isRequestSecure(r, h.trustedProxy)
}
```

Then route every cookie write through `cookieSecure(r)` rather than opportunistic proxy detection alone.

- [ ] **Step 6: Run test to verify it passes**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
../scripts/go-with-wasmedge.sh test ./internal/tlsmgr ./cmd/spacedatanetwork ./internal/auth -run 'TestManagedTLSRedirectsHTTPRootToHTTPS|TestSessionCookieIsSecureWhenNativeTLSActive' -count=1
```

Expected: PASS

- [ ] **Step 7: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/tlsmgr/http.go sdn-server/internal/tlsmgr/http_test.go sdn-server/cmd/spacedatanetwork/main.go sdn-server/cmd/spacedatanetwork/main_test.go sdn-server/internal/auth/handler.go sdn-server/internal/auth/handler_test.go
git commit -m "feat: enforce HTTPS with managed TLS bootstrap"
```

### Task 4: Expose TLS trust state and bootstrap certificate download on `/login`

**Files:**
- Create: `sdn-server/internal/tlsmgr/status.go`
- Modify: `sdn-server/internal/auth/login_page.go`
- Modify: `sdn-server/internal/auth/handler.go`
- Modify: `sdn-server/internal/auth/handler_test.go`

- [ ] **Step 1: Write the failing login-page trust-block tests**

Add to `sdn-server/internal/auth/handler_test.go`:

```go
func TestLoginPage_RendersBootstrapTLSStatusBlock(t *testing.T) {
	html := buildLoginPage("/wallet-ui/dist/assets/wallet.js", "/wallet-ui/dist/assets/wallet.css", LoginPageTLSStatus{
		Mode:                  "managed",
		ActiveCertificateType: "bootstrap",
		FingerprintSHA256:     "aa:bb:cc",
		PeerID:                "12D3KooWTestPeer",
		EncryptionPublicKey:   "001122",
		ProofStatus:           "verified",
		BootstrapCertURL:      "/bootstrap.crt",
	})

	if !strings.Contains(html, "Bootstrap self-signed") {
		t.Fatalf("missing bootstrap certificate label: %s", html)
	}
	if !strings.Contains(html, "/bootstrap.crt") {
		t.Fatalf("missing bootstrap cert link: %s", html)
	}
	if !strings.Contains(html, "aa:bb:cc") {
		t.Fatalf("missing fingerprint: %s", html)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
../scripts/go-with-wasmedge.sh test ./internal/auth -run 'TestLoginPage_RendersBootstrapTLSStatusBlock' -count=1
```

Expected: FAIL because `LoginPageTLSStatus` and the new render path do not exist yet.

- [ ] **Step 3: Add a serializable TLS status model**

Create `sdn-server/internal/tlsmgr/status.go`:

```go
package tlsmgr

type Status struct {
	Mode                  string   `json:"mode"`
	ActiveCertificateType string   `json:"active_certificate_type"`
	FingerprintSHA256     string   `json:"fingerprint_sha256"`
	NotBefore             string   `json:"not_before,omitempty"`
	NotAfter              string   `json:"not_after,omitempty"`
	Hosts                 []string `json:"hosts,omitempty"`
	PeerID                string   `json:"peer_id,omitempty"`
	EncryptionPublicKey   string   `json:"encryption_public_key,omitempty"`
	ProofStatus           string   `json:"proof_status,omitempty"`
	BootstrapCertURL      string   `json:"bootstrap_cert_url,omitempty"`
	LastError             string   `json:"last_error,omitempty"`
}
```

- [ ] **Step 4: Render the TLS block and add `/bootstrap.crt`**

Update `internal/auth/login_page.go` to build the trust block into the current centered card:

```go
type LoginPageTLSStatus struct {
	Mode                  string
	ActiveCertificateType string
	FingerprintSHA256     string
	PeerID                string
	EncryptionPublicKey   string
	ProofStatus           string
	BootstrapCertURL      string
}

func buildTLSStatusMarkup(status LoginPageTLSStatus) string {
	if status.Mode == "" {
		return ""
	}
	return `<section class="sdn-tls-status">` +
		`<p class="sdn-tls-eyebrow">TLS status</p>` +
		`<h2>` + html.EscapeString(status.ActiveCertificateType) + `</h2>` +
		`<dl class="sdn-tls-grid">` +
		`<dt>Mode</dt><dd>` + html.EscapeString(status.Mode) + `</dd>` +
		`<dt>Fingerprint</dt><dd><code>` + html.EscapeString(status.FingerprintSHA256) + `</code></dd>` +
		`<dt>Peer ID</dt><dd><code>` + html.EscapeString(status.PeerID) + `</code></dd>` +
		`<dt>Encryption key</dt><dd><code>` + html.EscapeString(status.EncryptionPublicKey) + `</code></dd>` +
		`<dt>Proof</dt><dd>` + html.EscapeString(status.ProofStatus) + `</dd>` +
		`</dl>` +
		`<a class="sdn-bootstrap-download" href="` + html.EscapeString(status.BootstrapCertURL) + `">Download bootstrap certificate</a>` +
		`</section>`
}
```

Wire `/bootstrap.crt` in `internal/auth/handler.go`:

```go
mux.HandleFunc("/bootstrap.crt", h.handleBootstrapCert)
```

And implement:

```go
func (h *Handler) handleBootstrapCert(w http.ResponseWriter, r *http.Request) {
	raw, err := h.tlsManager.BootstrapCertPEM()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="sdn-bootstrap.crt"`)
	w.Write(raw)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
../scripts/go-with-wasmedge.sh test ./internal/auth -run 'TestLoginPage_RendersBootstrapTLSStatusBlock' -count=1
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/tlsmgr/status.go sdn-server/internal/auth/login_page.go sdn-server/internal/auth/handler.go sdn-server/internal/auth/handler_test.go
git commit -m "feat: show TLS trust state on login page"
```

### Task 5: Add explicit-hostname enrollment APIs and the hosted UI controls

**Files:**
- Create: `sdn-server/internal/api/tls.go`
- Test: `sdn-server/internal/api/tls_test.go`
- Create: `sdn-js/src/ui/runtime/tls-settings.ts`
- Test: `sdn-js/src/ui/runtime/tls-settings.test.ts`
- Modify: `sdn-js/src/ui/runtime/runtime-config.ts`
- Modify: `sdn-js/ui/src/upstream-webui/bundles/index.js`
- Modify: `scripts/admin-dev.sh`
- Modify: `scripts/dev-local.sh`
- Modify: `config/dev.yaml`
- Modify: `config/dev-docker.yaml`
- Modify: `README.md`

- [ ] **Step 1: Write the failing admin TLS API tests**

Create `sdn-server/internal/api/tls_test.go`:

```go
package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTLSSettingsAPI_RejectsInvalidHostnames(t *testing.T) {
	handler := newTestTLSAPIHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/tls/hosts", bytes.NewBufferString(`{"hosts":["localhost","127.0.0.1"]}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
../scripts/go-with-wasmedge.sh test ./internal/api -run 'TestTLSSettingsAPI_RejectsInvalidHostnames' -count=1
```

Expected: FAIL because the TLS API handler does not exist yet.

- [ ] **Step 3: Implement the admin TLS API**

Create `sdn-server/internal/api/tls.go`:

```go
package api

type TLSSettingsHandler struct {
	authHandler *auth.Handler
	tlsManager  *tlsmgr.Manager
}

func (h *TLSSettingsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/tls/status", h.authHandler.RequireAuth(peers.Admin, h.handleStatus))
	mux.HandleFunc("/api/v1/admin/tls/hosts", h.authHandler.RequireAuth(peers.Admin, h.handleHosts))
	mux.HandleFunc("/api/v1/admin/tls/issue", h.authHandler.RequireAuth(peers.Admin, h.handleIssue))
}
```

Host validation logic:

```go
func validateManagedHost(host string) error {
	host = strings.TrimSpace(strings.ToLower(host))
	switch {
	case host == "":
		return errors.New("hostname is required")
	case host == "localhost":
		return errors.New("localhost is not allowed for managed certificates")
	case net.ParseIP(host) != nil:
		return errors.New("ip literals are not allowed in hostname mode")
	default:
		return nil
	}
}
```

- [ ] **Step 4: Implement the `sdn-js` TLS settings client**

Create `sdn-js/src/ui/runtime/tls-settings.ts`:

```ts
export interface TlsStatus {
  mode: string
  active_certificate_type: string
  fingerprint_sha256: string
  proof_status?: string
  bootstrap_cert_url?: string
  hosts?: string[]
  last_error?: string
}

export async function fetchTlsStatus(): Promise<TlsStatus> {
  const res = await fetch('/api/v1/admin/tls/status', { credentials: 'same-origin' })
  if (!res.ok) throw new Error(`tls status ${res.status}`)
  return res.json()
}

export async function saveTlsHosts(hosts: string[]): Promise<void> {
  const res = await fetch('/api/v1/admin/tls/hosts', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'fetch' },
    body: JSON.stringify({ hosts }),
  })
  if (!res.ok) throw new Error(`tls hosts ${res.status}`)
}
```

Expose this runtime in the hosted dashboard where admin settings already live, rather than inventing a second settings page.

- [ ] **Step 5: Run tests to verify they pass**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
../scripts/go-with-wasmedge.sh test ./internal/api -run 'TestTLSSettingsAPI_RejectsInvalidHostnames' -count=1

cd /Users/tj/software/space-data-network/sdn-js
npx vitest run src/ui/runtime/tls-settings.test.ts
```

Expected: PASS

- [ ] **Step 6: Update dev scripts and docs**

Patch the dev scripts/config so local runs boot under managed TLS:

```yaml
admin:
  listen_addr: "127.0.0.1:5443"
  http_challenge_addr: "127.0.0.1:5080"
  tls_mode: managed
  tls_cache_dir: ".tmp/tls"
```

Update the scripts to print:

```bash
echo -e "  Login:     ${GREEN}https://localhost:5443/login${NC}"
echo -e "  Cert:      ${GREEN}https://localhost:5443/bootstrap.crt${NC}"
```

- [ ] **Step 7: Commit**

```bash
cd /Users/tj/software/space-data-network
git add sdn-server/internal/api/tls.go sdn-server/internal/api/tls_test.go sdn-js/src/ui/runtime/tls-settings.ts sdn-js/src/ui/runtime/tls-settings.test.ts scripts/admin-dev.sh scripts/dev-local.sh config/dev.yaml config/dev-docker.yaml README.md
git commit -m "feat: add hostname enrollment for managed TLS"
```

### Task 6: Full verification and browser walkthrough

**Files:**
- Test: `sdn-server/internal/tlsmgr/*.go`
- Test: `sdn-server/internal/api/tls_test.go`
- Test: `sdn-server/internal/auth/handler_test.go`
- Test: `sdn-server/cmd/spacedatanetwork/main_test.go`
- Test: `sdn-js/src/ui/runtime/tls-settings.test.ts`

- [ ] **Step 1: Run focused Go verification**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-server
../scripts/go-with-wasmedge.sh test ./internal/tlsmgr ./internal/api ./internal/auth ./cmd/spacedatanetwork -count=1
```

Expected: PASS

- [ ] **Step 2: Run focused `sdn-js` verification**

Run:

```bash
cd /Users/tj/software/space-data-network/sdn-js
npx vitest run src/ui/runtime/tls-settings.test.ts src/ui/runtime/server-adapter.test.ts src/ui/vite-config.test.ts
npm run build:ui
```

Expected: PASS

- [ ] **Step 3: Restart the local dev stack and verify over HTTPS**

Run:

```bash
cd /Users/tj/software/space-data-network
npm run admin:dev
```

Expected:

```text
Login: https://localhost:5443/login
Cert:  https://localhost:5443/bootstrap.crt
```

- [ ] **Step 4: Verify in Chrome MCP**

Use Chrome MCP to confirm:

```text
1. Visit https://localhost:5443/login
2. Click through the self-signed warning if needed
3. Confirm the login page shows:
   - bootstrap TLS label
   - cert fingerprint
   - encryption public key
   - proof verified indicator
   - bootstrap cert download link
4. Download /bootstrap.crt
5. Sign in with the dev wallet
6. Refresh /
7. Confirm session remains authenticated
8. Open /webui/
9. Confirm it stays authenticated under the same HTTPS origin
```

- [ ] **Step 5: Commit final verification-only adjustments**

```bash
cd /Users/tj/software/space-data-network
git add -A
git commit -m "test: verify managed TLS bootstrap flow"
```
