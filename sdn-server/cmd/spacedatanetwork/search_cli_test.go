package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

func TestRootHelpListsSearchCommand(t *testing.T) {
	help := rootCmd.UsageString()
	if !strings.Contains(help, "search") {
		t.Fatalf("root help did not list search:\n%s", help)
	}
}

func TestSearchCommandHelpListsSubcommands(t *testing.T) {
	help := searchCmd.UsageString()
	for _, want := range []string{"providers", "standards", "data"} {
		if !strings.Contains(help, want) {
			t.Fatalf("search help did not list %q:\n%s", want, help)
		}
	}
}

func TestNormalizeSearchFormat(t *testing.T) {
	tests := map[string]searchOutputFormat{
		"":      searchOutputTable,
		"table": searchOutputTable,
		"rows":  searchOutputTable,
		"json":  searchOutputJSON,
		"csv":   searchOutputCSV,
	}
	for input, want := range tests {
		got, err := normalizeSearchFormat(input)
		if err != nil {
			t.Fatalf("normalizeSearchFormat(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Fatalf("normalizeSearchFormat(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := normalizeSearchFormat("xml"); err == nil || !strings.Contains(err.Error(), "table, json, csv") {
		t.Fatalf("unsupported format error = %v", err)
	}
}

func TestWriteSearchResultJSONAndCSV(t *testing.T) {
	result := searchResult{
		Count: 1,
		Results: []map[string]any{{
			"schema_name":  "OMM.fbs",
			"provider_id":  "space-data-network-02",
			"source_name":  "celestrak-gp",
			"local_rows":   int64(42),
			"cached_bytes": int64(2048),
		}},
	}
	fields := []string{"schema_name", "provider_id", "source_name", "local_rows", "cached_bytes"}

	var jsonOut bytes.Buffer
	if err := writeSearchResult(&jsonOut, result, fields, searchOutputJSON); err != nil {
		t.Fatalf("write JSON search result: %v", err)
	}
	var decoded searchResult
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil {
		t.Fatalf("search JSON invalid: %v\n%s", err, jsonOut.String())
	}
	if decoded.Count != 1 || len(decoded.Results) != 1 {
		t.Fatalf("decoded JSON = %#v", decoded)
	}

	var csvOut bytes.Buffer
	if err := writeSearchResult(&csvOut, result, fields, searchOutputCSV); err != nil {
		t.Fatalf("write CSV search result: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(csvOut.String())).ReadAll()
	if err != nil {
		t.Fatalf("search CSV invalid: %v\n%s", err, csvOut.String())
	}
	if len(records) != 2 || records[0][0] != "schema_name" || records[1][1] != "space-data-network-02" {
		t.Fatalf("CSV records = %#v", records)
	}
}
