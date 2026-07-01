package testsupport

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var starlinkSourceWasmPathSuffixes = [][]string{
	{
		"space-data-network-modules",
		"data-source",
		"spacex-starlink-source",
		"dist",
		"isomorphic",
		"module.wasm",
	},
	{
		"space-data-network-plugins",
		"data-source",
		"spacex-starlink-source",
		"dist",
		"isomorphic",
		"module.wasm",
	},
}

// FindStarlinkSourceModuleWasmPath resolves the built spacex-starlink-source
// data-source module artifact for test packages running from either a normal
// checkout or a git worktree. Set ORBPRO_STARLINK_SOURCE_WASM_PATH to override.
func FindStarlinkSourceModuleWasmPath(t testing.TB) (string, bool) {
	t.Helper()
	return findStarlinkSourceModuleWasmPath(t, 1)
}

func findStarlinkSourceModuleWasmPath(t testing.TB, callerDepth int) (string, bool) {
	t.Helper()
	if envPath := os.Getenv("ORBPRO_STARLINK_SOURCE_WASM_PATH"); envPath != "" {
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
	for _, suffix := range starlinkSourceWasmPathSuffixes {
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

// SkipIfNoStarlinkSourceWasm returns the built spacex-starlink-source module
// artifact path, skipping the test when it is not available in this checkout.
func SkipIfNoStarlinkSourceWasm(t testing.TB) string {
	t.Helper()

	if path, ok := findStarlinkSourceModuleWasmPath(t, 2); ok {
		return path
	}

	t.Skip("spacex-starlink-source WASM artifact not available in this checkout " +
		"(set ORBPRO_STARLINK_SOURCE_WASM_PATH)")
	return ""
}
