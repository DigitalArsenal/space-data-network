package storage

// Lane ranking (security audit SEC-031, verifier S1): a lane's batches rank
// by publish time. A feed sequence numbers the windows of one batch, so it
// may only break ties; ranking by it let an older batch with more windows, or
// one forged high-sequence head, hold the lane against every newer batch.

import (
	"testing"
	"time"
)

func TestLaneBatchesRankByPublishTimeNotFeedSequence(t *testing.T) {
	tmpDir, provider, subscriber := newLatestTestStores(t)
	now := time.Now().UTC().Truncate(time.Second)
	batchA := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-a", 71000, 4, now.Add(-2*time.Hour))
	batchB := publishAndReplicateBatch(t, tmpDir, provider, subscriber, "batch-b", 71010, 2, now.Add(-time.Hour))

	// The old batch gains a high-sequence window row (a large batch's later
	// window, or a forged head's residue). Its publish time stays its own.
	extra := batchA.pub
	extra.Offset = extra.Limit
	extra.FeedSequence = 1 << 16
	extra.FeedHead = ""
	if err := subscriber.UpsertDatasetShardPublication(extra); err != nil {
		t.Fatalf("upsert high-sequence window of batch-a: %v", err)
	}

	newest, _, ok, _, err := subscriber.NewestServableSourceBatch("CAT.fbs", batchB.tags.ProviderID, batchB.tags.SourceName)
	if err != nil {
		t.Fatalf("NewestServableSourceBatch: %v", err)
	}
	if !ok || newest != "batch-b" {
		t.Fatalf("newest servable batch = %q (ok=%v), want batch-b: a higher feed sequence on an older batch must not outrank a newer batch", newest, ok)
	}
	current, older, ok, err := subscriber.OlderLaneBatches("CAT.fbs", batchB.tags.ProviderID, batchB.tags.SourceName)
	if err != nil {
		t.Fatalf("OlderLaneBatches: %v", err)
	}
	if !ok || current != "batch-b" || len(older) != 1 || older[0] != "batch-a" {
		t.Fatalf("lane = current %q older %v, want current batch-b older [batch-a]", current, older)
	}
	_, keep, err := subscriber.RetainNewestSourceBatch("CAT.fbs", batchB.tags.ProviderID, batchB.tags.SourceName, RetainCandidate{})
	if err != nil {
		t.Fatalf("RetainNewestSourceBatch: %v", err)
	}
	if keep != "batch-b" {
		t.Fatalf("retention kept %q, want batch-b", keep)
	}
	if got := countBatchTaggedRecords(t, subscriber, batchB.tags.ProviderID, batchB.tags.SourceName, "batch-b"); got != 2 {
		t.Fatalf("batch-b records after retention = %d, want 2", got)
	}
}
