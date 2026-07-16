package flowrt

// netmodule_test.go proves the Phase-2 Part-B fetch-to-bake primitive: a signed
// module bundle stored in a content-addressed blockstore is fetched by its
// content hash, its Ed25519 signature is verified against the trusted signer set
// (signed-only, fail-closed), and it is staged into a flowcc home so it becomes
// bakeable — without ever having been staged from a local dist tree. It also
// asserts the fail-closed paths (untrusted signer, tampered signature) reject.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/flowcc"
)

// memBlockstore is a tiny in-memory appmanifest.ModuleBlockstore for tests.
type memBlockstore struct{ m map[string]blocks.Block }

func newMemBlockstore() *memBlockstore { return &memBlockstore{m: map[string]blocks.Block{}} }

func (b *memBlockstore) Put(_ context.Context, blk blocks.Block) error {
	b.m[blk.Cid().String()] = blk
	return nil
}
func (b *memBlockstore) Get(_ context.Context, c cid.Cid) (blocks.Block, error) {
	if blk, ok := b.m[c.String()]; ok {
		return blk, nil
	}
	return nil, fmt.Errorf("memBlockstore: block %s not found", c)
}
func (b *memBlockstore) Has(_ context.Context, c cid.Cid) (bool, error) {
	_, ok := b.m[c.String()]
	return ok, nil
}

func TestNetModuleFetchStageSignedOnly(t *testing.T) {
	root := resolveModulesRoot(t) // skips cleanly if the modules monorepo is absent
	const pluginID = "com.digitalarsenal.foundation.omm-json"
	gl := filepath.Join(root, "foundation", "omm-json", "dist", "guest-link")
	obj := mustRead(t, filepath.Join(gl, "module-link.o"))
	meta := mustRead(t, filepath.Join(gl, "metadata.json"))
	manifest := mustRead(t, filepath.Join(root, "foundation", "omm-json", "dist", "plugin-manifest.json"))

	// Build + content-address the signed bundle.
	bundleBytes, err := MarshalBundle(&ModuleBundle{PluginID: pluginID, Object: obj, Metadata: meta, Manifest: manifest})
	if err != nil {
		t.Fatalf("MarshalBundle: %v", err)
	}
	bs := newMemBlockstore()
	ch, _, err := appmanifest.StoreModuleBytes(context.Background(), bs, bundleBytes)
	if err != nil {
		t.Fatalf("StoreModuleBytes: %v", err)
	}
	if ch != HashBundleBytes(bundleBytes) {
		t.Fatalf("content hash mismatch: store=%s local=%s", ch, HashBundleBytes(bundleBytes))
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	digest, _ := hex.DecodeString(ch)
	goodRef := BakeModuleRef{
		PluginID:     pluginID,
		BundleHash:   ch,
		Signature:    base64.StdEncoding.EncodeToString(ed25519.Sign(priv, digest)),
		SignerPubKey: hex.EncodeToString(pub),
	}

	// 1) Trusted signer -> fetch + verify + stage. The module was NEVER staged
	// from a local dist tree, yet it lands staged + bakeable.
	home := flowcc.HomeAt(t.TempDir())
	f := NewNetModuleFetcher(bs, []ed25519.PublicKey{pub})
	if err := f.FetchAndStage(context.Background(), home, goodRef); err != nil {
		t.Fatalf("FetchAndStage (trusted): %v", err)
	}
	if _, err := os.Stat(home.ModuleLinkObjectPath(pluginID)); err != nil {
		t.Fatalf("guest-link object not staged: %v", err)
	}
	m, err := home.LoadModuleMetadata(pluginID)
	if err != nil {
		t.Fatalf("LoadModuleMetadata: %v", err)
	}
	if m.Source != "network" {
		t.Errorf("staged Source = %q, want \"network\"", m.Source)
	}
	if len(m.MethodSymbols) == 0 {
		t.Error("staged metadata has no methodSymbols (not bakeable)")
	}
	if len(m.MethodPorts) == 0 {
		t.Error("staged metadata has no methodPorts (schema-typed ports lost)")
	}

	// It also shows up as a bakeable staged module with typed ports.
	staged, err := stagedModulesFromHome(home)
	if err != nil || len(staged) != 1 || staged[0].PluginID != pluginID {
		t.Fatalf("stagedModulesFromHome = %+v, err=%v", staged, err)
	}

	// PublishNetworkModule yields catalog methods for the palette.
	e, err := f.PublishNetworkModule(context.Background(), goodRef)
	if err != nil {
		t.Fatalf("PublishNetworkModule: %v", err)
	}
	if len(e.Methods) == 0 {
		t.Error("published entry has no methods for the palette")
	}

	// 2) Fail-closed: no trusted signers -> refused even with a valid signature.
	if err := NewNetModuleFetcher(bs, nil).FetchAndStage(context.Background(), flowcc.HomeAt(t.TempDir()), goodRef); err == nil {
		t.Error("expected signed-only rejection with empty trusted set, got nil")
	}

	// 3) Fail-closed: tampered signature (valid Ed25519 over the WRONG message)
	// by an otherwise-trusted key is rejected.
	badRef := goodRef
	badRef.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("not the bundle hash")))
	if err := f.FetchAndStage(context.Background(), flowcc.HomeAt(t.TempDir()), badRef); err == nil {
		t.Error("expected rejection for a signature over the wrong message, got nil")
	}

	// 4) Fail-closed: untrusted signer (valid signature, key not trusted).
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := NewNetModuleFetcher(bs, []ed25519.PublicKey{otherPub}).FetchAndStage(context.Background(), flowcc.HomeAt(t.TempDir()), goodRef); err == nil {
		t.Error("expected rejection for an untrusted signer, got nil")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("module asset not available (%s): %v", path, err)
	}
	return b
}
