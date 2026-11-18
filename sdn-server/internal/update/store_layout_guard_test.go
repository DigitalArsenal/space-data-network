package update

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

func writeFormat5Store(t *testing.T) string {
	t.Helper()
	root := writeFormat1Store(t)
	var uuid [16]byte
	uuid[0] = 0x55
	dir := filepath.Join(root, marker.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, marker.MigratedFile), marker.MigratedBytes(uuid, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, marker.StoreFile), marker.StoreBytes(uuid, 1, 1, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := marker.FinishActivation(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStoreLayoutGuard(t *testing.T) {
	store := StoreFormat{Format: 5, Layout: 2}
	for _, layout := range []int{0, 1, 2} {
		err := guardStoreFormat("rollback", "test", "", store, func() (slotStamp, bool, error) {
			return slotStamp{Format: 5, Layout: layout}, true, nil
		})
		if (err == nil) != (layout == 2) {
			t.Fatalf("layout %d: %v", layout, err)
		}
		if err != nil {
			var refusal *StoreFormatRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("layout refusal must be typed: %v", err)
			}
		}
	}
}

func TestLayoutOneRollbackToReleasedStamp(t *testing.T) {
	root := writeFormat5Store(t)
	store, err := ReadStoreFormat(root)
	if err != nil || store.Format != 5 || store.Layout != 1 {
		t.Fatalf("released format-5 store: %+v %v", store, err)
	}
	if err := guardStoreFormat("rollback", "previous release", "", store, func() (slotStamp, bool, error) {
		return slotStamp{Format: 5}, true, nil
	}); err != nil {
		t.Fatal(err)
	}

	// A pending STORE.unlimited promotion intent raises the required layout to 2.
	var uuid [16]byte
	uuid[0] = 0x55
	intent := marker.StoreBytes(uuid, 1, 1, 1)
	binary.LittleEndian.PutUint16(intent[6:], 2)
	binary.LittleEndian.PutUint32(intent[56:], crc32.Checksum(intent[:56], storeFileCRCTable))
	if err := os.WriteFile(filepath.Join(root, marker.Dir, "STORE.unlimited"), intent, 0o600); err != nil {
		t.Fatal(err)
	}
	promoted, err := ReadStoreFormat(root)
	if err != nil || promoted.Format != 5 || promoted.Layout != 2 {
		t.Fatalf("pending promotion store: %+v %v", promoted, err)
	}
	err = guardStoreFormat("rollback", "previous release", "", promoted, func() (slotStamp, bool, error) {
		return slotStamp{Format: 5, Layout: 1}, true, nil
	})
	var refusal *StoreFormatRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("expected typed layout refusal: %v", err)
	}
}
