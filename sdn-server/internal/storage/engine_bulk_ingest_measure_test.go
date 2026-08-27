package storage

// A/B: N x flatsql_ingest_one_with_source (today's engine mirror) vs ONE
// flatsql_ingest_with_source carrying the same N records as a size-prefixed
// stream. Both exports already exist on the embedded engine; the only
// difference is how many times the host crosses into the guest.

import (
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestMeasureEngineBulkVsSingleIngest(t *testing.T) {
	records := loadTBSFixture(t, 4000)
	binding, routed := engineRoutedSchemaFor("TBS.fbs")
	if !routed {
		t.Fatal("TBS.fbs is not engine-routed")
	}

	for _, mode := range []string{"single", "single-tx", "single-wal", "bulk-tx"} {
		validator, err := sds.NewValidator(nil)
		if err != nil {
			t.Fatalf("NewValidator: %v", err)
		}
		store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "db"), validator)
		if err != nil {
			t.Fatalf("NewFlatSQLStore: %v", err)
		}
		source := "measure/bulk-ab"
		if err := store.ensureEngineSource(source); err != nil {
			t.Fatalf("ensureEngineSource: %v", err)
		}

		payloads := make([][]byte, 0, len(records))
		for _, r := range records {
			p, reason, ok := engineIngestablePayload(binding, r)
			if !ok {
				t.Fatalf("engineIngestablePayload refused a fixture record: %s", reason)
			}
			payloads = append(payloads, p)
		}

		if mode == "single-wal" {
			for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL"} {
				r, err := store.engineDB.Query(pragma)
				if err != nil {
					t.Fatalf("%s: %v", pragma, err)
				}
				t.Logf("  %s -> %v", pragma, r.Rows)
			}
		}
		if mode == "single-tx" || mode == "bulk-tx" {
			if _, err := store.engineDB.Query("BEGIN"); err != nil {
				t.Fatalf("BEGIN: %v", err)
			}
		}
		started := time.Now()
		switch mode {
		case "single", "single-tx", "single-wal":
			for _, p := range payloads {
				if _, err := store.engineDB.IngestOneWithSource(p, source); err != nil {
					t.Fatalf("IngestOneWithSource: %v", err)
				}
			}
		case "bulk-tx":
			var stream []byte
			for _, p := range payloads {
				var pre [4]byte
				binary.LittleEndian.PutUint32(pre[:], uint32(len(p)))
				stream = append(stream, pre[:]...)
				stream = append(stream, p...)
			}
			if _, err := store.engineDB.IngestWithSource(stream, source); err != nil {
				t.Fatalf("IngestWithSource: %v", err)
			}
		}
		if mode == "single-tx" || mode == "bulk-tx" {
			if _, err := store.engineDB.Query("COMMIT"); err != nil {
				t.Fatalf("COMMIT: %v", err)
			}
		}
		elapsed := time.Since(started)
		t.Logf("%-6s %d records in %s = %.0f rows/s (%.3f ms/record)",
			mode, len(payloads), elapsed.Round(time.Millisecond),
			float64(len(payloads))/elapsed.Seconds(),
			float64(elapsed.Microseconds())/float64(len(payloads))/1000)
		_ = store.Close()
	}
}
