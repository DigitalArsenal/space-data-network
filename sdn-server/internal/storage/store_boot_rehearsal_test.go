package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// THE REHEARSAL THE DEPLOY LAW REQUIRES, AND THE ONE THAT FOUND THIS DEFECT.
//
// It opens an EXISTING store directory with this build and reports what the
// boot cost, phase by phase. Run it against a daemon-stopped consistent COPY
// of a real store — never against a live one — before the binary that contains
// a boot-path change reaches any box.
//
// SHAPE MATTERS. The daemon opens with WithDeferredRecordCatalogReplay and
// WithDeferredBootRebuilds (its boot log says so on every box), so a rehearsal
// that omits them measures work production does in the BACKGROUND and
// misattributes it to the critical path. Both are on by default here; set
// SDN_STORE_REHEARSAL_SYNCHRONOUS=1 to measure the synchronous shape instead.
//
// Skipped unless SDN_STORE_REHEARSAL_DIR names a store copy.
func TestOpenExistingStoreRehearsal(t *testing.T) {
	base := os.Getenv("SDN_STORE_REHEARSAL_DIR")
	if base == "" {
		t.Skip("set SDN_STORE_REHEARSAL_DIR to a COPY of a store directory")
	}

	// THE RUNTIME PIN, STATED WITH THE MEASUREMENT. The store opens the engine
	// with WithPrecompiledAOTCache, which LOADS an artifact but never compiles
	// one — so a cold cache silently means an INTERPRETED engine, and an
	// interpreted engine turns a 30-second boot statement into one that blows
	// the engine's uninterruptible 5-minute per-call budget and poisons it.
	// A rehearsal that does not say which of the two it measured is not
	// evidence. Prewarm, then report.
	artifact, alreadyPresent, err := flatsqlrt.PrewarmAOTArtifact(engineAOTCacheDir(), "flatsql", flatsqlrt.EmbeddedWasm())
	if err != nil {
		t.Logf("AOT prewarm failed (%v) — this rehearsal runs INTERPRETED", err)
	} else {
		t.Logf("AOT artifact %s (already present: %v)", artifact, alreadyPresent)
	}

	before := rehearsalDirBytes(t, base)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	opts := []StoreOption{WithDeferredRecordCatalogReplay(), WithDeferredBootRebuilds()}
	shape := "production (deferred catalog replay + deferred derived rebuilds)"
	if os.Getenv("SDN_STORE_REHEARSAL_SYNCHRONOUS") == "1" {
		opts = nil
		shape = "synchronous (catalog replay + derived rebuilds ON the critical path)"
	}
	t.Logf("rehearsal shape: %s", shape)

	started := time.Now()
	store, err := NewFlatSQLStore(base, validator, opts...)
	if err != nil {
		t.Fatalf("NewFlatSQLStore(%s) after %s: %v", base, time.Since(started).Round(time.Millisecond), err)
	}
	openElapsed := time.Since(started)
	if engine, _ := store.EngineRuntime(); engine != nil {
		t.Logf("engine AOT in use: %v", engine.AOT())
		t.Logf("engine per-call budget: %s", engine.ExecBudget())
	}
	t.Logf("OPEN TOOK %s", openElapsed.Round(time.Millisecond))

	for _, q := range []struct{ label, sql string }{
		{"record index rows", `SELECT COUNT(*) FROM sdn_record_index`},
		{"source tag rows", `SELECT COUNT(*) FROM sdn_record_source_tags`},
		{"persisted views", `SELECT COUNT(*) FROM sqlite_master WHERE type = 'view'`},
		{"shadow partitions", `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name LIKE '%@%'`},
	} {
		var n int64
		queryStarted := time.Now()
		if err := store.db.QueryRow(q.sql).Scan(&n); err != nil {
			t.Errorf("%s: %v", q.label, err)
			continue
		}
		t.Logf("  %-22s %12d  (%s)", q.label, n, time.Since(queryStarted).Round(time.Millisecond))
	}

	closeStarted := time.Now()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	t.Logf("close took %s", time.Since(closeStarted).Round(time.Millisecond))

	after := rehearsalDirBytes(t, base)
	t.Logf("store %d B -> %d B (%+d B)", before, after, after-before)

	// THE BOUND IS THE POINT. An open that outlives the engine's own per-call
	// budget cannot be certified for the fleet: a single call crossing it
	// abandons the execution thread and poisons the node.
	if bound := rehearsalBound(); openElapsed > bound {
		t.Fatalf("open took %s, over the stated bound %s — this store size is not shippable (set SDN_STORE_REHEARSAL_BOUND to restate the bound deliberately)",
			openElapsed.Round(time.Millisecond), bound)
	}
}

// rehearsalBound is the wall-clock bound a boot must meet. It defaults to the
// engine's uninterruptible per-call budget, because a boot that takes longer
// than one engine call is allowed to has, by definition, at least one call
// that could grow past it.
func rehearsalBound() time.Duration {
	if raw := os.Getenv("SDN_STORE_REHEARSAL_BOUND"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return flatsqlrt.DefaultEngineExecTimeout
}

func rehearsalDirBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return total
}

// TestHotWindowRebuildCostRehearsal times ONE schema's hot-window rebuild
// against a real store, which is the statement that poisoned the engine.
//
// It opens in the PRODUCTION shape (both rebuilds deferred), so the open itself
// is the ~2-minute journal scan and everything after it is the measurement.
// SDN_STORE_REHEARSAL_SCHEMA names the schema (default CAT.fbs, the one that
// blew the budget on host-02).
func TestHotWindowRebuildCostRehearsal(t *testing.T) {
	base := os.Getenv("SDN_STORE_REHEARSAL_DIR")
	if base == "" || os.Getenv("SDN_STORE_REHEARSAL_HOT_WINDOW") == "" {
		t.Skip("set SDN_STORE_REHEARSAL_DIR and SDN_STORE_REHEARSAL_HOT_WINDOW=1")
	}
	if _, _, err := flatsqlrt.PrewarmAOTArtifact(engineAOTCacheDir(), "flatsql", flatsqlrt.EmbeddedWasm()); err != nil {
		t.Logf("AOT prewarm failed (%v) — this rehearsal runs INTERPRETED", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(base, validator, WithDeferredRecordCatalogReplay(), WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	engine, _ := store.EngineRuntime()
	t.Logf("engine AOT in use: %v, per-call budget %s, page cache %d MiB",
		engine.AOT(), engine.ExecBudget(), resolveEnginePageCacheMiB())

	schemaName := os.Getenv("SDN_STORE_REHEARSAL_SCHEMA")
	if schemaName == "" {
		schemaName = "CAT.fbs"
	}
	engine.SetPhase("rehearsal: hot-window rebuild " + schemaName)
	ioBefore := engine.FileIO().Stats()
	started := time.Now()
	store.mu.Lock()
	err = store.rebuildEngineRecordsForSchema(schemaName)
	store.mu.Unlock()
	elapsed := time.Since(started)
	ioAfter := engine.FileIO().Stats()
	t.Logf("HOT-WINDOW REBUILD %s TOOK %s (err=%v); resident %d; host reads %d (%d B)",
		schemaName, elapsed.Round(time.Millisecond), err,
		store.engineResidentCount(schemaName),
		ioAfter.Reads-ioBefore.Reads, ioAfter.BytesRead-ioBefore.BytesRead)
	if err != nil {
		t.Fatalf("rebuild %s: %v", schemaName, err)
	}
	if elapsed > engine.ExecBudget() {
		t.Fatalf("one schema's hot-window rebuild took %s, over the engine's %s per-call budget",
			elapsed.Round(time.Millisecond), engine.ExecBudget())
	}
}

// TestExplainHotWindowRehearsal asks the ENGINE's own SQLite how it plans the
// hot-window statement. The CLI's plan is not evidence: the engine is a
// different SQLite build, and a 0.97 s native answer against a 5-minute engine
// answer for the identical SQL is a plan difference, not a speed difference.
func TestExplainHotWindowRehearsal(t *testing.T) {
	base := os.Getenv("SDN_STORE_REHEARSAL_DIR")
	if base == "" || os.Getenv("SDN_STORE_REHEARSAL_EXPLAIN") == "" {
		t.Skip("set SDN_STORE_REHEARSAL_DIR and SDN_STORE_REHEARSAL_EXPLAIN=1")
	}
	if _, _, err := flatsqlrt.PrewarmAOTArtifact(engineAOTCacheDir(), "flatsql", flatsqlrt.EmbeddedWasm()); err != nil {
		t.Logf("AOT prewarm failed: %v", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(base, validator, WithDeferredRecordCatalogReplay(), WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	var version string
	if err := store.db.QueryRow(`SELECT sqlite_version()`).Scan(&version); err == nil {
		t.Logf("engine sqlite_version: %s", version)
	}
	var stat1 int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'sqlite_stat1'`).Scan(&stat1); err == nil {
		t.Logf("sqlite_stat1 present: %v", stat1 > 0)
	}

	for _, probe := range explainProbes(t, store) {
		rows, err := store.db.Query("EXPLAIN QUERY PLAN " + probe.sql)
		if err != nil {
			t.Errorf("%s: explain: %v", probe.label, err)
			continue
		}
		t.Logf("PLAN %s:", probe.label)
		cols, _ := rows.Columns()
		for rows.Next() {
			cells := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range cells {
				ptrs[i] = &cells[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				break
			}
			parts := make([]string, 0, len(cells))
			for _, c := range cells {
				parts = append(parts, fmt.Sprint(c))
			}
			t.Logf("   %s", strings.Join(parts, " | "))
		}
		rows.Close()
	}
}

type explainProbe struct {
	label string
	sql   string
}

func explainProbes(t *testing.T, store *FlatSQLStore) []explainProbe {
	t.Helper()
	readSource, err := store.recordReadSource("CAT.fbs")
	if err != nil {
		t.Fatalf("recordReadSource: %v", err)
	}
	return []explainProbe{
		{"correlated LIMIT-1 subquery (shipped)", `
			SELECT tags.source_name FROM sdn_record_source_tags tags
			WHERE tags.schema_name = 'CAT.fbs' AND tags.cid = 'x'
			ORDER BY tags.created_at DESC LIMIT 1`},
		{"MAX(created_at) form", `
			SELECT tags.source_name, MAX(tags.created_at) FROM sdn_record_source_tags tags
			WHERE tags.schema_name = 'CAT.fbs' AND tags.cid = 'x'`},
		{"paged hot window (shipped shape)", fmt.Sprintf(`
			SELECT rid, stream_path, stream_offset, record_length, source_name FROM (
			  SELECT idx.rowid AS rid, rr.stream_path AS stream_path, rr.stream_offset AS stream_offset,
			         rr.record_length AS record_length,
			         COALESCE((SELECT tags.source_name FROM sdn_record_source_tags tags
			                   WHERE tags.schema_name = idx.schema_name AND tags.cid = idx.cid
			                   ORDER BY tags.created_at DESC LIMIT 1), '') AS source_name
			  FROM sdn_record_index idx JOIN %s rr ON rr.cid = idx.cid
			  WHERE idx.schema_name IN ('CAT.fbs','CAT') AND idx.rowid < 9223372036854775807
			  ORDER BY idx.rowid DESC LIMIT 2000) ORDER BY rid DESC`, readSource)},
		{"page first, resolve source after", fmt.Sprintf(`
			SELECT page.rid, page.stream_path, page.stream_offset, page.record_length,
			       COALESCE((SELECT tags.source_name FROM sdn_record_source_tags tags
			                 WHERE tags.schema_name = page.schema_name AND tags.cid = page.cid
			                 ORDER BY tags.created_at DESC LIMIT 1), '') AS source_name
			FROM (
			  SELECT idx.rowid AS rid, idx.cid AS cid, idx.schema_name AS schema_name,
			         rr.stream_path AS stream_path, rr.stream_offset AS stream_offset,
			         rr.record_length AS record_length
			  FROM sdn_record_index idx JOIN %s rr ON rr.cid = idx.cid
			  WHERE idx.schema_name IN ('CAT.fbs','CAT') AND idx.rowid < 9223372036854775807
			  ORDER BY idx.rowid DESC LIMIT 2000) page
			ORDER BY page.rid DESC`, readSource)},
		{"page + LEFT JOIN latest tag", fmt.Sprintf(`
			SELECT page.rid, page.stream_path, page.stream_offset, page.record_length,
			       COALESCE(tags.source_name, '') AS source_name
			FROM (
			  SELECT idx.rowid AS rid, idx.cid AS cid, idx.schema_name AS schema_name,
			         rr.stream_path AS stream_path, rr.stream_offset AS stream_offset,
			         rr.record_length AS record_length
			  FROM sdn_record_index idx JOIN %s rr ON rr.cid = idx.cid
			  WHERE idx.schema_name IN ('CAT.fbs','CAT') AND idx.rowid < 9223372036854775807
			  ORDER BY idx.rowid DESC LIMIT 2000) page
			LEFT JOIN sdn_record_source_tags tags
			  ON tags.schema_name = page.schema_name AND tags.cid = page.cid
			ORDER BY page.rid DESC, tags.created_at ASC`, readSource)},
	}
}
