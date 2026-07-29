package flatsqlrt

// The AOT cache must survive a rollback.
//
// Priming the cache for three new ingest-flow hashes evicted the three previous
// hashes, so when those bundles were restored the sole CelesTrak producer came
// back up INTERPRETED — next to a cache directory that looked fully populated.
// On a 1-vCPU box that is the difference between a catalog parse that finishes
// and one that never does.
//
// Two rules, both pinned here:
//   1. PrewarmAOTArtifact never deletes anything. An operator priming the
//      artifact they are about to run must not delete the one they may have to
//      roll back to.
//   2. The service path still evicts, but keeps the immediate predecessor, so
//      a rollback finds compiled code without the cache growing without bound.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	testEpoch = time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	testStep  = time.Hour
)

// writeArtifact fakes a cached artifact for `wasm` under `prefix` without
// invoking the AOT compiler: retention is a filesystem policy and is testable
// as one.
func writeArtifact(t *testing.T, cacheDir, prefix string, wasm []byte) string {
	t.Helper()
	path := aotArtifactPath(cacheDir, prefix, wasm)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(path, []byte("compiled:"+string(wasm)), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return path
}

func TestPruneKeepsTheImmediatePredecessorForRollback(t *testing.T) {
	cacheDir := t.TempDir()
	const prefix = "flowmount-test_flow"

	oldest := writeArtifact(t, cacheDir, prefix, []byte("generation-1"))
	previous := writeArtifact(t, cacheDir, prefix, []byte("generation-2"))
	current := writeArtifact(t, cacheDir, prefix, []byte("generation-3"))
	sibling := writeArtifact(t, cacheDir, "flowmount-other_flow", []byte("generation-1"))

	// Order the generations in time so "predecessor" is unambiguous.
	mustTouch(t, oldest, 1)
	mustTouch(t, previous, 2)
	mustTouch(t, current, 3)

	pruneAOTArtifacts(cacheDir, prefix, filepath.Base(current))

	if _, err := os.Stat(current); err != nil {
		t.Fatalf("the artifact just compiled must survive: %v", err)
	}
	if _, err := os.Stat(previous); err != nil {
		t.Fatalf("the immediate predecessor must survive so a rollback stays AOT: %v", err)
	}
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatalf("generations older than the predecessor must be evicted, stat err=%v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("another flow's artifact must never be touched: %v", err)
	}
}

func TestPrewarmNeverEvicts(t *testing.T) {
	cacheDir := t.TempDir()
	const prefix = "flowmount-test_flow"

	deployed := writeArtifact(t, cacheDir, prefix, []byte("deployed"))
	older := writeArtifact(t, cacheDir, prefix, []byte("older"))
	mustTouch(t, older, 1)
	mustTouch(t, deployed, 2)

	// Prewarming a THIRD hash: whatever it does with the compiler, it must not
	// remove either artifact already on disk. The compile itself is allowed to
	// fail in this environment — the assertion is about the directory.
	_, alreadyPresent, err := PrewarmAOTArtifact(cacheDir, prefix, []byte("incoming"))
	if alreadyPresent {
		t.Fatalf("a hash never written cannot be already present")
	}
	_ = err // a WasmEdge compiler failure is not what this test is about.

	for _, path := range []string{deployed, older} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("prewarm evicted %s: %v — a rollback would come up interpreted",
				filepath.Base(path), statErr)
		}
	}
}

// mustTouch orders artifacts in time: ordinal 1 is the oldest.
func mustTouch(t *testing.T, path string, ordinal int) {
	t.Helper()
	when := testEpoch.Add(testStep * time.Duration(ordinal))
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}
