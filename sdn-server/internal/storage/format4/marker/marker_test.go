package marker

import (
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// The contract's golden vectors (§2.2): uuid 00..0f, created 1790000000000,
// floor 1060922, migrated_from 1; MIGRATED written 1790000000123.
const (
	goldenStore    = "4653513404000100000102030405060708090a0b0c0d0e0f006c50c4a00100003a3010000000000001000000000000000000000000000000dd6684d300000000"
	goldenMigrated = "4653514d04000000000102030405060708090a0b0c0d0e0f7b6c50c4a001000086bbeba000000000"
)

func goldenUUID() (u [16]byte) {
	for i := range u {
		u[i] = byte(i)
	}
	return u
}

func TestGoldenMarkerBytes(t *testing.T) {
	if got := hex.EncodeToString(StoreBytes(goldenUUID(), 1790000000000, 1060922, 1)); got != goldenStore {
		t.Fatalf("STORE\n got %s\nwant %s", got, goldenStore)
	}
	if got := hex.EncodeToString(MigratedBytes(goldenUUID(), 1790000000123)); got != goldenMigrated {
		t.Fatalf("MIGRATED\n got %s\nwant %s", got, goldenMigrated)
	}
}

func writeHex(t *testing.T, name, h string) {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	writeBytes(t, name, b)
}

func writeBytes(t *testing.T, name string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadGoldenStore(t *testing.T) {
	root := t.TempDir()
	writeHex(t, filepath.Join(root, Dir, MigratedFile), goldenMigrated)
	writeHex(t, filepath.Join(root, Dir, StoreFile), goldenStore)
	m, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if !m.StorePresent || !m.StoreValid || !m.MigratedPresent || !m.MigratedValid {
		t.Fatalf("markers %+v", m)
	}
	if m.Format != 4 || m.Layout != 1 || m.UUID != goldenUUID() || m.CreatedMs != 1790000000000 ||
		m.GseqFloor != 1060922 || m.MigratedFrom != 1 {
		t.Fatalf("fields %+v", m)
	}
	if !m.Format4() || !m.Activated() || !m.NeedsFinish() {
		t.Fatalf("Format4 %v Activated %v NeedsFinish %v", m.Format4(), m.Activated(), m.NeedsFinish())
	}
}

func TestReadStates(t *testing.T) {
	other := goldenUUID()
	other[0] = 0xff
	torn, _ := hex.DecodeString(goldenStore)
	torn[60-1] ^= 1 // inside the crc
	// A valid STORE of a later format: valid, but not activated here.
	future := StoreBytes(goldenUUID(), 1, 1, 0)
	future[4] = 5
	fixCRC(future, 56)
	for _, tc := range []struct {
		name                  string
		store, migrated       []byte
		control               string // "", "file", "dir"
		format4, activated    bool
		needsFinish           bool
		storeValid, migrValid bool
	}{
		{name: "empty"},
		{name: "format 1", control: "file"},
		{name: "migration in progress", migrated: MigratedBytes(goldenUUID(), 1), control: "file", format4: true, migrValid: true},
		{name: "torn STORE", migrated: MigratedBytes(goldenUUID(), 1), store: torn, control: "file", format4: true, migrValid: true},
		{name: "store without MIGRATED", store: StoreBytes(goldenUUID(), 1, 1, 0), format4: true, storeValid: true},
		{name: "uuid mismatch", migrated: MigratedBytes(other, 1), store: StoreBytes(goldenUUID(), 1, 1, 0), format4: true, storeValid: true, migrValid: true},
		{name: "later format", migrated: MigratedBytes(goldenUUID(), 1), store: future, format4: true, storeValid: true, migrValid: true},
		{name: "activated, unfinished", migrated: MigratedBytes(goldenUUID(), 1), store: StoreBytes(goldenUUID(), 1, 1, 1), control: "file",
			format4: true, activated: true, needsFinish: true, storeValid: true, migrValid: true},
		{name: "activated, finished", migrated: MigratedBytes(goldenUUID(), 1), store: StoreBytes(goldenUUID(), 1, 1, 1), control: "dir",
			format4: true, activated: true, storeValid: true, migrValid: true},
		{name: "empty MIGRATED", migrated: []byte{}, format4: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.migrated != nil {
				writeBytes(t, filepath.Join(root, Dir, MigratedFile), tc.migrated)
			}
			if tc.store != nil {
				writeBytes(t, filepath.Join(root, Dir, StoreFile), tc.store)
			}
			switch tc.control {
			case "file":
				writeBytes(t, filepath.Join(root, LegacyControl), []byte("SQLite format 3\x00"))
			case "dir":
				if err := os.Mkdir(filepath.Join(root, LegacyControl), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			m, err := Read(root)
			if err != nil {
				t.Fatal(err)
			}
			if m.Format4() != tc.format4 || m.Activated() != tc.activated || m.NeedsFinish() != tc.needsFinish ||
				m.StoreValid != tc.storeValid || m.MigratedValid != tc.migrValid {
				t.Fatalf("Format4 %v Activated %v NeedsFinish %v StoreValid %v MigratedValid %v (%+v)",
					m.Format4(), m.Activated(), m.NeedsFinish(), m.StoreValid, m.MigratedValid, m)
			}
			if m.LegacyControlFile != (tc.control == "file") || m.LegacyControlDir != (tc.control == "dir") {
				t.Fatalf("legacy control file %v dir %v", m.LegacyControlFile, m.LegacyControlDir)
			}
		})
	}
}

func fixCRC(b []byte, at int) {
	binary.LittleEndian.PutUint32(b[at:], crc32.Checksum(b[:at], castagnoli))
}

func TestFinishActivation(t *testing.T) {
	root := t.TempDir()
	sidecars := []string{"control.flatsqldb", "control.flatsqldb-wal", "control.flatsqldb-shm",
		"control.flatsqldb-journal", "control.flatsqldb.fsdata", "control.flatsqldb.lane-ckpt"}
	for _, n := range sidecars {
		writeBytes(t, filepath.Join(root, n), []byte(n))
	}
	writeBytes(t, filepath.Join(root, "unrelated.db"), []byte("x"))

	// Not activated: refused, nothing moves.
	writeBytes(t, filepath.Join(root, Dir, MigratedFile), MigratedBytes(goldenUUID(), 1))
	if err := FinishActivation(root); err == nil {
		t.Fatal("FinishActivation on an unactivated store succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "control.flatsqldb-wal")); err != nil {
		t.Fatalf("a refused finish moved files: %v", err)
	}

	writeBytes(t, filepath.Join(root, Dir, StoreFile), StoreBytes(goldenUUID(), 1, 1, 1))
	for run := 0; run < 2; run++ { // idempotent
		if err := FinishActivation(root); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	for _, n := range sidecars {
		b, err := os.ReadFile(filepath.Join(root, PreFormat4Dir, n))
		if err != nil || string(b) != n {
			t.Fatalf("%s in pre-format4: %q %v", n, b, err)
		}
	}
	if info, err := os.Stat(filepath.Join(root, LegacyControl)); err != nil || !info.IsDir() {
		t.Fatalf("control.flatsqldb is not a directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "unrelated.db")); err != nil {
		t.Fatalf("an unrelated file moved: %v", err)
	}
	m, err := Read(root)
	if err != nil || m.NeedsFinish() || !m.Activated() {
		t.Fatalf("after finish: %+v %v", m, err)
	}

	// A crash between moves: one sidecar left at the root finishes on rerun.
	root2 := t.TempDir()
	writeBytes(t, filepath.Join(root2, Dir, MigratedFile), MigratedBytes(goldenUUID(), 1))
	writeBytes(t, filepath.Join(root2, Dir, StoreFile), StoreBytes(goldenUUID(), 1, 1, 1))
	writeBytes(t, filepath.Join(root2, PreFormat4Dir, "control.flatsqldb"), []byte("db"))
	writeBytes(t, filepath.Join(root2, "control.flatsqldb-wal"), []byte("wal"))
	if err := FinishActivation(root2); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root2, PreFormat4Dir, "control.flatsqldb-wal")); err != nil || string(b) != "wal" {
		t.Fatalf("resumed move: %q %v", b, err)
	}

	// Never overwrite a kept copy.
	root3 := t.TempDir()
	writeBytes(t, filepath.Join(root3, Dir, MigratedFile), MigratedBytes(goldenUUID(), 1))
	writeBytes(t, filepath.Join(root3, Dir, StoreFile), StoreBytes(goldenUUID(), 1, 1, 1))
	writeBytes(t, filepath.Join(root3, PreFormat4Dir, "control.flatsqldb"), []byte("kept"))
	writeBytes(t, filepath.Join(root3, "control.flatsqldb"), []byte("new"))
	if err := FinishActivation(root3); err == nil {
		t.Fatal("FinishActivation overwrote the kept control database")
	}
	if b, _ := os.ReadFile(filepath.Join(root3, PreFormat4Dir, "control.flatsqldb")); string(b) != "kept" {
		t.Fatalf("kept copy is %q", b)
	}
}
