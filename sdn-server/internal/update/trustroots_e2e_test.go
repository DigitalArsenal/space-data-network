package update

// The trust move (owner 2026-10-09): a release signed by the current update
// root carries the distribution key as the only root. Once it applies, the node
// installs nothing the old root signs, and installs what the distribution key
// signs.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReleaseMovesTheUpdateRootsToTheDistributionKey(t *testing.T) {
	current := newTestSigner(t)
	paths, _ := setupBundleRoot(t, current)
	distribution := newTestSigner(t)
	distribution.keyID = RootKeyID(distribution.publicKey)

	stageAs := func(signer *testSigner, updateID, version string, sequence int64, mutate func(map[string]any)) {
		t.Helper()
		bundleBytes := makeBundleTarGz(t, version, map[string]string{"bin/spacedatanetwork": "binary-" + version})
		wasmBytes := BuildCarrier(bundleBytes)
		manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
			doc["update_id"], doc["version"], doc["sequence"] = updateID, version, sequence
			doc["bundle"].(map[string]any)["hash"] = sha256Hex(bundleBytes)
			doc["bundle"].(map[string]any)["size"] = int64(len(bundleBytes))
			doc["wasm"].(map[string]any)["hash"] = sha256Hex(wasmBytes)
			if mutate != nil {
				mutate(doc)
			}
		}, bundleBytes, wasmBytes)
		// Delivered and staged; Apply re-verifies against the roots on disk.
		if _, err := Stage(paths, manifestBytes, wasmBytes, HostVerifyOptions(signer.roots(t), 0, time.Now()), nil); err != nil {
			t.Fatalf("stage %s: %v", updateID, err)
		}
	}

	stageAs(current, "move-roots", "3.0.0", 100, func(doc map[string]any) {
		doc["trust_roots"] = map[string]any{distribution.keyID: distribution.publicKeyBase64SPKI(t)}
	})
	if _, err := Apply(paths, ApplyOptions{UpdateID: "move-roots"}); err != nil {
		t.Fatalf("apply the release that moves the roots: %v", err)
	}
	var installed TrustedRoots
	data, err := os.ReadFile(paths.Trust)
	if err != nil || json.Unmarshal(data, &installed) != nil {
		t.Fatalf("read installed roots: %v", err)
	}
	if len(installed) != 1 || installed[distribution.keyID] != distribution.publicKeyBase64SPKI(t) {
		t.Fatalf("installed roots = %v, want only the distribution key %s", installed, distribution.keyID)
	}

	stageAs(current, "old-root", "3.0.1", 101, nil)
	if _, err := Apply(paths, ApplyOptions{UpdateID: "old-root"}); err == nil || !strings.Contains(err.Error(), "untrusted") {
		t.Fatalf("a release signed by the retired root: %v, want refused as untrusted", err)
	}
	stageAs(distribution, "distribution-key", "3.0.2", 102, nil)
	if _, err := Apply(paths, ApplyOptions{UpdateID: "distribution-key"}); err != nil {
		t.Fatalf("apply a release signed by the distribution key: %v", err)
	}
}
