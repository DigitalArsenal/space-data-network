package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

func useReadableMachineSource(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "product_uuid")
	if err := os.WriteFile(path, []byte("00000000-0000-4000-8000-000000000001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(keys.SetPlatformUUIDPathForTesting(path))
}
