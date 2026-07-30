package node

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/pmm"
)

// writeCatalog lays out a minimal but REAL module tree: one anonymous module
// backed by actual bytes, one entitled module backed by none.
func writeCatalog(t *testing.T) (root string, wasm []byte) {
	t.Helper()
	root = t.TempDir()
	wasm = []byte("\x00asm\x01\x00\x00\x00fake-module-bytes")
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "open.wasm"), wasm, 0o644); err != nil {
		t.Fatal(err)
	}
	cat := map[string]any{
		"provider_domain": "sdn.example.test",
		"provider_name":   "Test Node",
		"entries": []map[string]any{
			{
				"MODULE_ID": "com.example.open", "PLUGIN_ID": "com.example.open",
				"VERSION": "1.0.0", "TRUST_TIER": "CORE", "ACCESS_POLICY": "ANONYMOUS",
				"DEFAULT_ENABLED": true, "ENTRY_STATE": "ACTIVE", "PLUGIN_TYPE": "Propagator",
				"ARTIFACT_PATH":   "/modules/com.example.open/1.0.0/module.wasm",
				"source_artifact": "artifacts/open.wasm",
			},
			{
				"MODULE_ID": "com.example.closed", "PLUGIN_ID": "com.example.closed",
				"VERSION": "0.1.0", "TRUST_TIER": "OPTIONAL", "ACCESS_POLICY": "ENTITLED",
				"DEFAULT_ENABLED": false, "ENTRY_STATE": "ACTIVE", "PLUGIN_TYPE": "Comms",
				"ARTIFACT_PATH": "", "CONTENT_HASH": strings.Repeat("c", 64),
			},
		},
		"browse": []map[string]any{},
	}
	raw, _ := json.MarshalIndent(cat, "", " ")
	if err := os.WriteFile(filepath.Join(root, pmmCatalogFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root, wasm
}

// buildPlugin wires a plugin against a catalog without standing up a whole Node.
func buildPlugin(t *testing.T, root string) (*pmmPlugin, ed25519.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	p := &pmmPlugin{
		source:    &pmm.StaticSource{},
		artifacts: map[string]string{},
		root:      root,
		catalog:   filepath.Join(root, pmmCatalogFile),
		stop:      make(chan struct{}),
	}
	cf, err := pmm.LoadCatalog(p.catalog, root)
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	trust := pmm.TrustAnchor{
		ProviderDomain: "sdn.example.test", NodePeerID: "16Uiu2HAmTest",
		SigningPublicKey: hex.EncodeToString(pub), SignatureAlgorithm: "ed25519",
	}
	m, err := pmm.BuildManifest(cf, trust, 1, "https://sdn.example.test"+pmm.Path,
		time.Now(), pmmManifestTTL, nodeSigner{priv})
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}
	for i := range cf.Entries {
		e := &cf.Entries[i]
		if e.AccessPolicy == "ANONYMOUS" && e.ArtifactPath != "" && e.SourceArtifact != "" {
			p.artifacts[e.ArtifactPath] = e.SourceArtifact
		}
	}
	p.source.Set(m, cf.Browse)
	p.mounted = true
	return p, pub
}

func TestPMMPluginServesSignedManifestAndArtifact(t *testing.T) {
	root, wasm := writeCatalog(t)
	p, pub := buildPlugin(t, root)

	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	// Manifest, JSON projection.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, pmm.Path, nil)
	req.Header.Set("Accept", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest: want 200, got %d", rec.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(doc["MODULES"].([]any)) != 2 {
		t.Fatalf("want 2 modules, got %d", len(doc["MODULES"].([]any)))
	}
	sig, _ := hex.DecodeString(doc["SIGNATURE"].(string))
	if !ed25519.Verify(pub, []byte(doc["SIGNED_STATEMENT"].(string)), sig) {
		t.Fatal("served manifest signature does not verify against the node key")
	}

	// The open artifact is served, and its bytes hash to the published value.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/modules/com.example.open/1.0.0/module.wasm", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("artifact: want 200, got %d", rec2.Code)
	}
	if string(rec2.Body.Bytes()) != string(wasm) {
		t.Fatal("served artifact bytes differ from the staged file")
	}
	sum := sha256.Sum256(rec2.Body.Bytes())
	if got := rec2.Header().Get("ETag"); got != `"`+hex.EncodeToString(sum[:])+`"` {
		t.Fatalf("ETag must be the content hash, got %s", got)
	}
}

// The single most important guarantee: closed bytes are unreachable anonymously.
func TestPMMPluginNeverServesClosedModuleBytes(t *testing.T) {
	root, _ := writeCatalog(t)
	p, _ := buildPlugin(t, root)
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)

	for _, path := range []string{
		"/modules/com.example.closed/0.1.0/module.wasm",
		"/modules/../modules-catalog.json",
		"/modules/artifacts/open.wasm", // real file, but not a published path
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusOK {
			t.Fatalf("%s must not be served, got 200", path)
		}
	}
}

// With no catalog the node publishes nothing rather than an empty signed claim.
func TestPMMPluginMountsNothingWithoutCatalog(t *testing.T) {
	p := &pmmPlugin{source: &pmm.StaticSource{}, artifacts: map[string]string{}, stop: make(chan struct{})}
	mux := http.NewServeMux()
	p.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pmm.Path, nil))
	if rec.Code == http.StatusOK {
		t.Fatal("a node with no catalog must not serve a manifest")
	}
}

func TestPMMPluginRefusesUnusableSigningKey(t *testing.T) {
	if _, err := (&pmmPlugin{node: &Node{}}).signer(); err == nil {
		t.Fatal("a node with no usable signing key must refuse to sign, never serve unsigned")
	}
}
