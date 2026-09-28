package storage

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// dataset_publication_series_test.go — retention planning over a lane written
// before series existed (graph: sdn-publication-hygiene-20260928). No kubo:
// the plan is computed from publication rows, the pin ledger and the store.

const seriesTestPeer = "16Uiu2HAmSelfPeer"

func seriesTestLane() DatasetPublicationLane {
	return DatasetPublicationLane{SchemaName: "IQC.fbs", ProviderID: identityTestProvider, SourceName: identityTestSource}
}

func seriesTestRow(t *testing.T, s *FlatSQLStore, batch string, at int64) DatasetShardPublication {
	t.Helper()
	pub := DatasetShardPublication{
		SchemaName: "IQC.fbs", ProviderID: identityTestProvider, SourceName: identityTestSource, BatchID: batch,
		QueryProfile: DatasetPublicationQueryProfile, Offset: 0, Limit: 50000, RecordCount: 10, ByteCount: 100,
		ShardCID: "shard-" + batch, IndexCID: "index-" + batch, ManifestCID: "manifest-" + batch,
		ShardSHA256: fmt.Sprintf("%064d", at), IndexSHA256: fmt.Sprintf("%064d", at+1), QuerySHA256: fmt.Sprintf("%064d", at+2),
		PublishedAt: time.Unix(at, 0).UTC(),
	}
	if err := s.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatal(err)
	}
	for role, cid := range map[string]string{PinLedgerRoleShard: pub.ShardCID, PinLedgerRoleIndex: pub.IndexCID, PinLedgerRoleManifest: pub.ManifestCID, PinLedgerRoleShardGroupCAR: "car-" + batch} {
		seriesTestPin(t, s, "IQC.fbs", identityTestSource, batch, role, cid, at)
	}
	return pub
}

func seriesTestPin(t *testing.T, s *FlatSQLStore, schema, source, batch, role, cid string, at int64) {
	t.Helper()
	if err := s.UpsertPinLedgerEntry(PinLedgerEntry{
		CID: cid, SchemaName: schema, ProviderPeerID: seriesTestPeer, ProviderID: identityTestProvider, SourceName: source,
		BatchID: batch, QueryProfile: DatasetPublicationQueryProfile, Role: role, ByteCount: 100,
		VerificationState: "verified", VerifiedAt: time.Unix(at, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

func planCIDs(entries []PinLedgerEntry) []string {
	var out []string
	for _, entry := range entries {
		out = append(out, entry.CID)
	}
	sort.Strings(out)
	return out
}

func TestRetentionPlanAdoptsLegacyRowsAndRespectsBatchSemantics(t *testing.T) {
	s := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	defer s.Close()
	lane := seriesTestLane()

	seriesTestRow(t, s, "b1", 1000)
	seriesTestRow(t, s, "b2", 2000)
	seriesTestRow(t, s, "b3", 3000)
	// Windows b3 overwrote: pinned, named by no row.
	seriesTestPin(t, s, "IQC.fbs", identityTestSource, "b3", PinLedgerRoleShard, "shard-b3-overwritten", 2500)
	seriesTestPin(t, s, "IQC.fbs", identityTestSource, "b3", PinLedgerRoleIndex, "index-b3-overwritten", 2500)
	// b2's shard is also another lane's pin: it must stay pinned.
	seriesTestPin(t, s, "IQC.fbs", "another-source", "x", PinLedgerRoleShard, "shard-b2", 2000)
	// The store still holds b1 (a part of the dataset); b2 and b3 hold nothing.
	if _, err := s.StoreBatchWithSourceTags("IQC.fbs", [][]byte{buildIdentityTestIQC(1, "2026-09-15T00:00:00Z", "held")}, "module:sigmf", nil, identityTestTags("b1")); err != nil {
		t.Fatal(err)
	}

	policy := DatasetPublicationRetentionPolicy{KeepSeries: 1}
	if plan, err := s.PlanDatasetPublicationRetention(lane, seriesTestPeer, policy); err != nil || len(plan.Unpin)+len(plan.RetireRows) != 0 {
		t.Fatalf("a lane with no series must plan nothing: %+v %v", plan, err)
	}
	dry, err := s.PlanDatasetPublicationRetentionWithAdoption(lane, seriesTestPeer, policy)
	if err != nil {
		t.Fatal(err)
	}
	if series, _ := s.ListDatasetPublicationSeries(lane); len(series) != 0 {
		t.Fatalf("the dry run wrote %d series", len(series))
	}
	if want := []string{"legacy:b3", "legacy:b1"}; fmt.Sprint(dry.KeptSeries) != fmt.Sprint(want) {
		t.Fatalf("kept %v, want %v (newest, plus the part the store still holds)", dry.KeptSeries, want)
	}
	if fmt.Sprint(dry.RetiredSeries) != "[legacy:b2]" {
		t.Fatalf("retired %v, want [legacy:b2] (its batch left the store)", dry.RetiredSeries)
	}
	if got, want := planCIDs(dry.Unpin), []string{"car-b2", "index-b2", "index-b3-overwritten", "manifest-b2", "shard-b3-overwritten"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("unpin %v, want %v", got, want)
	}
	if got := planCIDs(dry.Shared); fmt.Sprint(got) != "[shard-b2]" {
		t.Fatalf("shared %v, want [shard-b2]", got)
	}
	if len(dry.RetireRows) != 1 || dry.RetireRows[0].BatchID != "b2" {
		t.Fatalf("retire rows %+v, want b2's", dry.RetireRows)
	}

	adopted, err := s.AdoptDatasetPublicationSeries(lane, seriesTestPeer)
	if err != nil || adopted != 3 {
		t.Fatalf("adopted %d (err %v), want 3", adopted, err)
	}
	plan, err := s.PlanDatasetPublicationRetention(lane, seriesTestPeer, policy)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(planCIDs(plan.Unpin)) != fmt.Sprint(planCIDs(dry.Unpin)) || fmt.Sprint(plan.RetiredSeries) != fmt.Sprint(dry.RetiredSeries) {
		t.Fatalf("applied plan %v / %v differs from the dry run %v / %v", planCIDs(plan.Unpin), plan.RetiredSeries, planCIDs(dry.Unpin), dry.RetiredSeries)
	}

	unpinned := map[string]bool{}
	for _, entry := range plan.Unpin {
		unpinned[entry.CID] = true
	}
	result, err := s.ApplyDatasetPublicationRetention(plan, seriesTestPeer, unpinned, t.TempDir(), time.Unix(4000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if result.Unpinned != 5 || result.SharedRetained != 1 || result.RowsDeleted != 1 || result.RetiredSeries != 1 {
		t.Fatalf("apply = %+v", result)
	}
	rows, _ := s.ListDatasetShardPublications(DatasetShardPublicationQuery{SchemaName: "IQC.fbs", ProviderID: identityTestProvider, SourceName: identityTestSource, QueryProfile: DatasetPublicationQueryProfile})
	if len(rows) != 2 {
		t.Fatalf("%d rows advertised after the pass, want 2 (b1, b3)", len(rows))
	}
	retired, _ := s.ListPinLedgerEntries(PinLedgerQuery{SchemaName: "IQC.fbs", SourceName: identityTestSource, VerificationState: PinLedgerStateRetired})
	if got := planCIDs(retired); fmt.Sprint(got) != "[car-b2 index-b2 index-b3-overwritten manifest-b2 shard-b2 shard-b3-overwritten]" {
		t.Fatalf("retired ledger entries %v", got)
	}
	other, _ := s.ListPinLedgerEntries(PinLedgerQuery{CID: "shard-b2", SourceName: "another-source", VerificationState: "verified"})
	if len(other) != 1 {
		t.Fatal("another lane's pin entry was retired")
	}

	// Declared snapshots: b1 is superseded by the newer editions too.
	snap, err := s.PlanDatasetPublicationRetention(lane, seriesTestPeer, DatasetPublicationRetentionPolicy{KeepSeries: 1, SnapshotBatches: true})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(snap.RetiredSeries) != "[legacy:b1]" || len(snap.RetireRows) != 1 || snap.RetireRows[0].BatchID != "b1" {
		t.Fatalf("snapshot plan retired %v rows %+v, want b1", snap.RetiredSeries, snap.RetireRows)
	}
	// keep 0 = archive everything.
	if none, _ := s.PlanDatasetPublicationRetention(lane, seriesTestPeer, DatasetPublicationRetentionPolicy{KeepSeries: 0, SnapshotBatches: true}); len(none.Unpin)+len(none.RetireRows) != 0 {
		t.Fatalf("keep 0 planned releases: %+v", none)
	}
}
