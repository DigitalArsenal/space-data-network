package testsupport

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var licensingWasmPathSuffix = []string{
	"space-data-network-plugins",
	"licensing",
	"core",
	"dist",
	"isomorphic",
	"module.wasm",
}

// MustFindLicensingModuleWasmPath resolves the unified licensing module
// artifact for test packages running from either a normal checkout or a git
// worktree.
func MustFindLicensingModuleWasmPath(t testing.TB) string {
	t.Helper()

	if envPath := os.Getenv("ORBPRO_LICENSING_WASM_PATH"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath
		}
	}

	_, callerFile, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("runtime.Caller(1) failed")
	}

	anchorDir := filepath.Dir(callerFile)
	candidates := []string{
		filepath.Join(append([]string{anchorDir, "..", "..", "..", ".."}, licensingWasmPathSuffix...)...),
		filepath.Join(append([]string{anchorDir, "..", "..", "..", "..", "..", ".."}, licensingWasmPathSuffix...)...),
	}

	for _, candidate := range candidates {
		cleaned := filepath.Clean(candidate)
		if _, err := os.Stat(cleaned); err == nil {
			return cleaned
		}
	}

	t.Fatalf(
		"could not find unified licensing artifact; checked %q",
		candidates,
	)
	return ""
}
