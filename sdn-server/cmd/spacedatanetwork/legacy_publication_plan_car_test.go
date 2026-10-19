package main

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Coverage is judged against the plan: segments the plan bundles must be
// covered, and a lone shard needs no bundle.
func TestVerifyShardGroupCARCoverageFollowsThePlan(t *testing.T) {
	planned := []storage.ShardGroupCARBundle{{SegmentStart: 0, SegmentCount: 2, Rows: 200}}
	if err := verifyShardGroupCARCoverage([]storage.PinLedgerEntry{{SegmentStart: 0, SegmentCount: 2, RowCount: 200}}, planned, 3); err != nil {
		t.Fatalf("a bundle over [0:2] with segment 2 standing alone: %v", err)
	}
	if err := verifyShardGroupCARCoverage(nil, planned, 3); err == nil {
		t.Fatal("a planned bundle with no recorded CAR verified")
	}
	if err := verifyShardGroupCARCoverage([]storage.PinLedgerEntry{{SegmentStart: 0, SegmentCount: 1, RowCount: 100}}, planned, 3); err == nil {
		t.Fatal("a recorded CAR over [0:1] verified a planned bundle over [0:2]")
	}
	if err := verifyShardGroupCARCoverage(nil, nil, 3); err != nil {
		t.Fatalf("a lane of lone shards needs no bundle: %v", err)
	}
}
