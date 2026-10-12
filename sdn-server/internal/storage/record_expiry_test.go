package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/WXF"
	flatbuffers "github.com/google/flatbuffers/go"
)

// testWXF builds a minimal size-prefixed $WXF: a FIELD_ID and a VALID_TIME_MS.
func testWXF(fieldID string, valid time.Time) []byte {
	b := flatbuffers.NewBuilder(128)
	id := b.CreateString(fieldID)
	WXF.WXFStart(b)
	WXF.WXFAddFIELD_ID(b, id)
	WXF.WXFAddVALID_TIME_MS(b, uint64(valid.UnixMilli()))
	WXF.FinishSizePrefixedWXFBuffer(b, WXF.WXFEnd(b))
	return b.FinishedBytes()
}

// A WXF record is indexed by its valid time (seconds, floored) and FIELD_ID.
func TestWXFIndexedByValidTime(t *testing.T) {
	valid := time.Date(2026, 10, 6, 12, 0, 0, 750_000_000, time.UTC)
	f, err := extractIndexedFields("WXF.fbs", testWXF("gfs:2026100606:f006:t2m:s4:26:6", valid))
	if err != nil {
		t.Fatal(err)
	}
	if f.epochUnix == nil || *f.epochUnix != valid.Unix() || f.epochDay != "2026-10-06" || f.entityID != "gfs:2026100606:f006:t2m:s4:26:6" {
		t.Fatalf("WXF index fields = epoch %v day %q entity %q; want %d 2026-10-06 the FIELD_ID", f.epochUnix, f.epochDay, f.entityID, valid.Unix())
	}
}

// ExpireRecords deletes one lane's records older than the cutoff - every
// stored record, through the record index - and nothing of another lane, on
// the legacy store and the partition stores alike.
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
			var sea, other [][]byte
			for h := 0; h < 10; h++ {
				sea = append(sea, testWXF(fmt.Sprintf("gfs:2026100600:f%03d:t2m:s4:26:6", h), base.Add(time.Duration(h)*time.Hour)))
			}
			for h := 0; h < 3; h++ {
				other = append(other, testWXF(fmt.Sprintf("other:f%03d", h), base.Add(time.Duration(h)*time.Hour)))
			}
			if n, err := s.StoreBatchWithSourceTags("WXF.fbs", sea, "source:gfs", nil, SourceTags{ProviderID: "noaa-ncep", SourceName: "gfs-0p25-sea", BatchID: "b-sea"}); err != nil || n != 10 {
				t.Fatalf("store sea: n %d err %v", n, err)
			}
			if n, err := s.StoreBatchWithSourceTags("WXF.fbs", other, "source:other", nil, SourceTags{ProviderID: "noaa-ncep", SourceName: "other-lane", BatchID: "b-other"}); err != nil || n != 3 {
				t.Fatalf("store other: n %d err %v", n, err)
			}
			if _, err := s.ExpireRecords(RecordExpiryQuery{SchemaName: "WXF.fbs"}); err == nil {
				t.Fatal("an expiry naming no lane must be refused")
			}
			cutoff := base.Add(5 * time.Hour).Unix()
			old, n, err := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: "WXF.fbs", SourceName: "gfs-0p25-sea", EpochBefore: &cutoff, Limit: 50})
			if err != nil || n != 5 || len(old) != 5 {
				t.Fatalf("records before the cutoff: %d listed (total %d) err %v; want 5", len(old), n, err)
			}
			res, err := s.ExpireRecords(RecordExpiryQuery{SchemaName: "WXF.fbs", SourceName: "gfs-0p25-sea", BeforeUnix: cutoff, Batch: 2})
			if err != nil {
				t.Fatal(err)
			}
			if res.Deleted != 5 || res.Capped {
				t.Fatalf("expiry = %+v; want 5 deleted (hours 0-4), not capped", res)
			}
			left, total, err := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: "WXF.fbs", SourceName: "gfs-0p25-sea", Limit: 50})
			if err != nil || total != 5 {
				t.Fatalf("sea lane after expiry: total %d err %v; want 5", total, err)
			}
			for _, r := range left {
				if r.EpochUnix == nil || *r.EpochUnix < cutoff {
					t.Errorf("record %s kept with epoch %v, before the cutoff %d", r.CID, r.EpochUnix, cutoff)
				}
			}
			if _, n, _ := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: "WXF.fbs", SourceName: "other-lane", Limit: 50}); n != 3 {
				t.Errorf("other lane: %d records; want its 3 untouched", n)
			}
			for _, r := range old {
				if rec, err := s.GetRawRecord("WXF.fbs", r.CID); err == nil && rec != nil {
					t.Errorf("record %s (epoch %v) still stored after expiry", r.CID, r.EpochUnix)
				}
			}
			if again, _ := s.ExpireRecords(RecordExpiryQuery{SchemaName: "WXF.fbs", SourceName: "gfs-0p25-sea", BeforeUnix: cutoff}); again.Deleted != 0 {
				t.Errorf("a second pass deleted %d; want 0", again.Deleted)
			}
		})
	}
}
