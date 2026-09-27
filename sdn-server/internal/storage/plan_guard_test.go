package storage

// The plan guard: every statement a trusted host read path runs is EXPLAINed
// with its real parameters, and the path fails if any of them walks a
// partition (sds_p_*) or the tag table, or unions producer tables under a
// GROUP BY. Those are the plans that held host-02's engine for minutes and
// poisoned it (2026-09-26/27).

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
)

// recordTableAliases are the names this package gives a partition or the tag
// table in its SQL; a plan names a table by its alias.
var recordTableAliases = map[string]bool{
	"records": true, "tags": true, "d": true, "rr": true, "ft": true, "pt": true,
	"held": true, "t": true, "lrb_row": true, "chunk": true, "gone": true,
}

// planWalksPartitions reports the first plan line that walks a partition or
// the tag table. An ordered index scan under a LIMIT stops at the limit and
// is allowed; a full scan never is.
func planWalksPartitions(query string, plan []string) (string, bool) {
	joined := strings.Join(plan, "\n")
	if (strings.Contains(joined, "COMPOUND") || strings.Contains(joined, "UNION")) && strings.Contains(joined, "GROUP BY") {
		return "UNION under GROUP BY", true
	}
	limited := strings.Contains(strings.ToUpper(query), " LIMIT ")
	for _, line := range plan {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "SCAN" {
			continue
		}
		name := fields[1]
		isRecord := strings.HasPrefix(name, "sds_p_") || name == "sdn_record_source_tags" ||
			recordTableAliases[name] || (len(name) > 1 && name[0] == 'r' && name[1] >= '0' && name[1] <= '9') ||
			strings.Contains(line, "sds_p_") || strings.Contains(line, "sdn_record_source_tags")
		if !isRecord {
			continue
		}
		if limited && strings.Contains(line, " USING ") && strings.Contains(line, "INDEX") {
			continue
		}
		return line, true
	}
	return "", false
}

// planGuard runs call and fails the test on every statement it executed whose
// plan walks a partition or the tag table.
func planGuard(t *testing.T, s *FlatSQLStore, name string, call func()) {
	t.Helper()
	var mu sync.Mutex
	var violations []string
	statements := 0
	s.mu.RLock()
	engineDB := s.engineDB
	s.mu.RUnlock()
	restore := flatsqldrv.SetStatementHook(func(query string, params []interface{}) {
		head := strings.ToUpper(strings.TrimSpace(query))
		switch {
		case strings.HasPrefix(head, "SELECT"), strings.HasPrefix(head, "WITH"),
			strings.HasPrefix(head, "INSERT"), strings.HasPrefix(head, "DELETE"), strings.HasPrefix(head, "UPDATE"):
		default:
			return
		}
		res, err := engineDB.Query("EXPLAIN QUERY PLAN "+query, params...)
		if err != nil {
			return
		}
		plan := make([]string, 0, len(res.Rows))
		for _, row := range res.Rows {
			if len(row) > 0 {
				if detail, ok := row[len(row)-1].(string); ok {
					plan = append(plan, detail)
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		statements++
		if line, bad := planWalksPartitions(query, plan); bad {
			violations = append(violations, fmt.Sprintf("%s\n    SQL: %s\n    plan:\n      %s",
				line, strings.Join(strings.Fields(query), " "), strings.Join(plan, "\n      ")))
		}
	})
	call()
	restore()
	mu.Lock()
	defer mu.Unlock()
	if statements == 0 {
		t.Fatalf("%s: the guard saw no statement — it is not watching this path", name)
	}
	if len(violations) > 0 {
		t.Errorf("%s: %d statement(s) walk a partition or the tag table:\n  %s", name, len(violations), strings.Join(violations, "\n  "))
	}
}

// seedPlanGuardStore holds OMM in two partitions with shared CIDs, tags in
// two batches, and aged records for GC.
func seedPlanGuardStore(t *testing.T) *FlatSQLStore {
	t.Helper()
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	t.Cleanup(func() { store.Close() })
	for i := 0; i < 30; i++ {
		data := buildEngineOMM(t, uint32(56000+i), fmt.Sprintf("SAT %d", i), 1_700_000_000+int64(i)*3600)
		if _, err := store.StoreWithSourceTags("OMM.fbs", data, "peer-a", nil,
			SourceTags{ProviderID: "prov", SourceName: "gp", BatchID: fmt.Sprintf("b%d", i%2)}); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			if _, err := store.StoreWithSourceTags("OMM.fbs", data, "peer-b", nil,
				SourceTags{ProviderID: "prov-b", SourceName: "mirror", BatchID: "m1"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if tables, _ := store.recordTablesForSchema("OMM.fbs"); len(tables) != 2 {
		t.Fatalf("fixture holds OMM in %d partitions, want 2", len(tables))
	}
	return store
}

func TestPlanGuardTrustedReadPaths(t *testing.T) {
	store := seedPlanGuardStore(t)
	filter := RawRecordQuery{SchemaName: "OMM.fbs", Limit: 10}

	planGuard(t, store, "CountRawRecords (schema)", func() {
		if _, err := store.CountRawRecords(filter); err != nil {
			t.Fatal(err)
		}
	})
	planGuard(t, store, "RawRecordSnapshot (schema)", func() {
		if _, _, err := store.RawRecordSnapshot(filter); err != nil {
			t.Fatal(err)
		}
	})
	planGuard(t, store, "QueryRoutedByStandard", func() {
		if _, err := store.QueryRoutedByStandard("OMM.fbs", 5); err != nil {
			t.Fatal(err)
		}
	})
	planGuard(t, store, "ReconcileSourceBatch (dry run)", func() {
		if _, err := store.ReconcileSourceBatch("OMM.fbs", "prov", "gp", "b1", false); err != nil {
			t.Fatal(err)
		}
	})
	planGuard(t, store, "ReconcileSourceBatchIndexedDuplicates (dry run)", func() {
		if _, err := store.ReconcileSourceBatchIndexedDuplicates("OMM.fbs", "prov", "gp", "b1", false); err != nil {
			t.Fatal(err)
		}
	})
	planGuard(t, store, "ReconcileSourceBatch (apply)", func() {
		if _, err := store.ReconcileSourceBatch("OMM.fbs", "prov", "gp", "b1", true); err != nil {
			t.Fatal(err)
		}
	})
	planGuard(t, store, "ReconcileSourceBatchIndexedDuplicates (apply)", func() {
		if _, err := store.ReconcileSourceBatchIndexedDuplicates("OMM.fbs", "prov", "gp", "b1", true); err != nil {
			t.Fatal(err)
		}
	})
	planGuard(t, store, "GarbageCollect", func() {
		if _, err := store.GarbageCollect(time.Duration(0)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPlanGuardGarbageCollectToQuota(t *testing.T) {
	store := seedPlanGuardStore(t)
	planGuard(t, store, "GarbageCollectToQuota", func() {
		if _, err := store.GarbageCollectToQuota(1); err != nil {
			t.Fatal(err)
		}
	})
}
