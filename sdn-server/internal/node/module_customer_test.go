package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/plugins"
	"os"
	"path/filepath"
	"testing"
)

// These fixtures were emitted by the SDK JS writer, exercising the real Go
// reader across implementations rather than using a same-language crypto mock.
func TestModuleCustomerDecryptsOnlyForTheIntendedKey(t *testing.T) {
	root := "../license/testdata/protected-publication"
	var manifest struct {
		Key      string `json:"key"`
		Fixtures []struct {
			Name string `json:"name"`
			Hash string `json:"plaintextSha256"`
		} `json:"fixtures"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString(manifest.Key)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range manifest.Fixtures {
		if f.Name != "gcm-mbl-enc-pnm" {
			continue
		}
		blob, err := os.ReadFile(filepath.Join(root, f.Name+".bin"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(blob)
		digest := hex.EncodeToString(sum[:])
		plain, err := verifyCustomerModule(blob, key, digest, f.Hash)
		if err != nil {
			t.Fatal(err)
		}
		clear(plain)
		if _, err := verifyCustomerModule(blob, bytes.Repeat([]byte{9}, 32), digest, f.Hash); err == nil {
			t.Fatal("another customer decrypted the module")
		}
		changed := bytes.Clone(blob)
		changed[0] ^= 1
		if _, err := verifyCustomerModule(changed, key, digest, f.Hash); err == nil {
			t.Fatal("accepted tampered ciphertext")
		}
		if _, err := verifyCustomerModule(blob, key, digest, hex.EncodeToString(make([]byte, 32))); err == nil {
			t.Fatal("accepted the wrong module")
		}
		return
	}
	t.Fatal("SDK protected publication fixture missing")
}

func TestModuleCustomerInstallsIntoRealRuntime(t *testing.T) {
	path := os.Getenv("SDN_CUSTOMER_MODULE_TEST_WASM")
	if path == "" {
		t.Skip("set SDN_CUSTOMER_MODULE_TEST_WASM to a built SDK module")
	}
	plain, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := modulert.NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatal(err)
	}
	n := newCatalogTestNode(t, t.TempDir(), policy)
	probe, err := modulert.NewModule(plain, n.buildCapRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := &customerModule{PluginID: probe.ID(), Version: probe.Manifest().Version}
	m.Artifact.CanonicalSHA256 = probe.ContentHash()
	probe.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := n.plugins.StartAll(ctx, plugins.RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	if err := n.installCustomerModule(m, plain); err != nil {
		t.Fatal(err)
	}
	defer n.plugins.Get(m.PluginID).Close()
	if !n.customerModuleRegistered(m) {
		t.Fatal("successful install absent from the runtime")
	}
	if err := n.installCustomerModule(m, plain); err != nil {
		t.Fatalf("idempotent install: %v", err)
	}
	entries := n.plugins.RuntimeSnapshot().Modules
	if len(entries) != 1 || entries[0].Status != "running" {
		t.Fatalf("runtime after install: %+v", entries)
	}
	wrong := *m
	wrong.PluginID = "wrong.module"
	if err := n.installCustomerModule(&wrong, plain); err == nil {
		t.Fatal("accepted mismatched manifest identity")
	}
	wrong = *m
	wrong.Version = "wrong-version"
	if err := n.installCustomerModule(&wrong, plain); err == nil {
		t.Fatal("accepted a different installed version")
	}
}
func TestModuleCustomerRejectsUnpinnedAndMalformedArtifacts(t *testing.T) {
	for _, blob := range [][]byte{nil, {1, 2, 3}, bytes.Repeat([]byte{255}, 64)} {
		sum := sha256.Sum256(blob)
		if _, err := verifyCustomerModule(blob, make([]byte, 32), hex.EncodeToString(sum[:]), hex.EncodeToString(sum[:])); err == nil {
			t.Fatal("accepted malformed module")
		}
	}
	if _, err := decodeModuleHash("../../somewhere"); err == nil {
		t.Fatal("accepted a path as a hash")
	}
}
