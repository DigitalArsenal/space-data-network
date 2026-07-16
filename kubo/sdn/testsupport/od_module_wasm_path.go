package testsupport

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// odModuleWasmPathSuffixes locate the REAL analysis/od (orbit-determination)
// module-sdk WASM artifact — the module that fits SGP4 SupGP/OMM mean elements
// to operator ephemeris and returns a JSON fit result with RMS. The
// supplemental-OMM run test loads it through modulert and drives its "fit"
// method so the OD fit is the real WASM module executing (not a Go stub).
var odModuleWasmPathSuffixes = [][]string{
	{
		"space-data-network-modules",
		"analysis",
		"od",
		"dist",
		"isomorphic",
		"module.wasm",
	},
}

// odCommandModuleWasmPathSuffixes locate the analysis/od COMMAND-surface WASM
// artifact (module.command.wasm): the legacy WASI command build whose _start
// reads a $PIV request from stdin. dist/isomorphic/module.wasm is now the
// RESIDENT REACTOR build; the command build is retained beside it purely as the
// reactor==command RMS parity reference (TestReactorCommandFitParity).
var odCommandModuleWasmPathSuffixes = [][]string{
	{
		"space-data-network-modules",
		"analysis",
		"od",
		"dist",
		"isomorphic",
		"module.command.wasm",
	},
}

// odEphemerisFixtureSuffixes locate a REAL, checked-in operator ephemeris the OD
// fit consumes: the trimmed NASA public ISS OEM (CCSDS KVN, EME2000/UTC, ~12h of
// 4-min position+velocity state vectors, NORAD 25544). It is the canned
// ephemeris the run test feeds to od.fit (the ephemeris SOURCE fetch is
// firewalled from a workstation, so the fetch is stubbed with this fixture; the
// OD fit + OMM production + RMS + parity are all real).
var odEphemerisFixtureSuffixes = [][]string{
	{
		"space-data-network-modules",
		"analysis",
		"od",
		"tests",
		"data",
		"supgp-reference",
		"iss",
		"ISS.OEM_J2K_EPH.trimmed.txt",
	},
}

// odCelestrakReferenceSuffixes locate the same-day CelesTrak SupGP reference CSV
// for the ISS (NORAD 25544): the REFERENCE elements + RMS the run compares its
// own OD fit against (never an input — a reference for parity). The run test
// seeds an $OMM built from this row into the store as the CelesTrak reference
// lane, then scores it over the same ephemeris (A2.4d same-ephemeris parity).
var odCelestrakReferenceSuffixes = [][]string{
	{
		"space-data-network-modules",
		"analysis",
		"od",
		"tests",
		"data",
		"supgp-reference",
		"iss",
		"celestrak_supgp_iss-e_2026-07-13.csv",
	},
}

// findStackArtifact resolves one of the given suffix candidates for a test
// package running from either a normal checkout or a git worktree, honoring an
// optional environment override.
func findStackArtifact(t testing.TB, callerDepth int, envVar string, suffixes [][]string) (string, bool) {
	t.Helper()
	if envVar != "" {
		if envPath := os.Getenv(envVar); envPath != "" {
			if _, err := os.Stat(envPath); err == nil {
				return envPath, true
			}
		}
	}

	_, callerFile, _, ok := runtime.Caller(callerDepth)
	if !ok {
		t.Fatalf("runtime.Caller(%d) failed", callerDepth)
	}

	anchorDir := filepath.Dir(callerFile)
	var candidates []string
	candidates = appendStackPackageArtifactCandidates(candidates, anchorDir, suffixes)
	for _, suffix := range suffixes {
		candidates = append(candidates,
			filepath.Join(append([]string{anchorDir, "..", "..", "..", ".."}, suffix...)...),
			filepath.Join(append([]string{anchorDir, "..", "..", "..", "..", "..", ".."}, suffix...)...),
		)
	}
	for _, candidate := range candidates {
		cleaned := filepath.Clean(candidate)
		if _, err := os.Stat(cleaned); err == nil {
			return cleaned, true
		}
	}
	return "", false
}

// SkipIfNoODModuleWasm returns the analysis/od module WASM path, skipping the
// test when the artifact is not present in this checkout.
func SkipIfNoODModuleWasm(t testing.TB) string {
	t.Helper()
	if path, ok := findStackArtifact(t, 2, "SDN_OD_MODULE_WASM_PATH", odModuleWasmPathSuffixes); ok {
		return path
	}
	t.Skip("real analysis/od module WASM artifact not available in this checkout")
	return ""
}

// SkipIfNoODCommandModuleWasm returns the analysis/od COMMAND-surface module WASM
// path (module.command.wasm), skipping the test when the artifact is not present.
func SkipIfNoODCommandModuleWasm(t testing.TB) string {
	t.Helper()
	if path, ok := findStackArtifact(t, 2, "SDN_OD_COMMAND_MODULE_WASM_PATH", odCommandModuleWasmPathSuffixes); ok {
		return path
	}
	t.Skip("analysis/od command-surface module WASM (module.command.wasm) not available in this checkout")
	return ""
}

// SkipIfNoODEphemerisFixture returns the trimmed ISS OEM ephemeris fixture path,
// skipping the test when it is not present in this checkout.
func SkipIfNoODEphemerisFixture(t testing.TB) string {
	t.Helper()
	if path, ok := findStackArtifact(t, 2, "SDN_OD_EPHEMERIS_FIXTURE", odEphemerisFixtureSuffixes); ok {
		return path
	}
	t.Skip("real ISS OEM ephemeris fixture not available in this checkout")
	return ""
}

// FindODCelestrakReferenceCSV returns the CelesTrak SupGP ISS reference CSV path
// when present in this checkout.
func FindODCelestrakReferenceCSV(t testing.TB) (string, bool) {
	t.Helper()
	return findStackArtifact(t, 2, "SDN_OD_CELESTRAK_REFERENCE_CSV", odCelestrakReferenceSuffixes)
}
