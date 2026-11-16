//go:build linux

package keys

import (
	"os"
	"path/filepath"
	"testing"
)

// useTestMachineFingerprint isolates successful derivation from DMI permissions.
// The source-state tests exercise readable, absent, and refused fixtures separately.
func useTestMachineFingerprint(t *testing.T) {
	t.Helper()
	saved := platuuidPath
	platuuidPath = filepath.Join(t.TempDir(), "product_uuid")
	t.Cleanup(func() { platuuidPath = saved })
	if err := os.WriteFile(platuuidPath, []byte("00000000-0000-4000-8000-000000000001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
