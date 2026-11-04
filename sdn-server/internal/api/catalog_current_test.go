package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OCM"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// catalogTestSpaceWeather is the AP set's daily rows: DATE, F107_OBS,
// F107_OBS_CENTER81, AP_AVG, F107_DATA_TYPE.
var catalogTestSpaceWeather = []struct {
	date      string
	f107, c81 float32
	ap        int32
	kind      OCM.F107DataType
}{
	{"2026-11-02", 151.2, 148.9, 9, OCM.F107DataTypeOBS},
	{"2026-11-03", 149.8, 149.1, 12, OCM.F107DataTypeOBS},
	{"2026-11-04", 150.0, 149.3, 10, OCM.F107DataTypePRD},
}

// buildCatalogTestOCM writes one $OCM element set of a theory; the AP set
// carries the daily space-weather rows in PERTURBATIONS.SPACE_WEATHER.
func buildCatalogTestOCM(norad uint32, theory string, epoch time.Time) []byte {
	b := flatbuffers.NewBuilder(1024)
	orig := b.CreateString("catalog-current-test")
	OCM.HeaderStart(b)
	OCM.HeaderAddORIGINATOR(b, orig)
	header := OCM.HeaderEnd(b)
	design := b.CreateString("1998-067A")
	OCM.MetadataStart(b)
	OCM.MetadataAddINTERNATIONAL_DESIGNATOR(b, design)
	OCM.MetadataAddNORAD_CAT_ID(b, norad)
	meta := OCM.MetadataEnd(b)
	ep := b.CreateString(epoch.Format("2006-01-02T15:04:05.000000Z"))
	OCM.OCMMeanElementsStart(b)
	OCM.OCMMeanElementsAddMEAN_ELEMENT_THEORY(b, OCM.EnumValuesmeanElementSource[theory])
	OCM.OCMMeanElementsAddEPOCH(b, ep)
	OCM.OCMMeanElementsAddMEAN_MOTION(b, 15.5)
	OCM.OCMMeanElementsAddECCENTRICITY(b, 0.0005)
	OCM.OCMMeanElementsAddINCLINATION(b, 51.64)
	mean := OCM.OCMMeanElementsEnd(b)
	var pert flatbuffers.UOffsetT
	if theory == "AP" {
		rows := make([]flatbuffers.UOffsetT, len(catalogTestSpaceWeather))
		for i, row := range catalogTestSpaceWeather {
			date := b.CreateString(row.date)
			OCM.SPWStart(b)
			OCM.SPWAddDATE(b, date)
			OCM.SPWAddF107_OBS(b, row.f107)
			OCM.SPWAddF107_OBS_CENTER81(b, row.c81)
			OCM.SPWAddAP_AVG(b, row.ap)
			OCM.SPWAddF107_DATA_TYPE(b, row.kind)
			rows[i] = OCM.SPWEnd(b)
		}
		OCM.PerturbationsStartSPACE_WEATHERVector(b, len(rows))
		for i := len(rows) - 1; i >= 0; i-- {
			b.PrependUOffsetT(rows[i])
		}
		weather := b.EndVector(len(rows))
		OCM.PerturbationsStart(b)
		OCM.PerturbationsAddSPACE_WEATHER(b, weather)
		pert = OCM.PerturbationsEnd(b)
	}
	OCM.OCMStart(b)
	OCM.OCMAddHEADER(b, header)
	OCM.OCMAddMETADATA(b, meta)
	OCM.OCMAddMEAN_ELEMENTS(b, mean)
	if pert != 0 {
		OCM.OCMAddPERTURBATIONS(b, pert)
	}
	OCM.FinishOCMBuffer(b, OCM.OCMEnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

func storeCatalogTestOCM(t *testing.T, store *storage.FlatSQLStore, payload []byte) {
	t.Helper()
	tags := storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "sdn-element-sync-ocm",
		SourceURL: "https://provider.example/ocm", BatchID: "ocm-batch"}
	if _, err := store.StoreWithSourceTags("OCM.fbs", payload, "source:sdn-element-sync-ocm", nil, tags); err != nil {
		t.Fatalf("store OCM failed: %v", err)
	}
}

func getCatalogCurrent(t *testing.T, h *DataQueryHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handleCatalogCurrent(rec, httptest.NewRequest(http.MethodGet, CatalogCurrentPath+query, nil))
	return rec
}

// TestCatalogCurrentLanes is the route's E2E over a fixture store holding one
// SGP4 $OMM, one SGP4XP $OCM and one AP $OCM: each theory serves its lane's
// stored bytes, all serves both, any other theory is refused by name, and the
// $OCM lane's JSON names MEAN_ELEMENTS and SPACE_WEATHER with enums by name.
func TestCatalogCurrentLanes(t *testing.T) {
	store := newDataAPITestStore(t)
	day := "2026-11-04"
	epoch, _ := time.Parse(time.RFC3339, day+"T12:00:00Z")
	omm := storeDataAPITestOMMWithSource(t, store, 25544, "ISS (ZARYA)", day, "gp-batch", "space-data-network-02", "celestrak-gp")
	xp := buildCatalogTestOCM(25544, "SGP4XP", epoch.Add(-time.Hour))
	ap := buildCatalogTestOCM(25544, "AP", epoch.Add(-2*time.Hour))
	storeCatalogTestOCM(t, store, xp)
	storeCatalogTestOCM(t, store, ap)

	h := NewDataQueryHandler(store)
	h.catalog.now = func() time.Time { return epoch.Add(time.Hour) }

	frames := func(theory string) ([][]byte, string) {
		rec := getCatalogCurrent(t, h, "?theory="+theory)
		if rec.Code != http.StatusOK {
			t.Fatalf("theory=%s: status %d: %q", theory, rec.Code, rec.Body.String())
		}
		return readLengthPrefixedRecords(t, rec.Body.Bytes()), rec.Header().Get("X-SDN-Schema")
	}
	for _, c := range []struct {
		theory, schema string
		want           [][]byte
	}{
		{"SGP4", "OMM.fbs", [][]byte{omm}},
		{"SGP4XP", "OCM.fbs", [][]byte{xp}},
		{"AP", "OCM.fbs", [][]byte{ap}},
		{"all", "OMM.fbs,OCM.fbs", [][]byte{omm, ap, xp}},
	} {
		got, schema := frames(c.theory)
		if schema != c.schema {
			t.Errorf("theory=%s: X-SDN-Schema %q, want %q", c.theory, schema, c.schema)
		}
		if len(got) != len(c.want) {
			t.Fatalf("theory=%s: %d frames, want %d", c.theory, len(got), len(c.want))
		}
		for i := range got {
			if !bytes.Equal(got[i], c.want[i]) {
				t.Errorf("theory=%s: frame %d (%s) differs from the stored bytes", c.theory, i, got[i][4:8])
			}
		}
		t.Logf("theory=%s: %d frames, %s", c.theory, len(got), schema)
	}

	refused := getCatalogCurrent(t, h, "?theory=SDNAP")
	if refused.Code != http.StatusBadRequest || !bytes.Contains(refused.Body.Bytes(), []byte(catalogTheoryError)) {
		t.Errorf("theory=SDNAP: status %d, body %q; want 400 naming SGP4, SGP4XP, AP, all", refused.Code, refused.Body.String())
	}

	rec := getCatalogCurrent(t, h, "?format=json&theory=AP")
	var sets []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &sets); err != nil || len(sets) != 1 {
		t.Fatalf("AP json: %v, %d sets: %s", err, len(sets), rec.Body.String())
	}
	mean, _ := sets[0]["MEAN_ELEMENTS"].(map[string]any)
	if mean["MEAN_ELEMENT_THEORY"] != "AP" {
		t.Errorf("AP json MEAN_ELEMENTS.MEAN_ELEMENT_THEORY = %v, want AP", mean["MEAN_ELEMENT_THEORY"])
	}
	pert, _ := sets[0]["PERTURBATIONS"].(map[string]any)
	rows, _ := pert["SPACE_WEATHER"].([]any)
	if len(rows) != len(catalogTestSpaceWeather) {
		t.Fatalf("AP json SPACE_WEATHER rows = %d, want %d", len(rows), len(catalogTestSpaceWeather))
	}
	for i, row := range rows {
		r, _ := row.(map[string]any)
		if r["F107_DATA_TYPE"] != catalogTestSpaceWeather[i].kind.String() || r["DATE"] != catalogTestSpaceWeather[i].date {
			t.Errorf("AP json SPACE_WEATHER[%d] = %v, want DATE %s F107_DATA_TYPE %s", i, r, catalogTestSpaceWeather[i].date, catalogTestSpaceWeather[i].kind)
		}
	}
	t.Logf("AP json: MEAN_ELEMENT_THEORY %v, %d SPACE_WEATHER rows, F107_DATA_TYPE %v %v %v", mean["MEAN_ELEMENT_THEORY"], len(rows),
		rows[0].(map[string]any)["F107_DATA_TYPE"], rows[1].(map[string]any)["F107_DATA_TYPE"], rows[2].(map[string]any)["F107_DATA_TYPE"])

	rec = getCatalogCurrent(t, h, "?format=json")
	var both map[string][]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &both); err != nil || len(both["$OMM"]) != 1 || len(both["$OCM"]) != 2 {
		t.Fatalf("all json: %v, $OMM %d, $OCM %d", err, len(both["$OMM"]), len(both["$OCM"]))
	}
}
