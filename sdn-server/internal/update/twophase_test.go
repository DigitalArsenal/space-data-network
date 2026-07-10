package update

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// setupTwoPhaseBundleRoot extends setupBundleRoot with a seeded
// runtime/kubo/ subtree and a non-kubo runtime/ child (runtime/sdn/), so
// tests can exercise the Kubo-first two-phase apply path and assert both
// phases actually swap their own content independently.
func setupTwoPhaseBundleRoot(t *testing.T, signer *testSigner) (Paths, string) {
	t.Helper()
	paths, root := setupBundleRoot(t, signer)
	if err := os.MkdirAll(filepath.Join(root, "runtime", "kubo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime", "kubo", "ipfs"), []byte("old-kubo-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "runtime", "sdn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime", "sdn", "asset.txt"), []byte("old-sdn-asset"), 0o644); err != nil {
		t.Fatal(err)
	}
	return paths, root
}

func twoPhaseBundleFiles(binContents, kuboContents, sdnAssetContents string) map[string]string {
	return map[string]string{
		"bin/spacedatanetwork":  binContents,
		"runtime/kubo/ipfs":     kuboContents,
		"runtime/sdn/asset.txt": sdnAssetContents,
	}
}

// recordingKuboHook is the KuboPhaseHook test double: it records call order
// and lets a test snapshot bundle-root state exactly when AfterSwap fires,
// i.e. after phase 1 has committed but before phase 2 has started.
type recordingKuboHook struct {
	calls             []string
	snapshotAfterSwap func()
}

func (h *recordingKuboHook) BeforeSwap(Paths, string) error {
	h.calls = append(h.calls, "BeforeSwap")
	return nil
}

func (h *recordingKuboHook) AfterSwap(Paths, string) error {
	h.calls = append(h.calls, "AfterSwap")
	if h.snapshotAfterSwap != nil {
		h.snapshotAfterSwap()
	}
	return nil
}

func TestApplyTwoPhaseAppliesKuboThenSDNInOrder(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupTwoPhaseBundleRoot(t, signer)
	stageSignedUpdate(t, paths, signer, "9.9.9", twoPhaseBundleFiles("new-binary", "new-kubo-binary", "new-sdn-asset"))

	var kuboAtHookTime, binAtHookTime string
	hook := &recordingKuboHook{}
	hook.snapshotAfterSwap = func() {
		if b, err := os.ReadFile(filepath.Join(root, "runtime", "kubo", "ipfs")); err == nil {
			kuboAtHookTime = string(b)
		}
		if b, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork")); err == nil {
			binAtHookTime = string(b)
		}
	}

	result, err := Apply(paths, ApplyOptions{KuboPhaseHook: hook})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.TwoPhase {
		t.Fatal("expected a two-phase apply for a bundle with a separable runtime/kubo/ subtree")
	}
	if want := []string{"BeforeSwap", "AfterSwap"}; !slices.Equal(hook.calls, want) {
		t.Fatalf("hook call order = %v, want %v", hook.calls, want)
	}
	if kuboAtHookTime != "new-kubo-binary" {
		t.Fatalf("kubo contents at AfterSwap = %q, want new-kubo-binary (phase 1 already committed)", kuboAtHookTime)
	}
	if binAtHookTime != "old-binary" {
		t.Fatalf("bin contents at AfterSwap = %q, want old-binary (phase 2/SDN has not started yet)", binAtHookTime)
	}

	finalKubo, err := os.ReadFile(filepath.Join(root, "runtime", "kubo", "ipfs"))
	if err != nil || string(finalKubo) != "new-kubo-binary" {
		t.Fatalf("final kubo = %q, err=%v", finalKubo, err)
	}
	finalBin, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil || string(finalBin) != "new-binary" {
		t.Fatalf("final bin = %q, err=%v", finalBin, err)
	}
	finalAsset, err := os.ReadFile(filepath.Join(root, "runtime", "sdn", "asset.txt"))
	if err != nil || string(finalAsset) != "new-sdn-asset" {
		t.Fatalf("final sdn asset = %q, err=%v", finalAsset, err)
	}

	oldKubo, err := os.ReadFile(filepath.Join(result.RollbackPath, "runtime", "kubo", "ipfs"))
	if err != nil || string(oldKubo) != "old-kubo-binary" {
		t.Fatalf("rollback kubo = %q, err=%v", oldKubo, err)
	}
	oldBin, err := os.ReadFile(filepath.Join(result.RollbackPath, "bin", "spacedatanetwork"))
	if err != nil || string(oldBin) != "old-binary" {
		t.Fatalf("rollback bin = %q, err=%v", oldBin, err)
	}

	if _, err := os.Stat(paths.Phase); !os.IsNotExist(err) {
		t.Fatalf("phase marker should be cleared after a successful apply, stat err=%v", err)
	}
}

func TestApplyTwoPhaseRollsBackBothPhasesOnPhase2Failure(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupTwoPhaseBundleRoot(t, signer)
	stageSignedUpdate(t, paths, signer, "9.9.9", twoPhaseBundleFiles("new-binary", "new-kubo-binary", "new-sdn-asset"))

	injected := errors.New("simulated sdn phase failure")
	_, err := Apply(paths, ApplyOptions{
		testFault: &applyFaultInjection{beforeMainPhase: func() error { return injected }},
	})
	if err == nil || !strings.Contains(err.Error(), injected.Error()) {
		t.Fatalf("Apply error = %v, want it to wrap %v", err, injected)
	}

	kubo, err := os.ReadFile(filepath.Join(root, "runtime", "kubo", "ipfs"))
	if err != nil || string(kubo) != "old-kubo-binary" {
		t.Fatalf("kubo after rollback = %q, err=%v, want old-kubo-binary (phase 1 rolled back too)", kubo, err)
	}
	bin, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil || string(bin) != "old-binary" {
		t.Fatalf("bin after rollback = %q, err=%v, want old-binary", bin, err)
	}
	asset, err := os.ReadFile(filepath.Join(root, "runtime", "sdn", "asset.txt"))
	if err != nil || string(asset) != "old-sdn-asset" {
		t.Fatalf("sdn asset after rollback = %q, err=%v, want old-sdn-asset", asset, err)
	}

	if _, err := os.Stat(paths.Phase); !os.IsNotExist(err) {
		t.Fatalf("phase marker should be cleared after a rolled-back apply, stat err=%v", err)
	}
}

func TestApplyDegradesToSinglePhaseWithoutKuboSubtree(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer) // no runtime/kubo/ seeded or shipped
	stageSignedUpdate(t, paths, signer, "9.9.9", map[string]string{
		"bin/spacedatanetwork": "new-binary",
	})

	result, err := Apply(paths, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.TwoPhase {
		t.Fatal("expected the degraded single-phase path when the bundle has no separable runtime/kubo/ subtree")
	}
	if _, err := os.Stat(paths.Phase); !os.IsNotExist(err) {
		t.Fatalf("phase marker should never be created on the single-phase path, stat err=%v", err)
	}
	bin, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil || string(bin) != "new-binary" {
		t.Fatalf("bin = %q, err=%v", bin, err)
	}
}

func TestManifestValidateEnforcesMinKuboVersionCompatibilityGate(t *testing.T) {
	signer := newTestSigner(t)
	bundleBytes := []byte("bundle-bytes")
	wasmBytes := BuildCarrier(bundleBytes)
	manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
		doc["compatibility"] = map[string]any{"min_kubo_version": "0.30.0"}
	}, bundleBytes, wasmBytes)
	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}

	baseOpts := VerifyOptions{
		Platform:        runtime.GOOS,
		Arch:            runtime.GOARCH,
		CurrentSequence: 41,
		TrustedRoots:    signer.roots(t),
	}

	t.Run("rejects an older installed kubo version", func(t *testing.T) {
		opts := baseOpts
		opts.InstalledKuboVersion = "0.28.0"
		_, err := manifest.VerifyPayload(wasmBytes, bundleBytes, opts)
		if err == nil || !strings.Contains(err.Error(), "requires kubo >= 0.30.0") {
			t.Fatalf("error = %v, want a min_kubo_version rejection", err)
		}
	})

	t.Run("accepts an equal or newer installed kubo version", func(t *testing.T) {
		opts := baseOpts
		opts.InstalledKuboVersion = "0.30.0"
		if _, err := manifest.VerifyPayload(wasmBytes, bundleBytes, opts); err != nil {
			t.Fatalf("VerifyPayload returned error: %v", err)
		}
		opts.InstalledKuboVersion = "v0.31.2"
		if _, err := manifest.VerifyPayload(wasmBytes, bundleBytes, opts); err != nil {
			t.Fatalf("VerifyPayload returned error: %v", err)
		}
	})

	t.Run("skips the gate when the installed version is unknown", func(t *testing.T) {
		opts := baseOpts // InstalledKuboVersion left empty: back-compat, no regression.
		if _, err := manifest.VerifyPayload(wasmBytes, bundleBytes, opts); err != nil {
			t.Fatalf("VerifyPayload returned error: %v", err)
		}
	})
}

func TestApplySimulatedCrashLeavesRecoverableStateThenRecovers(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupTwoPhaseBundleRoot(t, signer)
	stageSignedUpdate(t, paths, signer, "9.9.9", twoPhaseBundleFiles("new-binary", "new-kubo-binary", "new-sdn-asset"))

	_, err := Apply(paths, ApplyOptions{
		testFault: &applyFaultInjection{crashAfterKuboPhase: true},
	})
	if !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("Apply error = %v, want errSimulatedCrash", err)
	}

	// Partial "crashed" on-disk state: phase 1 committed (kubo already new),
	// phase 2 never ran (sdn/bin still old).
	kubo, err := os.ReadFile(filepath.Join(root, "runtime", "kubo", "ipfs"))
	if err != nil || string(kubo) != "new-kubo-binary" {
		t.Fatalf("kubo after simulated crash = %q, err=%v, want new-kubo-binary (phase 1 committed)", kubo, err)
	}
	bin, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil || string(bin) != "old-binary" {
		t.Fatalf("bin after simulated crash = %q, err=%v, want old-binary (phase 2 never ran)", bin, err)
	}
	if _, err := os.Stat(paths.Phase); err != nil {
		t.Fatalf("expected the phase marker to survive the simulated crash: %v", err)
	}

	recovered, err := RecoverPendingApply(paths)
	if err != nil {
		t.Fatalf("RecoverPendingApply returned error: %v", err)
	}
	if !recovered {
		t.Fatal("expected RecoverPendingApply to report a recovery")
	}

	kuboAfterRecovery, err := os.ReadFile(filepath.Join(root, "runtime", "kubo", "ipfs"))
	if err != nil || string(kuboAfterRecovery) != "old-kubo-binary" {
		t.Fatalf("kubo after recovery = %q, err=%v, want old-kubo-binary (provably rolled back)", kuboAfterRecovery, err)
	}
	if _, err := os.Stat(paths.Phase); !os.IsNotExist(err) {
		t.Fatalf("phase marker should be cleared after recovery, stat err=%v", err)
	}

	recoveredAgain, err := RecoverPendingApply(paths)
	if err != nil || recoveredAgain {
		t.Fatalf("RecoverPendingApply should be a no-op once already recovered: recovered=%v err=%v", recoveredAgain, err)
	}
}

// TestRollbackLastRestoresBundleAppliedViaTwoPhasePath confirms the
// existing RollbackLast (a single whole-tree swap, unmodified by G3) still
// works after a two-phase apply: phase 1 and phase 2 each write their old
// bytes into the SAME rollbackDir/runtime/<child> slots, so by the time
// Apply finishes, rollbackDir/runtime is a complete reconstruction of the
// pre-apply runtime/ tree and RollbackLast's ordinary directory-level swap
// restores it correctly.
func TestRollbackLastRestoresBundleAppliedViaTwoPhasePath(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupTwoPhaseBundleRoot(t, signer)
	stageSignedUpdate(t, paths, signer, "9.9.9", twoPhaseBundleFiles("new-binary", "new-kubo-binary", "new-sdn-asset"))

	applied, err := Apply(paths, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !applied.TwoPhase {
		t.Fatal("expected a two-phase apply")
	}

	result, err := RollbackLast(paths, RollbackOptions{Reason: "test rollback after two-phase apply"})
	if err != nil {
		t.Fatalf("RollbackLast returned error: %v", err)
	}
	if result.RestoredVersion != "1.0.0" {
		t.Fatalf("RestoredVersion = %s, want 1.0.0", result.RestoredVersion)
	}

	kubo, err := os.ReadFile(filepath.Join(root, "runtime", "kubo", "ipfs"))
	if err != nil || string(kubo) != "old-kubo-binary" {
		t.Fatalf("kubo after rollback = %q, err=%v, want old-kubo-binary", kubo, err)
	}
	bin, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil || string(bin) != "old-binary" {
		t.Fatalf("bin after rollback = %q, err=%v, want old-binary", bin, err)
	}
	asset, err := os.ReadFile(filepath.Join(root, "runtime", "sdn", "asset.txt"))
	if err != nil || string(asset) != "old-sdn-asset" {
		t.Fatalf("sdn asset after rollback = %q, err=%v, want old-sdn-asset", asset, err)
	}
}

func TestApplyRecoversFromCrashOnNextApplyCall(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupTwoPhaseBundleRoot(t, signer)
	stageSignedUpdate(t, paths, signer, "9.9.9", twoPhaseBundleFiles("new-binary", "new-kubo-binary", "new-sdn-asset"))

	_, err := Apply(paths, ApplyOptions{testFault: &applyFaultInjection{crashAfterKuboPhase: true}})
	if !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("first Apply error = %v, want errSimulatedCrash", err)
	}

	// The next Apply call (no fault injection: this is what a restarted
	// daemon would naturally do) must self-heal the crash and then proceed
	// to successfully re-apply the still-staged update end to end.
	result, err := Apply(paths, ApplyOptions{})
	if err != nil {
		t.Fatalf("second Apply (next apply after crash) returned error: %v", err)
	}
	if result.Version != "9.9.9" || !result.TwoPhase {
		t.Fatalf("unexpected recovered apply result: %+v", result)
	}
	kubo, _ := os.ReadFile(filepath.Join(root, "runtime", "kubo", "ipfs"))
	bin, _ := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	asset, _ := os.ReadFile(filepath.Join(root, "runtime", "sdn", "asset.txt"))
	if string(kubo) != "new-kubo-binary" || string(bin) != "new-binary" || string(asset) != "new-sdn-asset" {
		t.Fatalf("state after recovery+reapply is not fully new: kubo=%q bin=%q asset=%q", kubo, bin, asset)
	}
	if _, err := os.Stat(paths.Phase); !os.IsNotExist(err) {
		t.Fatalf("phase marker should be cleared after a successful reapply, stat err=%v", err)
	}
	if entries, _ := os.ReadDir(paths.Staged); len(entries) != 0 {
		t.Fatalf("staged dir not cleaned up after a successful reapply: %v", entries)
	}
}
