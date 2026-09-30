package storage

// The host-02 format-2 cutover rehearsal's NO-GO findings
// (sdn-format2-cutover-fixes-20260930), each held to format 1's answer.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

// B2: a batch reports each record the engine refused to its caller, after
// storing the rest. The batch API returned nil for a partly refused batch,
// so the publish API reported a refused record stored (HTTP 201 with its
// CID) and logged a PLOG entry for a CID that was never stored.
func TestFormat2BatchReportsEachRefusedRecord(t *testing.T) {
	requireFormat2Engine(t)
	f2 := openFormat2ForTest(t, t.TempDir())
	defer f2.Close()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	good := f2TestOMM(26100, base, "REFUSED-NEIGHBOUR")
	other := f2TestOMM(26101, base.Add(time.Minute), "REFUSED-OTHER")
	bad := append([]byte(nil), f2TestOMM(26102, base.Add(2*time.Minute), "REFUSED")...)
	copy(bad[4:8], []byte{0, 0, 0, 0}) // no file identifier: the engine refuses it
	tags := SourceTags{ProviderID: "space-data-network-02", SourceName: "refusals", BatchID: "r-1"}
	n, err := f2.StoreBatchWithSourceTags("OMM.fbs", [][]byte{good, bad, other}, "source:refusals", nil, tags)
	var refused *RefusedRecordsError
	if !errors.As(err, &refused) {
		t.Fatalf("StoreBatchWithSourceTags with a refused record: n %d, err %v; want a *RefusedRecordsError", n, err)
	}
	badCID := ComputeCID(bad)
	if n != 2 || len(refused.Order) != 1 || refused.Order[0] != badCID || refused.Refused[badCID] == nil || refused.Records != 3 {
		t.Fatalf("n %d, refused %+v; want 2 stored and %s refused", n, refused, badCID)
	}
	for _, c := range []string{ComputeCID(good), ComputeCID(other)} {
		if _, err := f2.GetRecord("OMM.fbs", c); err != nil {
			t.Fatalf("stored record %s: %v", c, err)
		}
	}
	if _, err := f2.GetRecord("OMM.fbs", badCID); err == nil {
		t.Fatalf("refused record %s is readable", badCID)
	}
	// Every record refused: the same error, nothing stored.
	n, err = f2.StoreBatch("OMM.fbs", [][]byte{bad}, "source:refusals", nil)
	if !errors.As(err, &refused) || n != 0 || len(refused.Order) != 1 {
		t.Fatalf("a batch of one refused record: n %d, err %v", n, err)
	}
}

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
			proof := f2.f2TagCovers(f2.f2ctx(), schema, f2TagSpec{source: "IQEngine"})
			if proof.covers != want {
				t.Errorf("%s: %s source IQEngine covers the type = %v (count %d), want %v", step, schema, proof.covers, proof.all, want)
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

// B4 (cutover review): a page read with its tag elided is checked when the
// type moved after the proof. An untagged record labeled between the proof
// and the read would otherwise show on a source-filtered page.
func TestFormat2ElidedTagPageIsCheckedWhenTheTypeMoved(t *testing.T) {
	requireFormat2Engine(t)
	f2 := openFormat2ForTest(t, t.TempDir())
	defer f2.Close()
	var cat [][]byte
	for i := 0; i < 30; i++ {
		cat = append(cat, f2TestCAT(uint32(700+i), fmt.Sprintf("ELIDED %d", i), "PAYLOAD", "OPERATIONAL"))
	}
	iq := SourceTags{ProviderID: "space-data-network-02", SourceName: "IQEngine", BatchID: "iq-1"}
	if _, err := f2.StoreBatchWithSourceTags("CAT.fbs", cat, "source:iqengine", nil, iq); err != nil {
		t.Fatal(err)
	}
	ctx := f2.f2ctx()
	tag := f2TagSpec{source: "IQEngine"}
	proof := f2.f2TagCovers(ctx, "CAT.fbs", tag)
	if !proof.covers {
		t.Fatal("one lane holding every record: the lanes do not prove it covers the type")
	}
	untagged := f2TestCAT(799, "UNTAGGED", "PAYLOAD", "OPERATIONAL")
	if _, err := f2.Store("CAT.fbs", untagged, "16Uiu2HAmRelayPeer", nil); err != nil {
		t.Fatal(err)
	}
	q := format2.WindowQuery{Schema: "CAT.fbs", Source: "IQEngine", Order: "cid", Limit: 100}
	rows, err := f2.f2IndexWindow(ctx, q, tag, proof, func(q format2.WindowQuery) ([]format2.WindowRow, error) {
		return f2.ps.WindowColumns(ctx, q, "NULL")
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(cat) {
		t.Fatalf("%d rows, want the %d IQEngine records", len(rows), len(cat))
	}
	for _, r := range rows {
		if r.Rec.CID == ComputeCID(untagged) {
			t.Fatal("the untagged record labeled after the proof is on the IQEngine page")
		}
	}
}

// B4: a CID-ordered tag window read in the type's CID order and filtered by
// tag (f2ScanTagByCID) equals format 1's page, for a tag held by a record's
// FIRST copy, by a REPEAT copy only, and by both.
func TestFormat2CIDOrderTagScanEqualsFormat1(t *testing.T) {
	requireFormat2Engine(t)
	legacy := reopenDeferred(t, t.TempDir())
	defer legacy.Close()
	f2 := openFormat2ForTest(t, t.TempDir())
	defer f2.Close()
	var cat [][]byte
	for i := 0; i < 60; i++ {
		cat = append(cat, f2TestCAT(uint32(800+i), fmt.Sprintf("SCAN %d", i), "PAYLOAD", "OPERATIONAL"))
	}
	a := SourceTags{ProviderID: "space-data-network-02", SourceName: "scan-a", BatchID: "a-1"}
	b := SourceTags{ProviderID: "space-data-network-01", SourceName: "scan-b", BatchID: "b-1"}
	for _, s := range []*FlatSQLStore{legacy, f2} {
		if _, err := s.StoreBatchWithSourceTags("CAT.fbs", cat[:40], "source:scan-a", nil, a); err != nil {
			t.Fatal(err)
		}
		// Records 30..59 under scan-b from another producer: 30..39 are
		// REPEAT copies there.
		if _, err := s.StoreBatchWithSourceTags("CAT.fbs", cat[30:], "source:scan-b", nil, b); err != nil {
			t.Fatal(err)
		}
	}
	ctx := f2.f2ctx()
	cols := func(q format2.WindowQuery) ([]format2.WindowRow, error) { return f2.ps.WindowColumns(ctx, q, "NULL") }
	for _, source := range []string{"scan-a", "scan-b"} {
		for _, page := range [][2]int{{0, 7}, {5, 7}, {26, 9}, {33, 10}} {
			want, _, err := legacy.RecordIndexPage(RecordIndexPageQuery{SchemaName: "CAT.fbs", SourceName: source, Offset: page[0], Limit: page[1]})
			if err != nil {
				t.Fatal(err)
			}
			q := format2.WindowQuery{Schema: "CAT.fbs", Source: source, Order: "cid", Offset: page[0], Limit: page[1]}
			got, ok, err := f2.f2ScanTagByCID(ctx, q, f2TagSpec{source: source}, cols, 1<<20)
			if err != nil || !ok {
				t.Fatalf("%s %v: %v %v", source, page, ok, err)
			}
			var g, w []string
			for _, r := range got {
				g = append(g, r.Rec.CID)
			}
			for _, r := range want {
				w = append(w, r.CID)
			}
			if fmt.Sprint(g) != fmt.Sprint(w) {
				t.Errorf("%s offset %d limit %d: scan %v, format 1 %v", source, page[0], page[1], g, w)
			}
		}
	}
}
