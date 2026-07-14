package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestPrewarmAOTCommandRegistered(t *testing.T) {
	requireCommand(t, []string{"prewarm-aot"}, "prewarm-aot")
	if help := prewarmAOTCmd.UsageString(); !strings.Contains(help, "--cache-dir") {
		t.Fatalf("prewarm-aot help missing --cache-dir:\n%s", help)
	}
}

// TestPrewarmAOTDefaultCacheDirMatchesDaemonResolution locks the load-bearing
// invariant: with --cache-dir unset the command targets the IDENTICAL
// directory the daemon opens the engine against. Any drift here would leave
// the daemon interpreting despite a "successful" prewarm.
func TestPrewarmAOTDefaultCacheDirMatchesDaemonResolution(t *testing.T) {
	if prewarmAOTCacheDir != "" {
		t.Fatalf("prewarm-aot --cache-dir default = %q, want empty (so runPrewarmAOT falls back to the daemon dir)", prewarmAOTCacheDir)
	}
	got := storage.EngineAOTCacheDir()
	var want string
	if base, err := os.UserCacheDir(); err == nil {
		want = filepath.Join(base, "flatsql-aot")
	} else {
		want = filepath.Join(os.TempDir(), "flatsql-aot")
	}
	if got != want {
		t.Fatalf("EngineAOTCacheDir() = %q, want %q", got, want)
	}
}

// sharedPrewarmAOTDir gives the prewarm test a stable cache dir so only the
// first run on a machine pays the ~35 s LLVM compile (mirrors flatsqlrt's
// sharedAOTDir convention); later runs are cache hits.
func sharedPrewarmAOTDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		return t.TempDir()
	}
	return filepath.Join(base, "sdn-prewarm-aot-test")
}

// TestRunPrewarmAOTWritesEngineArtifactAndIsIdempotent exercises the real
// compile path end-to-end and proves idempotency: run 1 leaves the engine
// artifact on disk; run 2 reports "already present" and never rewrites it.
func TestRunPrewarmAOTWritesEngineArtifactAndIsIdempotent(t *testing.T) {
	dir := sharedPrewarmAOTDir(t)

	// Run 1 populates the cache. On a warm machine run 1 is itself a cache
	// hit, so "compiled" vs "already present" is not deterministic here — we
	// only assert the artifact lands and the path is reported.
	var out1 bytes.Buffer
	if err := prewarmAOTArtifacts(&out1, dir); err != nil {
		if strings.Contains(err.Error(), "AOT compiler unavailable") {
			t.Skipf("libwasmedge lacks the AOT compiler; nothing to prewarm: %v", err)
		}
		t.Fatalf("prewarmAOTArtifacts run 1: %v", err)
	}
	enginePath := findEngineArtifact(t, dir)
	if !strings.Contains(out1.String(), enginePath) {
		t.Fatalf("run 1 output missing engine path %s:\n%s", enginePath, out1.String())
	}
	info1, err := os.Stat(enginePath)
	if err != nil {
		t.Fatalf("engine artifact not written by run 1: %v", err)
	}

	// Run 2 must be an idempotent no-op: run 1 populated the cache, so this
	// reports "already present" and leaves the artifact byte-for-byte intact.
	var out2 bytes.Buffer
	if err := prewarmAOTArtifacts(&out2, dir); err != nil {
		t.Fatalf("prewarmAOTArtifacts run 2: %v", err)
	}
	if !strings.Contains(out2.String(), "already present") {
		t.Fatalf("run 2 not idempotent (no 'already present' line):\n%s", out2.String())
	}
	if !strings.Contains(out2.String(), enginePath) {
		t.Fatalf("run 2 output missing engine path %s:\n%s", enginePath, out2.String())
	}
	info2, err := os.Stat(enginePath)
	if err != nil {
		t.Fatalf("engine artifact vanished after run 2: %v", err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) || info1.Size() != info2.Size() {
		t.Fatalf("run 2 rewrote the artifact (not a no-op): mtime %v->%v size %d->%d",
			info1.ModTime(), info2.ModTime(), info1.Size(), info2.Size())
	}
}

// findEngineArtifact returns the single engine AOT artifact in dir. The
// "flatsql-" glob deliberately excludes the "flatsqllink-" shim that shares
// the directory (the char after "flatsql" is "l", not the required "-").
func findEngineArtifact(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "flatsql-*.aot.wasm"))
	if err != nil {
		t.Fatalf("glob engine artifact: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one flatsql engine artifact in %s, got %v", dir, matches)
	}
	return matches[0]
}
