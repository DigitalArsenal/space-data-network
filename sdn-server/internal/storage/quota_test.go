package storage

import (
	"fmt"
	"os"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// setRecordTimestampForTest directly overwrites a stored record's
// timestamp columns (both the backing schema table's `timestamp` and
// sdn_record_index's `source_timestamp`, which storeOne always populates
// from the SAME time.Now().Unix() value) so tests can control eviction
// ordering deterministically instead of racing real wall-clock seconds.
func setRecordTimestampForTest(t *testing.T, store *FlatSQLStore, schemaName, cid string, ts int64) {
	t.Helper()

	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		t.Fatalf("SchemaNameToTable(%q) failed: %v", schemaName, err)
	}
	if exists, _ := store.tableExists(tableName); exists {
		if _, err := store.db.Exec(fmt.Sprintf(`UPDATE %s SET timestamp = ? WHERE cid = ?`, tableName), ts, cid); err != nil {
			t.Fatalf("update legacy table timestamp: %v", err)
		}
	}
	producerTables, err := store.listProducerStandardTables()
	if err != nil {
		t.Fatalf("listProducerStandardTables failed: %v", err)
	}
	for _, pt := range producerTables {
		if pt.Standard != tableName {
			continue
		}
		if _, err := store.db.Exec(fmt.Sprintf(`UPDATE %s SET timestamp = ? WHERE cid = ?`, pt.TableName), ts, cid); err != nil {
			t.Fatalf("update routed table %s timestamp: %v", pt.TableName, err)
		}
	}
	if _, err := store.db.Exec(`UPDATE sdn_record_index SET source_timestamp = ? WHERE schema_name = ? AND cid = ?`, ts, schemaName, cid); err != nil {
		t.Fatalf("update sdn_record_index source_timestamp: %v", err)
	}
}

// seedQuotaTestRecords stores n same-size records under schemaName with
// strictly increasing timestamps (base, base+1, base+2, ...) so record i
// is unambiguously older than record i+1. Returns the CIDs in the same
// (oldest-to-newest) order.
func seedQuotaTestRecords(t *testing.T, store *FlatSQLStore, schemaName string, n int, payloadSize int, base int64) []string {
	t.Helper()
	cids := make([]string, n)
	for i := 0; i < n; i++ {
		// Vary payload content per record so CIDs are distinct (content-addressed).
		payload := make([]byte, payloadSize)
		for b := range payload {
			payload[b] = byte((i + b) % 251)
		}
		cid, err := store.Store(schemaName, payload, "TestPeer", nil)
		if err != nil {
			t.Fatalf("Store record %d failed: %v", i, err)
		}
		setRecordTimestampForTest(t, store, schemaName, cid, base+int64(i))
		cids[i] = cid
	}
	return cids
}

func newQuotaTestStore(t *testing.T) *FlatSQLStore {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "flatsql-quota-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestGarbageCollectToQuotaEvictsOldestFirstAndRetainsNewest(t *testing.T) {
	store := newQuotaTestStore(t)

	const payloadSize = 200
	cids := seedQuotaTestRecords(t, store, "RFM.fbs", 10, payloadSize, 1_700_000_000)

	before, err := store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes failed: %v", err)
	}
	if before < int64(10*payloadSize) {
		t.Fatalf("LiveRecordBytes = %d, want >= %d (10 records of %d bytes)", before, 10*payloadSize, payloadSize)
	}

	// Cap tight enough to force evicting roughly half the records.
	maxBytes := int64(5 * payloadSize)
	deleted, err := store.GarbageCollectToQuota(maxBytes)
	if err != nil {
		t.Fatalf("GarbageCollectToQuota failed: %v", err)
	}
	if deleted <= 0 {
		t.Fatalf("GarbageCollectToQuota deleted = %d, want > 0", deleted)
	}

	after, err := store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes (after) failed: %v", err)
	}
	if after > maxBytes {
		t.Fatalf("LiveRecordBytes after eviction = %d, want <= cap %d", after, maxBytes)
	}

	// Oldest record must be gone.
	if rows, err := store.Query("RFM.fbs", "cid = ?", cids[0]); err != nil {
		t.Fatalf("query oldest record %s: %v", cids[0], err)
	} else if len(rows) != 0 {
		t.Fatalf("oldest record %s still present after quota eviction", cids[0])
	}
	// Newest record must be retained.
	if rows, err := store.Query("RFM.fbs", "cid = ?", cids[len(cids)-1]); err != nil {
		t.Fatalf("query newest record %s: %v", cids[len(cids)-1], err)
	} else if len(rows) != 1 {
		t.Fatalf("newest record %s was evicted (should be retained): rows=%d", cids[len(cids)-1], len(rows))
	}

	// New writes must still succeed after eviction.
	if _, err := store.Store("RFM.fbs", []byte("post-eviction record"), "TestPeer", nil); err != nil {
		t.Fatalf("Store after quota eviction failed: %v", err)
	}
}

func TestGarbageCollectToQuotaNoOpWhenUnderCap(t *testing.T) {
	store := newQuotaTestStore(t)
	seedQuotaTestRecords(t, store, "RFM.fbs", 3, 100, 1_700_000_000)

	live, err := store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes failed: %v", err)
	}

	deleted, err := store.GarbageCollectToQuota(live * 10)
	if err != nil {
		t.Fatalf("GarbageCollectToQuota failed: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("GarbageCollectToQuota under cap deleted = %d, want 0", deleted)
	}
}

func TestGarbageCollectToQuotaDisabledWhenMaxBytesNotPositive(t *testing.T) {
	store := newQuotaTestStore(t)
	seedQuotaTestRecords(t, store, "RFM.fbs", 3, 100, 1_700_000_000)

	for _, maxBytes := range []int64{0, -1} {
		deleted, err := store.GarbageCollectToQuota(maxBytes)
		if err != nil {
			t.Fatalf("GarbageCollectToQuota(%d) failed: %v", maxBytes, err)
		}
		if deleted != 0 {
			t.Fatalf("GarbageCollectToQuota(%d) deleted = %d, want 0 (disabled)", maxBytes, deleted)
		}
	}
}

func TestGarbageCollectToQuotaHysteresisLowWaterMark(t *testing.T) {
	store := newQuotaTestStore(t)

	const payloadSize = 100
	seedQuotaTestRecords(t, store, "RFM.fbs", 20, payloadSize, 1_700_000_000)

	maxBytes := int64(10 * payloadSize)
	deleted, err := store.GarbageCollectToQuota(maxBytes)
	if err != nil {
		t.Fatalf("GarbageCollectToQuota failed: %v", err)
	}
	if deleted <= 0 {
		t.Fatal("expected eviction to occur")
	}

	after, err := store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes failed: %v", err)
	}
	lowWater := int64(float64(maxBytes) * quotaLowWaterMarkFraction)
	// Eviction targets the low-water mark, not the bare cap: confirms the
	// hysteresis buffer actually did something (rather than stopping the
	// instant it dipped under maxBytes).
	if after > lowWater+int64(payloadSize) {
		t.Fatalf("LiveRecordBytes after eviction = %d, want close to low-water mark %d (cap %d)", after, lowWater, maxBytes)
	}
	if after > maxBytes {
		t.Fatalf("LiveRecordBytes after eviction = %d, want <= cap %d", after, maxBytes)
	}
}

func TestLiveRecordBytesSumsRecordLength(t *testing.T) {
	store := newQuotaTestStore(t)

	const payloadSize = 150
	const n = 4
	seedQuotaTestRecords(t, store, "RFM.fbs", n, payloadSize, 1_700_000_000)

	live, err := store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes failed: %v", err)
	}
	want := int64(n * payloadSize)
	if live != want {
		t.Fatalf("LiveRecordBytes = %d, want %d (%d records of %d bytes)", live, want, n, payloadSize)
	}
}

func TestDiskUsageBytesReflectsStreamFiles(t *testing.T) {
	store := newQuotaTestStore(t)

	const payloadSize = 150
	seedQuotaTestRecords(t, store, "RFM.fbs", 5, payloadSize, 1_700_000_000)

	usage, err := store.DiskUsageBytes()
	if err != nil {
		t.Fatalf("DiskUsageBytes failed: %v", err)
	}
	live, err := store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes failed: %v", err)
	}
	if usage < live {
		t.Fatalf("DiskUsageBytes = %d, want >= LiveRecordBytes %d (frame overhead only adds bytes)", usage, live)
	}
	if usage == 0 {
		t.Fatal("DiskUsageBytes = 0 after writing records, want > 0")
	}
}
