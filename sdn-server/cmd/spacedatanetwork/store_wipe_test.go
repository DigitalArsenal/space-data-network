package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// A wipe removes exactly the named record layers and nothing else: the
// auxiliary journal, the auth database, secrets, modules and the identity all
// survive, a dry run touches nothing, and a held store is refused.
func TestStoreWipeRemovesOnlyTheRecordLayers(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "store")
	mk := func(rel string, dir bool) {
		p := filepath.Join(store, rel)
		if dir {
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p, "payload"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed := []string{"control.flatsqldb", "control.flatsqldb-journal", "control.flatsqldb.fsdata",
		"record-catalog.flatsqlmeta", "record-catalog.flatsqlmeta.prefix"}
	removedDirs := []string{"flatsql-streams", "dataset-publication-replay", "dataset-feed-head-sync", "ui-cache"}
	kept := []string{"auxiliary.flatsqlmeta", "auth.db", "auth.db-wal", "sdn.db", "storefront.db", "source-metrics.db", "secrets/credentials.enc"}
	keptDirs := []string{"modules", "customer-modules", "license", "flows", "p2p-forge-certs"}
	for _, f := range removed {
		mk(f, false)
	}
	for _, d := range removedDirs {
		mk(d, true)
	}
	for _, f := range kept {
		mk(f, false)
	}
	for _, d := range keptDirs {
		mk(d, true)
	}
	// Identity sits OUTSIDE the store, beside it.
	mk("../keys/node.key", false)
	authDB := filepath.Join(store, "auth.db")

	// Dry run: plan only.
	var out bytes.Buffer
	if err := wipeStoreRecordLayers(&out, store, authDB, false); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	for _, f := range append(removed, removedDirs...) {
		if _, err := os.Stat(filepath.Join(store, f)); err != nil {
			t.Fatalf("dry run removed %s", f)
		}
	}
	if !bytes.Contains(out.Bytes(), []byte("dry run: nothing removed")) {
		t.Fatalf("dry run did not say so:\n%s", out.String())
	}

	// Held store: refused.
	holder, err := storage.LockStoreForMaintenance(store)
	if err != nil {
		t.Fatal(err)
	}
	err = wipeStoreRecordLayers(&out, store, authDB, true)
	if !errors.Is(err, storage.ErrStoreLocked) {
		t.Fatalf("wipe of a held store = %v, want ErrStoreLocked", err)
	}
	_ = holder.Release()

	// Apply.
	out.Reset()
	if err := wipeStoreRecordLayers(&out, store, authDB, true); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	for _, f := range append(removed, removedDirs...) {
		if _, err := os.Stat(filepath.Join(store, f)); !os.IsNotExist(err) {
			t.Fatalf("%s survived the wipe (err=%v)", f, err)
		}
	}
	for _, f := range append(kept, "../keys/node.key") {
		if _, err := os.Stat(filepath.Join(store, f)); err != nil {
			t.Fatalf("%s did not survive the wipe: %v", f, err)
		}
	}
	for _, d := range keptDirs {
		if _, err := os.Stat(filepath.Join(store, d, "payload")); err != nil {
			t.Fatalf("%s/ did not survive the wipe: %v", d, err)
		}
	}
	// The wiped store opens as an empty record store.
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := storage.NewFlatSQLStore(store, validator, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("open after wipe: %v", err)
	}
	defer opened.Close()
}

// The refusal list is checked before anything is touched.
func TestStoreWipeRefusesToNameAKeptEntry(t *testing.T) {
	if err := storeWipeRefuse("/store", "auxiliary.flatsqlmeta", "/store/auth.db"); err == nil {
		t.Fatal("the auxiliary journal was accepted as a wipe target")
	}
	if err := storeWipeRefuse("/store", "ui-cache/", "/store/ui-cache/auth.db"); err == nil {
		t.Fatal("a directory holding the auth database was accepted as a wipe target")
	}
	if err := storeWipeRefuse("/store", "ui-cache/", "/store/auth.db"); err != nil {
		t.Fatalf("a record layer was refused: %v", err)
	}
}
