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
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	e = openReal(t, root, format4.OpenExisting, 0, nil)
	states, err := e.FTS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := ""
	for _, row := range states {
		if row.Type == "CAT" {
			state = row.State
		}
	}
	if state != "building" && state != "ready" {
		t.Fatalf("upgrade state: %q", state)
	}
	t.Logf("first upgrade SUMMARY: %s", state)
	// Repair can finish between SUMMARY and SCAN. A search must either say
	// BUSY or return the complete repaired result, never the old partial index.
	first, err := e.Scan(ctx, query)
	if err != nil && !errors.Is(err, format4.ErrBusy) {
		t.Fatal(err)
	}
	if err == nil && len(first) != len(inputs) {
		t.Fatalf("partly repaired search returned %d records", len(first))
	}
	if errors.Is(err, format4.ErrBusy) && len(first) != 0 {
		t.Fatal("BUSY returned partial records")
	}
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
