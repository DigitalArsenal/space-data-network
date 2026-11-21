package node

// A followed lane whose stream holds the producer's current batch does not
// fetch its publications: their records arrive on the stream. A gateway host
// (host-01) whose readers fetch the producer's manifests and shards from it
// then holds none of them, and the console's reads of the producer's newest
// batch failed ("the provider node no longer holds the published files",
// 2026-11-21). A subscription that keeps its publications fetches and
// imports each one, as 1.1.9 imported every publication; the flag is kept in
// the subscription's SYNC_FILTER across restarts.

import (
	"context"
	"testing"
)

// streamFixtureLane marks the fixture's subscription as streaming: its
// stream is connected and holds the producer's current batch.
func (f *replayNodeFixture) streamFixtureLane(t *testing.T, keep bool) {
	t.Helper()
	key := normalizeFollowKey(FollowKey{Schema: "CAT.fbs", ProviderID: f.tags.ProviderID, SourceName: f.tags.SourceName, Producer: f.providerID.String()})
	f.node.follows.mu.Lock()
	defer f.node.follows.mu.Unlock()
	e := f.node.follows.subs[key]
	if e == nil {
		t.Fatal("the fixture's lane is not subscribed")
	}
	e.status.Connected, e.batchHeld, e.settled = true, true, true
	e.status.KeepPublications = keep
}

func TestStreamedLanePublicationNotFetched(t *testing.T) {
	t.Parallel()
	f := newReplayNodeFixture(t, true)
	f.streamFixtureLane(t, false)
	materialized, err := f.node.materializeDatasetPublicationPNM(f.node.ctx, "CAT.fbs", f.pnm, f.providerID)
	if err != nil || materialized {
		t.Fatalf("materialize = %v, %v; want a streamed lane's publication passed over", materialized, err)
	}
	if shard, idx := f.requestsFor(f.export.ShardCID), f.requestsFor(f.export.IndexCID); shard != 0 || idx != 0 {
		t.Fatalf("a streamed lane fetched its publication: %d shard and %d index requests", shard, idx)
	}
}

func TestStreamedLaneKeepsItsPublications(t *testing.T) {
	t.Parallel()
	f := newReplayNodeFixture(t, true)
	f.streamFixtureLane(t, true)
	materialized, err := f.node.materializeDatasetPublicationPNM(f.node.ctx, "CAT.fbs", f.pnm, f.providerID)
	if err != nil || !materialized {
		t.Fatalf("materialize = %v, %v; want a lane that keeps its publications to import this one", materialized, err)
	}
	if shard, idx := f.requestsFor(f.export.ShardCID), f.requestsFor(f.export.IndexCID); shard == 0 || idx == 0 {
		t.Fatalf("the kept publication was not fetched: %d shard and %d index requests", shard, idx)
	}
	if n := f.laneRecords(t); n != 1 {
		t.Fatalf("records = %d, want 1", n)
	}
	if state := f.replayState(t); state == "" {
		t.Fatal("no replay state for the imported publication")
	}
}

// Turning keep on for a running lane reads again a publication the node
// passed over while it streamed: the 24-hour mark of the declined PNM goes.
func TestKeepPublicationsReadsAPassedOverPublication(t *testing.T) {
	// Not parallel: the declined-PNM marks it clears are the package's.
	f := newReplayNodeFixture(t, true)
	f.streamFixtureLane(t, false)
	if materialized, err := f.node.materializeDatasetPublicationPNM(f.node.ctx, "CAT.fbs", f.pnm, f.providerID); err != nil || materialized {
		t.Fatalf("streamed: materialize = %v, %v", materialized, err)
	}
	key := FollowKey{Schema: "CAT.fbs", ProviderID: f.tags.ProviderID, SourceName: f.tags.SourceName, Producer: f.providerID.String()}
	if _, err := f.node.follows.Subscribe(context.Background(), key, FollowOptions{Publications: FollowPublicationsKeep}); err != nil {
		t.Fatalf("subscribe --publications keep on the followed lane: %v", err)
	}
	if !f.node.followKeepsPublications(f.providerID, "CAT.fbs", f.tags.ProviderID, f.tags.SourceName) {
		t.Fatal("the lane does not keep its publications after --publications keep")
	}
	materialized, err := f.node.materializeDatasetPublicationPNM(f.node.ctx, "CAT.fbs", f.pnm, f.providerID)
	if err != nil || !materialized {
		t.Fatalf("after keep: materialize = %v, %v; want the passed-over publication imported", materialized, err)
	}
	if n := f.laneRecords(t); n != 1 {
		t.Fatalf("records = %d, want 1", n)
	}
	if _, err := f.node.follows.Subscribe(context.Background(), key, FollowOptions{Publications: "everything"}); err == nil {
		t.Fatal("an unknown publications value was accepted")
	}
}

// The flag survives a restart (the subscriptions file), and a build that
// does not know it holds the subscription without running it.
func TestKeepPublicationsSurvivesARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	n := &Node{}
	m := newFollowManager(n, dir)
	n.follows = m
	key := normalizeFollowKey(FollowKey{Schema: "OMM.fbs", ProviderID: "space-data-network-02", SourceName: "celestrak-gp", Producer: "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"})
	m.subs[key] = &followEntry{status: FollowStatus{FollowKey: key, Start: FollowStartCurrent, Cursor: "c1", KeepPublications: true}, cancel: func() {}, done: closedChan()}
	other := key
	other.SourceName = "celestrak-satcat"
	m.subs[other] = &followEntry{status: FollowStatus{FollowKey: other, Start: FollowStartCurrent, Cursor: "c2"}, cancel: func() {}, done: closedChan()}
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	subs, err := readFollowFile(m.subsPath)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]FollowStatus{}
	for _, s := range subs {
		got[s.SourceName] = s
	}
	if !got["celestrak-gp"].KeepPublications || got["celestrak-gp"].Refused != "" {
		t.Fatalf("celestrak-gp read back as %+v; want it to keep its publications", got["celestrak-gp"])
	}
	if got["celestrak-satcat"].KeepPublications {
		t.Fatal("celestrak-satcat read back keeping its publications")
	}
	if f := followFilter(got["celestrak-gp"]); f != "publications=keep&start=current" {
		t.Fatalf("SYNC_FILTER = %q", f)
	}
	var st FollowStatus
	if err := parseFollowFilter("publications=all&start=current", &st); err == nil {
		t.Fatal("publications=all read as a SYNC_FILTER")
	}
}
