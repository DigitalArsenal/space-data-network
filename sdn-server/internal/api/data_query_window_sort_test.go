package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// recordOMM opens one record frame of a query run: the frame prefix, then the
// record as stored (bare, or size-prefixed by its producer).
func recordOMM(frame []byte) *OMM.OMM {
	body := frame[syncwire.FramePrefixLength:]
	if len(body) >= 8 && string(body[8:12]) == "$OMM" {
		return OMM.GetSizePrefixedRootAsOMM(body, 0)
	}
	return OMM.GetRootAsOMM(body, 0)
}

// POST /api/v1/data/query: a window query takes SORT_FIELD EPOCH (oldest
// first) and SORT_DESC (newest first), its page header carries the window,
// and a raw-lane page still refuses a sort (stack #1020).
func TestDataQueryWindowSortsByEpochAndEchoesTheWindow(t *testing.T) {
	store := newDataAPITestStore(t)
	t0 := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	tags := storage.SourceTags{ProviderID: "catalog-inputs", SourceName: "history", BatchID: "days"}
	for day := 0; day < 6; day++ {
		at := t0.Add(-time.Duration(day) * 24 * time.Hour)
		omm := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").
			WithEpoch(at.Format("2006-01-02T15:04:05.000000Z")).WithEpochTimestamp(float64(at.Unix())).Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", omm, "peer-window", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	NewDataQueryHandler(store).RegisterRoutes(mux)
	post := func(q QRPFields) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/data/query", bytes.NewReader(BuildQRP(q)))
		r.Header.Set("Content-Type", StreamContentType)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	from := t0.Add(-4 * 24 * time.Hour)
	window := QRPFields{Kind: QRPKindRequest, SchemaName: "OMM.fbs", ProviderID: "catalog-inputs", SourceName: "history",
		Filters:     []syncwire.QRPFilterField{{Field: "NORAD_CAT_ID", Op: 0, Value: "25544"}},
		FromEpochMs: from.UnixMilli(), ToEpochMs: t0.UnixMilli(), SortField: "EPOCH", Limit: 10}
	read := func(q QRPFields) ([]string, uint64, int64, int64) {
		t.Helper()
		w := post(q)
		if w.Code != http.StatusOK {
			t.Fatalf("window query: %d %s", w.Code, w.Body.String())
		}
		frames, err := SplitFrames(w.Body.Bytes())
		if err != nil || len(frames) == 0 {
			t.Fatalf("frames: %d %v", len(frames), err)
		}
		page, err := ParseQRP(frames[0])
		if err != nil {
			t.Fatal(err)
		}
		var epochs []string
		for _, frame := range frames[1:] {
			epochs = append(epochs, string(recordOMM(frame).EPOCH()))
		}
		return epochs, page.TOTAL_COUNT(), page.FROM_EPOCH_MS(), page.TO_EPOCH_MS()
	}
	day := func(d int) string {
		return t0.Add(-time.Duration(d) * 24 * time.Hour).Format("2006-01-02T15:04:05.000000Z")
	}
	epochs, total, gotFrom, gotTo := read(window)
	if fmt.Sprint(epochs) != fmt.Sprint([]string{day(4), day(3), day(2), day(1)}) || total != 4 {
		t.Fatalf("ascending window: %v, TOTAL_COUNT %d", epochs, total)
	}
	if gotFrom != from.UnixMilli() || gotTo != t0.UnixMilli() {
		t.Fatalf("page header window %d..%d", gotFrom, gotTo)
	}
	desc := window
	desc.SortDesc, desc.Limit = true, 2
	epochs, total, _, _ = read(desc)
	if fmt.Sprint(epochs) != fmt.Sprint([]string{day(1), day(2)}) || total != 4 {
		t.Fatalf("descending window: %v, TOTAL_COUNT %d", epochs, total)
	}
	desc.Page = 2
	epochs, _, _, _ = read(desc)
	if fmt.Sprint(epochs) != fmt.Sprint([]string{day(3), day(4)}) {
		t.Fatalf("descending window page 2: %v", epochs)
	}

	for name, q := range map[string]QRPFields{
		"a raw-lane sort":       {Kind: QRPKindRequest, SchemaName: "OMM.fbs", ProviderID: "catalog-inputs", SourceName: "history", SortField: "EPOCH"},
		"SORT_FIELD OBJECT_ID":  func() QRPFields { q := window; q.SortField = "OBJECT_ID"; return q }(),
		"SORT_DESC on raw lane": {Kind: QRPKindRequest, SchemaName: "OMM.fbs", SortDesc: true},
	} {
		if w := post(q); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
}
