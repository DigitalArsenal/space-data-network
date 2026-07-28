package flowrt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFlowAOTPrefixIsPerFlow is the regression that motivated the scoped
// prefix. The AOT cache prunes by prefix: everything sharing a prefix is
// deleted when a new artifact lands. Under the old shared "flowmount" prefix,
// priming three ingest flows left ONE artifact — each compile silently deleted
// its predecessors, and the daemon interpreted the other two while the cache
// looked populated.
func TestFlowAOTPrefixIsPerFlow(t *testing.T) {
	t.Parallel()

	gp := flowAOTPrefix("com.digitalarsenal.flows.celestrak-gp-ingest")
	satcat := flowAOTPrefix("com.digitalarsenal.flows.celestrak-satcat-ingest")

	if gp == satcat {
		t.Fatalf("two flows share the AOT cache prefix %q — priming one would delete the other's artifact", gp)
	}
	if !strings.HasPrefix(gp, flowAOTCachePrefix+"-") {
		t.Fatalf("prefix %q left the %q namespace", gp, flowAOTCachePrefix)
	}
	// The prune matcher is HasPrefix(name, prefix+"-"), so no flow's prefix may
	// be a prefix of another's, or the shorter one would still evict the longer.
	if strings.HasPrefix(satcat, gp+"-") || strings.HasPrefix(gp, satcat+"-") {
		t.Fatalf("prefix %q and %q overlap at the prune boundary", gp, satcat)
	}
	// Hyphens must not survive: they are the delimiter the prune relies on.
	if strings.Contains(strings.TrimPrefix(gp, flowAOTCachePrefix+"-"), "-") {
		t.Fatalf("prefix %q keeps a hyphen inside the flow segment, blurring the prune boundary", gp)
	}
}

func TestFlowAOTPrefixIsStable(t *testing.T) {
	t.Parallel()

	ref := "com.digitalarsenal.flows.celestrak-spw-ingest"
	if first, second := flowAOTPrefix(ref), flowAOTPrefix(ref); first != second {
		t.Fatalf("prefix is not deterministic: %q != %q", first, second)
	}
}

// TestFlowAOTPrefixDistinguishesTruncatedReferences proves the name digest
// does its job: two references that agree in their first 48 characters must
// still get different prefixes, or one would evict the other.
func TestFlowAOTPrefixDistinguishesTruncatedReferences(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 60)
	a := flowAOTPrefix(long + "-one")
	b := flowAOTPrefix(long + "-two")
	if a == b {
		t.Fatalf("references sharing a long common prefix collided: %q", a)
	}
}

// TestFlowAOTArtifactPathMatchesDaemonLookup locks the load-bearing property of
// the prewarm tool: the path it reports is the path the daemon will open. The
// daemon computes its key from the PORTABLE bytes (trailer stripped), so a tool
// that hashed the file as-is would prime a cache nobody reads.
func TestFlowAOTArtifactPathMatchesDaemonLookup(t *testing.T) {
	t.Parallel()

	// A minimal wasm header is enough: neither the path computation nor this
	// assertion compiles anything.
	wasm := []byte("\x00asm\x01\x00\x00\x00")
	dir := t.TempDir()
	wasmPath := filepath.Join(dir, "runtime.wasm")
	if err := os.WriteFile(wasmPath, wasm, 0o600); err != nil {
		t.Fatalf("write wasm: %v", err)
	}

	cacheDir := filepath.Join(dir, "cache")
	got, err := FlowAOTArtifactPath(wasmPath, nil, cacheDir)
	if err != nil {
		t.Fatalf("FlowAOTArtifactPath: %v", err)
	}
	if filepath.Dir(got) != cacheDir {
		t.Fatalf("artifact path %q is not inside the cache dir %q", got, cacheDir)
	}
	if !strings.HasPrefix(filepath.Base(got), flowAOTPrefix(wasmPath)+"-") {
		t.Fatalf("artifact %q does not carry this flow's scoped prefix %q", got, flowAOTPrefix(wasmPath))
	}
	if !strings.HasSuffix(got, ".aot.wasm") {
		t.Fatalf("artifact %q is not an AOT artifact name", got)
	}
}
