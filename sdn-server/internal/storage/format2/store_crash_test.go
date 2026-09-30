package format2

// Crash points of the store markers (A5; task sdn-update-store-format-guard,
// item 6). A kill -9 at any instruction that writes fsql2/STORE or
// fsql2/MIGRATED must leave a store every open accepts: absent (created
// again) or MIGRATED. Two writers exist:
//
//   - SDN's own writeDurable (store-migrate's staging store: STORE, then
//     MIGRATED): temp file, fsync, rename, fsync the directory.
//   - The engine, creating a fresh store for Open(AllowFresh): the registry
//     files, MIGRATED, then STORE, whose one torn state the engine finishes
//     from MIGRATED (flatsql ps/open.cpp; its crash_fault_test replays every
//     I/O call of a creation). The I/O ABI has no rename, so the order carries
//     the atomicity there.
//
// The kill -9 failures this replaces ("fsql2/STORE is corrupt", "fsql2/MIGRATED
// is corrupt", TestRouterKill9KeepsEveryAckedRecord, 2026-09-29/30) were the
// engine writing STORE first, in place: a kill between its create and its
// write, or between STORE and MIGRATED, left a store no open accepted.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

var durableWriteSteps = []string{"created", "written", "synced", "renamed"}

// SDN's marker writes survive a kill after every step: each marker reads as
// absent or whole, never torn, and store-migrate's resume (ensureStagingStore:
// write STORE when absent, then MIGRATED when not MIGRATED) completes the
// store with the UUID it started with.
func TestMarkerWritesSurviveAKillAfterEveryStep(t *testing.T) {
	for _, file := range []string{"STORE", "MIGRATED"} {
		for i, step := range durableWriteSteps {
			t.Run(file+"/"+step, func(t *testing.T) {
				root := t.TempDir()
				sf, err := NewStoreFile(7, 1)
				if err != nil {
					t.Fatal(err)
				}
				if file == "MIGRATED" {
					if err := WriteStoreFile(root, sf); err != nil {
						t.Fatal(err)
					}
				}
				crashStep = step
				if file == "STORE" {
					err = WriteStoreFile(root, sf)
				} else {
					err = WriteMigrated(root, sf.UUID)
				}
				crashStep = ""
				if !errors.Is(err, errSimulatedCrash) {
					t.Fatalf("write with a crash after %q: %v", step, err)
				}
				renamed := i >= 3

				got, err := ReadStoreFile(root)
				switch {
				case file == "STORE" && !renamed:
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("STORE after a crash before its rename: %+v, %v; want absent", got, err)
					}
				default:
					if err != nil || got.UUID != sf.UUID || got.GseqFloor != sf.GseqFloor {
						t.Fatalf("STORE after a crash after %q: %+v, %v", step, got, err)
					}
				}
				migrated, err := Migrated(root)
				if err != nil || migrated != (file == "MIGRATED" && renamed) {
					t.Fatalf("Migrated after a crash after %q in %s: %v, %v", step, file, migrated, err)
				}

				// store-migrate's resume.
				cur, err := ReadStoreFile(root)
				if errors.Is(err, os.ErrNotExist) {
					if err := WriteStoreFile(root, sf); err != nil {
						t.Fatalf("resume STORE: %v", err)
					}
					cur = sf
				} else if err != nil {
					t.Fatal(err)
				}
				if ok, err := Migrated(root); err != nil {
					t.Fatal(err)
				} else if !ok {
					if err := WriteMigrated(root, cur.UUID); err != nil {
						t.Fatalf("resume MIGRATED: %v", err)
					}
				}
				if ok, err := Migrated(root); err != nil || !ok {
					t.Fatalf("after the resume: Migrated %v, %v", ok, err)
				}
				if s, err := ReadStoreFile(root); err != nil || s.UUID != sf.UUID {
					t.Fatalf("after the resume: STORE %+v, %v", s, err)
				}
				entries, err := os.ReadDir(filepath.Join(root, Dir))
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range entries {
					if e.Name() != "STORE" && e.Name() != "MIGRATED" {
						t.Fatalf("the resume left %s behind", e.Name())
					}
				}
			})
		}
	}
}

// storeBytes is fsql2/STORE as the engine writes it for a fresh store: at
// its kFormatMax (the level it writes).
func storeBytes(uuid [16]byte, createdMs int64) []byte {
	return StoreFile{Format: versioninfo.PSEngineStoreFormatMax, UUID: uuid, CreatedMs: createdMs, GseqFloor: 1}.encode()
}

// creationCrashState is what a kill leaves at one point of the engine's
// creation of a fresh store (flatsql ps/open.cpp): the registry files,
// MIGRATED, then STORE, each file created, then written, then synced.
type creationCrashState struct {
	name     string
	build    func(t *testing.T, root string)
	migrated bool // how Migrated reads it; false means Open creates the store again
}

func creationCrashStates() []creationCrashState {
	uuid := [16]byte{0x5a, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	other := [16]byte{0xa5, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	write := func(t *testing.T, root, name string, b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, Dir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, Dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registry := func(files int) func(t *testing.T, root string) {
		return func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, Dir), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"registry.fsl", "registry.fsh"}[:files] {
				write(t, root, f, nil)
			}
		}
	}
	migrated := func(t *testing.T, root string, id [16]byte) {
		t.Helper()
		if err := WriteMigrated(root, id); err != nil {
			t.Fatal(err)
		}
	}
	return []creationCrashState{
		{"nothing yet", func(t *testing.T, root string) {}, false},
		{"fsql2 created", registry(0), false},
		{"registry.fsl created", registry(1), false},
		{"registry files created", registry(2), false},
		{"MIGRATED created, not written", func(t *testing.T, root string) {
			registry(2)(t, root)
			write(t, root, "MIGRATED", nil)
		}, false},
		{"MIGRATED written", func(t *testing.T, root string) {
			registry(2)(t, root)
			migrated(t, root, uuid)
		}, false},
		{"MIGRATED of an earlier attempt, STORE never written", func(t *testing.T, root string) {
			registry(2)(t, root)
			migrated(t, root, other)
		}, false},
		{"STORE created, not written", func(t *testing.T, root string) {
			registry(2)(t, root)
			migrated(t, root, uuid)
			write(t, root, "STORE", nil)
		}, true},
		{"STORE written, its bytes lost with power", func(t *testing.T, root string) {
			registry(2)(t, root)
			migrated(t, root, uuid)
			write(t, root, "STORE", make([]byte, storeFileLen))
		}, true},
		{"STORE written", func(t *testing.T, root string) {
			registry(2)(t, root)
			migrated(t, root, uuid)
			write(t, root, "STORE", storeBytes(uuid, time.Now().UnixMilli()))
		}, true},
	}
}

// Migrated reads every creation crash state as absent (Open creates the store
// again, which Open allows only while STORE does not exist) or as MIGRATED
// (Open opens it), never as an error.
func TestMigratedReadsEveryCreationCrashStateAsAbsentOrMigrated(t *testing.T) {
	for _, st := range creationCrashStates() {
		t.Run(st.name, func(t *testing.T) {
			root := t.TempDir()
			st.build(t, root)
			migrated, err := Migrated(root)
			if err != nil || migrated != st.migrated {
				t.Fatalf("Migrated = %v, %v; want %v", migrated, err, st.migrated)
			}
			if !migrated {
				if _, err := os.Stat(filepath.Join(root, Dir, "STORE")); err == nil {
					t.Fatal("not MIGRATED with a STORE present: Open refuses that as an unfinished migration")
				}
			}
		})
	}
}

// A torn STORE is finished only when nothing can be lost: beside a store with
// registry frames, or without a whole MIGRATED, it is still refused.
func TestTornStoreIsRefusedWhenItIsNotAnUnfinishedCreation(t *testing.T) {
	uuid := [16]byte{1}
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, root string)
	}{
		{"registry has frames", func(t *testing.T, root string) {
			mustWrite(t, filepath.Join(root, Dir, "registry.fsl"), []byte("frames"))
			if err := WriteMigrated(root, uuid); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(root, Dir, "STORE"), nil)
		}},
		// The engines before the ordered creation wrote STORE first; a kill
		// between its create and its write left this, which stays refused.
		{"no MIGRATED", func(t *testing.T, root string) {
			mustWrite(t, filepath.Join(root, Dir, "STORE"), nil)
		}},
		{"MIGRATED torn too", func(t *testing.T, root string) {
			mustWrite(t, filepath.Join(root, Dir, "MIGRATED"), []byte("torn"))
			mustWrite(t, filepath.Join(root, Dir, "STORE"), nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.build(t, root)
			if migrated, err := Migrated(root); err == nil {
				t.Fatalf("Migrated = %v, nil; want the torn STORE refused", migrated)
			}
		})
	}
}

func mustWrite(t *testing.T, name string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// engineWritesStoreFirst is the embedded engine that predates the ordered
// creation (flatsql 3.5.0, ps/open.cpp wrote STORE first and in place); on it
// the end-to-end test below cannot pass. The flatsql release carrying the
// ordered creation changes the embedded artifact and turns the test on.
const engineWritesStoreFirst = "787adbcccf52a9e9fe2767d6679b6f2d94a4ead41985bf2a7bb0855f821abc72"

// End to end on the embedded engine: Open(AllowFresh) accepts every creation
// crash state, the store takes a record, and it reopens with the record.
func TestOpenAcceptsEveryCreationCrashState(t *testing.T) {
	requireEngine(t)
	if flatsqlrt.PSThreadsDigest() == engineWritesStoreFirst {
		t.Skipf("the embedded engine (%s) writes STORE first; the ordered creation ships in the next flatsql release", flatsqlrt.PSThreadsPackage)
	}
	ctx := context.Background()
	base := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	tags := &Tags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "crash", License: "CC-BY-4.0"}
	for _, st := range creationCrashStates() {
		t.Run(st.name, func(t *testing.T) {
			root := t.TempDir()
			st.build(t, root)
			open := func() *Store {
				t.Helper()
				s, err := Open(StoreConfig{Root: root, AOTCacheDir: testAOTDir(t), CompileOnMiss: true, AllowFresh: true,
					Topology: Topology{Writers: 1, InteractiveLanes: 2, BulkLanes: 1}, GatePeriod: 50 * time.Millisecond})
				if err != nil {
					t.Fatalf("open: %v", err)
				}
				return s
			}
			s := open()
			res, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(1, 0, base, "CRASH"), "source:celestrak", nil, tags)
			if err != nil || len(res) != 1 || res[0].Err != nil {
				t.Fatalf("put: %v %+v", err, res)
			}
			cid := res[0].CID
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(root, Dir, "STORE"))
			if err != nil || len(b) != storeFileLen || binary.LittleEndian.Uint32(b[56:]) != crc32.Checksum(b[:56], castagnoli) {
				t.Fatalf("STORE after the open: % x, %v", b, err)
			}
			if st.migrated && st.name != "STORE written" {
				// The engine finished STORE from MIGRATED: same UUID.
				m, err := os.ReadFile(filepath.Join(root, Dir, "MIGRATED"))
				if err != nil || !bytes.Equal(m[8:24], b[8:24]) {
					t.Fatalf("finished STORE names another store than MIGRATED (%v)", err)
				}
			}
			s = open()
			defer s.Close()
			if _, err := s.GetRecord(ctx, "OMM.fbs", cid); err != nil {
				t.Fatalf("the record written after the crash state is gone after a reopen: %v", err)
			}
		})
	}
}
