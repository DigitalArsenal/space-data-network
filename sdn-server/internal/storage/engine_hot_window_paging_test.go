package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// THE PAGED HOT-WINDOW READ MUST BE THE SAME ANSWER, not a similar one.
//
// The single statement it replaces held host-02's engine for 5m0.001s and
// poisoned it (rehearsal, 2026-08-27). Splitting it is only admissible if the
// rows it yields — and their ORDER, which decides the arena sequence every
// engine _rowid is derived from — are identical. This runs both against the
// same store and compares them element by element.
func TestPagedHotWindowReadReproducesTheSingleStatement(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	defer store.Close()

	const records = 25
	tags := SourceTags{ProviderID: "prov-page", SourceName: "paging-src", BatchID: "batch-page"}
	for i := 0; i < records; i++ {
		record := buildEngineOMM(t, uint32(9000+i), "PAGE-SAT", int64(1700000000+i))
		// Production writers use both spellings. Interleave them here so the
		// paged per-alias merge has to reconstruct the historical global rowid
		// order rather than getting a free single-alias pass.
		schemaName := "OMM.fbs"
		if i%3 == 1 {
			schemaName = "OMM"
		}
		cid, err := store.StoreWithSourceTags(schemaName, record, "peer-paging", nil, tags)
		if err != nil {
			t.Fatalf("store record %d: %v", i, err)
		}
		if i == records/2 {
			// The reader receives all tag rows in ascending created_at order and
			// must retain this later source tag, just like the replaced
			// correlated ORDER BY created_at DESC LIMIT 1 query did.
			newer := tags
			newer.SourceName = "paging-src-newest"
			if err := store.UpsertSourceTags(schemaName, cid, newer); err != nil {
				t.Fatalf("attach later source tag: %v", err)
			}
			if _, err := store.db.Exec(`
				UPDATE sdn_record_source_tags
				SET created_at = created_at + 1
				WHERE schema_name = ? AND cid = ? AND source_name = ?
			`, schemaName, cid, newer.SourceName); err != nil {
				t.Fatalf("make source-tag ordering deterministic: %v", err)
			}
		}
	}

	readSource, err := store.recordReadSource("OMM.fbs")
	if err != nil {
		t.Fatalf("recordReadSource: %v", err)
	}
	aliases := engineSchemaNameAliases("OMM.fbs")
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(aliases)), ", ")

	// The statement as it was before paging.
	args := make([]any, 0, len(aliases)+1)
	for _, alias := range aliases {
		args = append(args, alias)
	}
	args = append(args, records)
	rows, err := store.db.Query(fmt.Sprintf(`
		SELECT idx.rowid AS rid, rr.stream_path, rr.stream_offset, rr.record_length,
		       COALESCE((
		           SELECT tags.source_name FROM sdn_record_source_tags tags
		           WHERE tags.schema_name = idx.schema_name AND tags.cid = idx.cid
		           ORDER BY tags.created_at DESC LIMIT 1
		       ), '') AS source_name
		FROM sdn_record_index idx
		JOIN %s rr ON rr.cid = idx.cid
		WHERE idx.schema_name IN (%s)
		ORDER BY idx.rowid DESC
		LIMIT ?
	`, readSource, placeholders), args...)
	if err != nil {
		t.Fatalf("baseline query: %v", err)
	}
	var want []engineHotWindowRow
	for rows.Next() {
		var r engineHotWindowRow
		if err := rows.Scan(&r.rid, &r.streamPath, &r.streamOffset, &r.recordLength, &r.sourceName); err != nil {
			rows.Close()
			t.Fatalf("scan baseline: %v", err)
		}
		want = append(want, r)
	}
	rows.Close()
	if len(want) != records {
		t.Fatalf("baseline returned %d rows, want %d", len(want), records)
	}

	// A page size that forces several pages AND a short final page.
	restore := engineHotWindowRebuildPage
	engineHotWindowRebuildPage = 7
	defer func() { engineHotWindowRebuildPage = restore }()

	pages, stats, err := store.readEngineHotWindowPages("OMM.fbs", readSource, aliases, placeholders, records)
	if err != nil {
		t.Fatalf("paged read: %v", err)
	}
	if stats.pages < 3 {
		t.Fatalf("paged read used %d page(s) — the multi-page path was not exercised", stats.pages)
	}

	var got []engineHotWindowRow
	for _, page := range pages {
		got = append(got, page...)
	}
	if len(got) != len(want) {
		t.Fatalf("paged read returned %d rows, baseline %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d differs\n paged: %+v\n baseline: %+v", i, got[i], want[i])
		}
	}

	// A WINDOW SMALLER THAN THE STORE MUST STILL TAKE THE NEWEST RECORDS.
	// Paging walks rowid DESCENDING for exactly this reason; a page loop that
	// walked the other way would silently make a restart resident on the
	// OLDEST records. Same store, because opening the engine costs ~12 s and
	// this package's test binary already runs close to its 30-minute cap.
	const window = 6
	engineHotWindowRebuildPage = 4
	windowPages, _, err := store.readEngineHotWindowPages("OMM.fbs", readSource, aliases, placeholders, window)
	if err != nil {
		t.Fatalf("paged read (small window): %v", err)
	}
	total := 0
	var lowest, highest int64
	first := true
	for _, page := range windowPages {
		for _, row := range page {
			total++
			if first {
				lowest, highest, first = row.rid, row.rid, false
			}
			if row.rid < lowest {
				lowest = row.rid
			}
			if row.rid > highest {
				highest = row.rid
			}
		}
	}
	if total != window {
		t.Fatalf("paged read returned %d rows for a window of %d", total, window)
	}
	var maxRid int64
	if err := store.db.QueryRow(`SELECT MAX(rowid) FROM sdn_record_index WHERE schema_name = ?`, "OMM.fbs").Scan(&maxRid); err != nil {
		t.Fatalf("max rowid: %v", err)
	}
	if highest != maxRid {
		t.Fatalf("window's highest rowid = %d, store max = %d — the window did not take the NEWEST records", highest, maxRid)
	}
	if highest-lowest != int64(window-1) {
		t.Fatalf("window spans rowids %d..%d for %d rows — the pages are not contiguous", lowest, highest, window)
	}
}

// The persistent schema index is a migration, not a new-store optimisation.
// This test removes it after records exist and asks initTables to recreate it,
// proving the required-index path backfills existing catalog rows. Its EXPLAIN
// assertion is deliberately against the actual FlatSQL engine, not the host
// SQLite CLI: the production regression was a planner/runtime mismatch.
func TestHotWindowSchemaRowIDIndexBackfillsAndSeeks(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	for i := 0; i < 8; i++ {
		if _, err := store.Store("OMM.fbs", buildEngineOMM(t, uint32(9900+i), "INDEX-SAT", int64(1710000000+i)), "peer-index", nil); err != nil {
			t.Fatalf("store record %d: %v", i, err)
		}
	}
	if _, err := store.db.Exec(`DROP INDEX idx_sdn_record_index_schema_rowid`); err != nil {
		t.Fatalf("drop schema rowid index: %v", err)
	}
	if err := store.initTables(); err != nil {
		t.Fatalf("recreate required schema rowid index: %v", err)
	}

	var indexCount int
	if err := store.db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_sdn_record_index_schema_rowid'
	`).Scan(&indexCount); err != nil {
		t.Fatalf("inspect schema rowid index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("schema rowid index count = %d, want 1", indexCount)
	}

	rows, err := store.db.Query(`
		EXPLAIN QUERY PLAN
		SELECT rowid
		FROM sdn_record_index INDEXED BY idx_sdn_record_index_schema_rowid
		WHERE schema_name = ? AND rowid < ?
		ORDER BY rowid DESC
		LIMIT ?
	`, "OMM.fbs", int64(1<<63-1), 4)
	if err != nil {
		t.Fatalf("explain schema rowid seek: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan schema rowid plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schema rowid plan: %v", err)
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "idx_sdn_record_index_schema_rowid") ||
		!strings.Contains(plan, "schema_name=? AND rowid<?") {
		t.Fatalf("hot-window query is not a bounded schema-local seek:\n%s", plan)
	}
}

// Maintenance must preserve the old one-statement rebuild's GLOBAL
// oldest-first engine insertion order, even when the indexed read is split
// across many writer-lock yields. Otherwise each page is locally reversed but
// newer pages arrive before older ones, and the next live write evicts a newer
// record rather than the actual oldest resident record.
func TestMaintenanceHotWindowKeepsGlobalOldestFirstAcrossChunks(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	const (
		window  = 128
		initial = 130
		first   = 10000
	)

	seed := newEngineRecordsStoreWithOptions(t, basePath, WithEngineHotWindow(window))
	for i := 0; i < initial; i++ {
		if _, err := seed.Store("OMM.fbs", buildEngineOMM(t, uint32(first+i), "ORDER-SAT", int64(1720000000+i)), "peer-order", nil); err != nil {
			seed.Close()
			t.Fatalf("store seed record %d: %v", i, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}

	// A daemon-style deferred reopen has no engine rows, so this exercises the
	// maintenance rebuild rather than the ordinary live mirror.
	reopened := newEngineRecordsStoreWithOptions(t, basePath,
		WithEngineHotWindow(window), WithDeferredBootRebuilds(), WithDeferredRecordCatalogReplay())
	defer reopened.Close()
	if count, err := reopened.EngineRecordCount("OMM.fbs"); err != nil || count != 0 {
		t.Fatalf("deferred engine count = %d err=%v, want 0 nil", count, err)
	}
	if err := reopened.RebuildDerivedState(); err != nil {
		t.Fatalf("maintenance rebuild: %v", err)
	}
	if count, err := reopened.EngineRecordCount("OMM.fbs"); err != nil || count != window {
		t.Fatalf("rebuild engine count = %d err=%v, want %d nil", count, err, window)
	}
	if !reopened.EngineHotWindowHydrated() {
		t.Fatal("successful maintenance rebuild did not mark its engine hot window hydrated")
	}
	// A second derived-state call on this LIVE engine must leave the cache
	// alone. New writes already mirror into it; replaying the pre-snapshot
	// durable prefix would append every row a second time even if the resident
	// counter happened to look correct.
	if err := reopened.RebuildDerivedState(); err != nil {
		t.Fatalf("repeat derived-state maintenance: %v", err)
	}
	beforeLive, err := reopened.QueryRawStream("SELECT _data FROM OMM ORDER BY _rowid ASC")
	if err != nil {
		t.Fatalf("read repeated-rebuild OMM window: %v", err)
	}
	beforeFrames, err := flatsqlrt.DecodeSizePrefixedStream(beforeLive.Bytes)
	if err != nil {
		t.Fatalf("decode repeated-rebuild OMM window: %v", err)
	}
	if len(beforeFrames) != window {
		t.Fatalf("repeat rebuild left %d engine frames, want %d without duplicate durable replay", len(beforeFrames), window)
	}

	// The post-rebuild record overflows the window by exactly one. It must
	// evict first+2, the oldest member of the 128-record durable window.
	if _, err := reopened.Store("OMM.fbs", buildEngineOMM(t, first+initial, "ORDER-SAT", 1720000000+initial), "peer-order", nil); err != nil {
		t.Fatalf("store post-rebuild record: %v", err)
	}
	stream, err := reopened.QueryRawStream("SELECT _data FROM OMM ORDER BY _rowid ASC")
	if err != nil {
		t.Fatalf("read rebuilt OMM window: %v", err)
	}
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode rebuilt OMM window: %v", err)
	}
	if len(frames) != window {
		t.Fatalf("rebuilt OMM frame count = %d, want %d", len(frames), window)
	}
	for i, frame := range frames {
		if !OMM.OMMBufferHasIdentifier(frame) {
			t.Fatalf("frame %d is not an OMM FlatBuffer", i)
		}
		want := uint32(first + 3 + i)
		if got := OMM.GetRootAsOMM(frame, 0).NORAD_CAT_ID(); got != want {
			t.Fatalf("engine _rowid %d has NORAD %d, want %d: rebuild pages were not globally oldest-first", i, got, want)
		}
	}
}

// TestMaintenanceHotWindowDetachedScanDoesNotStarveAWriterOrColdReader pins
// the real lock inversion. The old maintenance scan held j.mu for its entire
// journal walk; a live writer then held s.mu while waiting on that mutex, and
// Go's writer-preferring RWMutex queued every cold RecordIndexPage reader.
func TestMaintenanceHotWindowDetachedScanDoesNotStarveAWriterOrColdReader(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	cat := sds.NewCATBuilder().WithNoradCatID(27559).WithObjectName("ALGERIA").Build()
	if _, err := seed.Store("CAT.fbs", cat, "peer-cat", nil); err != nil {
		t.Fatalf("store CAT: %v", err)
	}
	omm := make([][]byte, 0, 130)
	for i := 0; i < cap(omm); i++ {
		omm = append(omm, buildEngineOMM(t, uint32(81000+i), "LOCK-SCAN", int64(1730000000+i)))
	}
	if n, err := seed.StoreBatch("OMM.fbs", omm, "peer-hot", nil); err != nil || n != len(omm) {
		t.Fatalf("seed hot journal = %d, %v", n, err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}

	store := reopenDeferred(t, basePath)
	defer store.Close()
	started := make(chan struct{})
	releaseScan := make(chan struct{})
	oldBeforeScan := engineHotWindowMaintenanceBeforeScan
	engineHotWindowMaintenanceBeforeScan = func() {
		close(started)
		<-releaseScan
	}
	t.Cleanup(func() { engineHotWindowMaintenanceBeforeScan = oldBeforeScan })

	hydrated := make(chan error, 1)
	go func() {
		_, err := store.HydrateEngineHotWindowFromRecordCatalogContext(context.Background())
		hydrated <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance scan did not reach unlocked scan hook")
	}

	writer := make(chan error, 1)
	go func() {
		_, err := store.Store("OMM.fbs", buildEngineOMM(t, 99999, "LIVE-WRITER", 1730000999), "peer-live", nil)
		writer <- err
	}()
	select {
	case err := <-writer:
		if err != nil {
			t.Fatalf("writer during detached scan: %v", err)
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("writer remained blocked by the hot-window candidate scan")
	}

	startedRead := time.Now()
	rows, total, err := store.RecordIndexPage(RecordIndexPageQuery{SchemaName: "CAT.fbs", NoradLike: "27559", Limit: 1})
	if err != nil {
		t.Fatalf("cold CAT RecordIndexPage during scan: %v", err)
	}
	if elapsed := time.Since(startedRead); elapsed >= 750*time.Millisecond {
		t.Fatalf("cold CAT RecordIndexPage took %s while scan was paused", elapsed)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("cold CAT RecordIndexPage = %d rows (%d total), want 1", len(rows), total)
	}
	close(releaseScan)
	if err := <-hydrated; err != nil {
		t.Fatalf("hydrate after writer: %v", err)
	}
}

// TestCloseWaitsForDetachedHotWindowMaintenance pins the shutdown lifetime
// boundary. The candidate scan intentionally runs with s.mu released; Close
// must therefore wait on derivedStateMu before it can tear down the engine,
// database, or journal the scan will use after it resumes.
func TestCloseWaitsForDetachedHotWindowMaintenance(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	for i := 0; i < 8; i++ {
		if _, err := seed.Store("OMM.fbs", buildEngineOMM(t, uint32(84000+i), "CLOSE-SCAN", int64(1760000000+i)), "peer-close", nil); err != nil {
			t.Fatalf("store OMM %d: %v", i, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}

	store := reopenDeferred(t, basePath)
	started := make(chan struct{})
	releaseScan := make(chan struct{})
	oldBeforeScan := engineHotWindowMaintenanceBeforeScan
	engineHotWindowMaintenanceBeforeScan = func() {
		close(started)
		<-releaseScan
	}
	t.Cleanup(func() { engineHotWindowMaintenanceBeforeScan = oldBeforeScan })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hydrated := make(chan error, 1)
	go func() {
		_, err := store.HydrateEngineHotWindowFromRecordCatalogContext(ctx)
		hydrated <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance did not reach detached scan pause")
	}

	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned during a detached maintenance scan: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// The scan's next cancellation poll is after the hook. Releasing it only
	// after cancellation proves Close remains blocked for the actual owner,
	// not merely for an incidental store mutex hold.
	cancel()
	close(releaseScan)
	select {
	case err := <-hydrated:
		if err != nil {
			t.Fatalf("cancelled hydration returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hydration did not finish after scan release")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close after hydration: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after hydration released derived state")
	}
}

// TestMaintenanceHotWindowRetriesCompactionBetweenScanAndStreamPin proves a
// compacted journal cannot be paired with old stream offsets. The test forces
// CompactStreams exactly after the detached scan and before the first stream
// FD is opened; the first snapshot must be discarded and the retry must load a
// complete window from one coherent inode generation.
func TestMaintenanceHotWindowRetriesCompactionBetweenScanAndStreamPin(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	for i := 0; i < 130; i++ {
		if _, err := seed.Store("OMM.fbs", buildEngineOMM(t, uint32(82000+i), "COMPACT-SCAN", int64(1740000000+i)), "peer-compact", nil); err != nil {
			t.Fatalf("store OMM %d: %v", i, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}

	store := reopenDeferred(t, basePath)
	defer store.Close()
	oldAfterScan := engineHotWindowMaintenanceAfterScan
	fired := false
	engineHotWindowMaintenanceAfterScan = func() {
		if fired {
			return
		}
		fired = true
		if _, err := store.CompactStreams(); err != nil {
			t.Fatalf("force compaction between scan and pin: %v", err)
		}
	}
	t.Cleanup(func() { engineHotWindowMaintenanceAfterScan = oldAfterScan })

	loaded, err := store.HydrateEngineHotWindowFromRecordCatalog()
	if err != nil {
		t.Fatalf("hydrate after forced compaction: %v", err)
	}
	if !fired || loaded != 130 {
		t.Fatalf("forced compaction hydrate fired=%v loaded=%d, want true/130", fired, loaded)
	}
	if passes := store.recordCatalog.EngineHotWindowPasses(); passes != 2 {
		t.Fatalf("compaction snapshot made %d journal scans, want stale scan + coherent retry", passes)
	}
	if count, err := store.EngineRecordCount("OMM.fbs"); err != nil || count != 130 {
		t.Fatalf("coherent compacted OMM window = %d err=%v, want 130 nil", count, err)
	}
}

// TestMaintenanceHotWindowCancelAfterScanLeavesWindowUnhydrated covers the
// cancellation point a long scan alone cannot reach: after candidate selection
// but before a later bounded ingest chunk. Partial cache contents are allowed;
// advertising the cache as hydrated is not.
func TestMaintenanceHotWindowCancelAfterScanLeavesWindowUnhydrated(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	seed := newEngineRecordsStore(t, basePath)
	for i := 0; i < 130; i++ {
		if _, err := seed.Store("OMM.fbs", buildEngineOMM(t, uint32(83000+i), "CANCEL-SCAN", int64(1750000000+i)), "peer-cancel", nil); err != nil {
			t.Fatalf("store OMM %d: %v", i, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}

	store := reopenDeferred(t, basePath)
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldBeforeChunk := engineHotWindowMaintenanceBeforeChunk
	engineHotWindowMaintenanceBeforeChunk = func(start int) {
		if start >= engineHotWindowMaintenanceChunk {
			cancel()
		}
	}
	t.Cleanup(func() { engineHotWindowMaintenanceBeforeChunk = oldBeforeChunk })
	loaded, err := store.HydrateEngineHotWindowFromRecordCatalogContext(ctx)
	if err != nil {
		t.Fatalf("cancelled maintenance hydrate returned an error: %v", err)
	}
	if loaded == 0 {
		t.Fatal("cancel hook fired before a bounded page was consumed")
	}
	if store.EngineHotWindowHydrated() {
		t.Fatal("cancel after scan/later chunk marked hot window hydrated")
	}
}
