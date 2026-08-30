package api

import (
	"net/http/httptest"
	"strings"
	"testing"
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
