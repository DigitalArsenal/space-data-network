package testsupport

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var licensingWasmPathSuffixes = [][]string{
	{
		"space-data-network-modules",
		"licensing",
		"core",
		"dist",
		"isomorphic",
		"module.wasm",
	},
	{
		"space-data-network-plugins",
		"licensing",
		"core",
		"dist",
		"isomorphic",
		"module.wasm",
	},
}

// FindLicensingModuleWasmPath resolves the unified licensing module artifact
// for test packages running from either a normal checkout or a git worktree.
func FindLicensingModuleWasmPath(t testing.TB) (string, bool) {
	t.Helper()
	return findLicensingModuleWasmPath(t, 1)
}

func findLicensingModuleWasmPath(t testing.TB, callerDepth int) (string, bool) {
	t.Helper()
	if envPath := os.Getenv("ORBPRO_LICENSING_WASM_PATH"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath, true
		}
	}

	_, callerFile, _, ok := runtime.Caller(callerDepth)
	if !ok {
		t.Fatalf("runtime.Caller(%d) failed", callerDepth)
	}

	anchorDir := filepath.Dir(callerFile)
	var candidates []string
	candidates = appendStackPackageArtifactCandidates(candidates, anchorDir, licensingWasmPathSuffixes)
	for _, suffix := range licensingWasmPathSuffixes {
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

// MustFindLicensingModuleWasmPath resolves the artifact and fails when it is
// not available.
func MustFindLicensingModuleWasmPath(t testing.TB) string {
	t.Helper()

	if path, ok := findLicensingModuleWasmPath(t, 2); ok {
		return path
	}

	t.Fatalf(
		"could not find unified licensing artifact; checked %q",
		licensingWasmPathSuffixes,
	)
	return ""
}

// SkipIfNoLicensingModuleWasm skips tests whose purpose is to exercise the
// external licensing module artifact. Public CI can run the rest of the suite
// without checking the private artifact repo into this repository.
func SkipIfNoLicensingModuleWasm(t testing.TB) string {
	t.Helper()

	if path, ok := findLicensingModuleWasmPath(t, 2); ok {
		return path
	}

	t.Skip("unified licensing WASM artifact not available in this checkout")
	return ""
}
