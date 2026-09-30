package storage

// The host-02 format-2 cutover rehearsal's NO-GO findings
// (sdn-format2-cutover-fixes-20260930), each held to format 1's answer.

import (
	"fmt"
	"testing"
	"time"
)

// B4: a tag that selects every live record of its type (host-02's IQC:
// one IQEngine lane holding all 1.1M records) is answered without walking
// the tag's postings: the lanes prove it covers the type, the total is the
// type head's count and the page is the unfiltered page. Each step below
// changes whether the lanes can prove it (an untagged record, a second
// matching lane); every page must equal format 1's either way.
func TestFormat2IndexPagesOfACoveringTag(t *testing.T) {
	requireFormat2Engine(t)
	legacy := reopenDeferred(t, t.TempDir())
	defer legacy.Close()
	f2 := openFormat2ForTest(t, t.TempDir())
	defer f2.Close()

	var cat, omm [][]byte
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 45; i++ {
		cat = append(cat, f2TestCAT(uint32(400+i), fmt.Sprintf("COVER %d", i), "PAYLOAD", "OPERATIONAL"))
		omm = append(omm, f2TestOMM(uint32(26000+i), base.Add(time.Duration(i)*13*time.Minute), fmt.Sprintf("COVER-%d", i)))
	}
	iq := SourceTags{ProviderID: "space-data-network-02", SourceName: "IQEngine", BatchID: "iq-1"}
	pages := []RecordIndexPageQuery{
		{SchemaName: "CAT.fbs", SourceName: "IQEngine", Limit: 10, Offset: 10},
		{SchemaName: "CAT.fbs", SourceName: "IQEngine", ProviderID: "space-data-network-02", Limit: 10},
		{SchemaName: "CAT.fbs", BatchID: "iq-1", Limit: 7, Offset: 21},
		{SchemaName: "CAT.fbs", SourceName: "IQEngine", NoradLike: "41", Limit: 10},
		{SchemaName: "OMM.fbs", SourceName: "IQEngine", Limit: 10, Offset: 5},
		{SchemaName: "OMM.fbs", SourceName: "IQEngine", BatchID: "iq-2", Limit: 50},
		{SchemaName: "CAT.fbs", SourceName: "absent", Limit: 10},
	}
	compare := func(step string, wantCovers map[string]bool) {
		t.Helper()
		for _, q := range pages {
			a, na, err := legacy.RecordIndexPage(q)
			if err != nil {
				t.Fatal(err)
			}
			b, nb, err := f2.RecordIndexPage(q)
			if err != nil {
				t.Fatal(err)
			}
			if na != nb || fmt.Sprint(indexKeys(a)) != fmt.Sprint(indexKeys(b)) {
				t.Errorf("%s %+v: format 2 total %d %v, format 1 total %d %v", step, q, nb, indexKeys(b), na, indexKeys(a))
			}
		}
		for schema, want := range wantCovers {
			covers, n, err := f2.f2TagCovers(f2.f2ctx(), schema, f2TagSpec{source: "IQEngine"})
			if err != nil {
				t.Fatal(err)
			}
			if covers != want {
				t.Errorf("%s: %s source IQEngine covers the type = %v (count %d), want %v", step, schema, covers, n, want)
			}
		}
	}
	for _, s := range []*FlatSQLStore{legacy, f2} {
		if n, err := s.StoreBatchWithSourceTags("CAT.fbs", cat[:40], "source:iqengine", nil, iq); err != nil || n != 40 {
			t.Fatalf("CAT: %d, %v", n, err)
		}
		if n, err := s.StoreBatchWithSourceTags("OMM.fbs", omm[:40], "source:iqengine", nil, iq); err != nil || n != 40 {
			t.Fatalf("OMM: %d, %v", n, err)
		}
	}
	compare("one lane holding every record", map[string]bool{"CAT.fbs": true, "OMM.fbs": true})

	// An untagged record: the lane no longer holds every record.
	for _, s := range []*FlatSQLStore{legacy, f2} {
		if _, err := s.Store("CAT.fbs", cat[40], "16Uiu2HAmRelayPeer", nil); err != nil {
			t.Fatal(err)
		}
	}
	compare("an untagged record", map[string]bool{"CAT.fbs": false, "OMM.fbs": true})

	// A second batch of the source re-tags some records and adds others: two
	// lanes match, and their counts overlap.
	iq2 := iq
	iq2.BatchID = "iq-2"
	for _, s := range []*FlatSQLStore{legacy, f2} {
		if _, err := s.StoreBatchWithSourceTags("OMM.fbs", omm[30:45], "source:iqengine", nil, iq2); err != nil {
			t.Fatal(err)
		}
	}
	compare("two lanes of the source", map[string]bool{"CAT.fbs": false, "OMM.fbs": false})

	// The superseded batch is reconciled away: one lane holds every record again.
	for _, s := range []*FlatSQLStore{legacy, f2} {
		if _, err := s.ReconcileSourceBatch("OMM.fbs", "space-data-network-02", "IQEngine", "iq-2", true); err != nil {
			t.Fatal(err)
		}
	}
	compare("reconciled to one lane", map[string]bool{"CAT.fbs": false, "OMM.fbs": true})
}

func indexKeys(rows []RecordIndexRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s/%v/%v", r.CID, deref64(r.NoradCatID), deref64(r.EpochUnix)))
	}
	return out
}

func deref64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
