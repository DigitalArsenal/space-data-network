package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedFile writes contents at root/relPath (forward-slash), creating parent
// directories as needed — used to pre-populate bundle-root state a
// module-targeted apply must (or must not) touch.
func seedFile(t *testing.T, root, relPath, contents string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// stageSignedModuleUpdate stages a G4 module-update manifest declaring
// exactly the given module targets, whose bytes come from files (bundle-
// relative path -> contents; the module targets' declared Hash is computed
// from moduleHashSource so tests can intentionally mismatch it against the
// real shipped bytes in files).
func stageSignedModuleUpdate(t *testing.T, paths Paths, signer *testSigner, version string, files map[string]string, modules []ManifestModuleTarget) *StagedUpdate {
	t.Helper()
	bundleBytes := makeBundleTarGz(t, version, files)
	wasmBytes := BuildCarrier(bundleBytes)
	manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
		doc["version"] = version
		doc["bundle"].(map[string]any)["hash"] = sha256Hex(bundleBytes)
		doc["bundle"].(map[string]any)["size"] = int64(len(bundleBytes))
		doc["wasm"].(map[string]any)["hash"] = sha256Hex(wasmBytes)
		doc["target"].(map[string]any)["kind"] = TargetKindModuleUpdate
		modulesDoc := make([]map[string]any, len(modules))
		for i, m := range modules {
			modulesDoc[i] = map[string]any{"id": m.ID, "hash": m.Hash, "path": m.Path}
		}
		doc["modules"] = modulesDoc
	}, bundleBytes, wasmBytes)
	staged, err := Stage(paths, manifestBytes, wasmBytes, HostVerifyOptions(signer.roots(t), 0, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	return staged
}

func TestApplyModuleTargetSwapsOnlyDeclaredArtifact(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	seedFile(t, root, "runtime/modules/flatsql/flatsql.wasm", "old-flatsql-bytes")
	seedFile(t, root, "runtime/modules/sitl/sitl.wasm", "old-sitl-bytes")

	newFlatsql := "new-flatsql-bytes-v2"
	stageSignedModuleUpdate(t, paths, signer, "9.9.9",
		map[string]string{"runtime/modules/flatsql/flatsql.wasm": newFlatsql},
		[]ManifestModuleTarget{{ID: "flatsql", Hash: sha256Hex([]byte(newFlatsql)), Path: "runtime/modules/flatsql/flatsql.wasm"}},
	)

	result, err := Apply(paths, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.ModuleUpdate {
		t.Fatal("expected ModuleUpdate=true for a manifest declaring Modules[]")
	}
	if result.TwoPhase {
		t.Fatal("a module-targeted update must never report TwoPhase")
	}
	if len(result.AppliedModules) != 1 || result.AppliedModules[0] != "flatsql" {
		t.Fatalf("AppliedModules = %v, want [flatsql]", result.AppliedModules)
	}

	// The declared module artifact was swapped...
	if got := readFile(t, filepath.Join(root, "runtime", "modules", "flatsql", "flatsql.wasm")); got != newFlatsql {
		t.Fatalf("flatsql.wasm = %q, want %q", got, newFlatsql)
	}
	// ...the pre-existing bytes are preserved in the rollback dir...
	if got := readFile(t, filepath.Join(result.RollbackPath, "runtime", "modules", "flatsql", "flatsql.wasm")); got != "old-flatsql-bytes" {
		t.Fatalf("rollback flatsql.wasm = %q, want old-flatsql-bytes", got)
	}
	// ...and everything else in the bundle (another module, the CLI binary)
	// is completely untouched.
	if got := readFile(t, filepath.Join(root, "runtime", "modules", "sitl", "sitl.wasm")); got != "old-sitl-bytes" {
		t.Fatalf("sitl.wasm = %q, want untouched old-sitl-bytes", got)
	}
	if got := readFile(t, filepath.Join(root, "bin", "spacedatanetwork")); got != "old-binary" {
		t.Fatalf("bin/spacedatanetwork = %q, want untouched old-binary", got)
	}

	// A module-targeted apply never uses the two-phase crash marker.
	if _, err := os.Stat(paths.Phase); !os.IsNotExist(err) {
		t.Fatalf("module-targeted apply should never create a phase marker, stat err=%v", err)
	}
}

func TestApplyModuleTargetsRejectsTamperedArtifactBeforeTouchingAnyFile(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	seedFile(t, root, "runtime/modules/flatsql/flatsql.wasm", "old-flatsql-bytes")

	shippedBytes := "actually-shipped-bytes"
	stageSignedModuleUpdate(t, paths, signer, "9.9.9",
		map[string]string{"runtime/modules/flatsql/flatsql.wasm": shippedBytes},
		// Declared hash does not match the bytes actually shipped in the
		// archive — simulates corruption/tampering between seal and apply.
		[]ManifestModuleTarget{{ID: "flatsql", Hash: sha256Hex([]byte("a-completely-different-payload")), Path: "runtime/modules/flatsql/flatsql.wasm"}},
	)

	_, err := Apply(paths, ApplyOptions{})
	if err == nil {
		t.Fatal("expected Apply to reject a module artifact whose bytes do not match its declared hash")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want a checksum mismatch rejection", err)
	}

	// Nothing was touched: the pre-existing artifact is exactly as it was.
	if got := readFile(t, filepath.Join(root, "runtime", "modules", "flatsql", "flatsql.wasm")); got != "old-flatsql-bytes" {
		t.Fatalf("flatsql.wasm = %q, want untouched old-flatsql-bytes", got)
	}
}

func TestApplyModuleTargetsRollsBackAllSwappedModulesOnPartialFailure(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	seedFile(t, root, "runtime/modules/flatsql/flatsql.wasm", "old-flatsql-bytes")
	seedFile(t, root, "runtime/modules/sitl/sitl.wasm", "old-sitl-bytes")
	// "broken" pre-exists as a DIRECTORY, so installing a module artifact at
	// that exact path fails in the install phase (after flatsql and sitl
	// have already been swapped) — an install-time fault distinct from a
	// phase-1 hash mismatch (which never reaches the install phase at all).
	seedFile(t, root, "runtime/modules/broken/keep.txt", "unrelated pre-existing content")

	newFlatsql, newSitl, newBroken := "new-flatsql-bytes", "new-sitl-bytes", "new-broken-bytes"
	stageSignedModuleUpdate(t, paths, signer, "9.9.9",
		map[string]string{
			"runtime/modules/flatsql/flatsql.wasm": newFlatsql,
			"runtime/modules/sitl/sitl.wasm":       newSitl,
			"runtime/modules/broken":               newBroken,
		},
		[]ManifestModuleTarget{
			{ID: "flatsql", Hash: sha256Hex([]byte(newFlatsql)), Path: "runtime/modules/flatsql/flatsql.wasm"},
			{ID: "sitl", Hash: sha256Hex([]byte(newSitl)), Path: "runtime/modules/sitl/sitl.wasm"},
			{ID: "broken", Hash: sha256Hex([]byte(newBroken)), Path: "runtime/modules/broken"},
		},
	)

	_, err := Apply(paths, ApplyOptions{})
	if err == nil {
		t.Fatal("expected Apply to fail when a later module's install path collides with an existing directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Fatalf("error = %v, want an install-path-is-a-directory failure", err)
	}

	// Both already-swapped modules (flatsql, sitl) must be rolled back to
	// their pre-apply bytes, not left half-applied.
	if got := readFile(t, filepath.Join(root, "runtime", "modules", "flatsql", "flatsql.wasm")); got != "old-flatsql-bytes" {
		t.Fatalf("flatsql.wasm after rollback = %q, want old-flatsql-bytes", got)
	}
	if got := readFile(t, filepath.Join(root, "runtime", "modules", "sitl", "sitl.wasm")); got != "old-sitl-bytes" {
		t.Fatalf("sitl.wasm after rollback = %q, want old-sitl-bytes", got)
	}
	// The untouched directory-shaped target is exactly as it was.
	if got := readFile(t, filepath.Join(root, "runtime", "modules", "broken", "keep.txt")); got != "unrelated pre-existing content" {
		t.Fatalf("broken/keep.txt = %q, want untouched", got)
	}
}

func TestApplyFullBundleManifestUnaffectedByModuleFields(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	// A full-bundle manifest (no Modules[]) must apply exactly as it did
	// before G4: whole-bundle swap, no module bookkeeping.
	stageSignedUpdate(t, paths, signer, "9.9.9", map[string]string{
		"bin/spacedatanetwork": "new-binary",
		"runtime/asset.txt":    "new-asset",
	})

	result, err := Apply(paths, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.ModuleUpdate {
		t.Fatal("full-bundle manifest (no Modules[]) must not be treated as a module update")
	}
	if len(result.AppliedModules) != 0 {
		t.Fatalf("AppliedModules = %v, want empty for a full-bundle apply", result.AppliedModules)
	}
	if got := readFile(t, filepath.Join(root, "bin", "spacedatanetwork")); got != "new-binary" {
		t.Fatalf("bin/spacedatanetwork = %q, want new-binary", got)
	}
}

// TestManifestSignatureVerifiesWithModuleFieldsPresent confirms G4's new
// additive fields (target.kind = module-update, modules[]) are covered by
// the signature via CanonicalManifestBytes exactly like every other field:
// a manifest carrying them verifies normally, and tampering with the
// modules[] array after signing is caught like any other tamper.
func TestManifestSignatureVerifiesWithModuleFieldsPresent(t *testing.T) {
	signer := newTestSigner(t)
	bundleBytes := []byte("bundle-bytes")
	wasmBytes := BuildCarrier(bundleBytes)
	moduleHash := sha256Hex([]byte("module-artifact-bytes"))

	manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
		doc["target"].(map[string]any)["kind"] = TargetKindModuleUpdate
		doc["modules"] = []map[string]any{
			{"id": "flatsql", "hash": moduleHash, "path": "runtime/modules/flatsql/flatsql.wasm"},
		}
	}, bundleBytes, wasmBytes)

	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.IsModuleUpdate() {
		t.Fatal("expected IsModuleUpdate() to be true")
	}
	if _, err := manifest.VerifyPayload(wasmBytes, bundleBytes, VerifyOptions{
		Platform:        manifest.Target.Platform,
		Arch:            manifest.Target.Arch,
		CurrentSequence: 41,
		TrustedRoots:    signer.roots(t),
	}); err != nil {
		t.Fatalf("VerifyPayload rejected a manifest with module fields present: %v", err)
	}

	// Tampering with the module target's declared hash after signing must
	// invalidate the signature exactly like tampering with any other field.
	tampered := strings.Replace(string(manifestBytes), moduleHash, sha256Hex([]byte("swapped-in-a-different-hash")), 1)
	if tampered == string(manifestBytes) {
		t.Fatal("tamper failed to change manifest")
	}
	tamperedManifest, err := ParseManifest([]byte(tampered))
	if err != nil {
		t.Fatal(err)
	}
	_, err = tamperedManifest.VerifyPayload(wasmBytes, bundleBytes, VerifyOptions{
		Platform:        tamperedManifest.Target.Platform,
		Arch:            tamperedManifest.Target.Arch,
		CurrentSequence: 41,
		TrustedRoots:    signer.roots(t),
	})
	if err == nil || err.Error() != "invalid update signature" {
		t.Fatalf("error = %v, want invalid update signature", err)
	}
}

func TestManifestValidateRejectsMalformedModuleTargets(t *testing.T) {
	signer := newTestSigner(t)
	bundleBytes := []byte("bundle-bytes")
	wasmBytes := BuildCarrier(bundleBytes)
	validHash := sha256Hex([]byte("ok"))

	cases := []struct {
		name    string
		modules []map[string]any
		kind    string // overrides target.kind when non-empty
		wantErr string
	}{
		{
			name:    "missing id",
			modules: []map[string]any{{"hash": validHash, "path": "runtime/modules/x/x.wasm"}},
			wantErr: "missing module target id at index 0",
		},
		{
			name:    "invalid hash",
			modules: []map[string]any{{"id": "x", "hash": "not-hex", "path": "runtime/modules/x/x.wasm"}},
			wantErr: "missing or invalid module target hash for x",
		},
		{
			name:    "missing path",
			modules: []map[string]any{{"id": "x", "hash": validHash, "path": ""}},
			wantErr: "missing module target path for x",
		},
		{
			name:    "path escapes bundle root",
			modules: []map[string]any{{"id": "x", "hash": validHash, "path": "../../etc/passwd"}},
			wantErr: "module target x: update bundle entry escapes bundle root: ../../etc/passwd",
		},
		{
			name:    "duplicate path",
			modules: []map[string]any{{"id": "x", "hash": validHash, "path": "runtime/modules/x/x.wasm"}, {"id": "y", "hash": validHash, "path": "runtime/modules/x/x.wasm"}},
			wantErr: "duplicate module target path: runtime/modules/x/x.wasm",
		},
		{
			name:    "module-update kind with no modules",
			modules: nil,
			kind:    TargetKindModuleUpdate,
			wantErr: "module-update target requires at least one module target",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
				if tc.kind != "" {
					doc["target"].(map[string]any)["kind"] = tc.kind
				}
				if tc.modules != nil {
					doc["modules"] = tc.modules
				}
			}, bundleBytes, wasmBytes)
			manifest, err := ParseManifest(manifestBytes)
			if err != nil {
				t.Fatal(err)
			}
			_, err = manifest.VerifyPayload(wasmBytes, bundleBytes, VerifyOptions{
				Platform:        manifest.Target.Platform,
				Arch:            manifest.Target.Arch,
				CurrentSequence: 41,
				TrustedRoots:    signer.roots(t),
			})
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("error = %v, want %s", err, tc.wantErr)
			}
		})
	}
}
