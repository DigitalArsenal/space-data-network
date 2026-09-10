package flatsqlrt

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReindexStepPreservesControlTablesAndSourceBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "upgrade.db")
	rt := newDiskRuntime(t, root)
	db, err := rt.OpenDatabase(ommTestSchema, "upgrade", path, JournalTruncate)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.RegisterFileID("$OMM", "OMM"); err != nil {
		t.Fatal(err)
	}
	if err = db.RegisterSource("provider"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Query("CREATE TABLE control(k TEXT PRIMARY KEY,v TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Query("INSERT INTO control VALUES('checkpoint','retained')"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if _, err = db.IngestOneWithSource(fixtureBuffer(t), "provider"); err != nil {
			t.Fatal(err)
		}
	}
	beforeRows, beforeErr := db.Query(`SELECT COUNT(*) FROM "OMM@provider"`)
	if beforeErr != nil || len(beforeRows.Rows) != 1 || beforeRows.Rows[0][0] != int64(7) {
		t.Fatalf("fixture source partition missing: %#v %v", beforeRows, beforeErr)
	}
	if err = db.FlushIndex(); err != nil {
		t.Fatal(err)
	}
	db.Destroy()
	rt.Close()
	before, err := os.ReadFile(path + ".fsdata")
	if err != nil {
		t.Fatal(err)
	}
	rt = newDiskRuntime(t, root)
	changed := strings.Replace(ommTestSchema, "OBJECT_NAME:string;", "OBJECT_NAME:[ubyte];", 1)
	if changed == ommTestSchema {
		t.Fatal("schema fixture did not change")
	}
	db, err = rt.OpenDatabase(changed, "upgrade", path, JournalTruncate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Destroy)
	if err = db.RegisterFileID("$OMM", "OMM"); err != nil {
		t.Fatal(err)
	}
	if err = db.RegisterSource("provider"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.OpenState(); !errors.Is(err, ErrStateVersionMismatch) {
		t.Fatalf("expected schema mismatch, got %v", err)
	}
	for _, invalid := range []int{0, -1, 4097} {
		if _, err = db.ReindexStep(invalid); err == nil {
			t.Fatal("accepted invalid record budget")
		}
	}
	for step := 0; step < 4; step++ {
		done, err := db.ReindexStep(2)
		if err != nil {
			t.Fatal(err)
		}
		if done != (step == 3) {
			t.Fatalf("step %d done=%v", step, done)
		}
	}
	rows, err := db.Query("SELECT v FROM control WHERE k='checkpoint'")
	if err != nil || len(rows.Rows) != 1 || rows.Rows[0][0] != "retained" {
		t.Fatalf("control checkpoint lost: %#v %v", rows, err)
	}
	rows, err = db.Query(`SELECT COUNT(*) FROM "OMM@provider"`)
	if err != nil || len(rows.Rows) != 1 || rows.Rows[0][0] != int64(7) {
		t.Fatalf("source partition lost: %#v %v", rows, err)
	}
	after, err := os.ReadFile(path + ".fsdata")
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("recovery changed durable source bytes")
	}
}
