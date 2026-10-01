package update

// Gate G7 (stack design flatsql-sqlite-partitions.md §11 "Format guard";
// build-out contract §2.4): a format-4 store reads as format 4 in every
// marker state, ahead of the format-2 rules, and refuses an apply or a
// rollback to any build stamped below 4 and to an unstamped build.

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// The contract's golden markers (§2.2).
const (
	goldenStore4    = "4653513404000100000102030405060708090a0b0c0d0e0f006c50c4a00100003a3010000000000001000000000000000000000000000000dd6684d300000000"
	goldenMigrated4 = "4653514d04000000000102030405060708090a0b0c0d0e0f7b6c50c4a001000086bbeba000000000"
)

func writeMarker(t *testing.T, root, name, hexBytes string) {
	t.Helper()
	b, err := hex.DecodeString(hexBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, marker.Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, marker.Dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeFormat4Store lays out an activated, finished format-4 store over a
// format-1 one: both markers, the legacy control database in pre-format4/
// and a directory in its place.
func writeFormat4Store(t *testing.T) string {
	t.Helper()
	root := writeFormat1Store(t)
	writeMarker(t, root, marker.MigratedFile, goldenMigrated4)
	writeMarker(t, root, marker.StoreFile, goldenStore4)
	if err := marker.FinishActivation(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadStoreFormatSeesFormat4First(t *testing.T) {
	torn := goldenStore4[:len(goldenStore4)-10] + "ffffffffff"
	for _, tc := range []struct {
		name     string
		setup    func(t *testing.T) string
		evidence string
	}{
		{"migration in progress: MIGRATED only, format 1 still in place", func(t *testing.T) string {
			root := writeFormat1Store(t)
			writeMarker(t, root, marker.MigratedFile, goldenMigrated4)
			return root
		}, "fsql4/MIGRATED present (migration or activation in progress)"},
		{"activation cut short: MIGRATED and a torn STORE", func(t *testing.T) string {
			root := writeFormat1Store(t)
			writeMarker(t, root, marker.MigratedFile, goldenMigrated4)
			writeMarker(t, root, marker.StoreFile, torn)
			return root
		}, "fsql4/MIGRATED and fsql4/STORE present (migration or activation in progress)"},
		{"engine step done, legacy control not yet retired", func(t *testing.T) string {
			root := writeFormat1Store(t)
			writeMarker(t, root, marker.MigratedFile, goldenMigrated4)
			writeMarker(t, root, marker.StoreFile, goldenStore4)
			return root
		}, "(activated, legacy control database not yet retired)"},
		{"activated", writeFormat4Store, "fsql4/MIGRATED and fsql4/STORE present (activated)"},
		{"STORE alone", func(t *testing.T) string {
			root := t.TempDir()
			writeMarker(t, root, marker.StoreFile, goldenStore4)
			return root
		}, "fsql4/STORE present"},
		{"an empty MIGRATED", func(t *testing.T) string {
			root := t.TempDir()
			writeMarker(t, root, marker.MigratedFile, "")
			return root
		}, "fsql4/MIGRATED present"},
		{"format 4 ahead of an activated format-2 store", func(t *testing.T) string {
			root := writeFormat2Store(t, 3)
			writeMarker(t, root, marker.MigratedFile, goldenMigrated4)
			return root
		}, "fsql4/MIGRATED present"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sf, err := ReadStoreFormat(tc.setup(t))
			if err != nil || sf.Format != 4 || !strings.Contains(sf.Evidence, tc.evidence) {
				t.Fatalf("ReadStoreFormat = %+v, %v; want format 4 with evidence %q", sf, err, tc.evidence)
			}
		})
	}
	// Without fsql4 the format-2 rules answer as before.
	if sf, err := ReadStoreFormat(writeFormat2Store(t, 3)); err != nil || sf.Format != 3 {
		t.Fatalf("format-2 store: %+v, %v", sf, err)
	}
}

func TestApplyOnAFormat4Store(t *testing.T) {
	for _, tc := range []struct {
		name    string
		format  string
		files   map[string]string
		refused bool
		slotMax int
	}{
		{"unstamped release build", "tar.gz", releaseLayout(0, "old"), true, 1},
		{"unstamped fleet build", "zip", fleetLayout(0, "old"), true, 1},
		{"format-2 build", "tar.gz", releaseLayout(2, "two"), true, 2},
		{"format-3 build", "tar.gz", fleetLayout(3, "three"), true, 3},
		{"payload without a daemon binary", "tar.gz", map[string]string{"bin/spacedatanetwork": launcherScript}, true, 1},
		{"format-4 build", "tar.gz", releaseLayout(4, "four"), false, 0},
		{"this build", "tar.gz", fleetLayout(versioninfo.MaxStoreFormat, "now"), false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := writeFormat4Store(t)
			signer := newTestSigner(t)
			paths, root := setupBundleRoot(t, signer)
			installed := readFile(t, filepath.Join(root, "bin", "spacedatanetwork"))
			stageBuild(t, paths, signer, "u-x", "4.0.0", 70, 0, tc.format, tc.files)
			err := CheckStagedStoreFormat(paths, "u-x", store)
			if !tc.refused {
				if err != nil {
					t.Fatalf("preflight: %v", err)
				}
				if _, err := Apply(paths, ApplyOptions{UpdateID: "u-x", StoreRoot: store}); err != nil {
					t.Fatalf("apply: %v", err)
				}
				return
			}
			requireRefusal(t, err, "apply", tc.slotMax, 4)
			_, err = Apply(paths, ApplyOptions{UpdateID: "u-x", StoreRoot: store})
			refusal := requireRefusal(t, err, "apply", tc.slotMax, 4)
			if !strings.Contains(refusal.Error(), "fsql4/") {
				t.Fatalf("refusal does not name the format-4 markers: %v", refusal)
			}
			if got := readFile(t, filepath.Join(root, "bin", "spacedatanetwork")); got != installed {
				t.Fatal("a refused apply swapped the bundle")
			}
		})
	}
}

// A box that moved to format 4 on a stamped-4 build cannot roll back onto
// the stamped-3 build it came from, nor onto an unstamped one; it can roll
// back onto a stamped-4 build.
func TestRollbackOnAFormat4Store(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	store := writeFormat1Store(t)
	seedFile(t, root, "bin/spacedatanetwork", fakeDaemon(0, "A"))
	stageBuild(t, paths, signer, "u-B3", "5.0.0", 10, 0, "tar.gz", fleetLayout(3, "B3"))
	if _, err := Apply(paths, ApplyOptions{UpdateID: "u-B3", StoreRoot: store}); err != nil {
		t.Fatal(err)
	}
	stageBuild(t, paths, signer, "u-C4", "5.1.0", 11, 10, "tar.gz", fleetLayout(4, "C4"))
	if _, err := Apply(paths, ApplyOptions{UpdateID: "u-C4", StoreRoot: store}); err != nil {
		t.Fatal(err)
	}
	// The box migrates to format 4 on C4.
	writeMarker(t, store, marker.MigratedFile, goldenMigrated4)
	writeMarker(t, store, marker.StoreFile, goldenStore4)
	if err := marker.FinishActivation(store); err != nil {
		t.Fatal(err)
	}
	inv, err := Inventory(paths)
	if err != nil || len(inv.Slots) != 2 {
		t.Fatalf("inventory %+v, %v", inv, err)
	}
	_, err = RollbackLast(paths, RollbackOptions{Reason: "health gate", StoreRoot: store})
	requireRefusal(t, err, "rollback", 3, 4)
	for _, slot := range inv.Slots {
		_, err := Rollback(paths, RollbackOptions{Slot: slot.Path, Reason: "named", StoreRoot: store})
		var want int
		switch {
		case strings.Contains(readFile(t, filepath.Join(slot.Path, "bin", "spacedatanetwork")), "B3"):
			want = 3
		default:
			want = 1 // A, unstamped
		}
		requireRefusal(t, err, "rollback", want, 4)
	}
	if got := readFile(t, filepath.Join(root, "bin", "spacedatanetwork")); got != fleetLayout(4, "C4")["bin/spacedatanetwork"] {
		t.Fatal("a refused rollback swapped the bundle")
	}
	// Forward to D4: the slot then holds C4, stamped 4, and rolling back to
	// it is accepted.
	stageBuild(t, paths, signer, "u-D4", "5.2.0", 12, 11, "tar.gz", fleetLayout(4, "D4"))
	if _, err := Apply(paths, ApplyOptions{UpdateID: "u-D4", StoreRoot: store}); err != nil {
		t.Fatal(err)
	}
	if res, err := RollbackLast(paths, RollbackOptions{Reason: "health gate", StoreRoot: store}); err != nil || res.RestoredUpdateID != "u-C4" {
		t.Fatalf("rollback to a stamped-4 build: %+v, %v", res, err)
	}
}
