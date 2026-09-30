package update

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/metrics"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// fakeDaemon is an executable (ELF magic) whose bytes carry the store-format
// stamp of a build opening formats up to stamp; stamp 0 is a binary built
// before the stamp existed.
func fakeDaemon(stamp int, tag string) string {
	var b strings.Builder
	b.WriteString("\x7fELF\x02\x01\x01")
	b.WriteString(strings.Repeat("\x00", 512))
	b.WriteString(tag)
	if stamp > 0 {
		b.Write(versioninfo.StoreFormatStamp(stamp))
	}
	b.WriteString(strings.Repeat("\x90", 512))
	return b.String()
}

const launcherScript = "#!/bin/sh\nexec \"$(dirname \"$0\")/../runtime/sdn/spacedatanetwork\" \"$@\"\n"

// releaseLayout is the release bundle: the daemon in runtime/sdn/, a launcher
// script at bin/spacedatanetwork.
func releaseLayout(stamp int, tag string) map[string]string {
	return map[string]string{
		"bin/spacedatanetwork":         launcherScript,
		"runtime/sdn/spacedatanetwork": fakeDaemon(stamp, tag),
	}
}

// fleetLayout is the lean fleet-lane bundle: the daemon itself at bin/.
func fleetLayout(stamp int, tag string) map[string]string {
	return map[string]string{"bin/spacedatanetwork": fakeDaemon(stamp, tag)}
}

func stageBuild(t *testing.T, paths Paths, signer *testSigner, updateID, version string, sequence, current int64, format string, files map[string]string) *StagedUpdate {
	t.Helper()
	var bundleBytes []byte
	if format == "zip" {
		bundleBytes = makeBundleZip(t, version, files)
	} else {
		bundleBytes = makeBundleTarGz(t, version, files)
	}
	wasmBytes := BuildCarrier(bundleBytes)
	manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
		doc["update_id"] = updateID
		doc["version"] = version
		doc["sequence"] = sequence
		doc["bundle"].(map[string]any)["hash"] = sha256Hex(bundleBytes)
		doc["bundle"].(map[string]any)["size"] = int64(len(bundleBytes))
		doc["bundle"].(map[string]any)["format"] = format
		doc["wasm"].(map[string]any)["hash"] = sha256Hex(wasmBytes)
	}, bundleBytes, wasmBytes)
	staged, err := Stage(paths, manifestBytes, wasmBytes, HostVerifyOptions(signer.roots(t), current, time.Now()))
	if err != nil {
		t.Fatalf("stage %s: %v", updateID, err)
	}
	return staged
}

// writeFormat1Store lays out a format-1 store: the legacy control database.
func writeFormat1Store(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "control.flatsqldb"), []byte("legacy flatsql store"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func storeFileBytes(format uint16) []byte {
	b := make([]byte, 64)
	binary.LittleEndian.PutUint32(b[0:], 0x32515346)
	binary.LittleEndian.PutUint16(b[4:], format)
	copy(b[8:24], "0123456789abcdef")
	binary.LittleEndian.PutUint64(b[32:], 1)
	binary.LittleEndian.PutUint32(b[56:], crc32.Checksum(b[:56], crc32.MakeTable(crc32.Castagnoli)))
	return b
}

// activateFormat2 turns root into an activated format-2 store at format, the
// way store-migrate leaves it (A5 activation order): fsql2/STORE and
// fsql2/MIGRATED, the legacy control database moved into pre-format2/, and a
// directory in its place.
func activateFormat2(t *testing.T, root string, format uint16) {
	t.Helper()
	for _, dir := range []string{"fsql2", "pre-format2"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := filepath.Join(root, "control.flatsqldb")
	if info, err := os.Stat(legacy); err == nil && !info.IsDir() {
		if err := os.Rename(legacy, filepath.Join(root, "pre-format2", "control.flatsqldb")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fsql2", "STORE"), storeFileBytes(format), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fsql2", "MIGRATED"), make([]byte, 40), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFormat2Store(t *testing.T, format uint16) string {
	t.Helper()
	root := writeFormat1Store(t)
	activateFormat2(t, root, format)
	return root
}

// refusalCount reads the guard's counter from the node's metrics registry,
// the one served at /metrics.
func refusalCount(t *testing.T, action string) float64 {
	t.Helper()
	families, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if mf.GetName() != "sdn_update_store_format_refusals_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "action" && l.GetValue() == action {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	t.Fatalf("sdn_update_store_format_refusals_total{action=%q} is not registered", action)
	return 0
}

func ledgerActions(t *testing.T, paths Paths) []string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(paths.Root, deployLedgerName)); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var actions []string
	for _, e := range readLedger(t, paths) {
		actions = append(actions, e.Action)
	}
	return actions
}

func requireRefusal(t *testing.T, err error, action string, slotMax, storeFormat int) *StoreFormatRefusal {
	t.Helper()
	var refusal *StoreFormatRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want a store-format refusal", err)
	}
	if refusal.Action != action || refusal.SlotMaxStoreFormat != slotMax || refusal.Store.Format != storeFormat {
		t.Fatalf("refusal %+v, want action %s slot %d store %d", refusal, action, slotMax, storeFormat)
	}
	return refusal
}

// THE FORWARD PATH ON TODAY'S FLEET: a format-1 store accepts every build,
// the unstamped ones built before this guard and the stamped ones after it,
// in both bundle layouts. Nothing about the guard may make a current host
// refuse a normal forward update.
func TestForwardUpdatesOnAFormat1StoreAreAccepted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store func(t *testing.T) string
	}{
		{"format-1 store", writeFormat1Store},
		{"no store yet", func(t *testing.T) string { return t.TempDir() }},
		{"store directory not created yet", func(t *testing.T) string { return filepath.Join(t.TempDir(), "data") }},
		// A STORE without MIGRATED is an unfinished migration: the legacy
		// store is still the store.
		{"unfinished migration", func(t *testing.T) string {
			root := writeFormat1Store(t)
			if err := os.MkdirAll(filepath.Join(root, "fsql2"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "fsql2", "STORE"), storeFileBytes(2), 0o600); err != nil {
				t.Fatal(err)
			}
			return root
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.store(t)
			if sf, err := ReadStoreFormat(store); err != nil || sf.Format != 1 {
				t.Fatalf("ReadStoreFormat = %+v, %v; want format 1", sf, err)
			}
			signer := newTestSigner(t)
			paths, root := setupBundleRoot(t, signer)
			// On a format-1 store the guard reads nothing of the update: not
			// even a staged update that does not exist can make it fail.
			if err := CheckStagedStoreFormat(paths, "no-such-update", store); err != nil {
				t.Fatalf("preflight on a format-1 store read the staged update: %v", err)
			}
			before := refusalCount(t, "apply")
			builds := []struct {
				id    string
				files map[string]string
			}{
				{"u-unstamped-fleet", fleetLayout(0, "a")},
				{"u-unstamped-release", releaseLayout(0, "b")},
				{"u-stamped-release", releaseLayout(versioninfo.MaxStoreFormat, "c")},
				{"u-stamped-fleet", map[string]string{
					"bin/spacedatanetwork":         fakeDaemon(versioninfo.MaxStoreFormat, "d"),
					"runtime/sdn/spacedatanetwork": fakeDaemon(versioninfo.MaxStoreFormat, "d"),
				}},
			}
			for i, b := range builds {
				stageBuild(t, paths, signer, b.id, "2.0."+string(rune('0'+i)), int64(100+i), int64(99+i), "tar.gz", b.files)
				if err := CheckStagedStoreFormat(paths, b.id, store); err != nil {
					t.Fatalf("preflight of %s: %v", b.id, err)
				}
				result, err := Apply(paths, ApplyOptions{UpdateID: b.id, StoreRoot: store})
				if err != nil {
					t.Fatalf("forward apply of %s on a format-1 store: %v", b.id, err)
				}
				if result.UpdateID != b.id {
					t.Fatalf("applied %s, want %s", result.UpdateID, b.id)
				}
			}
			if got := readFile(t, filepath.Join(root, "bin", "spacedatanetwork")); got != builds[3].files["bin/spacedatanetwork"] {
				t.Fatal("the last forward update is not installed")
			}
			if after := refusalCount(t, "apply"); after != before {
				t.Fatalf("refusal counter moved %v -> %v on a format-1 store", before, after)
			}
		})
	}
}

// Apply to a lower slot on a format-2 store is refused before anything
// happens: no ledger line, no swap, the staged payload left in place, and the
// dry run and the preflight say the same.
func TestApplyOfALowerSlotOnAFormat2StoreIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name        string
		storeFormat uint16
		format      string
		files       map[string]string
		slotMax     int
	}{
		{"unstamped release build on format 2", 2, "tar.gz", releaseLayout(0, "old"), 1},
		{"unstamped fleet build on format 2", 2, "tar.gz", fleetLayout(0, "old"), 1},
		{"unstamped zip build on format 2", 2, "zip", releaseLayout(0, "old"), 1},
		{"payload without a daemon binary on format 2", 2, "tar.gz", map[string]string{"bin/spacedatanetwork": launcherScript}, 1},
		{"format-2 build on a store ratcheted to 3", 3, "tar.gz", releaseLayout(2, "two"), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := writeFormat2Store(t, tc.storeFormat)
			signer := newTestSigner(t)
			paths, root := setupBundleRoot(t, signer)
			installed := readFile(t, filepath.Join(root, "bin", "spacedatanetwork"))
			staged := stageBuild(t, paths, signer, "u-lower", "1.9.0", 50, 0, tc.format, tc.files)
			before := refusalCount(t, "apply")

			_, err := Apply(paths, ApplyOptions{UpdateID: "u-lower", DryRun: true, StoreRoot: store})
			requireRefusal(t, err, "apply", tc.slotMax, int(tc.storeFormat))
			err = CheckStagedStoreFormat(paths, "u-lower", store)
			requireRefusal(t, err, "apply", tc.slotMax, int(tc.storeFormat))
			_, err = Apply(paths, ApplyOptions{UpdateID: "u-lower", StoreRoot: store})
			refusal := requireRefusal(t, err, "apply", tc.slotMax, int(tc.storeFormat))
			if !strings.Contains(err.Error(), "store-format guard REFUSED to apply update u-lower") ||
				!strings.Contains(err.Error(), store) {
				t.Fatalf("refusal text does not name the update and the store: %v", err)
			}
			t.Logf("log line: %s", refusal)

			if after := refusalCount(t, "apply"); after != before+3 {
				t.Fatalf("refusal counter %v -> %v, want +3", before, after)
			}
			if got := readFile(t, filepath.Join(root, "bin", "spacedatanetwork")); got != installed {
				t.Fatal("a refused apply swapped the bundle")
			}
			if actions := ledgerActions(t, paths); len(actions) != 0 {
				t.Fatalf("a refused apply wrote ledger lines %v", actions)
			}
			if _, err := os.Stat(staged.Dir); err != nil {
				t.Fatalf("a refused apply removed the staged payload: %v", err)
			}
			if entries, _ := os.ReadDir(paths.Rollback); len(entries) != 0 {
				t.Fatalf("a refused apply created rollback slots %v", entries)
			}
			if state, err := LoadState(paths); err != nil || state.UpdateID != "" || state.Sequence != 0 {
				t.Fatalf("a refused apply wrote state %+v (%v)", state, err)
			}
			// The same payload with no store root is not checked: the guard
			// only ever refuses what it could read.
			if _, err := Apply(paths, ApplyOptions{UpdateID: "u-lower", DryRun: true}); err != nil {
				t.Fatalf("dry run without a store root: %v", err)
			}
		})
	}
}

// Apply of an equal or higher slot on a format-2 store is accepted.
func TestApplyOfAnEqualOrHigherSlotOnAFormat2StoreIsAccepted(t *testing.T) {
	for _, tc := range []struct {
		name        string
		storeFormat uint16
		format      string
		files       map[string]string
	}{
		{"this build on format 2", 2, "tar.gz", releaseLayout(versioninfo.MaxStoreFormat, "now")},
		{"equal, fleet layout", 2, "tar.gz", fleetLayout(2, "eq")},
		{"equal, zip", 2, "zip", releaseLayout(2, "eq")},
		{"higher", 2, "tar.gz", releaseLayout(3, "hi")},
		{"equal on a ratcheted store", 3, "tar.gz", releaseLayout(3, "eq3")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := writeFormat2Store(t, tc.storeFormat)
			signer := newTestSigner(t)
			paths, root := setupBundleRoot(t, signer)
			stageBuild(t, paths, signer, "u-ok", "2.1.0", 60, 0, tc.format, tc.files)
			if _, err := Apply(paths, ApplyOptions{UpdateID: "u-ok", StoreRoot: store}); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := readFile(t, filepath.Join(root, "bin", "spacedatanetwork")); got != tc.files["bin/spacedatanetwork"] {
				t.Fatal("the accepted update is not installed")
			}
		})
	}
}

// Rollback is refused the same way: a box that moved to format 2 on a stamped
// build cannot be rolled back onto the unstamped build it came from, and can
// be rolled back onto a stamped one.
func TestRollbackToALowerSlotOnAFormat2StoreIsRefused(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	store := writeFormat1Store(t)
	unstamped := fakeDaemon(0, "A")
	seedFile(t, root, "bin/spacedatanetwork", unstamped)

	// Forward from the unstamped build A to the stamped build B, on format 1.
	stageBuild(t, paths, signer, "u-B", "3.0.0", 10, 0, "tar.gz", fleetLayout(2, "B"))
	if _, err := Apply(paths, ApplyOptions{UpdateID: "u-B", StoreRoot: store}); err != nil {
		t.Fatalf("forward apply on format 1: %v", err)
	}
	// While the store is format 1, the rollback to A is allowed (dry check).
	inv, err := Inventory(paths)
	if err != nil || len(inv.Slots) != 1 {
		t.Fatalf("inventory %+v, %v", inv, err)
	}
	if err := guardSlotStoreFormat(store, inv.Slots[0]); err != nil {
		t.Fatalf("rollback to the unstamped build on a format-1 store: %v", err)
	}

	// The box migrates to format 2 on B.
	activateFormat2(t, store, 2)
	before := refusalCount(t, "rollback")
	_, err = RollbackLast(paths, RollbackOptions{Reason: "health gate", StoreRoot: store})
	refusal := requireRefusal(t, err, "rollback", 1, 2)
	if !strings.Contains(refusal.Slot, "no max_store_format stamp") {
		t.Fatalf("refusal does not say the slot is unstamped: %v", err)
	}
	t.Logf("log line: %s", refusal)
	if after := refusalCount(t, "rollback"); after != before+1 {
		t.Fatalf("rollback refusal counter %v -> %v, want +1", before, after)
	}
	if got := readFile(t, filepath.Join(root, "bin", "spacedatanetwork")); got != fleetLayout(2, "B")["bin/spacedatanetwork"] {
		t.Fatal("a refused rollback swapped the bundle")
	}
	if inv, err := Inventory(paths); err != nil || len(inv.Slots) != 1 || len(inv.Missing) != 0 {
		t.Fatalf("a refused rollback consumed the slot: %+v, %v", inv, err)
	}
	for _, a := range ledgerActions(t, paths) {
		if a == "rollback" {
			t.Fatal("a refused rollback wrote a ledger line")
		}
	}

	// Forward to C, also stamped 2: the slot now holds B, which opens the
	// store, and rolling back to it is accepted.
	stageBuild(t, paths, signer, "u-C", "3.0.1", 11, 10, "tar.gz", fleetLayout(2, "C"))
	if _, err := Apply(paths, ApplyOptions{UpdateID: "u-C", StoreRoot: store}); err != nil {
		t.Fatalf("forward apply on format 2: %v", err)
	}
	result, err := RollbackLast(paths, RollbackOptions{Reason: "health gate", StoreRoot: store})
	if err != nil {
		t.Fatalf("rollback to a stamped build on format 2: %v", err)
	}
	if result.RestoredUpdateID != "u-B" {
		t.Fatalf("restored %s, want u-B", result.RestoredUpdateID)
	}
	if got := readFile(t, filepath.Join(root, "bin", "spacedatanetwork")); got != fleetLayout(2, "B")["bin/spacedatanetwork"] {
		t.Fatal("the accepted rollback did not restore B")
	}
	// The older slot (A, unstamped) is still refused when named.
	inv, err = Inventory(paths)
	if err != nil || len(inv.Slots) != 1 {
		t.Fatalf("inventory %+v, %v", inv, err)
	}
	_, err = Rollback(paths, RollbackOptions{Slot: inv.Slots[0].Path, Reason: "named the unstamped slot", StoreRoot: store})
	requireRefusal(t, err, "rollback", 1, 2)
}

// A slot published before this change carries no stamp and counts as format 1.
// The release layout's launcher script is not the daemon and is not scanned.
func TestUnstampedSlotCountsAsFormat1(t *testing.T) {
	dir := func(files map[string]string) string {
		d := t.TempDir()
		for rel, contents := range files {
			seedFile(t, d, rel, contents)
		}
		return d
	}
	for _, tc := range []struct {
		name      string
		files     map[string]string
		format    int
		binaries  int
		unstamped int
	}{
		{"unstamped fleet binary", fleetLayout(0, "x"), 1, 1, 1},
		{"unstamped release binary beside its launcher", releaseLayout(0, "x"), 1, 1, 1},
		{"stamped release binary beside its launcher", releaseLayout(2, "x"), 2, 1, 0},
		{"stamped fleet binary", fleetLayout(3, "x"), 3, 1, 0},
		{"no daemon binary", map[string]string{"bin/spacedatanetwork": launcherScript, "runtime/modules/m.wasm": "\x00asm"}, 1, 0, 0},
		{"one stamped, one not", map[string]string{"bin/spacedatanetwork": fakeDaemon(2, "x"), "runtime/sdn/spacedatanetwork": fakeDaemon(0, "y")}, 1, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := stampOfDir(dir(tc.files))
			if err != nil || s.Format != tc.format || len(s.Binaries) != tc.binaries || len(s.Unstamped) != tc.unstamped {
				t.Fatalf("stampOfDir = %+v, %v; want format %d, %d binaries, %d unstamped", s, err, tc.format, tc.binaries, tc.unstamped)
			}
		})
	}
}

// The guard reads a real Go binary: this test binary links versioninfo, so it
// is stamped with this build's MaxStoreFormat.
func TestStampOfARealBinaryIsThisBuild(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	target := filepath.Join(d, "bin", "spacedatanetwork")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(self, target); err != nil {
		src, err := os.Open(self)
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		dst, err := os.Create(target)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(dst, src); err != nil {
			t.Fatal(err)
		}
		dst.Close()
	}
	s, err := stampOfDir(d)
	if err != nil || s.Format != versioninfo.MaxStoreFormat || len(s.Binaries) != 1 || len(s.Unstamped) != 0 {
		t.Fatalf("stamp of the test binary = %+v, %v; want %d", s, err, versioninfo.MaxStoreFormat)
	}
}

func TestReadStoreFormat(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(t *testing.T, root string)
		format   int
		evidence string
	}{
		{"empty directory", func(t *testing.T, root string) {}, 1, "format-1 layout"},
		{"format-1 store", func(t *testing.T, root string) {
			seedFile(t, root, "control.flatsqldb", "legacy")
		}, 1, "format-1 layout"},
		{"activated format 2", func(t *testing.T, root string) {
			seedFile(t, root, "control.flatsqldb", "legacy")
			activateFormat2(t, root, 2)
		}, 2, "fsql2/STORE format 2, fsql2/MIGRATED"},
		{"fresh format-2 store (no legacy files)", func(t *testing.T, root string) {
			seedFile(t, root, "fsql2/STORE", string(storeFileBytes(2)))
			seedFile(t, root, "fsql2/MIGRATED", strings.Repeat("\x00", 40))
		}, 2, "fsql2/MIGRATED"},
		{"ratcheted to 5", func(t *testing.T, root string) {
			activateFormat2(t, root, 5)
		}, 5, "fsql2/STORE format 5"},
		{"activation begun, MIGRATED not yet written", func(t *testing.T, root string) {
			seedFile(t, root, "fsql2/STORE", string(storeFileBytes(2)))
			seedFile(t, root, "pre-format2/control.flatsqldb", "legacy")
			if err := os.MkdirAll(filepath.Join(root, "control.flatsqldb"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, 2, "control.flatsqldb is a directory"},
		{"activated with an unreadable STORE", func(t *testing.T, root string) {
			activateFormat2(t, root, 2)
			seedFile(t, root, "fsql2/STORE", "garbage")
		}, 2, "unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			sf, err := ReadStoreFormat(root)
			if err != nil || sf.Format != tc.format || !strings.Contains(sf.Evidence, tc.evidence) {
				t.Fatalf("ReadStoreFormat = %+v, %v; want format %d with evidence containing %q", sf, err, tc.format, tc.evidence)
			}
		})
	}
}

// A module-targeted update does not replace the daemon binary, so the guard
// leaves it alone even on a format-2 store; one that ships a daemon binary is
// judged by it.
func TestModuleUpdatesAreJudgedByTheBinaryTheyShip(t *testing.T) {
	store := writeFormat2Store(t, 2)
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	seedFile(t, root, "runtime/modules/flatsql/flatsql.wasm", "old")
	module := "new-module-bytes"
	stageSignedModuleUpdate(t, paths, signer, "9.9.9",
		map[string]string{"runtime/modules/flatsql/flatsql.wasm": module},
		[]ManifestModuleTarget{{ID: "flatsql", Hash: sha256Hex([]byte(module)), Path: "runtime/modules/flatsql/flatsql.wasm"}})
	if _, err := Apply(paths, ApplyOptions{StoreRoot: store}); err != nil {
		t.Fatalf("module update on a format-2 store: %v", err)
	}

	paths2, _ := setupBundleRoot(t, signer)
	daemon := fakeDaemon(0, "module-shipped-daemon")
	stageSignedModuleUpdate(t, paths2, signer, "9.9.9",
		map[string]string{"bin/spacedatanetwork": daemon},
		[]ManifestModuleTarget{{ID: "daemon", Hash: sha256Hex([]byte(daemon)), Path: "bin/spacedatanetwork"}})
	_, err := Apply(paths2, ApplyOptions{StoreRoot: store})
	requireRefusal(t, err, "apply", 1, 2)
}
