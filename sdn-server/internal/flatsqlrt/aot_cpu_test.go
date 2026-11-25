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

// Two CPUs that differ — including two x86-64-v4 CPUs where only one carries
// AVX512-FP16 (stack issue 815) — must not share an AOT cache key.
func TestAOTCPUProfileDistinguishesInstructionSets(t *testing.T) {
	profile := aotCPUProfile()
	if profile == "" {
		t.Fatal("empty CPU profile would collapse every CPU onto one key")
	}
	if runtime.GOARCH == "amd64" {
		if !strings.HasPrefix(profile, "x86-64-v") {
			t.Fatalf("amd64 profile %q does not start with a psABI microarchitecture level", profile)
		}
	} else if profile != runtime.GOARCH && !strings.HasPrefix(profile, runtime.GOARCH+"-") {
		t.Fatalf("non-amd64 profile = %q, want GOARCH prefix %q", profile, runtime.GOARCH)
	}

	v4Base := []string{"avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl"}
	v4FP16 := append(append([]string(nil), v4Base...), "avx512_fp16")
	pBase := aotCPUProfileFor("amd64", v4Base)
	pFP16 := aotCPUProfileFor("amd64", v4FP16)
	if !strings.HasPrefix(pBase, "x86-64-v4-") || !strings.HasPrefix(pFP16, "x86-64-v4-") {
		t.Fatalf("unexpected v4 profiles: base=%q fp16=%q", pBase, pFP16)
	}
	if pBase == pFP16 {
		t.Fatalf("two x86-64-v4 CPUs differing in avx512_fp16 shared profile %q", pBase)
	}
	if got := aotCPUProfileFor("amd64", []string{"avx512vl", "avx512f", "avx512dq", "avx512cd", "avx512bw", "avx512f"}); got != pBase {
		t.Fatalf("reordered/duplicated flags changed profile: got %q, want %q", got, pBase)
	}
	parsed := parseProcCPUInfoFlags("processor\t: 0\nflags\t\t: avx512f avx512_fp16\n")
	if len(parsed) != 2 || parsed[0] != "avx512f" || parsed[1] != "avx512_fp16" {
		t.Fatalf("parseProcCPUInfoFlags = %v", parsed)
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
