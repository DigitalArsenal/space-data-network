package storage

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IQC"
	flatbuffers "github.com/google/flatbuffers/go"
)

// record_ingest_identity_test.go — the host-02 IQC replay (graph:
// sdn-publication-hygiene-20260928): the sigmf parser stamps RETRIEVED_AT and
// CREATED_AT/UPDATED_AT with the fetch time, so re-running the flow on the SAME
// IQEngine payload (same batch id) produced records with new CIDs. The store
// must land zero new records for that replay, across a restart, and still land
// a capture whose metadata actually changed.

const identityTestProvider = "space-data-network-02"
const identityTestSource = "IQEngine"

// buildIdentityTestIQC builds one $IQC record the way the sigmf parser does:
// deterministic capture fields plus the three fetch stamps.
func buildIdentityTestIQC(seq int, stamp, description string) []byte {
	b := flatbuffers.NewBuilder(512)
	id := b.CreateString(fmt.Sprintf("iqengine:local/local/capture-%06d", seq))
	capture := b.CreateString(fmt.Sprintf("capture-%06d", seq))
	source := b.CreateString(identityTestSource)
	sha := b.CreateString(fmt.Sprintf("%064x", uint64(seq)*0x9e3779b97f4a7c15))
	retrieved := b.CreateString(stamp)
	desc := b.CreateString(description)
	datatype := b.CreateString("cf32_le")
	created := b.CreateString(stamp)
	IQC.IQCStart(b)
	IQC.IQCAddID(b, id)
	IQC.IQCAddCAPTURE_ID(b, capture)
	IQC.IQCAddSOURCE_NAME(b, source)
	IQC.IQCAddSOURCE_SHA256(b, sha)
	IQC.IQCAddRETRIEVED_AT(b, retrieved)
	IQC.IQCAddDESCRIPTION(b, desc)
	IQC.IQCAddDATATYPE(b, datatype)
	IQC.IQCAddSAMPLE_RATE_HZ(b, 2.4e6)
	IQC.IQCAddCENTER_FREQ_HZ(b, 1.0e8+float64(seq)*1e3)
	IQC.IQCAddCREATED_AT(b, created)
	IQC.IQCAddUPDATED_AT(b, created)
	root := IQC.IQCEnd(b)
	b.FinishWithFileIdentifier(root, []byte(IQC.IQCIdentifier))
	return append([]byte(nil), b.FinishedBytes()...)
}

func identityTestBatch(n int, stamp string) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = buildIdentityTestIQC(i, stamp, fmt.Sprintf("SigMF capture %d", i))
	}
	return out
}

func identityTestTags(batch string) SourceTags {
	return SourceTags{ProviderID: identityTestProvider, SourceName: identityTestSource, BatchID: batch, ContentKeyID: "public"}
}

func TestRecordIngestIdentityMasksOnlyFetchStamps(t *testing.T) {
	first := buildIdentityTestIQC(7, "2026-09-15T02:11:22Z", "SigMF capture 7")
	again := buildIdentityTestIQC(7, "2026-09-28T19:15:04Z", "SigMF capture 7")
	if computeCID(first) == computeCID(again) {
		t.Fatal("fixture error: the two fetches must differ in bytes")
	}
	id := recordIngestIdentity("IQC.fbs", first)
	if id == "" {
		t.Fatal("IQC record has no ingest identity")
	}
	if got := recordIngestIdentity("IQC.fbs", again); got != id {
		t.Fatalf("same capture, new fetch stamps: identity %s, want %s", got, id)
	}
	changed := buildIdentityTestIQC(7, "2026-09-28T19:15:04Z", "SigMF capture 7 (re-described)")
	if recordIngestIdentity("IQC.fbs", changed) == id {
		t.Fatal("a capture whose metadata changed kept the old identity")
	}
	other := buildIdentityTestIQC(8, "2026-09-15T02:11:22Z", "SigMF capture 7")
	if recordIngestIdentity("IQC.fbs", other) == id {
		t.Fatal("two captures share one identity")
	}
	longer := buildIdentityTestIQC(7, "2026-09-28T19:15:04.123Z", "SigMF capture 7")
	if recordIngestIdentity("IQC.fbs", longer) == id {
		t.Fatal("a stamp of another length must yield a new identity (store it, never drop it)")
	}
	if got := recordIngestIdentity("OMM.fbs", first); got != "" {
		t.Fatalf("a standard without fetch stamps has identity %q, want none", got)
	}
	if got := recordIngestIdentity("IQC.fbs", []byte("not a flatbuffer")); got != "" {
		t.Fatalf("an unwalkable buffer has identity %q, want none", got)
	}
}

// TestIQCReplayAcrossRestartInsertsNothing is the acceptance: the same batch
// fetched again after a restart lands 0 records.
func TestIQCReplayAcrossRestartInsertsNothing(t *testing.T) {
	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)
	const n = 300
	const batch = "60cc968008101521f0f062a74bf83f9c7e72fcc3bcd8bc3ed84b97d12f4a29e3"

	store := openBootStore(t, base, v)
	inserted, err := store.StoreBatchWithSourceTags("IQC.fbs", identityTestBatch(n, "2026-09-15T02:11:22Z"), "module:sigmf", nil, identityTestTags(batch))
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if inserted != n {
		t.Fatalf("first ingest inserted %d, want %d", inserted, n)
	}
	fingerprint, count, err := store.DatasetPublicationSetFingerprint("IQC.fbs", identityTestProvider, identityTestSource, batch)
	if err != nil || count != n {
		t.Fatalf("fingerprint after first ingest: count %d err %v", count, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The daemon restarts and the flow re-fetches the same payload.
	store = openBootStore(t, base, v)
	defer store.Close()
	planGuard(t, store, "IQC replay ingest", func() {
		inserted, err = store.StoreBatchWithSourceTags("IQC.fbs", identityTestBatch(n, "2026-09-28T19:15:04Z"), "module:sigmf", nil, identityTestTags(batch))
	})
	if err != nil {
		t.Fatalf("replay ingest: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("replay of the same batch inserted %d records, want 0", inserted)
	}
	if got := len(batchTestIndexCIDs(t, store, "IQC.fbs")); got != n {
		t.Fatalf("store holds %d IQC records after the replay, want %d", got, n)
	}
	again, count, err := store.DatasetPublicationSetFingerprint("IQC.fbs", identityTestProvider, identityTestSource, batch)
	if err != nil || count != n || again != fingerprint {
		t.Fatalf("replay changed the publication set: count %d fingerprint %s (was %s) err %v", count, again, fingerprint, err)
	}

	// The next upstream edition: 3 captures re-described, 2 new captures.
	next := identityTestBatch(n, "2026-10-05T02:00:00Z")
	for i := 0; i < 3; i++ {
		next[i] = buildIdentityTestIQC(i, "2026-10-05T02:00:00Z", fmt.Sprintf("SigMF capture %d, corrected", i))
	}
	next = append(next, buildIdentityTestIQC(n, "2026-10-05T02:00:00Z", "new"), buildIdentityTestIQC(n+1, "2026-10-05T02:00:00Z", "new"))
	const nextBatch = "next-edition"
	inserted, err = store.StoreBatchWithSourceTags("IQC.fbs", next, "module:sigmf", nil, identityTestTags(nextBatch))
	if err != nil {
		t.Fatalf("next edition: %v", err)
	}
	if inserted != 5 {
		t.Fatalf("next edition inserted %d, want 5 (3 changed + 2 new)", inserted)
	}
	// Every capture of the new edition is in the new batch, including the 297
	// that did not change — a batch-scoped publication of it is complete.
	if _, count, err := store.DatasetPublicationSetFingerprint("IQC.fbs", identityTestProvider, identityTestSource, nextBatch); err != nil || count != n+2 {
		t.Fatalf("next batch names %d records (err %v), want %d", count, err, n+2)
	}
}

func TestStoreWithSourceTagsIdentityRepeatReturnsHeldRecord(t *testing.T) {
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	defer store.Close()
	first := buildIdentityTestIQC(1, "2026-09-15T02:11:22Z", "one")
	held, err := store.StoreWithSourceTags("IQC.fbs", first, "module:sigmf", nil, identityTestTags("b1"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.StoreWithSourceTags("IQC.fbs", buildIdentityTestIQC(1, "2026-09-28T19:15:04Z", "one"), "module:sigmf", nil, identityTestTags("b2"))
	if err != nil {
		t.Fatal(err)
	}
	if again != held {
		t.Fatalf("identity repeat stored as %s, want the held record %s", again, held)
	}
	if got := len(batchTestIndexCIDs(t, store, "IQC.fbs")); got != 1 {
		t.Fatalf("store holds %d records, want 1", got)
	}
	tags := batchTestTagCIDs(t, store, "IQC.fbs")
	if len(tags) != 2 || tags[0] != held || tags[1] != held {
		t.Fatalf("held record tags = %v, want it tagged in both batches", tags)
	}
	// Without a lane there is no identity scope: plain CID dedupe applies.
	if _, err := store.Store("IQC.fbs", buildIdentityTestIQC(1, "2026-09-29T00:00:00Z", "one"), "relay", nil); err != nil {
		t.Fatal(err)
	}
	if got := len(batchTestIndexCIDs(t, store, "IQC.fbs")); got != 2 {
		t.Fatalf("an unattributed write was deduped by identity: %d records, want 2", got)
	}
}

// TestReconcileLaneIngestIdentitiesCollapsesLegacyCopies repairs a lane
// written before identities existed (host-02: 532,945 rows, 36,636 captures).
func TestReconcileLaneIngestIdentitiesCollapsesLegacyCopies(t *testing.T) {
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	defer store.Close()
	const n = 40
	const batch = "60cc9680"
	stamps := []string{"2026-09-15T02:11:22Z", "2026-09-15T11:15:00Z", "2026-09-22T14:00:00Z"}
	var firstCIDs []string
	for i, stamp := range stamps {
		records := identityTestBatch(n, stamp)
		if i == 0 {
			for _, r := range records {
				firstCIDs = append(firstCIDs, computeCID(r))
			}
		}
		// A pre-change store has no identity rows: every replay lands anew.
		if _, err := store.db.Exec(`DELETE FROM sdn_record_ingest_identity`); err != nil {
			t.Fatal(err)
		}
		if inserted, err := store.StoreBatchWithSourceTags("IQC.fbs", records, "module:sigmf", nil, identityTestTags(batch)); err != nil || inserted != n {
			t.Fatalf("legacy ingest %d: inserted %d err %v", i, inserted, err)
		}
	}
	if got := len(batchTestIndexCIDs(t, store, "IQC.fbs")); got != n*len(stamps) {
		t.Fatalf("fixture holds %d records, want %d", got, n*len(stamps))
	}

	dry, err := store.ReconcileLaneIngestIdentities("IQC.fbs", identityTestProvider, identityTestSource, false)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Scanned != int64(n*len(stamps)) || dry.Identities != n || dry.Duplicates != int64(n*(len(stamps)-1)) || dry.RecordsDeleted != 0 {
		t.Fatalf("dry run = %+v", dry)
	}
	if got := len(batchTestIndexCIDs(t, store, "IQC.fbs")); got != n*len(stamps) {
		t.Fatalf("dry run deleted records: %d left", got)
	}

	applied, err := store.ReconcileLaneIngestIdentities("IQC.fbs", identityTestProvider, identityTestSource, true)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Duplicates != int64(n*(len(stamps)-1)) || applied.RecordsDeleted != int64(n*(len(stamps)-1)) {
		t.Fatalf("apply = %+v", applied)
	}
	left := batchTestIndexCIDs(t, store, "IQC.fbs")
	if len(left) != n {
		t.Fatalf("after reconcile %d records, want %d", len(left), n)
	}
	// The OLDEST copy of each capture survives: its index rowid is the cursor
	// peers already hold.
	want := map[string]bool{}
	for _, cid := range firstCIDs {
		want[cid] = true
	}
	for _, cid := range left {
		if !want[cid] {
			t.Fatalf("reconcile kept %s, which is not the first copy", cid)
		}
	}
	if tags := batchTestTagCIDs(t, store, "IQC.fbs"); len(tags) != n {
		t.Fatalf("after reconcile %d tag rows, want %d", len(tags), n)
	}
	count, _, _ := batchTestSummary(t, store, "IQC.fbs")
	if count != n {
		t.Fatalf("source summary counts %d records, want %d", count, n)
	}
	// The backfilled identities make the next replay a no-op.
	inserted, err := store.StoreBatchWithSourceTags("IQC.fbs", identityTestBatch(n, "2026-09-28T19:15:04Z"), "module:sigmf", nil, identityTestTags(batch))
	if err != nil || inserted != 0 {
		t.Fatalf("replay after reconcile inserted %d (err %v), want 0", inserted, err)
	}
}
