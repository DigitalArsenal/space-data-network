package node

// The import gate on the PNM replay path: a trusted peer's dataset-update
// PNM reaches materializeDatasetPublicationPNM three ways (the schema-topic
// gossip, the PNM.fbs TipQueue, the stored-PNM catch-up). A lane this node
// does not subscribe to from that peer is skipped on every one of them once
// the signed DPM names it: the shard and index are never fetched and no
// record lands.

import (
	"testing"
	"time"

	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func (f *replayNodeFixture) laneRecords(t *testing.T) int {
	t.Helper()
	records, err := f.node.store.QueryIndexedRecords(storage.IndexedRecordQuery{
		SchemaName: "CAT.fbs", ProviderID: f.tags.ProviderID, SourceName: f.tags.SourceName, BatchID: f.tags.BatchID, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	return len(records)
}

func (f *replayNodeFixture) assertNothingImported(t *testing.T, path string) {
	t.Helper()
	if shard, idx := f.requestsFor(f.export.ShardCID), f.requestsFor(f.export.IndexCID); shard != 0 || idx != 0 {
		t.Fatalf("%s: a new, unlisted lane was fetched: %d shard and %d index requests", path, shard, idx)
	}
	if n := f.laneRecords(t); n != 0 {
		t.Fatalf("%s: %d records of a new, unlisted lane landed", path, n)
	}
	if state := f.replayState(t); state != "" {
		t.Fatalf("%s: replay state = %q; a skipped lane must leave none, so a later allow-list entry imports it", path, state)
	}
}

// newDenyingReplayFixture is the replay fixture without its subscription:
// the provider is trusted, but this node does not subscribe to the lane.
func newDenyingReplayFixture(t *testing.T) *replayNodeFixture {
	t.Helper()
	f := newReplayNodeFixture(t, true)
	if err := f.node.Unfollow("CAT.fbs", f.tags.ProviderID, f.tags.SourceName, f.providerID.String()); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestTrustedPNMReplaySkipsANewLane(t *testing.T) {
	t.Parallel()
	f := newDenyingReplayFixture(t)
	materialized, err := f.node.materializeDatasetPublicationPNM(f.node.ctx, "CAT.fbs", f.pnm, f.providerID)
	if err != nil || materialized {
		t.Fatalf("materialize = %v, %v; want skipped without error", materialized, err)
	}
	if f.requestsFor(f.manifestCID) != 1 {
		t.Fatalf("manifest requests = %d, want 1 (the DPM names the lane)", f.requestsFor(f.manifestCID))
	}
	f.assertNothingImported(t, "replay")
	// The same PNM again inside 24 hours is not even re-fetched.
	if _, err := f.node.materializeDatasetPublicationPNM(f.node.ctx, "CAT.fbs", f.pnm, f.providerID); err != nil {
		t.Fatal(err)
	}
	if f.requestsFor(f.manifestCID) != 1 {
		t.Fatalf("manifest requests = %d after a repeat, want 1", f.requestsFor(f.manifestCID))
	}
}

func TestTrustedPNMGossipSkipsANewLane(t *testing.T) {
	t.Parallel()
	f := newDenyingReplayFixture(t)
	if err := f.node.handleDatasetPublicationPNM(f.node.ctx, "CAT.fbs", f.pnm, f.providerID); err != nil {
		t.Fatalf("gossip handler: %v", err)
	}
	f.assertNothingImported(t, "gossip")
}

func TestTrustedPNMTipQueueSkipsANewLane(t *testing.T) {
	t.Parallel()
	f := newDenyingReplayFixture(t)
	f.node.handleTipQueueTip(&sdnpubsub.Tip{
		PeerID:     f.providerID.String(),
		CID:        pnmCID(t, f.pnm),
		SchemaType: pnmFileID(t, f.pnm),
		RawPNM:     f.pnm,
		ReceivedAt: time.Now(),
	}, sdnpubsub.ResolvedConfig{})
	f.assertNothingImported(t, "tipqueue")
}

// The TipQueue keeps nothing of a refused lane's tip: the handler declines
// it, the first time and on a re-announcement inside the 24-hour mark, so
// the queue neither fetches nor pins its CID and no PNM row is stored.
func TestTrustedPNMTipQueueDeclinesANewLane(t *testing.T) {
	t.Parallel()
	f := newDenyingReplayFixture(t)
	for i := 0; i < 2; i++ {
		tip := &sdnpubsub.Tip{
			PeerID:     f.providerID.String(),
			CID:        pnmCID(t, f.pnm),
			SchemaType: pnmFileID(t, f.pnm),
			RawPNM:     f.pnm,
			ReceivedAt: time.Now(),
		}
		f.node.handleTipQueueTip(tip, sdnpubsub.ResolvedConfig{})
		if !tip.IsDeclined() {
			t.Fatalf("announcement %d of a refused lane was not declined", i+1)
		}
	}
	f.assertNothingImported(t, "tipqueue")
}

func TestTrustedPNMStoredCatchUpSkipsANewLane(t *testing.T) {
	t.Parallel()
	f := newDenyingReplayFixture(t)
	if _, err := f.node.store.Store("PNM.fbs", f.pnm, f.providerID.String(), nil); err != nil {
		t.Fatal(err)
	}
	materialized, err := f.node.materializeStoredDatasetPublicationPNMs(f.node.ctx, 10)
	if err != nil || materialized != 0 {
		t.Fatalf("stored catch-up = %d, %v; want 0 materialized, no error", materialized, err)
	}
	f.assertNothingImported(t, "stored catch-up")
}

// Listed, the same lane imports through the same path.
func TestTrustedPNMReplayImportsAListedLane(t *testing.T) {
	t.Parallel()
	f := newDenyingReplayFixture(t)
	subscribeForTest(t, f.node, "CAT.fbs", f.tags.ProviderID, f.tags.SourceName, f.providerID)
	materialized, err := f.node.materializeDatasetPublicationPNM(f.node.ctx, "CAT.fbs", f.pnm, f.providerID)
	if err != nil || !materialized {
		t.Fatalf("materialize = %v, %v; want the listed lane imported", materialized, err)
	}
	if n := f.laneRecords(t); n != 1 {
		t.Fatalf("records = %d, want 1", n)
	}
}

