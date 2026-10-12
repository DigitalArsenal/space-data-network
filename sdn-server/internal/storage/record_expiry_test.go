package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/WXF"
	flatbuffers "github.com/google/flatbuffers/go"
)

// testWXF builds a minimal size-prefixed $WXF: its required FIELD_ID and GRID,
// and a VALID_TIME_MS.
func testWXF(fieldID string, valid time.Time) []byte {
	b := flatbuffers.NewBuilder(128)
	id := b.CreateString(fieldID)
	WXF.WXFGridStart(b)
	WXF.WXFGridAddNLAT(b, 1)
	WXF.WXFGridAddNLON(b, 1)
	grid := WXF.WXFGridEnd(b)
	WXF.WXFStart(b)
	WXF.WXFAddFIELD_ID(b, id)
	WXF.WXFAddGRID(b, grid)
	WXF.WXFAddVALID_TIME_MS(b, uint64(valid.UnixMilli()))
	WXF.FinishSizePrefixedWXFBuffer(b, WXF.WXFEnd(b))
	return b.FinishedBytes()
}

// ExpireRecords deletes one lane's records that arrived before the cutoff -
// every stored record, through the record index - and nothing newer or of
// another lane, on the legacy store and the partition stores alike. Arrival
// is the store's clock (seconds on format 4), so the two arrivals are two
// seconds apart.
func TestExpireRecordsRollingWindow(t *testing.T) {
	base := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	stores := map[string]func(t *testing.T) *FlatSQLStore{
		"legacy":  func(t *testing.T) *FlatSQLStore { return reopenDeferred(t, t.TempDir()) },
		"format2": func(t *testing.T) *FlatSQLStore { requireFormat2Engine(t); return openFormat2ForTest(t, t.TempDir()) },
		"format4": func(t *testing.T) *FlatSQLStore { requireFormat2Engine(t); return openFormat4ForTest(t, t.TempDir()) },
	}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()
			wxf := func(prefix string, from, n int) [][]byte {
				var out [][]byte
				for h := from; h < from+n; h++ {
					out = append(out, testWXF(fmt.Sprintf("%s:f%03d:t2m:s4:26:6", prefix, h), base.Add(time.Duration(h)*time.Hour)))
				}
				return out
			}
			store := func(lane string, recs [][]byte, batch string) {
				t.Helper()
				if n, err := s.StoreBatchWithSourceTags("WXF.fbs", recs, "source:"+lane, nil, SourceTags{ProviderID: "noaa-ncep", SourceName: lane, BatchID: batch}); err != nil || n != len(recs) {
					t.Fatalf("store %s %s: n %d err %v", lane, batch, n, err)
				}
			}
			store("gfs-0p25-sea", wxf("gfs:old", 0, 5), "b-old")
			store("other-lane", wxf("other", 0, 3), "b-other")
			time.Sleep(2100 * time.Millisecond)
			cutoff := time.Now().Add(-time.Second).Unix()
			store("gfs-0p25-sea", wxf("gfs:new", 0, 4), "b-new")

			if _, err := s.ExpireRecords(RecordExpiryQuery{SchemaName: "WXF.fbs", BeforeUnix: cutoff}); err == nil {
				t.Fatal("an expiry naming no lane must be refused")
			}
			old, n, err := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: "WXF.fbs", SourceName: "gfs-0p25-sea", ArrivedBefore: &cutoff, Limit: 50})
			if err != nil || n != 5 || len(old) != 5 {
				t.Fatalf("records that arrived before the cutoff: %d listed (total %d) err %v; want 5", len(old), n, err)
			}
			res, err := s.ExpireRecords(RecordExpiryQuery{SchemaName: "WXF.fbs", SourceName: "gfs-0p25-sea", BeforeUnix: cutoff, Batch: 2})
			if err != nil {
				t.Fatal(err)
			}
			if res.Deleted != 5 || res.Capped {
				t.Fatalf("expiry = %+v; want the 5 older arrivals deleted, not capped", res)
			}
			if _, total, err := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: "WXF.fbs", SourceName: "gfs-0p25-sea", BatchID: "b-new", Limit: 50}); err != nil || total != 4 {
				t.Fatalf("newer arrivals after expiry: total %d err %v; want 4", total, err)
			}
			if _, total, _ := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: "WXF.fbs", SourceName: "gfs-0p25-sea", Limit: 50}); total != 4 {
				t.Errorf("sea lane after expiry: %d records; want 4", total)
			}
			if _, n, _ := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: "WXF.fbs", SourceName: "other-lane", Limit: 50}); n != 3 {
				t.Errorf("other lane: %d records; want its 3 untouched", n)
			}
			for _, r := range old {
				if rec, err := s.GetRawRecord("WXF.fbs", r.CID); err == nil && rec != nil {
					t.Errorf("record %s still stored after expiry", r.CID)
				}
			}
			if again, _ := s.ExpireRecords(RecordExpiryQuery{SchemaName: "WXF.fbs", SourceName: "gfs-0p25-sea", BeforeUnix: cutoff}); again.Deleted != 0 {
				t.Errorf("a second pass deleted %d; want 0", again.Deleted)
			}
		})
	}
}
