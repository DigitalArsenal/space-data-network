package format4_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	_ "modernc.org/sqlite"
)

// Reproduce the legacy extractor's index using the same on-disk seam as the
// engine's upgrade tests: missing prefixed entries and extractor metadata.
// The records and their content addresses remain intact throughout the upgrade.
func TestFormat5LegacyFullTextIndexRepairsOnOpen(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	e := openReal(t, root, format4.CreateFresh, 0, nil)
	spec, err := format4.TypeSpecFor("CAT.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.RegisterType(spec); err != nil {
		t.Fatal(err)
	}
	prefixed := sds.NewCATBuilder().WithNoradCatID(999992).WithObjectName("Synthetic upgradeprobe").Build()
	bare := sds.NewCATBuilder().WithNoradCatID(999993).WithObjectName("Synthetic upgradeprobe").Build()[4:]
	inputs := []format4.In{putIn(bare, 1), putIn(prefixed, 2)}
	if _, err := e.Put(ctx, format4.Batch{Type: "CAT", Peer: "synthetic-peer", Records: inputs}); err != nil {
		t.Fatal(err)
	}
	query := format4.Query{Type: "CAT", Search: "upgradeprobe", Limit: 10, Hydrate: true}
	for {
		rows, err := e.Scan(ctx, query)
		if errors.Is(err, format4.ErrBusy) {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		if err != nil || len(rows) != 2 {
			t.Fatalf("seed search: %d rows, %v", len(rows), err)
		}
		break
	}
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	index, err := sql.Open("sqlite", filepath.Join(root, "fsql5", "T", "CAT.fts"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	index.SetMaxOpenConns(1)
	if _, err := index.Exec("DELETE FROM fts WHERE rowid=2; DELETE FROM ftsmeta WHERE k IN ('extractor_version','extractor_target')"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := index.QueryRow("SELECT count(*) FROM fts WHERE fts MATCH 'upgradeprobe'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy index: count=%d error=%v", count, err)
	}
	// Fail repair commits while inspecting coverage. The test removes this
	// trigger only with the engine closed; no two SQLite hosts write together.
	if _, err := index.Exec("CREATE TRIGGER hold_repair BEFORE INSERT ON ftsmeta WHEN NEW.k='extractor_version' AND NEW.v=1 BEGIN SELECT RAISE(ABORT, 'test repair barrier'); END"); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("legacy fixture ready; reopening")
	e = openReal(t, root, format4.OpenExisting, 0, nil)
	states, err := e.FTS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	building := false
	for _, state := range states {
		if state.Type == "CAT" && state.State == "building" {
			building = true
		}
	}
	if !building {
		t.Fatalf("legacy index must report building: %+v", states)
	}
	if _, err := e.Scan(ctx, query); !errors.Is(err, format4.ErrBusy) {
		t.Fatalf("search during repair: %v, want BUSY", err)
	}
	t.Log("building and BUSY observed; releasing repair barrier")
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	index, err = sql.Open("sqlite", filepath.Join(root, "fsql5", "T", "CAT.fts"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Exec("DROP TRIGGER hold_repair"); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("barrier removed; reopening for automatic repair")
	e = openReal(t, root, format4.OpenExisting, 0, nil)
	if err := format4.WaitFTSReady(ctx, e, "CAT"); err != nil {
		t.Fatal(err)
	}
	rows, err := e.Scan(ctx, query)
	if err != nil || len(rows) != len(inputs) {
		t.Fatalf("repaired search: %d rows, %v", len(rows), err)
	}
	for _, in := range inputs {
		found := false
		for _, row := range rows {
			if row.CID == in.CID && bytes.Equal(row.Data, in.Plain) {
				found = true
			}
		}
		if !found {
			t.Fatal("repaired search changed or omitted an original record")
		}
	}
}
