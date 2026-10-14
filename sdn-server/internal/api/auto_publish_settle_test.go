package api

import (
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// A batch stored in parts (the asteroid lane streams 895,910 records in ~16
// MiB parts so the node never holds the whole batch) publishes ONCE, after
// its last part: a lane with a settle time waits for the batch to go quiet
// (multi-body round 2, coordinator D3).
func TestAutoPublisherSettleWaitsForTheLastPart(t *testing.T) {
	service := newRecordingPublicationService()
	settle := 120 * time.Millisecond
	publisher := startAutoPublisher(t, service, []config.AutoPublishLane{
		{Schema: "SBD.fbs", ProviderID: "lowell-astorb", SourceName: "astorb-numbered", Settle: settle},
	})
	part := IngestedBatch{Schema: "SBD.fbs", ProviderID: "lowell-astorb", SourceName: "astorb-numbered", BatchID: "astorb-sha", Inserted: 40000}
	started := time.Now()
	for i := 0; i < 5; i++ {
		publisher.ObserveIngest(part)
		select {
		case <-service.fired:
			t.Fatalf("published after part %d, before the batch went quiet", i+1)
		case <-time.After(settle / 2):
		}
	}
	service.waitForPublications(t, 1)
	if waited := time.Since(started); waited < 3*settle-20*time.Millisecond { // last part at 2x settle, then the settle time
		t.Fatalf("published %s after the first part; want at least the parts plus the settle time", waited)
	}
	if got := service.snapshot(); len(got) != 1 || got[0].BatchID != "astorb-sha" {
		t.Fatalf("publications = %+v, want the one batch", got)
	}
	// A late repeat of the same batch does not republish it.
	publisher.ObserveIngest(part)
	select {
	case <-service.fired:
		t.Fatal("republished a settled batch")
	case <-time.After(2 * settle):
	}
}
