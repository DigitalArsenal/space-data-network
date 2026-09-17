package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeInstalledRuntime fills the bundle root's runtime/ tree with the files
// a real install carries there, so an amputation is observable as the loss of
// something the node needs rather than of an empty directory.
func writeInstalledRuntime(t *testing.T, root string) []string {
	t.Helper()
	files := map[string]string{
		"runtime/kubo/ipfs":                                 "kubo-binary",
		"runtime/sdn/spacedatanetwork":                      "daemon-binary",
		"runtime/modules/org.spacedatanetwork.updater.wasm": "updater-wasm",
		"runtime/ui/sdn/index.html":                         "dashboard",
	}
	var paths []string
	for rel, contents := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, full)
	}
	return paths
}

// TestApplyRefusesPayloadThatWouldAmputateInstalledRuntime pins the fix for
// the 2026-09-16 bricking: a binary-only payload (bin/spacedatanetwork plus
// manifest.json — exactly what the internal fleet publishing lane stages)
// applied over a full install used to retire runtime/ into the rollback
// directory and reinstall nothing, taking Kubo, the daemon, the updater wasm
// and the UI with it, while reporting success. It must now fail loudly and
// leave the install untouched.
func TestApplyRefusesPayloadThatWouldAmputateInstalledRuntime(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	runtimeFiles := writeInstalledRuntime(t, root)

	stageSignedUpdate(t, paths, signer, "9.9.9", map[string]string{
		"bin/spacedatanetwork": "new-binary",
	})

	_, err := Apply(paths, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply accepted a payload that omits runtime/; it would have amputated the installed runtime tree")
	}
	if !strings.Contains(err.Error(), "omits runtime/") {
		t.Fatalf("Apply error = %v, want a refusal naming the omitted runtime/ tree", err)
	}

	for _, file := range runtimeFiles {
		if _, statErr := os.Stat(file); statErr != nil {
			t.Fatalf("refused update still removed %s: %v", file, statErr)
		}
	}
	binary, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil {
		t.Fatal(err)
	}
	if string(binary) != "old-binary" {
		t.Fatalf("refused update swapped the binary anyway: %q", binary)
	}
	state, err := LoadState(paths)
	if err != nil {
		t.Fatal(err)
	}
	if state.Sequence != 0 || state.UpdateID != "" {
		t.Fatalf("refused update recorded itself as installed: %+v", state)
	}
}

// TestApplyAllowsPayloadWithoutRuntimeWhenInstallHasNone keeps the guard
// narrow: it refuses to RETIRE a runtime tree, so an install that never had
// one (a bare binary install) still takes the same payload.
func TestApplyAllowsPayloadWithoutRuntimeWhenInstallHasNone(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	if err := os.RemoveAll(filepath.Join(root, "runtime")); err != nil {
		t.Fatal(err)
	}

	stageSignedUpdate(t, paths, signer, "9.9.9", map[string]string{
		"bin/spacedatanetwork": "new-binary",
	})

	if _, err := Apply(paths, ApplyOptions{}); err != nil {
		t.Fatalf("Apply returned error for an install with no runtime tree: %v", err)
	}
	binary, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil {
		t.Fatal(err)
	}
	if string(binary) != "new-binary" {
		t.Fatalf("binary = %q, want new-binary", binary)
	}
}

// A TOKEN TREE DEFEATED THE FIRST VERSION OF THIS GUARD. It checked only that
// the payload HAD a runtime/ entry, so a payload shipping bin/spacedatanetwork
// plus a single runtime/.keep passed and then amputated exactly as before: the
// swap replaces the whole entry, so every child the payload does not carry is
// retired with the old tree. Apply returned nil and kubo, the daemon and the
// updater wasm were all gone afterwards.
func TestApplyRefusesTokenRuntimeTreeThatCarriesNothing(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	runtimeFiles := writeInstalledRuntime(t, root)

	stageSignedUpdate(t, paths, signer, "9.9.9", map[string]string{
		"bin/spacedatanetwork": "new-binary",
		"runtime/.keep":        "",
	})

	_, err := Apply(paths, ApplyOptions{})
	if err == nil {
		t.Fatal("Apply accepted a payload whose runtime/ carries none of the install's children; it would have amputated them")
	}
	if !strings.Contains(err.Error(), "runtime/") {
		t.Fatalf("Apply error = %v, want a refusal naming runtime/", err)
	}
	// The message must name what would have been lost, not just that something was.
	for _, child := range []string{"kubo", "sdn", "modules", "ui"} {
		if !strings.Contains(err.Error(), child) {
			t.Errorf("refusal does not name the lost child %q: %v", child, err)
		}
	}
	for _, file := range runtimeFiles {
		if _, statErr := os.Stat(file); statErr != nil {
			t.Fatalf("refused update still removed %s: %v", file, statErr)
		}
	}
}

// A DRY RUN THAT CHECKS NOTHING IS A GREEN LIGHT FOR A DOOMED APPLY. Apply
// returned at its DryRun branch before the bundle was even extracted, so the
// payload guard never ran and dry-running the amputating payload reported
// success — the one question an operator runs a dry run to answer.
func TestDryRunRefusesThePayloadThatApplyWouldRefuse(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	writeInstalledRuntime(t, root)

	stageSignedUpdate(t, paths, signer, "9.9.9", map[string]string{
		"bin/spacedatanetwork": "new-binary",
	})

	if _, err := Apply(paths, ApplyOptions{DryRun: true}); err == nil {
		t.Fatal("dry run reported success for a payload that a real apply refuses")
	}

	// And it must stay a dry run: nothing recorded, nothing swapped, no
	// scratch extraction left behind.
	state, err := LoadState(paths)
	if err != nil {
		t.Fatal(err)
	}
	if state.Sequence != 0 || state.UpdateID != "" {
		t.Fatalf("dry run recorded itself as installed: %+v", state)
	}
	binary, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil {
		t.Fatal(err)
	}
	if string(binary) != "old-binary" {
		t.Fatalf("dry run swapped the binary: %q", binary)
	}
	leftovers, err := filepath.Glob(filepath.Join(paths.Incoming, "*.dryrun"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("dry run left its extraction behind: %v", leftovers)
	}
}

// The dry run must still PASS a payload a real apply would accept, or it is
// just a different kind of useless.
func TestDryRunAcceptsACompletePayload(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	writeInstalledRuntime(t, root)

	stageSignedUpdate(t, paths, signer, "9.9.9", map[string]string{
		"bin/spacedatanetwork":                              "new-binary",
		"runtime/kubo/ipfs":                                 "kubo-binary",
		"runtime/sdn/spacedatanetwork":                      "daemon-binary",
		"runtime/modules/org.spacedatanetwork.updater.wasm": "updater-wasm",
		"runtime/ui/sdn/index.html":                         "dashboard",
	})

	res, err := Apply(paths, ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run refused a complete payload: %v", err)
	}
	if !res.DryRun || res.Version != "9.9.9" {
		t.Fatalf("dry run result = %+v, want DryRun with version 9.9.9", res)
	}
}
