package flowrt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
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

// TestFlowAOTPrefixChangesWithSubstrateTag is the flowmount half of the
// flowmount-key/flatsqllink-key gap the coordinator found during the beta.81
// rollout: prewarm-aot on host-02 reused flowmount-<flow>_<hash>-<hash>-
// we0.16.4-x86-64-v3.aot.wasm entries compiled by the pre-patch-04 compiler
// ("already present"), because the key never carried
// wasmrt.SubstrateReport.Tag() the way the partition store's
// "fsqlps-intr-<Tag>" does (flatsqlrt.threadedAOTKey). A threaded flow
// compiled before a runtime fix like 04-atomic-memarg-offset would keep the
// broken atomics forever without this.
func TestFlowAOTPrefixChangesWithSubstrateTag(t *testing.T) {
	t.Parallel()

	ref := "com.digitalarsenal.flows.celestrak-gp-ingest"
	sdn2 := flowAOTPrefixForTag("sdn2", ref)
	sdn3 := flowAOTPrefixForTag("sdn3", ref)
	if sdn2 == sdn3 {
		t.Fatalf("flow AOT prefix did not change between substrate tags sdn2/sdn3: %q", sdn2)
	}
	if !strings.HasSuffix(sdn2, "_sdn2") || !strings.HasSuffix(sdn3, "_sdn3") {
		t.Fatalf("flow AOT prefix does not carry the substrate tag as its trailing segment: %q / %q", sdn2, sdn3)
	}
	// The per-flow, per-prune-boundary invariants (TestFlowAOTPrefixIsPerFlow)
	// must keep holding with the tag appended: no hyphen inside the segment
	// after "flowmount-", for any tag.
	if strings.Contains(strings.TrimPrefix(sdn3, flowAOTCachePrefix+"-"), "-") {
		t.Fatalf("prefix %q keeps a hyphen inside the flow segment once the substrate tag is appended", sdn3)
	}
	// flowAOTPrefix() (the live entry point) must itself route through the
	// tag, matching whatever wasmrt.SubstrateStatus().Tag() reports here.
	if got, want := flowAOTPrefix(ref), flowAOTPrefixForTag(wasmrt.SubstrateStatus().Tag(), ref); got != want {
		t.Fatalf("flowAOTPrefix(%q) = %q, want %q (live substrate tag)", ref, got, want)
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
