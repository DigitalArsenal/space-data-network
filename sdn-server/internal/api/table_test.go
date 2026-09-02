package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	MPE "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

var catColumns = []string{"OBJECT_NAME", "OBJECT_ID", "NORAD_CAT_ID", "OWNER", "_source", "_rowid", "_offset", "_data"}

func TestBuildTableSQLDefaults(t *testing.T) {
	req := tableRequest{Schema: "CAT", Page: 1, Limit: 100, Filters: map[string]string{}}
	countSQL, pageSQL, projection, err := buildTableSQL(req, catColumns)
	if err != nil {
		t.Fatalf("buildTableSQL: %v", err)
	}
	if countSQL != "SELECT COUNT(*) FROM CAT" {
		t.Fatalf("countSQL = %q", countSQL)
	}
	if !strings.Contains(pageSQL, "ORDER BY _rowid DESC LIMIT 100 OFFSET 0") {
		t.Fatalf("pageSQL = %q", pageSQL)
	}
	for _, banned := range []string{"_data", "_offset"} {
		for _, c := range projection {
			if c == banned {
				t.Fatalf("default projection must not include %s", banned)
			}
		}
	}
}

func TestBuildTableSQLSortFilterSearchSource(t *testing.T) {
	req := tableRequest{
		Schema: "CAT", Page: 3, Limit: 50,
		Sort: "object_name", Dir: "desc",
		Q:      "iss",
		Source: "CAT@celestrak-satcat",
		Filters: map[string]string{
			"owner": "US",
		},
	}
	countSQL, pageSQL, _, err := buildTableSQL(req, catColumns)
	if err != nil {
		t.Fatalf("buildTableSQL: %v", err)
	}
	for _, want := range []string{
		"_source = 'CAT@celestrak-satcat'",
		`CAST(OWNER AS TEXT) LIKE '%US%' ESCAPE '\'`,
		`CAST(OBJECT_NAME AS TEXT) LIKE '%iss%' ESCAPE '\'`,
		"ORDER BY OBJECT_NAME DESC",
		"LIMIT 50 OFFSET 100",
	} {
		if !strings.Contains(pageSQL, want) {
			t.Fatalf("pageSQL %q lacks %q", pageSQL, want)
		}
	}
	if !strings.Contains(countSQL, "_source = 'CAT@celestrak-satcat'") {
		t.Fatalf("countSQL %q lacks the source clause", countSQL)
	}
	// The global search never scans lane bookkeeping columns.
	if strings.Contains(pageSQL, "CAST(_source AS TEXT) LIKE '%iss%'") {
		t.Fatalf("search must not scan _source: %q", pageSQL)
	}
}

func TestBuildTableSQLRefusesInjection(t *testing.T) {
	base := tableRequest{Schema: "CAT", Page: 1, Limit: 10, Filters: map[string]string{}}

	sortInjected := base
	sortInjected.Sort = "OBJECT_NAME; DROP TABLE CAT"
	if _, _, _, err := buildTableSQL(sortInjected, catColumns); err == nil {
		t.Fatal("injected sort identifier must be refused")
	}

	notAColumn := base
	notAColumn.Sort = "EVIL"
	if _, _, _, err := buildTableSQL(notAColumn, catColumns); err == nil {
		t.Fatal("unknown sort column must be refused")
	}

	filterInjected := base
	filterInjected.Filters = map[string]string{"OWNER) OR (1=1": "x"}
	if _, _, _, err := buildTableSQL(filterInjected, catColumns); err == nil {
		t.Fatal("injected filter identifier must be refused")
	}

	// Literal values are escaped, never refused.
	quoted := base
	quoted.Q = "o'brien %100_"
	_, pageSQL, _, err := buildTableSQL(quoted, catColumns)
	if err != nil {
		t.Fatalf("quoted literal: %v", err)
	}
	if !strings.Contains(pageSQL, `o''brien \%100\_`) {
		t.Fatalf("literal not escaped: %q", pageSQL)
	}
	if strings.Contains(pageSQL, "o'brien") {
		t.Fatalf("raw quote survived: %q", pageSQL)
	}
}

func TestParseTableRequest(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/api/v1/data/table?schema=cat.fbs&page=2&limit=9000&sort=OWNER&dir=DESC&q=iss&source=CAT%40celestrak-satcat&f.OWNER=US&cols=OBJECT_NAME,OWNER", nil)
	req, err := parseTableRequest(r)
	if err != nil {
		t.Fatalf("parseTableRequest: %v", err)
	}
	if req.Schema != "CAT" {
		t.Fatalf("schema = %q", req.Schema)
	}
	if req.Limit != tableMaxLimit {
		t.Fatalf("limit = %d, want clamped %d", req.Limit, tableMaxLimit)
	}
	if req.Dir != "desc" || req.Sort != "OWNER" || req.Q != "iss" {
		t.Fatalf("sort/dir/q = %q/%q/%q", req.Sort, req.Dir, req.Q)
	}
	if req.Source != "CAT@celestrak-satcat" {
		t.Fatalf("source = %q", req.Source)
	}
	if req.Filters["OWNER"] != "US" {
		t.Fatalf("filters = %v", req.Filters)
	}
	if len(req.Cols) != 2 {
		t.Fatalf("cols = %v", req.Cols)
	}

	if _, err := parseTableRequest(httptest.NewRequest("GET", "/api/v1/data/table?schema=nope!", nil)); err == nil {
		t.Fatal("malformed schema must be refused")
	}
}

func TestBuildSourcesSQL(t *testing.T) {
	got := buildSourcesSQL("OMM")
	if got != "SELECT _source, COUNT(*) FROM OMM GROUP BY _source ORDER BY COUNT(*) DESC" {
		t.Fatalf("buildSourcesSQL = %q", got)
	}
}

func TestTablePageReadsPastTheEngineHotWindow(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(
		filepath.Join(t.TempDir(), "store"),
		validator,
		storage.WithEngineHotWindow(10),
	)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	tags := storage.SourceTags{ProviderID: "test", SourceName: "full-page", BatchID: "b1", ContentKeyID: "public"}
	for i := 0; i < 25; i++ {
		record := sds.NewOMMBuilder().
			WithNoradCatID(uint32(30000 + i)).
			WithObjectName(fmt.Sprintf("FULL-%03d", i)).
			WithEpoch("2026-09-02T00:00:00Z").
			Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", record, "source:full-page", nil, tags); err != nil {
			t.Fatalf("store OMM %d: %v", i, err)
		}
	}
	resident, err := store.EngineRecordCount("OMM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount: %v", err)
	}
	if resident != 10 {
		t.Fatalf("resident engine rows = %d, want hot window 10", resident)
	}

	handler := NewCoreAPIHandler("", nil, nil, nil, store, nil, nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/v1/data/table?schema=OMM&page=4&limit=5&cols=OBJECT_NAME,_rowid", nil)
	res := httptest.NewRecorder()
	handler.handleTablePage(res, req)
	if res.Code != 200 {
		t.Fatalf("table page -> %d: %s", res.Code, res.Body.String())
	}
	var page tableResponse
	if err := json.Unmarshal(res.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode table response: %v", err)
	}
	if page.Total != 25 {
		t.Fatalf("total = %d, want all 25 stored records", page.Total)
	}
	if len(page.Rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(page.Rows))
	}
	if page.Rows[0][0] != "FULL-009" || page.Rows[4][0] != "FULL-005" {
		t.Fatalf("page past hot window = %v, want FULL-009 through FULL-005", page.Rows)
	}
	if page.Rows[0][1] == "" || page.Rows[4][1] == "" {
		t.Fatalf("durable row ids missing: %v", page.Rows)
	}
}

func TestTablePageProjectsMPEWithTheExistingRecordDecoder(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	builder := flatbuffers.NewBuilder(256)
	entityID := builder.CreateString("mpe-entity-42")
	MPE.MPEStart(builder)
	MPE.MPEAddENTITY_ID(builder, entityID)
	MPE.MPEAddEPOCH(builder, 1788393600)
	MPE.MPEAddMEAN_MOTION(builder, 15.25)
	root := MPE.MPEEnd(builder)
	MPE.FinishSizePrefixedMPEBuffer(builder, root)
	if _, err := store.StoreWithSourceTags("MPE.fbs", builder.FinishedBytes(), "source:mpe", nil,
		storage.SourceTags{ProviderID: "test", SourceName: "mpe", BatchID: "b1", ContentKeyID: "public"}); err != nil {
		t.Fatalf("store MPE: %v", err)
	}

	handler := NewCoreAPIHandler("", nil, nil, nil, store, nil, nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/v1/data/table?schema=MPE&cols=ENTITY_ID,EPOCH,MEAN_MOTION", nil)
	res := httptest.NewRecorder()
	handler.handleTablePage(res, req)
	if res.Code != 200 {
		t.Fatalf("MPE table page -> %d: %s", res.Code, res.Body.String())
	}
	var page tableResponse
	if err := json.Unmarshal(res.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode MPE table response: %v", err)
	}
	if page.Total != 1 || len(page.Rows) != 1 {
		t.Fatalf("MPE page = total %d rows %v", page.Total, page.Rows)
	}
	if got := page.Rows[0]; got[0] != "mpe-entity-42" || got[1] != "1.7883936e+09" || got[2] != "15.25" {
		t.Fatalf("MPE projection = %v", got)
	}
}

func TestTablePageFiltersSearchesAndSortsAcrossTheFullStoredSet(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(
		filepath.Join(t.TempDir(), "store"),
		validator,
		storage.WithEngineHotWindow(10),
	)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	const lastPageMatch = "FULL-SET-LAST-PAGE-MATCH"
	recordCount := tableMaxLimit + 2
	records := make([][]byte, 0, recordCount)
	for i := 0; i < recordCount; i++ {
		name := fmt.Sprintf("ROW-%04d", i)
		if i == 0 {
			// The default durable order is newest first, so the first inserted
			// record sits beyond the initial 1,000-row block.
			name = lastPageMatch
		}
		records = append(records, sds.NewOMMBuilder().
			WithNoradCatID(uint32(50000+i)).
			WithObjectName(name).
			WithEpoch("2026-09-02T00:00:00Z").
			Build())
	}
	tags := storage.SourceTags{ProviderID: "test", SourceName: "full-scan", BatchID: "b1", ContentKeyID: "public"}
	if inserted, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "source:full-scan", nil, tags); err != nil || inserted != recordCount {
		t.Fatalf("StoreBatchWithSourceTags = (%d, %v), want (%d, nil)", inserted, err, recordCount)
	}

	handler := NewCoreAPIHandler("", nil, nil, nil, store, nil, nil, nil, nil)
	requestPage := func(path string, budget tableScanBudget) tableResponse {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req = req.WithContext(withTableScanBudget(context.Background(), budget))
		res := httptest.NewRecorder()
		handler.handleTablePage(res, req)
		if res.Code != 200 {
			t.Fatalf("table page -> %d: %s", res.Code, res.Body.String())
		}
		var page tableResponse
		if err := json.Unmarshal(res.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode table response: %v", err)
		}
		return page
	}
	exactBudget := tableScanBudget{MaxRecords: tableScanRecordBudget, MaxTime: time.Minute}

	t.Run("column filter finds a match beyond the first durable block", func(t *testing.T) {
		page := requestPage("/api/v1/data/table?schema=OMM&limit=10&cols=OBJECT_NAME&f.OBJECT_NAME=LAST-PAGE", exactBudget)
		if page.Partial || page.Scanned != int64(recordCount) || page.Stored != int64(recordCount) {
			t.Fatalf("scan metadata = partial %v, scanned %d, stored %d", page.Partial, page.Scanned, page.Stored)
		}
		if page.Total != 1 || len(page.Rows) != 1 || page.Rows[0][0] != lastPageMatch {
			t.Fatalf("filtered page = total %d rows %v, want the last-page match", page.Total, page.Rows)
		}
	})

	t.Run("global search finds a match beyond the first durable block", func(t *testing.T) {
		page := requestPage("/api/v1/data/table?schema=OMM&limit=10&cols=OBJECT_NAME&q=FULL-SET", exactBudget)
		if page.Partial || page.Total != 1 || len(page.Rows) != 1 || page.Rows[0][0] != lastPageMatch {
			t.Fatalf("searched page = partial %v total %d rows %v", page.Partial, page.Total, page.Rows)
		}
	})

	t.Run("projected sort orders the full durable set", func(t *testing.T) {
		page := requestPage("/api/v1/data/table?schema=OMM&limit=2&cols=OBJECT_NAME&sort=OBJECT_NAME&dir=asc", exactBudget)
		if page.Partial || page.Total != int64(recordCount) || len(page.Rows) != 2 {
			t.Fatalf("sorted page = partial %v total %d rows %v", page.Partial, page.Total, page.Rows)
		}
		if page.Rows[0][0] != lastPageMatch || page.Rows[1][0] != "ROW-0001" {
			t.Fatalf("full-set ascending order starts %v", page.Rows)
		}
	})

	t.Run("record budget reports an honest partial result", func(t *testing.T) {
		page := requestPage("/api/v1/data/table?schema=OMM&limit=5&cols=OBJECT_NAME&q=ROW", tableScanBudget{
			MaxRecords: 10,
			MaxTime:    time.Minute,
		})
		if !page.Partial || page.Scanned != 10 || page.Stored != int64(recordCount) {
			t.Fatalf("partial scan metadata = partial %v, scanned %d, stored %d", page.Partial, page.Scanned, page.Stored)
		}
		if page.Total != 10 || len(page.Rows) != 5 {
			t.Fatalf("partial result = total %d rows %d, want 10 matches and a 5-row page", page.Total, len(page.Rows))
		}
	})
}
