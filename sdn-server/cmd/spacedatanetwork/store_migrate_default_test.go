package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// openDaemonStore opens root as node.New does.
func openDaemonStore(t *testing.T, root string) *storage.FlatSQLStore {
	t.Helper()
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storage.NewFlatSQLStore(root, v, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("open the store as the daemon does: %v", err)
	}
	return s
}

// rollbackToFormat1 is the documented rollback (store-migrate --help), the
// daemon stopped: the empty control.flatsqldb directory removed, fsql4/ and
// the journal moved to aside, pre-format4/control.flatsqldb* moved back.
func rollbackToFormat1(t *testing.T, root, aside string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, marker.LegacyControl)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(aside, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{marker.Dir, migrate4JournalName} {
		if err := os.Rename(filepath.Join(root, name), filepath.Join(aside, name)); err != nil {
			t.Fatal(err)
		}
	}
	pre := filepath.Join(root, marker.PreFormat4Dir)
	ents, err := os.ReadDir(pre)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), marker.LegacyControl) {
			if err := os.Rename(filepath.Join(pre, e.Name()), filepath.Join(root, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Format 4 is the default, end to end: with SDN_STORE_FORMAT unset the
// daemon's store open keeps a format-1 store on format 1; `store-migrate --to
// 4`, run as an operator runs it (daemon stopped, engines prewarmed, no
// environment), migrates it; the same open then serves it as format 4, equal
// to format 1 (--verify-only and the format-1 oracle), with format 1 kept in
// pre-format4/; and the documented rollback, started with SDN_STORE_FORMAT=1,
// serves format 1's records again.
func TestStoreMigrateFormat4WithoutTheEnvAndRollBack(t *testing.T) {
	requireFormat4Engine(t)
	legacy := t.TempDir()
	buildLegacyStore4(t, legacy)
	pristine := cloneStore(t, legacy)
	root := cloneStore(t, legacy)
	rec := migrateTestOMM(20000, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "OBJ-0") // buildLegacyStore's first
	assertServes := func(want string) {
		t.Helper()
		s := openDaemonStore(t, root)
		got, err := s.GetRecord("OMM.fbs", storage.ComputeCID(rec))
		f4, f2 := s.Format4(), s.Format2()
		if cerr := s.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		if format := map[bool]string{true: "4", false: "1"}[f4]; format != want || f2 || err != nil || !bytes.Equal(got.Data, rec) {
			t.Fatalf("the daemon's open serves format %s (format2 %v), GetRecord %v; want format %s and the record", format, f2, err, want)
		}
	}

	t.Setenv(format4.FormatEnv, "")
	assertServes("1")
	if err := prewarmAOTArtifacts(io.Discard, storage.EngineAOTCacheDir()); err != nil {
		t.Fatalf("prewarm-aot: %v", err)
	}
	storeMigrateTo, storeMigrateStore = "4", root
	defer func() { storeMigrateTo, storeMigrateStore = "", "" }()
	var out bytes.Buffer
	storeMigrateCmd.SetOut(&out)
	defer storeMigrateCmd.SetOut(nil)
	if err := runStoreMigrate(storeMigrateCmd, nil); err != nil {
		t.Fatalf("store-migrate --to 4 --store %s: %v\n%s", root, err, out.String())
	}
	if mk, err := marker.Read(root); err != nil || !mk.Activated() || mk.NeedsFinish() || mk.MigratedFrom != 1 {
		t.Fatalf("markers after store-migrate: %+v %v", mk, err)
	}
	if fi, err := os.Stat(filepath.Join(root, marker.PreFormat4Dir, marker.LegacyControl)); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("pre-format4/ does not keep format 1's control.flatsqldb: %v", err)
	}
	assertServes("4")
	o := engineOptions(t, root)
	o.VerifyOnly = true
	if rep, err := migrateStore4(context.Background(), o, nil); err != nil || rep.Check == nil || rep.Check.MismatchCount != 0 {
		t.Fatalf("--verify-only: %v %+v", err, rep)
	}
	api := openEngineAt(t, root, "OMM.fbs", "CAT.fbs", "IQC.fbs")
	assertFormat4EqualsFormat1(t, api, pristine, 0)
	_ = api.Close(context.Background())

	// The rollback: stop, restore pre-format4/, start with SDN_STORE_FORMAT=1.
	aside := filepath.Join(t.TempDir(), "rolled-back")
	rollbackToFormat1(t, root, aside)
	t.Setenv(format4.FormatEnv, "1")
	assertServes("1")
	if mk, err := marker.Read(root); err != nil || mk.Format4() || !mk.LegacyControlFile {
		t.Fatalf("after the rollback: %+v %v", mk, err)
	}
	// The format-4 store the rollback set aside equals the restored format 1.
	api = openEngineAt(t, aside, "OMM.fbs", "CAT.fbs", "IQC.fbs")
	defer api.Close(context.Background())
	assertFormat4EqualsFormat1(t, api, root, 0)
}
