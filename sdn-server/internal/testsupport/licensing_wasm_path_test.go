package testsupport

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindLicensingModuleWasmPathPrefersStackModulePackage(t *testing.T) {
	t.Setenv("ORBPRO_LICENSING_WASM_PATH", "")

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	stackRoot, ok := nearestTestStackRoot(filepath.Dir(file))
	if !ok {
		t.Skip("stack root not available from this checkout")
	}
	expected := filepath.Join(
		stackRoot,
		"repos",
		"main-packages",
		"space-data-network-modules",
		"licensing",
		"core",
		"dist",
		"isomorphic",
		"module.wasm",
	)
	if _, err := os.Stat(expected); err != nil {
		t.Skipf("stack licensing module artifact not available: %v", err)
	}

	got, ok := FindLicensingModuleWasmPath(t)
	if !ok {
		t.Fatal("FindLicensingModuleWasmPath() did not find an artifact")
	}
	if got != expected {
		t.Fatalf("FindLicensingModuleWasmPath() = %q, want stack artifact %q", got, expected)
	}
}

func nearestTestStackRoot(start string) (string, bool) {
	for dir := filepath.Clean(start); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "docs", "repository-catalog.md")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "repos", "main-packages")); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
	}
}
