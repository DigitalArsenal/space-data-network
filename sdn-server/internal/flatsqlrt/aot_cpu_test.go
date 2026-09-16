package flatsqlrt

import (
	"runtime"
	"strings"
	"testing"
)

// The bug this guards: an AOT artifact is native code for the CPU that compiled
// it, the cache key did not say which CPU, and a container image prewarmed on a
// runner with AVX-512 killed the node with SIGILL on a runner without it.
func TestAOTArtifactPathNamesTheCPU(t *testing.T) {
	path := aotArtifactPath(t.TempDir(), "flatsql", []byte("engine"))
	profile := aotCPUProfile()

	if !strings.Contains(path, profile) {
		t.Fatalf("AOT artifact path %q does not name the CPU profile %q; an artifact "+
			"compiled for one instruction set would load on a CPU that cannot run it", path, profile)
	}
	if !strings.HasSuffix(path, ".aot.wasm") {
		t.Fatalf("artifact path lost its extension: %s", path)
	}
}

// Two CPUs that differ must not share a key. Exercised through the profile
// itself, since the running CPU cannot be changed from a test.
func TestAOTCPUProfileDistinguishesInstructionSets(t *testing.T) {
	profile := aotCPUProfile()
	if profile == "" {
		t.Fatal("empty CPU profile would collapse every CPU onto one key")
	}
	if runtime.GOARCH == "amd64" {
		valid := map[string]bool{
			"x86-64-v1": true, "x86-64-v2": true, "x86-64-v3": true, "x86-64-v4": true,
		}
		if !valid[profile] {
			t.Fatalf("amd64 profile %q is not a psABI microarchitecture level", profile)
		}
	} else if profile != runtime.GOARCH {
		t.Fatalf("non-amd64 profile = %q, want GOARCH %q", profile, runtime.GOARCH)
	}
}

func TestSanitizeAOTKeyPartKeepsFilenamesSafe(t *testing.T) {
	for input, want := range map[string]string{
		"x86-64-v4":     "x86-64-v4",
		"0.16.4":        "0.16.4",
		"weird/../path": "weird_.._path",
		"a b\tc":        "a_b_c",
	} {
		if got := sanitizeAOTKeyPart(input); got != want {
			t.Errorf("sanitizeAOTKeyPart(%q) = %q, want %q", input, got, want)
		}
	}
}
