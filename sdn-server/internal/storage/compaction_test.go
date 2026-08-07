package storage

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// quotaTestPayloadBytes reproduces, byte for byte, the payload
// seedQuotaTestRecords (quota_test.go) generates for record i at the given
// size -- so tests can independently recompute the expected content of a
// seeded CID without threading extra state through the seeding helper.
func quotaTestPayloadBytes(i, payloadSize int) []byte {
	payload := make([]byte, payloadSize)
	for b := range payload {
		payload[b] = byte((i + b) % 251)
	}
	return payload
}

// assertNoCompactionDebris fails the test if any compaction temp file or
// commit manifest is left under basePath -- true after both a clean
// CompactStreams completion and a fully-resolved crash recovery (roll
// forward or roll back), never true mid-operation.
func assertNoCompactionDebris(t *testing.T, basePath string) {
	t.Helper()
	streamDir := filepath.Join(basePath, flatSQLStreamDirName)

	tempMatches, err := filepath.Glob(filepath.Join(streamDir, "*"+compactionTempInfix+"*"))
	if err != nil {
		t.Fatalf("glob stream compaction debris: %v", err)
	}
	if len(tempMatches) != 0 {
		t.Fatalf("leftover compaction stream temp file(s): %v", tempMatches)
	}

	manifestMatches, err := filepath.Glob(filepath.Join(streamDir, compactionManifestPrefix+"*"))
	if err != nil {
		t.Fatalf("glob compaction manifest debris: %v", err)
	}
	if len(manifestMatches) != 0 {
		t.Fatalf("leftover compaction commit manifest(s): %v", manifestMatches)
	}

	journalTempMatches, err := filepath.Glob(filepath.Join(basePath, recordCatalogJournalFileName+compactionTempInfix+"*"))
	if err != nil {
		t.Fatalf("glob journal compaction debris: %v", err)
	}
	if len(journalTempMatches) != 0 {
		t.Fatalf("leftover compaction journal temp file(s): %v", journalTempMatches)
	}
}

// compactionTestCrash is the panic payload runCompactionExpectingCrash
// injects via compactionCrashPoint to simulate the process dying at a named
// compaction stage.
type compactionTestCrash struct{ stage string }

// runCompactionExpectingCrash replaces the package-level compactionCrashPoint
// seam so that CompactStreams panics the instant it reaches `stage`,
// simulating a hard process crash at exactly that point: everything
// CompactStreams would do AFTER that call (including its own error-path
// temp cleanup, which only runs on ordinary `if err != nil` returns, never
// on a panic) never executes, matching what a real kill -9 would leave
// behind. The panic is recovered here (not inside CompactStreams) so the
// test can proceed to simulate a restart. Fails the test if CompactStreams
// returns normally without ever reaching `stage`.
func runCompactionExpectingCrash(t *testing.T, store *FlatSQLStore, stage string) {
	t.Helper()
	orig := compactionCrashPoint
	compactionCrashPoint = func(s string) {
		if s == stage {
			panic(compactionTestCrash{stage: s})
		}
	}
	defer func() { compactionCrashPoint = orig }()

	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("CompactStreams returned normally, want a simulated crash at stage %q", stage)
			}
			crash, ok := r.(compactionTestCrash)
			if !ok {
				panic(r) // an unrelated panic -- do not swallow it
			}
			if crash.stage != stage {
				t.Fatalf("simulated crash fired at stage %q, want %q", crash.stage, stage)
			}
		}()
		_, _ = store.CompactStreams()
	}()
}

// TestCompactStreamsReclaimsEvictedBytes covers design test 1: eviction
// alone never shrinks DiskUsageBytes (GarbageCollectToQuota only deletes
// metadata rows -- the stream bytes stay on disk), but CompactStreams does,
// every surviving record reads back byte-identical, and the store keeps
// accepting new writes at the new (smaller) end-of-file.
func TestCompactStreamsReclaimsEvictedBytes(t *testing.T) {
	store := newQuotaTestStore(t)

	const payloadSize = 200
	const n = 10
	cids := seedQuotaTestRecords(t, store, "RFM.fbs", n, payloadSize, 1_700_000_000)

	preEvictUsage, err := store.DiskUsageBytes()
	if err != nil {
		t.Fatalf("DiskUsageBytes (pre-evict) failed: %v", err)
	}

	deleted, err := store.GarbageCollectToQuota(int64(5 * payloadSize))
	if err != nil {
		t.Fatalf("GarbageCollectToQuota failed: %v", err)
	}
	if deleted <= 0 {
		t.Fatalf("GarbageCollectToQuota deleted = %d, want > 0", deleted)
	}

	postEvictUsage, err := store.DiskUsageBytes()
	if err != nil {
		t.Fatalf("DiskUsageBytes (post-evict) failed: %v", err)
	}
	if postEvictUsage < preEvictUsage {
		t.Fatalf("DiskUsageBytes dropped after logical-only eviction (got %d, want >= %d): eviction must never itself reclaim physical stream bytes", postEvictUsage, preEvictUsage)
	}

	liveBytes, err := store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes failed: %v", err)
	}

	reclaimed, err := store.CompactStreams()
	if err != nil {
		t.Fatalf("CompactStreams failed: %v", err)
	}
	if reclaimed <= 0 {
		t.Fatalf("CompactStreams reclaimed = %d, want > 0", reclaimed)
	}

	postCompactUsage, err := store.DiskUsageBytes()
	if err != nil {
		t.Fatalf("DiskUsageBytes (post-compact) failed: %v", err)
	}
	if postCompactUsage >= postEvictUsage {
		t.Fatalf("DiskUsageBytes after compaction = %d, want < pre-compaction %d", postCompactUsage, postEvictUsage)
	}
	if postCompactUsage < liveBytes {
		t.Fatalf("DiskUsageBytes after compaction = %d, want >= LiveRecordBytes %d (frame overhead only adds bytes)", postCompactUsage, liveBytes)
	}

	survivors := 0
	for i, cid := range cids {
		rows, err := store.Query("RFM.fbs", "cid = ?", cid)
		if err != nil {
			t.Fatalf("query %s after compaction: %v", cid, err)
		}
		if len(rows) == 0 {
			continue // evicted before compaction -- expected to be gone
		}
		survivors++
		want := quotaTestPayloadBytes(i, payloadSize)
		if !bytes.Equal(rows[0], want) {
			t.Fatalf("survivor %s payload mismatch after compaction", cid)
		}
	}
	if survivors == 0 {
		t.Fatal("expected at least one surviving record after eviction+compaction")
	}

	// New writes still land correctly at the new, smaller end-of-file.
	newCID, err := store.Store("RFM.fbs", []byte("post-compaction record"), "TestPeer", nil)
	if err != nil {
		t.Fatalf("Store after compaction failed: %v", err)
	}
	rows, err := store.Query("RFM.fbs", "cid = ?", newCID)
	if err != nil || len(rows) != 1 || !bytes.Equal(rows[0], []byte("post-compaction record")) {
		t.Fatalf("post-compaction record not read back correctly: rows=%v err=%v", rows, err)
	}
}

// TestCompactStreamsSurvivesReopen covers design test 2: after a clean
// CompactStreams, closing and reopening the store must replay every
// survivor byte-identical from the compacted journal, and the durable
// sdn_record_index rowid cursor (RawRecordHead.MaxRowID) must be unchanged.
func TestCompactStreamsSurvivesReopen(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-compact-reopen-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}

	const payloadSize = 150
	const n = 8
	cids := seedQuotaTestRecords(t, store, "RFM.fbs", n, payloadSize, 1_700_000_000)

	deleted, err := store.GarbageCollectToQuota(int64(4 * payloadSize))
	if err != nil {
		t.Fatalf("GarbageCollectToQuota failed: %v", err)
	}
	if deleted <= 0 {
		t.Fatalf("GarbageCollectToQuota deleted = %d, want > 0", deleted)
	}

	if _, err := store.CompactStreams(); err != nil {
		t.Fatalf("CompactStreams failed: %v", err)
	}

	headBefore, err := store.RawRecordHead(RawRecordQuery{SchemaName: "RFM.fbs"})
	if err != nil {
		t.Fatalf("RawRecordHead (pre-reopen) failed: %v", err)
	}
	if headBefore.MaxRowID <= 0 {
		t.Fatalf("RawRecordHead.MaxRowID (pre-reopen) = %d, want > 0", headBefore.MaxRowID)
	}

	survivorData := map[string][]byte{}
	for i, cid := range cids {
		rows, err := store.Query("RFM.fbs", "cid = ?", cid)
		if err != nil {
			t.Fatalf("query %s (pre-reopen): %v", cid, err)
		}
		if len(rows) == 1 {
			want := quotaTestPayloadBytes(i, payloadSize)
			if !bytes.Equal(rows[0], want) {
				t.Fatalf("survivor %s payload mismatch before reopen", cid)
			}
			survivorData[cid] = want
		}
	}
	if len(survivorData) == 0 {
		t.Fatal("expected at least one surviving record before reopen")
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("reopen after compaction failed: %v", err)
	}
	defer reopened.Close()

	headAfter, err := reopened.RawRecordHead(RawRecordQuery{SchemaName: "RFM.fbs"})
	if err != nil {
		t.Fatalf("RawRecordHead (post-reopen) failed: %v", err)
	}
	if headAfter.MaxRowID != headBefore.MaxRowID {
		t.Fatalf("RawRecordHead.MaxRowID after reopen = %d, want %d (durable cursor must survive compaction+reopen)", headAfter.MaxRowID, headBefore.MaxRowID)
	}

	for cid, want := range survivorData {
		rows, err := reopened.Query("RFM.fbs", "cid = ?", cid)
		if err != nil {
			t.Fatalf("query %s (post-reopen): %v", cid, err)
		}
		if len(rows) != 1 {
			t.Fatalf("survivor %s missing after compact+reopen: rows=%d", cid, len(rows))
		}
		if !bytes.Equal(rows[0], want) {
			t.Fatalf("survivor %s payload mismatch after compact+reopen", cid)
		}
	}

	newCID, err := reopened.Store("RFM.fbs", []byte("post-reopen record"), "TestPeer", nil)
	if err != nil {
		t.Fatalf("Store after reopen failed: %v", err)
	}
	rows, err := reopened.Query("RFM.fbs", "cid = ?", newCID)
	if err != nil || len(rows) != 1 || !bytes.Equal(rows[0], []byte("post-reopen record")) {
		t.Fatalf("post-reopen record not read back correctly: rows=%v err=%v", rows, err)
	}
}

// crashTestFixture seeds a store, durably evicts one record, and records
// the pre-crash-attempt disk usage plus the exact set of surviving
// (cid -> payload) records -- the ground truth every roll-back/roll-forward
// assertion below is checked against.
//
// Eviction uses the durable, single-record Delete API rather than
// GarbageCollectToQuota over setRecordTimestampForTest-faked timestamps:
// setRecordTimestampForTest only overwrites LIVE table state (a raw SQL
// UPDATE with no corresponding journal event -- record_catalog_journal.go's
// replay reconstructs each row's timestamp from its ORIGINAL
// recordCatalogEventRecordUpsert event, which still carries the real
// wall-clock time.Now().Unix() Store captured). That mismatch is invisible
// as long as the same live session is used afterward (LiveRecordBytes,
// Query, and CompactStreams's own journal rewrite all read LIVE state), but
// it means a cutoff-based GC eviction does NOT survive replaying the
// ORIGINAL (pre-compaction) journal -- exactly the journal the ROLLBACK
// half of crash recovery replays. Delete instead appends a real
// recordCatalogEventRecordDelete event (flatsql.go's Delete method), so the
// eviction is durable and correctly survives a plain reopen with no
// compaction involved at all -- which is what these rollback assertions
// require.
type crashTestFixture struct {
	tmpDir     string
	store      *FlatSQLStore
	preUsage   int64
	survivors  map[string][]byte
	evictedOne string // one CID known evicted before the compaction attempt
}

func newCrashTestFixture(t *testing.T) *crashTestFixture {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "flatsql-compact-crash-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}

	const payloadSize = 120
	const n = 6
	cids := seedQuotaTestRecords(t, store, "RFM.fbs", n, payloadSize, 1_700_000_000)

	evictedOne := cids[0]
	if err := store.Delete("RFM.fbs", evictedOne); err != nil {
		t.Fatalf("Delete (evict) failed: %v", err)
	}

	preUsage, err := store.DiskUsageBytes()
	if err != nil {
		t.Fatalf("DiskUsageBytes failed: %v", err)
	}

	survivors := map[string][]byte{}
	for i, cid := range cids {
		if cid == evictedOne {
			continue
		}
		rows, err := store.Query("RFM.fbs", "cid = ?", cid)
		if err != nil {
			t.Fatalf("query %s: %v", cid, err)
		}
		if len(rows) != 1 {
			t.Fatalf("expected surviving record %s intact before crash test, got rows=%d", cid, len(rows))
		}
		survivors[cid] = quotaTestPayloadBytes(i, payloadSize)
	}
	if len(survivors) == 0 {
		t.Fatal("expected at least one surviving record")
	}

	return &crashTestFixture{
		tmpDir:     tmpDir,
		store:      store,
		preUsage:   preUsage,
		survivors:  survivors,
		evictedOne: evictedOne,
	}
}

func (f *crashTestFixture) reopen(t *testing.T) *FlatSQLStore {
	t.Helper()
	// The store already panicked out of CompactStreams (recovered by
	// runCompactionExpectingCrash) or completed a real call; either way
	// Close() here only releases in-process handles (fds, the WASM module,
	// the writer flock) -- it never writes new bytes, so it cannot alter
	// whatever CompactStreams left on disk. This is what lets the test
	// simulate "the process died, then was restarted."
	if err := f.store.Close(); err != nil {
		t.Fatalf("Close after simulated crash failed: %v", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	reopened, err := NewFlatSQLStore(f.tmpDir, validator)
	if err != nil {
		t.Fatalf("reopen (recovery) failed: %v", err)
	}
	return reopened
}

func (f *crashTestFixture) assertSurvivorsIntact(t *testing.T, store *FlatSQLStore) {
	t.Helper()
	for cid, want := range f.survivors {
		rows, err := store.Query("RFM.fbs", "cid = ?", cid)
		if err != nil {
			t.Fatalf("query survivor %s: %v", cid, err)
		}
		if len(rows) != 1 {
			t.Fatalf("survivor %s not intact after recovery: rows=%d", cid, len(rows))
		}
		if !bytes.Equal(rows[0], want) {
			t.Fatalf("survivor %s payload corrupted after recovery", cid)
		}
	}
}

func (f *crashTestFixture) assertEvictedStillGone(t *testing.T, store *FlatSQLStore) {
	t.Helper()
	rows, err := store.Query("RFM.fbs", "cid = ?", f.evictedOne)
	if err != nil {
		t.Fatalf("query evicted %s: %v", f.evictedOne, err)
	}
	if len(rows) != 0 {
		t.Fatalf("evicted record %s resurrected after recovery", f.evictedOne)
	}
}

// TestCompactStreamsCrashRollBack covers design test 3 (roll-back half): a
// simulated crash BEFORE the commit manifest is durable must leave the
// store exactly as if CompactStreams had never been called -- every
// survivor intact, DiskUsageBytes unchanged, and no temp/manifest debris.
func TestCompactStreamsCrashRollBack(t *testing.T) {
	for _, stage := range []string{compactionStageAfterStreamTemps, compactionStageAfterJournalTemp} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			f := newCrashTestFixture(t)
			runCompactionExpectingCrash(t, f.store, stage)

			reopened := f.reopen(t)
			defer reopened.Close()

			postUsage, err := reopened.DiskUsageBytes()
			if err != nil {
				t.Fatalf("DiskUsageBytes after rollback failed: %v", err)
			}
			if postUsage != f.preUsage {
				t.Fatalf("DiskUsageBytes after rollback recovery = %d, want unchanged pre-crash value %d", postUsage, f.preUsage)
			}

			f.assertSurvivorsIntact(t, reopened)
			f.assertEvictedStillGone(t, reopened)
			assertNoCompactionDebris(t, f.tmpDir)

			if _, err := reopened.Store("RFM.fbs", []byte("post-rollback record"), "TestPeer", nil); err != nil {
				t.Fatalf("Store after rollback recovery failed: %v", err)
			}
		})
	}
}

// TestCompactStreamsCrashRollForward covers design test 3 (roll-forward
// half): a simulated crash AT OR AFTER the commit manifest becomes durable
// must leave the store fully post-compaction on the next open -- every
// survivor intact, the evicted record still gone, disk usage smaller than
// the pre-compaction figure, and no temp/manifest debris.
func TestCompactStreamsCrashRollForward(t *testing.T) {
	for _, stage := range []string{compactionStageAfterManifest, compactionStageMidRename} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			f := newCrashTestFixture(t)
			runCompactionExpectingCrash(t, f.store, stage)

			reopened := f.reopen(t)
			defer reopened.Close()

			postUsage, err := reopened.DiskUsageBytes()
			if err != nil {
				t.Fatalf("DiskUsageBytes after rollforward failed: %v", err)
			}
			if postUsage >= f.preUsage {
				t.Fatalf("DiskUsageBytes after rollforward recovery = %d, want < pre-compaction %d", postUsage, f.preUsage)
			}

			f.assertSurvivorsIntact(t, reopened)
			f.assertEvictedStillGone(t, reopened)
			assertNoCompactionDebris(t, f.tmpDir)

			// DiskUsageBytes must be stable (never torn) across repeated calls.
			postUsageAgain, err := reopened.DiskUsageBytes()
			if err != nil {
				t.Fatalf("DiskUsageBytes (again) failed: %v", err)
			}
			if postUsageAgain != postUsage {
				t.Fatalf("DiskUsageBytes unstable after rollforward recovery: %d then %d", postUsage, postUsageAgain)
			}

			if _, err := reopened.Store("RFM.fbs", []byte("post-rollforward record"), "TestPeer", nil); err != nil {
				t.Fatalf("Store after rollforward recovery failed: %v", err)
			}
		})
	}
}

// TestCompactStreamsRepeatCIDMirrors covers design test 4: the same payload
// stored by two different producers creates two metadata rows (one per
// producer's (producer, standard) table) sharing one physical frame.
// Compaction must remap BOTH rows to the same new offset so both producers'
// reads keep resolving correctly.
func TestCompactStreamsRepeatCIDMirrors(t *testing.T) {
	store := newQuotaTestStore(t)

	// Store a "padding" record FIRST (its own producer, own CID) so it
	// occupies the low offset in the shared RFM stream file. Then store the
	// SAME payload under two different producers -- cidA and cidB are
	// therefore identical (content-addressed), and StoreRoutedByProducer's
	// repeat-CID path (mirrorRoutedRecordFromExisting) mirrors producerB's
	// row onto the SAME physical frame producerA's write created, without
	// appending a second copy to the stream file.
	padding := []byte("padding-record-evicted-before-compaction")
	paddingCID, err := store.Store("RFM.fbs", padding, "producerC", nil)
	if err != nil {
		t.Fatalf("Store (padding record) failed: %v", err)
	}
	payload := []byte("shared-payload-published-by-two-producers")
	cidA, err := store.Store("RFM.fbs", payload, "producerA", nil)
	if err != nil {
		t.Fatalf("Store (producerA) failed: %v", err)
	}
	cidB, err := store.Store("RFM.fbs", payload, "producerB", nil)
	if err != nil {
		t.Fatalf("Store (producerB) failed: %v", err)
	}
	if cidA != cidB {
		t.Fatalf("expected identical CID for identical payload, got %s vs %s", cidA, cidB)
	}

	// Durably evict ONLY the padding record (single-record Delete, not a
	// GarbageCollectToQuota byte-budget pass -- GarbageCollectToQuota's
	// evict-count estimate rounds up on any remainder, e.g. with just two
	// distinct records left it can round 1-intended eviction up to 2 and
	// take the shared payload with it, which would invalidate this test's
	// premise rather than exercise it). This guarantees the shared frame's
	// new post-compaction offset differs from its old one: the padding
	// frame was written first (occupying the low offset), so once it's
	// evicted and the store compacted, the surviving shared frame shifts
	// down to a new, smaller offset -- a remap bug that leaves offsets
	// unchanged would NOT pass this test trivially.
	if err := store.Delete("RFM.fbs", paddingCID); err != nil {
		t.Fatalf("Delete (evict padding record) failed: %v", err)
	}

	if _, err := store.CompactStreams(); err != nil {
		t.Fatalf("CompactStreams failed: %v", err)
	}

	tableA, err := ProducerStandardTableName("producerA", "RFM.fbs")
	if err != nil {
		t.Fatalf("ProducerStandardTableName(producerA): %v", err)
	}
	tableB, err := ProducerStandardTableName("producerB", "RFM.fbs")
	if err != nil {
		t.Fatalf("ProducerStandardTableName(producerB): %v", err)
	}

	var pathA, pathB string
	var offsetA, offsetB int64
	if err := store.db.QueryRow(fmt.Sprintf(`SELECT stream_path, stream_offset FROM %s WHERE cid = ?`, tableA), cidA).Scan(&pathA, &offsetA); err != nil {
		t.Fatalf("read producerA row after compaction: %v", err)
	}
	if err := store.db.QueryRow(fmt.Sprintf(`SELECT stream_path, stream_offset FROM %s WHERE cid = ?`, tableB), cidB).Scan(&pathB, &offsetB); err != nil {
		t.Fatalf("read producerB row after compaction: %v", err)
	}
	if pathA != pathB || offsetA != offsetB {
		t.Fatalf("mirror rows resolved to different frames after compaction: producerA=(%s,%d) producerB=(%s,%d)", pathA, offsetA, pathB, offsetB)
	}

	rowsA, err := store.Query("RFM.fbs", "cid = ?", cidA)
	if err != nil || len(rowsA) != 1 || !bytes.Equal(rowsA[0], payload) {
		t.Fatalf("read via producerA-shared cid after compaction mismatched: rows=%v err=%v", rowsA, err)
	}

	routedA, err := store.QueryRoutedByProducer("producerA", 0)
	if err != nil {
		t.Fatalf("QueryRoutedByProducer(producerA) failed: %v", err)
	}
	if !containsRoutedCID(routedA, cidA) {
		t.Fatalf("producerA's routed row for %s missing after compaction", cidA)
	}
	routedB, err := store.QueryRoutedByProducer("producerB", 0)
	if err != nil {
		t.Fatalf("QueryRoutedByProducer(producerB) failed: %v", err)
	}
	if !containsRoutedCID(routedB, cidB) {
		t.Fatalf("producerB's routed row for %s missing after compaction", cidB)
	}
}

func containsRoutedCID(records []RoutedRecord, cid string) bool {
	for _, r := range records {
		if r.CID == cid {
			return true
		}
	}
	return false
}

// TestCompactStreamsIdempotentNoDeadBytes covers design test 5: compacting
// a store with no evictions is a near-no-op (no records lost or
// duplicated, no meaningful byte growth) and the result survives a reopen
// cleanly with no leftover temp/manifest debris.
func TestCompactStreamsIdempotentNoDeadBytes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-compact-idempotent-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}

	const payloadSize = 130
	const n = 5
	cids := seedQuotaTestRecords(t, store, "RFM.fbs", n, payloadSize, 1_700_000_000)

	before, err := store.DiskUsageBytes()
	if err != nil {
		t.Fatalf("DiskUsageBytes (before) failed: %v", err)
	}

	reclaimed, err := store.CompactStreams()
	if err != nil {
		t.Fatalf("CompactStreams failed: %v", err)
	}
	if reclaimed < 0 {
		t.Fatalf("CompactStreams reclaimed a negative amount: %d", reclaimed)
	}
	// No evictions happened, so there is nothing to reclaim beyond a little
	// journal-encoding slack; a reclaim on the order of a whole record would
	// indicate a live frame was lost.
	if reclaimed >= int64(payloadSize) {
		t.Fatalf("CompactStreams reclaimed %d bytes with no evictions, want a near-no-op (< %d)", reclaimed, payloadSize)
	}

	after, err := store.DiskUsageBytes()
	if err != nil {
		t.Fatalf("DiskUsageBytes (after) failed: %v", err)
	}
	if after > before {
		t.Fatalf("DiskUsageBytes grew after a no-dead-bytes compaction: before=%d after=%d", before, after)
	}

	for i, cid := range cids {
		rows, err := store.Query("RFM.fbs", "cid = ?", cid)
		if err != nil {
			t.Fatalf("query %s after no-op compaction: %v", cid, err)
		}
		if len(rows) != 1 {
			t.Fatalf("record %s missing/duplicated after no-op compaction: rows=%d", cid, len(rows))
		}
		if !bytes.Equal(rows[0], quotaTestPayloadBytes(i, payloadSize)) {
			t.Fatalf("record %s payload mismatch after no-op compaction", cid)
		}
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	reopened, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("reopen after no-op compaction failed: %v", err)
	}
	defer reopened.Close()

	for i, cid := range cids {
		rows, err := reopened.Query("RFM.fbs", "cid = ?", cid)
		if err != nil {
			t.Fatalf("query %s after reopen: %v", cid, err)
		}
		if len(rows) != 1 {
			t.Fatalf("record %s missing/duplicated after reopen: rows=%d", cid, len(rows))
		}
		if !bytes.Equal(rows[0], quotaTestPayloadBytes(i, payloadSize)) {
			t.Fatalf("record %s payload mismatch after reopen", cid)
		}
	}

	assertNoCompactionDebris(t, tmpDir)
}

// TestCompactStreamsLeadingGapRemapNoCorruption reproduces finding #1:
// applyCompactionOffsetRemap must move every metadata row exactly once,
// based on its OWN original stream_offset, no matter how much the old and
// new offset ranges overlap or what order rows/pairs are processed in.
//
// Before the fix, applyCompactionOffsetRemap issued one
// `UPDATE ... SET stream_offset = :new WHERE stream_offset = :old` PER
// (old, new) pair, looping over offsetMap -- a Go map, whose iteration
// order is randomized per run. Compaction always moves frames LEFT, so
// after any non-trailing delete, some pair's newOffset commonly equals
// ANOTHER pair's oldOffset (e.g. old->new = {204->0, 408->204}). If the
// loop happened to process the "moves INTO offset 204" pair before the
// "moves OUT OF offset 204" pair, the second UPDATE's `WHERE
// stream_offset = 204` re-captured BOTH the rows genuinely still at 204 AND
// the rows the first UPDATE had just written there, merging two distinct
// physical frames' metadata onto one offset and leaving the other frame's
// rows dangling -- silently (same-length frames: both rows now read
// whichever frame's bytes physically live at the merged offset, i.e. wrong
// data that still passes the length-prefix check) or as a vanished record
// (differing-length frames: the length-prefix check then fails and the read
// errors out). Run with `go test -count=20` (or more) to reliably surface
// this: any single run can pass by luck depending on that run's map
// iteration order.
func TestCompactStreamsLeadingGapRemapNoCorruption(t *testing.T) {
	t.Run("leading delete, three survivors", func(t *testing.T) {
		// 4 equal-size records, delete the FIRST: every survivor's new
		// offset chains onto another survivor's old offset (old->new =
		// {134->0, 268->134, 402->268}) -- the "collapse to 0" case, with
		// >=3 survivors as called for by the design's test coverage gap.
		testCompactStreamsGapRemapNoCorruption(t, []int{130, 130, 130, 130}, 0)
	})
	t.Run("middle delete", func(t *testing.T) {
		// Same 4 equal-size records, delete a genuinely MIDDLE one (index
		// 1, neither first nor last): survivors after it still chain
		// (old->new = {268->134, 402->268}) even though the deleted record
		// itself was not leading.
		testCompactStreamsGapRemapNoCorruption(t, []int{130, 130, 130, 130}, 1)
	})
	t.Run("leading delete, differing lengths", func(t *testing.T) {
		// Mixed sizes so the corrupted case can manifest as a VANISHED
		// record (length-prefix mismatch) rather than only wrong-but-same-
		// length bytes: the deleted record (size 100) and the next
		// survivor (also size 100) are equal length by construction, which
		// is what produces the old->new overlap {104->0, 208->104} despite
		// the remaining records (250 and 60 bytes) differing in size.
		testCompactStreamsGapRemapNoCorruption(t, []int{100, 100, 250, 60}, 0)
	})
}

// testCompactStreamsGapRemapNoCorruption stores one record per entry in
// sizes (all under the SAME standard and single producer, so
// recordReadSource resolves to a single bare table -- this test is scoped
// to finding #1's offset-remap bug, not finding #2's attribution
// ambiguity), deletes the record at deleteIdx (a non-trailing index so
// survivors after it shift down and their new offsets overlap some
// survivor's old offset), compacts, and -- WITHOUT reopening -- reads every
// surviving record back through the live Query path, asserting each
// returns ITS OWN original bytes untouched.
func testCompactStreamsGapRemapNoCorruption(t *testing.T, sizes []int, deleteIdx int) {
	t.Helper()
	store := newQuotaTestStore(t)

	n := len(sizes)
	cids := make([]string, n)
	payloads := make([][]byte, n)
	for i, size := range sizes {
		payload := quotaTestPayloadBytes(i, size)
		cid, err := store.Store("RFM.fbs", payload, "TestPeer", nil)
		if err != nil {
			t.Fatalf("Store record %d (size %d) failed: %v", i, size, err)
		}
		cids[i] = cid
		payloads[i] = payload
	}

	if err := store.Delete("RFM.fbs", cids[deleteIdx]); err != nil {
		t.Fatalf("Delete record %d failed: %v", deleteIdx, err)
	}

	if _, err := store.CompactStreams(); err != nil {
		t.Fatalf("CompactStreams failed: %v", err)
	}

	for i := 0; i < n; i++ {
		rows, err := store.Query("RFM.fbs", "cid = ?", cids[i])
		if err != nil {
			t.Fatalf("query record %d: %v", i, err)
		}
		if i == deleteIdx {
			if len(rows) != 0 {
				t.Fatalf("deleted record %d resurrected after compaction", i)
			}
			continue
		}
		if len(rows) != 1 {
			t.Fatalf("survivor %d (size %d) missing/duplicated after compaction remap: rows=%d -- offset-remap re-capture bug (finding #1)", i, sizes[i], len(rows))
		}
		if !bytes.Equal(rows[0], payloads[i]) {
			t.Fatalf("survivor %d (size %d) payload MISMATCH after compaction remap: got %d bytes, want its own %d bytes -- offset-remap re-capture bug (finding #1) landed it on a neighbor's frame", i, sizes[i], len(rows[0]), len(payloads[i]))
		}
	}
}

// TestCompactStreamsRepeatCIDAttributionMatchesPlainReopen covers finding
// #2: buildCompactedRecordCatalogSnapshot must attribute a repeat-CID
// record to the SAME producer a plain (never-compacted) reopen would land
// it under, not to an arbitrary co-publishing producer's un-journaled
// mirror row.
//
// recordReadSource's multi-table read path is `(... UNION ALL ...) GROUP BY
// cid` with bare (non-aggregated) peer_id/signature_hex columns -- SQLite's
// own documentation states the value returned for such columns, absent
// MIN()/MAX(), is UNDEFINED. listProducerStandardTables orders (producer,
// standard) tables ALPHABETICALLY by table name, and this engine's actual
// (unguaranteed, but observed) GROUP BY evaluation keeps the FIRST-scanned
// row's bare-column values for a group. The producer identities below are
// chosen specifically so the MIRROR writer's table name sorts BEFORE the
// ORIGINAL writer's -- reliably landing on the losing side of that
// ambiguous pick pre-fix (verified: reverting the fix makes this fail
// deterministically, 10/10 runs), without needing repeated runs the way
// finding #1's map-iteration-order bug does.
func TestCompactStreamsRepeatCIDAttributionMatchesPlainReopen(t *testing.T) {
	const payload = "shared-payload-two-producers-attribution-test"
	const originalWriter = "zzz-original-writer"
	const mirrorWriter = "aaa-mirror-writer"

	newAttributionTestStore := func(t *testing.T) (string, *sds.Validator, *FlatSQLStore) {
		t.Helper()
		tmpDir, err := os.MkdirTemp("", "flatsql-compact-attribution-test-*")
		if err != nil {
			t.Fatalf("MkdirTemp failed: %v", err)
		}
		validator, err := sds.NewValidator(nil)
		if err != nil {
			t.Fatalf("NewValidator failed: %v", err)
		}
		store, err := NewFlatSQLStore(tmpDir, validator)
		if err != nil {
			t.Fatalf("NewFlatSQLStore failed: %v", err)
		}
		return tmpDir, validator, store
	}

	writeRepeatCID := func(t *testing.T, store *FlatSQLStore) string {
		t.Helper()
		cidA, err := store.Store("RFM.fbs", []byte(payload), originalWriter, nil)
		if err != nil {
			t.Fatalf("Store (original writer) failed: %v", err)
		}
		cidB, err := store.Store("RFM.fbs", []byte(payload), mirrorWriter, nil)
		if err != nil {
			t.Fatalf("Store (mirror writer) failed: %v", err)
		}
		if cidA != cidB {
			t.Fatalf("expected identical CID for identical payload, got %s vs %s", cidA, cidB)
		}
		return cidA
	}

	// Ground truth: a plain reopen (CompactStreams never called) of a store
	// carrying this exact write history. mirrorRoutedRecordFromExisting
	// never journals, so replay only ever recreates the ORIGINAL writer's
	// row -- this is the attribution the compacted path below must match.
	plainDir, plainValidator, plainStore := newAttributionTestStore(t)
	defer os.RemoveAll(plainDir)
	cid := writeRepeatCID(t, plainStore)
	if err := plainStore.Close(); err != nil {
		t.Fatalf("Close (plain) failed: %v", err)
	}
	plainReopened, err := NewFlatSQLStore(plainDir, plainValidator)
	if err != nil {
		t.Fatalf("reopen (plain) failed: %v", err)
	}
	defer plainReopened.Close()
	wantProducers := attributedProducers(t, plainReopened, "RFM.fbs", cid)

	// Compacted path: the SAME write history, but CompactStreams runs
	// against the LIVE session while the mirror writer's un-journaled row
	// is still present in memory -- the exact scenario
	// buildCompactedRecordCatalogSnapshot must get right -- then close and
	// reopen to force a fresh replay of the COMPACTED journal.
	compactDir, compactValidator, compactStore := newAttributionTestStore(t)
	defer os.RemoveAll(compactDir)
	if cid2 := writeRepeatCID(t, compactStore); cid2 != cid {
		t.Fatalf("compacted-path CID %s != plain-path CID %s", cid2, cid)
	}
	if _, err := compactStore.CompactStreams(); err != nil {
		t.Fatalf("CompactStreams failed: %v", err)
	}
	if err := compactStore.Close(); err != nil {
		t.Fatalf("Close (compacted) failed: %v", err)
	}
	compactReopened, err := NewFlatSQLStore(compactDir, compactValidator)
	if err != nil {
		t.Fatalf("reopen (compacted) failed: %v", err)
	}
	defer compactReopened.Close()
	gotProducers := attributedProducers(t, compactReopened, "RFM.fbs", cid)

	if strings.Join(gotProducers, ",") != strings.Join(wantProducers, ",") {
		t.Fatalf("compact+reopen attributed %s to producers %v, want %v (same as a plain reopen of identical write history) -- recordReadSource's ambiguous GROUP BY pick (finding #2)", cid, gotProducers, wantProducers)
	}

	rows, err := compactReopened.Query("RFM.fbs", "cid = ?", cid)
	if err != nil || len(rows) != 1 || string(rows[0]) != payload {
		t.Fatalf("payload not read back correctly after compact+reopen: rows=%v err=%v", rows, err)
	}
}

// attributedProducers returns EVERY (producer, standard) table that currently
// holds a row for cid, sorted.
//
// THIS USED TO ASSERT "EXACTLY ONE", AND THAT EXPECTATION WAS AN ARTEFACT.
// mirrorRoutedRecordFromExisting deliberately writes a row for the SECOND
// producer of a repeat CID and deliberately does not journal it
// (producer_standard_tables.go:118 — "records that THIS producer also published
// it"). While the control database was `:memory:` that row was silently
// forgotten at every restart, so a reopen always showed one producer. The
// database is durable now, so real provenance the store chose to record
// survives — strictly more information, never less.
//
// It does not change what READS return: recordReadSource UNIONs the producer
// tables and collapses them with `GROUP BY cid`
// (producer_standard_tables.go:384), so a CID in two tables still yields one
// row. The property this helper exists for — compaction must not change
// attribution relative to a plain reopen of identical history — is preserved by
// comparing the SETS.
//
// (Recorded, not fixed here: that `GROUP BY cid` has no ORDER BY, so WHICH
// producer a multi-producer CID reports is arbitrary. That ambiguity is
// pre-existing and is now reachable after a restart as well as within a
// session. It belongs to the attribution owner, not to the durability lane.)
func attributedProducers(t *testing.T, store *FlatSQLStore, schemaName, cid string) []string {
	t.Helper()
	tables, err := store.listProducerStandardTables()
	if err != nil {
		t.Fatalf("listProducerStandardTables failed: %v", err)
	}
	standard, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		t.Fatalf("SchemaNameToTable failed: %v", err)
	}
	var found []string
	for _, tbl := range tables {
		if tbl.Standard != standard {
			continue
		}
		var exists int
		err := store.db.QueryRow(fmt.Sprintf(`SELECT 1 FROM %s WHERE cid = ?`, tbl.TableName), cid).Scan(&exists)
		if err == nil {
			found = append(found, tbl.ProducerID)
		} else if err != sql.ErrNoRows {
			t.Fatalf("query %s for cid: %v", tbl.TableName, err)
		}
	}
	if len(found) == 0 {
		t.Fatalf("no producer table holds cid %s", cid)
	}
	sort.Strings(found)
	return found
}
