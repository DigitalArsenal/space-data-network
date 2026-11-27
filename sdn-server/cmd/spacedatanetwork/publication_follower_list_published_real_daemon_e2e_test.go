package main

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// listPublishedFrom asks peerID for its flatsql-sync 2.0.0 list-published page
// on schema/source via caller's /api/v1/data/remote/{peerID}/list-published relay.
func (d *publicationDaemon) listPublishedFrom(t *testing.T, targetPeerID, schema, source string) []syncwire.Segment {
	t.Helper()
	var req bytes.Buffer
	if err := syncwire.Request(&req, syncwire.QRPFields{
		SchemaName: schema,
		ProviderID: "publication-e2e",
		SourceName: source,
		Limit:      50,
	}); err != nil {
		t.Fatalf("encode list-published request: %v", err)
	}
	raw := d.request(t, "POST", "/api/v1/data/remote/"+targetPeerID+"/"+syncwire.OpListPublished, publicationStreamType, req.Bytes(), http.StatusOK)
	frames, err := syncwire.SplitFrames(raw)
	if err != nil {
		t.Fatalf("split list-published frames: %v", err)
	}
	if len(frames) == 0 {
		t.Fatalf("empty list-published reply from %s", targetPeerID)
	}
	page, err := syncwire.ParseQRP(frames[0])
	if err != nil {
		t.Fatalf("parse list-published QRP: %v", err)
	}
	segs, err := syncwire.Segments(page)
	if err != nil {
		t.Fatalf("decode list-published segments: %v", err)
	}
	return segs
}

func awaitListedAvailable(t *testing.T, caller *publicationDaemon, targetPeerID, schema, source string, want publicationResult, budget time.Duration) syncwire.Segment {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last []syncwire.Segment
	for {
		last = caller.listPublishedFrom(t, targetPeerID, schema, source)
		for _, seg := range last {
			if seg.ManifestCID == want.ManifestCID && seg.ShardCID == want.ShardCID && seg.Availability == "available" {
				return seg
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("list-published on %s for %s/%s never listed available manifest=%s shard=%s (last=%+v)",
				targetPeerID, schema, source, want.ManifestCID, want.ShardCID, last)
		}
		time.Sleep(time.Second)
	}
}

// TestPublicationFollowerListPublishedRealDaemons proves that a follower daemon
// subscribed with --publications only or --publications keep registers each
// materialized publication in sdn_dataset_shard_publications so flatsql-sync
// list-published serves it as available, and that an upgraded follower whose
// replay state is already materialized automatically backfills a missing
// listing row from its local Kubo blockstore on restart.
func TestPublicationFollowerListPublishedRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "listpub-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "listpub-follower", a.address, a.id, "keep-all")
	b.start(t)
	time.Sleep(3 * time.Second)

	// 1. --publications only lane (like OMM.fbs on host-01).
	if out, err := b.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e",
		"--source-name", "only-list", "--from", a.id, "--publications", "only"); err != nil {
		t.Fatalf("subscribe only-list: %v\n%s", err, out)
	}
	a.ingestOMMCIDs(t, "only-list", "b1", 910_000, 64)
	pub1 := a.publish(t, "OMM.fbs", "only-list", "b1")
	seg1 := awaitListedAvailable(t, a, b.id, "OMM.fbs", "only-list", pub1, 2*time.Minute)
	if seg1.RecordCount != int64(pub1.RecordCount) || seg1.IndexCID != pub1.IndexCID || seg1.ByteCount <= 0 || seg1.PublishedAtMs <= 0 {
		t.Fatalf("unexpected listed segment for b1: %+v (want %+v)", seg1, pub1)
	}
	t.Logf("--publications only: follower listed b1 available (manifest=%s shard=%s records=%d bytes=%d)",
		seg1.ManifestCID, seg1.ShardCID, seg1.RecordCount, seg1.ByteCount)

	a.ingestOMMCIDs(t, "only-list", "b2", 920_000, 96)
	pub2 := a.publish(t, "OMM.fbs", "only-list", "b2")
	seg2 := awaitListedAvailable(t, a, b.id, "OMM.fbs", "only-list", pub2, 2*time.Minute)
	if seg2.RecordCount != int64(pub2.RecordCount) || seg2.PublishedAtMs < seg1.PublishedAtMs {
		t.Fatalf("unexpected listed segment for b2: %+v (prev %+v)", seg2, seg1)
	}
	t.Logf("--publications only: follower listed b2 available (manifest=%s shard=%s records=%d)",
		seg2.ManifestCID, seg2.ShardCID, seg2.RecordCount)

	// 2. --publications keep lane (like CAT.fbs / SPW.fbs on host-01).
	a.ingestOMMCIDs(t, "keep-list", "k1", 930_000, 48)
	if out, err := b.cli(t, "sync", "subscribe", "--schema", "OMM", "--provider-id", "publication-e2e",
		"--source-name", "keep-list", "--from", a.id, "--start", "current", "--publications", "keep"); err != nil {
		t.Fatalf("subscribe keep-list: %v\n%s", err, out)
	}
	pubKeep := a.publish(t, "OMM.fbs", "keep-list", "k1")
	segKeep := awaitListedAvailable(t, a, b.id, "OMM.fbs", "keep-list", pubKeep, 2*time.Minute)
	if segKeep.RecordCount != int64(pubKeep.RecordCount) || segKeep.ByteCount <= 0 {
		t.Fatalf("unexpected listed segment for keep-list: %+v", segKeep)
	}
	t.Logf("--publications keep: follower listed k1 available (manifest=%s shard=%s records=%d)",
		segKeep.ManifestCID, segKeep.ShardCID, segKeep.RecordCount)

	// 3. Upgrade backfill: simulate a v1.1.16 follower where b2's replay state
	// is already materialized and its Kubo blocks are held, but its row in
	// sdn_dataset_shard_publications is absent. On restart, stored-PNM catch-up
	// must backfill the listing row from local Kubo content without re-importing.
	b.stop(t)
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
		SourceName:   "only-list",
		BatchID:      "b2",
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil || len(rows) == 0 {
		_ = st.Close()
		t.Fatalf("expected b2 publication row before simulated upgrade: len=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		if _, err := st.DeleteDatasetShardPublication(row); err != nil {
			_ = st.Close()
			t.Fatalf("delete b2 publication row: %v", err)
		}
	}
	if has, err := st.HasDatasetShardPublicationByManifestCID("OMM.fbs", pub2.ManifestCID); err != nil || has {
		_ = st.Close()
		t.Fatalf("expected b2 manifest absent after delete: has=%t err=%v", has, err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close follower store: %v", err)
	}

	mark := len(readFileString(b.logPath))
	b.start(t)
	segBackfill := awaitListedAvailable(t, a, b.id, "OMM.fbs", "only-list", pub2, 2*time.Minute)
	if segBackfill.RecordCount != int64(pub2.RecordCount) || segBackfill.ByteCount != seg2.ByteCount {
		t.Fatalf("backfilled segment mismatch: got %+v, want records=%d bytes=%d", segBackfill, pub2.RecordCount, seg2.ByteCount)
	}
	if !strings.Contains(readFileString(b.logPath)[mark:], "Registered materialized dataset publication") {
		t.Fatalf("expected backfill log entry after restart")
	}
	t.Logf("upgrade backfill: follower re-registered b2 from local Kubo (manifest=%s shard=%s records=%d)",
		segBackfill.ManifestCID, segBackfill.ShardCID, segBackfill.RecordCount)
	b.requireAlive(t)
}
