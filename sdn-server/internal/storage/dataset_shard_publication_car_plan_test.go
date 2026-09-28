package storage

import "testing"

func carPlanShards(sizesMiB ...int64) []DatasetShardPublication {
	out := make([]DatasetShardPublication, 0, len(sizesMiB))
	for i, size := range sizesMiB {
		out = append(out, DatasetShardPublication{
			Offset:       i * 1000,
			Limit:        1000,
			RecordCount:  1000 + i,
			ByteCount:    size << 20,
			ShardCID:     "shard-" + string(rune('a'+i)),
			FeedSequence: int64(i + 1),
		})
	}
	return out
}

type carPlanBundle struct{ start, count int }

func planOf(bundles []ShardGroupCARBundle) []carPlanBundle {
	out := make([]carPlanBundle, 0, len(bundles))
	for _, bundle := range bundles {
		out = append(out, carPlanBundle{bundle.SegmentStart, bundle.SegmentCount})
	}
	return out
}

// Only groups of two or more shards get a CAR; a lone shard is its own bundle.
func TestPlanShardGroupCARBundlesSkipsLoneShards(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sizes []int64
		want  []carPlanBundle
	}{
		{"single-shard lane", []int64{3000}, nil},
		{"one small shard", []int64{1}, nil},
		{"two small shards share a bundle", []int64{1, 1}, []carPlanBundle{{0, 2}}},
		{"shards over the bound each stand alone", []int64{700, 700, 700}, nil},
		// 256+256 fits 512; the third starts a group of its own.
		{"a lone tail after a full group", []int64{256, 256, 256}, []carPlanBundle{{0, 2}}},
		// The lone 700 MiB shard keeps its segment index: the bundle after it
		// still names the segments it really covers.
		{"a lone shard between bundles", []int64{1, 1, 700, 1, 1}, []carPlanBundle{{0, 2}, {3, 2}}},
	} {
		got := planOf(PlanShardGroupCARBundles(carPlanShards(tc.sizes...), DefaultShardGroupCARMaxSourceBytes))
		if len(got) != len(tc.want) {
			t.Fatalf("%s: bundles %v, want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: bundles %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}

func TestPlanShardGroupCARBundlesCarriesTheGroupsShardsAndRows(t *testing.T) {
	shards := carPlanShards(1, 1, 700, 1, 1)
	bundles := PlanShardGroupCARBundles(shards, DefaultShardGroupCARMaxSourceBytes)
	if len(bundles) != 2 {
		t.Fatalf("%d bundles, want 2", len(bundles))
	}
	second := bundles[1]
	if len(second.Publications) != 2 || second.Publications[0].ShardCID != "shard-d" || second.Publications[1].ShardCID != "shard-e" {
		t.Fatalf("second bundle carries %+v, want shards d and e", second.Publications)
	}
	if want := int64(shards[3].RecordCount + shards[4].RecordCount); second.Rows != want {
		t.Fatalf("second bundle rows %d, want %d", second.Rows, want)
	}
}

func TestShardGroupCARCoversOneSegment(t *testing.T) {
	for _, tc := range []struct {
		entry    PinLedgerEntry
		segments int
		want     bool
	}{
		{PinLedgerEntry{SegmentCount: 1}, 5, true},
		{PinLedgerEntry{SegmentCount: 2}, 5, false},
		// A legacy bundle without a range covers the whole scope.
		{PinLedgerEntry{SegmentCount: 0}, 1, true},
		{PinLedgerEntry{SegmentCount: 0}, 3, false},
		{PinLedgerEntry{SegmentCount: 0}, 0, false},
	} {
		if got := ShardGroupCARCoversOneSegment(tc.entry, tc.segments); got != tc.want {
			t.Fatalf("SegmentCount %d of %d segments: %v, want %v", tc.entry.SegmentCount, tc.segments, got, tc.want)
		}
	}
}
