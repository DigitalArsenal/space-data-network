package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// TestPublicationUntaggedPNMCatchupRealDaemons proves that >5000 tagged PNM
// records (with non-dataset FILE_IDs) written after an untagged dataset
// publication $PNM do not starve stored-PNM catch-up or backfill on restart.
func TestPublicationUntaggedPNMCatchupRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "untagged-pnm-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "untagged-pnm-follower", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)

	if out, err := b.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e",
		"--source-name", "crowded-only", "--from", a.id, "--publications", "only"); err != nil {
		t.Fatalf("subscribe crowded-only: %v\n%s", err, out)
	}

	a.ingestOMMCIDs(t, "crowded-only", "b1", 940_000, 64)
	pub1 := a.publish(t, "OMM.fbs", "crowded-only", "b1")
	seg1 := awaitListedAvailable(t, a, b.id, "OMM.fbs", "crowded-only", pub1, 2*time.Minute)
	if seg1.RecordCount != int64(pub1.RecordCount) || seg1.ByteCount <= 0 {
		t.Fatalf("unexpected listed segment for b1: %+v (want %+v)", seg1, pub1)
	}
	want := a.scan(t, "OMM.fbs", "crowded-only")
	b.requireRecords(t, "OMM.fbs", "crowded-only", want)

	// Simulate missing publication listing on follower b, then crowd PNM.fbs
	// with >5000 newer tagged non-dataset PNM rows (exceeding both
	// QueryRecentRecords tagged-first bias and datasetPublicationCatchupLimit).
	b.stop(t)
	inspectionHome := publicationTemp(t, "untagged-pnm-inspection")
	inspectionCache := filepath.Join(inspectionHome, "Library", "Caches")
	if err := os.MkdirAll(inspectionCache, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(os.Getenv("SDN_E2E_PUBLICATION_AOT_CACHE"), "flatsql-aot"), filepath.Join(inspectionCache, "flatsql-aot")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", inspectionHome)
	t.Setenv("XDG_CACHE_HOME", os.Getenv("SDN_E2E_PUBLICATION_AOT_CACHE"))
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	st, err := storage.NewFlatSQLStore(b.store, validator)
	if err != nil {
		t.Fatalf("open follower store offline: %v", err)
	}
	rows, err := st.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "publication-e2e",
		SourceName:   "crowded-only",
		BatchID:      "b1",
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil || len(rows) == 0 {
		_ = st.Close()
		t.Fatalf("expected b1 publication row before crowding test: len=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		if _, err := st.DeleteDatasetShardPublication(row); err != nil {
			_ = st.Close()
			t.Fatalf("delete b1 publication row: %v", err)
		}
	}
	if has, err := st.HasDatasetShardPublicationByManifestCID("OMM.fbs", pub1.ManifestCID); err != nil || has {
		_ = st.Close()
		t.Fatalf("expected b1 manifest absent after delete: has=%t err=%v", has, err)
	}

	const taggedCount = 5200
	pnmBuilder := sds.NewPNMBuilder()
	taggedPNMs := make([][]byte, taggedCount)
	for i := 0; i < taggedCount; i++ {
		taggedPNMs[i] = pnmBuilder.
			WithCID(fmt.Sprintf("bafy-tagged-pnm-%05d", i)).
			WithFileID(fmt.Sprintf("tagged-crowder:terrain-tiles:tile-%05d.jpg", i)).
			WithFileName(fmt.Sprintf("tile-%05d.jpg", i)).
			Build()
	}
	inserted, err := st.StoreBatchWithSourceTags("PNM.fbs", taggedPNMs, a.id, nil, storage.SourceTags{
		ProviderID:   "tagged-crowder",
		SourceName:   "terrain-tiles",
		BatchID:      "c1",
		ContentKeyID: "public",
	})
	if err != nil || inserted != taggedCount {
		_ = st.Close()
		t.Fatalf("store tagged crowding PNMs: inserted=%d want=%d err=%v", inserted, taggedCount, err)
	}
	count, err := st.Count("PNM.fbs")
	if err != nil || count < taggedCount+1 {
		_ = st.Close()
		t.Fatalf("expected >=%d PNM.fbs rows after crowding, got count=%d err=%v", taggedCount+1, count, err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close follower store: %v", err)
	}

	mark := len(readFileString(b.logPath))
	b.start(t)
	segBackfill := awaitListedAvailable(t, a, b.id, "OMM.fbs", "crowded-only", pub1, 2*time.Minute)
	if segBackfill.RecordCount != int64(pub1.RecordCount) || segBackfill.ByteCount != seg1.ByteCount {
		t.Fatalf("backfilled segment mismatch: got %+v, want records=%d bytes=%d", segBackfill, pub1.RecordCount, seg1.ByteCount)
	}
	b.requireRecords(t, "OMM.fbs", "crowded-only", want)
	if !strings.Contains(readFileString(b.logPath)[mark:], "Registered materialized dataset publication") {
		t.Fatalf("expected backfill log entry after restart")
	}
	t.Logf("untagged PNM catch-up with %d tagged PNM rows succeeded (manifest=%s shard=%s records=%d)",
		taggedCount, segBackfill.ManifestCID, segBackfill.ShardCID, segBackfill.RecordCount)
	b.requireAlive(t)
}
