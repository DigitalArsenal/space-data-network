package storage

import (
	"context"
	"testing"
)

// A batch's publication plan read from its newest record down is the plan
// the lane read gives, at small page sizes, for a batch sharing records with
// an older batch, a lane with several batches, a superseded batch, a batch
// with a deleted record, a batch whose records are held by two peers (one
// sequence, several rows) and a batch stored during the lane read (records
// past the read's snapshot point).
func TestPublicationPlanNewestFirstIsTheLaneRead(t *testing.T) {
	s := openFormat4ForTest(t, t.TempDir())
	defer s.Close()
	b, ok := s.rb.(format4Backend)
	if !ok {
		t.Fatalf("the newest-first plan is format 4's; this store is %T", s.rb)
	}
	sc := newF2Script()
	store := func(recs [][]byte, peer, source, batch string) {
		t.Helper()
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", recs, peer, nil, SourceTags{ProviderID: "p", SourceName: source, BatchID: batch}); err != nil {
			t.Fatal(err)
		}
	}
	head := func() int64 {
		t.Helper()
		h, err := b.f4TypeHead("OMM")
		if err != nil {
			t.Fatal(err)
		}
		return h.Through
	}
	// Lane "shared": b1, then b2 sharing five records with it, then b3.
	store(sc.omm[0:20], "peer-a", "shared", "b1")
	store(sc.omm[15:35], "peer-a", "shared", "b2")
	store(sc.omm[35:50], "peer-a", "shared", "b3")
	// Lane "copies": one batch whose records two peers hold.
	store(sc.omm[50:70], "peer-a", "copies", "c1")
	store(sc.omm[50:70], "peer-b", "copies", "c1")
	// Lane "superseded": s1 superseded by s2.
	store(sc.omm[70:90], "peer-a", "superseded", "s1")
	store(sc.omm[90:110], "peer-a", "superseded", "s2")
	if _, err := s.ReconcileSourceBatch("OMM.fbs", "p", "superseded", "s2", true); err != nil {
		t.Fatal(err)
	}
	// Lane "deletes": a batch with one record deleted.
	store(sc.omm[110:130], "peer-a", "deletes", "d1")
	if err := s.Delete("OMM.fbs", ComputeCID(sc.omm[129])); err != nil {
		t.Fatal(err)
	}
	// Lane "during": w1 stored in two parts, the second after the lane read's
	// snapshot point.
	store(sc.omm[130:150], "peer-a", "during", "w1")
	snapshot := head()
	store(sc.omm[150:170], "peer-a", "during", "w1")
	now := head()

	cases := []struct {
		source, batch string
		through       int64
	}{
		{"shared", "b1", now}, {"shared", "b2", now}, {"shared", "b3", now},
		{"copies", "c1", now},
		{"superseded", "s1", now}, {"superseded", "s2", now},
		{"deletes", "d1", now},
		{"during", "w1", snapshot},
	}
	saved := publicationSetFingerprintPage
	defer func() { publicationSetFingerprintPage = saved }()
	for _, c := range cases {
		for _, page := range []int64{4, 7, 13, 1000} {
			publicationSetFingerprintPage = page
			plan := func(newestFirst bool) (string, int) {
				t.Helper()
				p, err := b.planPublicationThrough(context.Background(), "OMM.fbs", "p", c.source, c.batch, false, c.through, newestFirst)
				if err != nil {
					t.Fatalf("%s/%s page %d newest first %v: %v", c.source, c.batch, page, newestFirst, err)
				}
				defer p.Close()
				return p.Fingerprint()
			}
			lane, laneCount := plan(false)
			newest, newestCount := plan(true)
			if newest != lane || newestCount != laneCount {
				t.Fatalf("%s/%s page %d: newest first %d records %s, the lane read %d records %s",
					c.source, c.batch, page, newestCount, newest, laneCount, lane)
			}
		}
	}
	if _, count := func() (string, int) {
		p, err := b.planPublicationThrough(context.Background(), "OMM.fbs", "p", "during", "w1", false, snapshot, false)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		return p.Fingerprint()
	}(); count != 20 {
		t.Fatalf("the lane read as of the snapshot point holds %d records of w1, want the first part's 20", count)
	}
}
