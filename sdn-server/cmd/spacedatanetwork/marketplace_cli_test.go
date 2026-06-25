package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMarketplaceSearchSendsFiltersAndPrintsCSV(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/storefront/listings/search" {
			t.Fatalf("unexpected marketplace search request %s %s", r.Method, r.URL.String())
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode marketplace search request: %v", err)
		}
		if request["search_text"] != "maneuver" ||
			request["limit"].(float64) != 25 ||
			request["offset"].(float64) != 5 {
			t.Fatalf("unexpected marketplace search scalar filters: %#v", request)
		}
		assertJSONList(t, request["provider_peer_ids"], []string{"provider-alpha"})
		assertJSONList(t, request["data_types"], []string{"MPE"})
		assertJSONList(t, request["listing_kinds"], []string{"data_stream"})
		assertJSONList(t, request["tags"], []string{"maneuver", "encrypted"})
		assertJSONNumberList(t, request["access_types"], []float64{2})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"total":1,
			"listings":[{
				"listing_id":"listing-mpe-alpha",
				"listing_kind":"data_stream",
				"provider_peer_id":"provider-alpha",
				"title":"Protected maneuver ephemeris",
				"description":"Grant-scoped maneuver ephemeris feed",
				"data_types":["MPE"],
				"tags":["maneuver","encrypted"],
				"access_type":2,
				"encryption_required":true,
				"delivery_methods":["PubSubStream"],
				"protected_delivery":{
					"grant_scope":"stream:read:mpe",
					"field_stream_policy":{
						"policy_id":"policy-mpe-alpha",
						"policy_version":3,
						"stream_id":"maneuver-ephemeris-live",
						"schema_code":"MPE",
						"key_epoch":"epoch-7"
					}
				},
				"pricing":[{"name":"mission","price_amount":19900,"price_currency":"USD"}],
				"active":true
			}]
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newMarketplaceCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"search", "maneuver",
		"--api-url", server.URL,
		"--provider-id", "provider-alpha",
		"--standard", "MPE",
		"--kind", "stream",
		"--tag", "maneuver",
		"--tag", "encrypted",
		"--access-type", "streaming",
		"--limit", "25",
		"--offset", "5",
		"--format", "csv",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("marketplace search failed: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("marketplace search CSV invalid: %v\n%s", err, out.String())
	}
	if len(records) != 2 {
		t.Fatalf("marketplace search CSV records len = %d, want 2: %#v", len(records), records)
	}
	if strings.Join(records[0], ",") != "listing_id,listing_kind,provider_peer_id,title,data_types,tags,access_type,encryption_required,delivery_methods,module_id,stream_id,schema_code,policy_id,key_epoch,price,currency,active" {
		t.Fatalf("marketplace search CSV header = %#v", records[0])
	}
	row := records[1]
	if row[0] != "listing-mpe-alpha" ||
		row[1] != "data_stream" ||
		row[6] != "streaming" ||
		row[10] != "maneuver-ephemeris-live" ||
		row[12] != "policy-mpe-alpha" ||
		row[14] != "19900" ||
		row[15] != "USD" {
		t.Fatalf("marketplace search CSV row = %#v", row)
	}
}

func TestMarketplaceShowPrintsJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/storefront/listings/module-alpha" {
			t.Fatalf("unexpected marketplace show request %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"listing_id":"module-alpha",
			"listing_kind":"wasm_module",
			"provider_peer_id":"provider-alpha",
			"title":"Protected CA module",
			"data_types":["CDM","MPE"],
			"tags":["module","ca"],
			"access_type":1,
			"encryption_required":true,
			"delivery_methods":["ModuleDelivery"],
			"protected_delivery":{
				"module_id":"com.sdn.ca",
				"module_version":"1.2.3",
				"content_key_id":"module-key-alpha",
				"grant_scope":"module:run:ca"
			},
			"pricing":[{"name":"seat","price_amount":50000,"price_currency":"USD"}],
			"active":true
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	cmd := newMarketplaceCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"show", "module-alpha", "--api-url", server.URL, "--format", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("marketplace show failed: %v", err)
	}
	var payload searchResult
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("marketplace show JSON invalid: %v\n%s", err, out.String())
	}
	if payload.Count != 1 || len(payload.Results) != 1 {
		t.Fatalf("marketplace show JSON count = %d len = %d", payload.Count, len(payload.Results))
	}
	row := payload.Results[0]
	if row["listing_id"] != "module-alpha" ||
		row["listing_kind"] != "wasm_module" ||
		row["module_id"] != "com.sdn.ca" ||
		row["access_type"] != "subscription" ||
		row["price"].(float64) != 50000 {
		t.Fatalf("marketplace show JSON row = %#v", row)
	}
}

func TestMarketplaceSearchRejectsUnknownAccessType(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	cmd := newMarketplaceCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"search", "--api-url", "http://127.0.0.1:1", "--access-type", "side-channel"})

	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unsupported access type") {
		t.Fatalf("marketplace search error = %v, want unsupported access type", err)
	}
}

func assertJSONList(t *testing.T, value any, want []string) {
	t.Helper()
	values, ok := value.([]any)
	if !ok || len(values) != len(want) {
		t.Fatalf("JSON list = %#v, want %#v", value, want)
	}
	for i, item := range values {
		if item != want[i] {
			t.Fatalf("JSON list = %#v, want %#v", value, want)
		}
	}
}

func assertJSONNumberList(t *testing.T, value any, want []float64) {
	t.Helper()
	values, ok := value.([]any)
	if !ok || len(values) != len(want) {
		t.Fatalf("JSON number list = %#v, want %#v", value, want)
	}
	for i, item := range values {
		if item != want[i] {
			t.Fatalf("JSON number list = %#v, want %#v", value, want)
		}
	}
}
