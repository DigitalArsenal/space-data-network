package storage

import "testing"

// A format-4 dry-run reconcile (the count a "current" ingest takes before its
// batch lands) answers from the feed's lane counters, and that answer is the
// engine's count-only SUPERSEDE's tag count for every keep: several batches,
// a batch held by two producers, a content key, copies across peers, a
// re-tagged record and a keep the feed does not hold.
func TestFormat4DryRunReconcileIsTheEnginesCount(t *testing.T) {
	s := openFormat4ForTest(t, t.TempDir())
	defer s.Close()
	b, ok := s.rb.(format4Backend)
	if !ok {
		t.Skip("not a format-4 store")
	}
	sc := newF2Script()
	store := func(recs [][]byte, peer string, tags SourceTags) {
		t.Helper()
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", recs, peer, nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	store(sc.omm[0:20], "peer-a", SourceTags{ProviderID: "p", SourceName: "s", BatchID: "b1"})
	store(sc.omm[15:35], "peer-b", SourceTags{ProviderID: "p", SourceName: "s", BatchID: "b2"})
	store(sc.omm[30:45], "peer-a", SourceTags{ProviderID: "p", SourceName: "s", BatchID: "b3", ContentKeyID: "k1"})
	store(sc.omm[40:50], "peer-c", SourceTags{ProviderID: "p", SourceName: "s", BatchID: "b3"})
	store(sc.omm[0:10], "peer-a", SourceTags{ProviderID: "p", SourceName: "other", BatchID: "b1"})
	store(sc.omm[50:55], "peer-a", SourceTags{ProviderID: "q", SourceName: "s", BatchID: "b1"})
	if err := s.UpsertSourceTags("OMM.fbs", ComputeCID(sc.omm[3]), SourceTags{ProviderID: "p", SourceName: "s", BatchID: "b2"}); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"b1", "b2", "b3", "none-held"} {
		engine, err := b.d.api().Supersede(b.d.ctx, "OMM", "p", "s", keep, false)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.ReconcileSourceBatch("OMM.fbs", "p", "s", keep, false)
		if err != nil {
			t.Fatal(err)
		}
		if got.Matched != engine.TagsDeleted || got.Matched == 0 || got.Deleted != 0 {
			t.Fatalf("keep %s: dry run %+v, the engine's count %d tags", keep, got, engine.TagsDeleted)
		}
	}
	// After the apply the other batches are gone and the dry run finds none.
	if _, err := s.ReconcileSourceBatch("OMM.fbs", "p", "s", "b3", true); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReconcileSourceBatch("OMM.fbs", "p", "s", "b3", false); err != nil || got.Matched != 0 {
		t.Fatalf("dry run after the apply: %+v %v", got, err)
	}
}
