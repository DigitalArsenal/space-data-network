package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// The chart lane aggregates EVERY stored record in one pass — well past the
// engine hot window — with derived orbital columns computed on the node and
// the UTC range filter applied before aggregation.
func TestChartAggregatesEveryStoredRecordWithDerivedColumnsAndRanges(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator, storage.WithEngineHotWindow(10))
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	tags := storage.SourceTags{ProviderID: "test", SourceName: "chart", BatchID: "b1", ContentKeyID: "public"}
	// 20 LEO records on 2026-09-01, 10 GEO records on 2026-09-02.
	for i := 0; i < 30; i++ {
		builder := sds.NewOMMBuilder().WithNoradCatID(uint32(40000 + i)).WithObjectName(fmt.Sprintf("CHART-%02d", i)).WithInclination(float64(i))
		if i < 20 {
			builder = builder.WithMeanMotion(15.5).WithEccentricity(0.0005).WithEpoch("2026-09-01T12:00:00Z")
		} else {
			builder = builder.WithMeanMotion(1.0027).WithEccentricity(0.0002).WithEpoch("2026-09-02T12:00:00Z")
		}
		if _, err := store.StoreWithSourceTags("OMM.fbs", builder.Build(), "source:chart", nil, tags); err != nil {
			t.Fatalf("store OMM %d: %v", i, err)
		}
	}
	handler := NewCoreAPIHandler("", nil, nil, nil, store, nil, nil, nil, nil)
	get := func(query string) chartResponse {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/data/chart?"+query, nil)
		res := httptest.NewRecorder()
		handler.handleChart(res, req)
		if res.Code != 200 {
			t.Fatalf("%s -> %d: %s", query, res.Code, res.Body.String())
		}
		var out chartResponse
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	cat := get("schema=OMM&kind=category&x=ORBIT_REGIME")
	if cat.Matched != 30 || cat.Scanned != 30 || cat.Partial {
		t.Fatalf("category matched=%d scanned=%d partial=%v, want all 30", cat.Matched, cat.Scanned, cat.Partial)
	}
	counts := map[string]int64{}
	for _, c := range cat.Categories {
		counts[c.X] = c.Total
	}
	if counts["LEO"] != 20 || counts["GEO"] != 10 {
		t.Fatalf("regime counts = %v, want LEO 20 / GEO 10", counts)
	}

	hist := get("schema=OMM&kind=histogram&x=PERIOD_MIN&bins=4")
	var total int64
	for _, b := range hist.Bins {
		for _, n := range b.Counts {
			total += n
		}
	}
	if total != 30 || len(hist.Bins) != 4 {
		t.Fatalf("period histogram bins=%d total=%d, want 4 bins over 30 records", len(hist.Bins), total)
	}
	if hist.Bins[0].X0 < 90 || hist.Bins[len(hist.Bins)-1].X1 > 1440 {
		t.Fatalf("period range %v..%v outside 90..1440 min", hist.Bins[0].X0, hist.Bins[len(hist.Bins)-1].X1)
	}

	scatter := get("schema=OMM&kind=scatter&x=MEAN_MOTION&y=INCLINATION&split=ORBIT_REGIME")
	if len(scatter.Series) != 2 {
		t.Fatalf("scatter series = %d, want one per regime", len(scatter.Series))
	}
	var points int
	for _, s := range scatter.Series {
		points += len(s.Points)
	}
	if points != 30 {
		t.Fatalf("scatter points = %d, want 30", points)
	}

	ranged := get("schema=OMM&kind=category&x=ORBIT_REGIME&f.EPOCH=2026-09-02..2026-09-03")
	if ranged.Matched != 10 {
		t.Fatalf("range-filtered matched = %d, want the 10 records on 2026-09-02", ranged.Matched)
	}

	timeHist := get("schema=OMM&kind=histogram&x=EPOCH&bins=2")
	if !timeHist.XIsTime || len(timeHist.Bins) != 2 {
		t.Fatalf("epoch histogram xIsTime=%v bins=%d, want a 2-bin time histogram", timeHist.XIsTime, len(timeHist.Bins))
	}
}
