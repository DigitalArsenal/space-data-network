package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestRootHelpListsSearchCommand(t *testing.T) {
	help := rootCmd.UsageString()
	if !strings.Contains(help, "search") {
		t.Fatalf("root help did not list search:\n%s", help)
	}

	searchSource, err := os.ReadFile("search_cli.go")
	if err != nil {
		t.Fatalf("read search_cli.go: %v", err)
	}
	if strings.Contains(string(searchSource), "rootCmd.AddCommand(searchCmd)") {
		t.Fatalf("search root registration should live in main.go, not search_cli.go")
	}

	mainSource, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(mainSource), "rootCmd.AddCommand(searchCmd)") {
		t.Fatalf("main.go does not register search root command")
	}
}

func TestSearchCommandHelpListsSubcommands(t *testing.T) {
	commands := searchCmd.Commands()
	got := make([]string, 0, len(commands))
	for _, command := range commands {
		got = append(got, command.Name())
	}
	want := []string{"data", "providers", "standards"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("search subcommands = %#v, want %#v", got, want)
	}

	help := searchCmd.UsageString()
	for _, want := range []string{"providers", "standards", "data"} {
		if !strings.Contains(help, want) {
			t.Fatalf("search help did not list %q:\n%s", want, help)
		}
	}
}

func TestSearchProviderRunEResetsPositionalQuery(t *testing.T) {
	cfgPath, _ := newSyncCLITestStore(t)
	withSyncCLITestConfig(t, cfgPath)

	oldOptions := searchOptionsState.Provider
	searchOptionsState.Provider = searchProviderOptions{Format: "table", Limit: 100}
	t.Cleanup(func() { searchOptionsState.Provider = oldOptions })

	var out bytes.Buffer
	searchProvidersCmd.SetOut(&out)
	t.Cleanup(func() {
		searchProvidersCmd.SetOut(nil)
		searchProvidersCmd.SetArgs(nil)
	})
	if err := searchProvidersCmd.RunE(searchProvidersCmd, []string{"first"}); err != nil {
		t.Fatalf("first search providers execute failed: %v", err)
	}
	if err := searchProvidersCmd.RunE(searchProvidersCmd, []string{}); err != nil {
		t.Fatalf("second search providers execute failed: %v", err)
	}
	if searchOptionsState.Provider.Query != "" {
		t.Fatalf("provider query leaked across executions: %q", searchOptionsState.Provider.Query)
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
	wantJSON := `{
  "count": 1,
  "results": [
    {
      "cached_bytes": 2048,
      "local_rows": 42,
      "provider_id": "space-data-network-02",
      "schema_name": "OMM.fbs",
      "source_name": "celestrak-gp"
    }
  ]
}
`
	if got := jsonOut.String(); got != wantJSON {
		t.Fatalf("JSON output = %q, want %q", got, wantJSON)
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
	wantRecords := [][]string{
		{"schema_name", "provider_id", "source_name", "local_rows", "cached_bytes"},
		{"OMM.fbs", "space-data-network-02", "celestrak-gp", "42", "2048"},
	}
	if !reflect.DeepEqual(records, wantRecords) {
		t.Fatalf("CSV records = %#v, want %#v", records, wantRecords)
	}
}

func TestDataSearchFieldsMatchTaskStep4Order(t *testing.T) {
	want := []string{
		"schema_name", "provider_id", "source_name", "batch_id", "query_profile",
		"provider_peer_id", "provider_public_key", "local_rows", "pinned_rows",
		"cached_bytes", "pinned_bytes", "snapshot_id", "head", "high_water_mark", "last_synced_at",
	}
	if got := dataSearchFields(); !reflect.DeepEqual(got, want) {
		t.Fatalf("data search fields = %#v, want %#v", got, want)
	}
}

func TestSearchProviderSortUsesStableTieBreakers(t *testing.T) {
	rows := []map[string]any{
		{
			"dn":                  "CelesTrak",
			"provider_id":         "space-data-network-02",
			"source_name":         "celestrak-gp",
			"schema_name":         "OMM.fbs",
			"batch_id":            "batch-b",
			"query_profile":       storage.DatasetPublicationQueryProfile,
			"provider_peer_id":    "peer-b",
			"provider_public_key": "key-b",
			"snapshot_id":         "snapshot-b",
		},
		{
			"dn":                  "CelesTrak",
			"provider_id":         "space-data-network-02",
			"source_name":         "celestrak-gp",
			"schema_name":         "OMM.fbs",
			"batch_id":            "batch-a",
			"query_profile":       storage.DatasetPublicationQueryProfile,
			"provider_peer_id":    "peer-a",
			"provider_public_key": "key-a",
			"snapshot_id":         "snapshot-a",
		},
	}

	sortSearchRows(rows, providerSearchSortFields()...)
	if got := []string{searchValueString(rows[0]["batch_id"]), searchValueString(rows[1]["batch_id"])}; !reflect.DeepEqual(got, []string{"batch-a", "batch-b"}) {
		t.Fatalf("provider sort batch order = %#v, rows = %#v", got, rows)
	}
}

func TestSearchDataSortUsesStableTieBreakers(t *testing.T) {
	rows := []map[string]any{
		{
			"schema_name":         "OMM.fbs",
			"provider_id":         "space-data-network-02",
			"source_name":         "celestrak-gp",
			"batch_id":            "test-batch",
			"query_profile":       "profile-b",
			"provider_peer_id":    "peer-b",
			"provider_public_key": "key-b",
			"snapshot_id":         "snapshot-b",
		},
		{
			"schema_name":         "OMM.fbs",
			"provider_id":         "space-data-network-02",
			"source_name":         "celestrak-gp",
			"batch_id":            "test-batch",
			"query_profile":       "profile-a",
			"provider_peer_id":    "peer-a",
			"provider_public_key": "key-a",
			"snapshot_id":         "snapshot-a",
		},
	}

	sortSearchRows(rows, dataSearchSortFields()...)
	if got := []string{searchValueString(rows[0]["query_profile"]), searchValueString(rows[1]["query_profile"])}; !reflect.DeepEqual(got, []string{"profile-a", "profile-b"}) {
		t.Fatalf("data sort query profile order = %#v, rows = %#v", got, rows)
	}
}

func TestSearchProvidersJSONEnrichesDirectoryWithReplicaStats(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSyncCLITestData(t, store)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSearchProviders(&out, searchProviderOptions{
		Query:        "celestrak.eth",
		Schema:       "OMM",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Format:       "json",
	})
	if err != nil {
		t.Fatalf("runSearchProviders failed: %v", err)
	}

	var body searchResult
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("decode provider search JSON: %v\n%s", err, out.String())
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("provider result count = %#v", body)
	}
	row := body.Results[0]
	if row["peer_id"] != "16Uiu2HCelesTrak" || row["provider_id"] != "space-data-network-02" || row["schema_name"] != "OMM.fbs" {
		t.Fatalf("unexpected provider row: %#v", row)
	}
	if row["local_rows"] != float64(1) && row["local_rows"] != int64(1) {
		t.Fatalf("local_rows = %#v, want 1", row["local_rows"])
	}
}

func TestSearchProvidersProviderIDAliasResolvesBeforeStatsFilter(t *testing.T) {
	tests := []string{"celestrak.eth", "16Uiu2HCelesTrak"}
	for _, providerID := range tests {
		t.Run(providerID, func(t *testing.T) {
			cfgPath, store := newSyncCLITestStore(t)
			seedSyncCLITestData(t, store)
			withSyncCLITestConfig(t, cfgPath)

			var out bytes.Buffer
			err := runSearchProviders(&out, searchProviderOptions{
				ProviderID:   providerID,
				Schema:       "OMM",
				QueryProfile: storage.DatasetPublicationQueryProfile,
				Format:       "json",
			})
			if err != nil {
				t.Fatalf("runSearchProviders failed: %v", err)
			}

			var body searchResult
			if err := json.Unmarshal(out.Bytes(), &body); err != nil {
				t.Fatalf("decode provider search JSON: %v\n%s", err, out.String())
			}
			if body.Count != 1 || len(body.Results) != 1 {
				t.Fatalf("provider result count = %#v", body)
			}
			row := body.Results[0]
			if row["peer_id"] != "16Uiu2HCelesTrak" || row["provider_id"] != "space-data-network-02" || row["schema_name"] != "OMM.fbs" {
				t.Fatalf("unexpected provider row for %q: %#v", providerID, row)
			}
		})
	}
}

func TestSearchProvidersDottedProviderAliasResolvesBeforeStatsFilter(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSyncCLITestData(t, store)
	seedSearchCLIDottedAlias(t, store)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSearchProviders(&out, searchProviderOptions{
		ProviderID:   "sdn.spaceaware",
		Schema:       "OMM",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Format:       "json",
	})
	if err != nil {
		t.Fatalf("runSearchProviders failed: %v", err)
	}

	var body searchResult
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("decode provider search JSON: %v\n%s", err, out.String())
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("provider result count = %#v", body)
	}
	row := body.Results[0]
	if row["peer_id"] != "16Uiu2HCelesTrak" || row["provider_id"] != "space-data-network-02" || row["schema_name"] != "OMM.fbs" {
		t.Fatalf("unexpected provider row: %#v", row)
	}
}

func TestSearchProvidersProviderIDOnlyReturnsDirectoryOnlyRows(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind:      "node",
		PeerID:    "16Uiu2HDirectoryOnly",
		DN:        "Directory Only Provider",
		LegalName: "Directory Only LLC",
		Source:    "test",
		EPMJSON: `{
			"aliases": ["directory-only.alias"]
		}`,
		UpdatedAt: 1779689334,
	}); err != nil {
		t.Fatalf("upsert directory-only record failed: %v", err)
	}
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSearchProviders(&out, searchProviderOptions{
		ProviderID: "directory-only.alias",
		Format:     "json",
	})
	if err != nil {
		t.Fatalf("runSearchProviders failed: %v", err)
	}

	var body searchResult
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("decode provider search JSON: %v\n%s", err, out.String())
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("provider result count = %#v", body)
	}
	row := body.Results[0]
	if row["peer_id"] != "16Uiu2HDirectoryOnly" || row["dn"] != "Directory Only Provider" || row["schema_name"] != nil || row["local_rows"] != nil {
		t.Fatalf("unexpected directory-only provider row: %#v", row)
	}
}

func TestSearchProvidersCanonicalIDEnrichesDirectoryByMatchedPeer(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSyncCLITestData(t, store)
	seedSearchCLIDirectoryWithoutProviderID(t, store)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSearchProviders(&out, searchProviderOptions{
		ProviderID:   "space-data-network-02",
		Schema:       "OMM",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Format:       "json",
	})
	if err != nil {
		t.Fatalf("runSearchProviders failed: %v", err)
	}

	var body searchResult
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("decode provider search JSON: %v\n%s", err, out.String())
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("provider result count = %#v", body)
	}
	row := body.Results[0]
	if row["peer_id"] != "16Uiu2HCelesTrak" || row["dn"] != "CelesTrak No Provider ID" || row["provider_id"] != "space-data-network-02" {
		t.Fatalf("canonical provider ID row was not directory enriched: %#v", row)
	}
}

func TestSearchProvidersCanonicalIDMatchesStatsWithoutProviderID(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSearchCLIReplicaWithoutProviderID(t, store)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSearchProviders(&out, searchProviderOptions{
		ProviderID:   "space-data-network-02",
		Schema:       "OMM",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Format:       "json",
	})
	if err != nil {
		t.Fatalf("runSearchProviders failed: %v", err)
	}

	var body searchResult
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("decode provider search JSON: %v\n%s", err, out.String())
	}
	if body.Count != 1 || len(body.Results) != 1 {
		t.Fatalf("provider result count = %#v", body)
	}
	row := body.Results[0]
	if row["peer_id"] != "16Uiu2HCelesTrak" || row["dn"] != "CelesTrak" || row["provider_id"] != "" || row["local_rows"] != float64(1) || row["pinned_rows"] != float64(1) {
		t.Fatalf("canonical provider ID did not match peer-only stats: %#v", row)
	}
}

func TestSearchProvidersReplicaFiltersSkipDirectoryOnlyRows(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSyncCLITestData(t, store)
	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind:      "node",
		PeerID:    "16Uiu2HNoReplica",
		DN:        "No Replica Provider",
		LegalName: "No Replica LLC",
		Source:    "test",
		UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("upsert unrelated directory record failed: %v", err)
	}
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSearchProviders(&out, searchProviderOptions{
		Schema:       "OMM",
		SourceName:   "celestrak-gp",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Format:       "json",
	})
	if err != nil {
		t.Fatalf("runSearchProviders failed: %v", err)
	}

	var body searchResult
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("decode provider search JSON: %v\n%s", err, out.String())
	}
	if body.Count != 1 || len(body.Results) != 1 || body.Results[0]["peer_id"] != "16Uiu2HCelesTrak" {
		t.Fatalf("provider rows with replica filter = %#v", body)
	}
}

func TestSearchProvidersAppliesLimitAfterSort(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind:      "node",
		PeerID:    "peer-z",
		DN:        "Z Provider",
		LegalName: "Z Provider LLC",
		Source:    "test",
		UpdatedAt: 3,
	}); err != nil {
		t.Fatalf("upsert Z provider failed: %v", err)
	}
	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind:      "node",
		PeerID:    "peer-a",
		DN:        "A Provider",
		LegalName: "A Provider LLC",
		Source:    "test",
		UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("upsert A provider failed: %v", err)
	}
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSearchProviders(&out, searchProviderOptions{
		Query:  "Provider",
		Format: "json",
		Limit:  1,
	})
	if err != nil {
		t.Fatalf("runSearchProviders failed: %v", err)
	}

	var body searchResult
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("decode provider search JSON: %v\n%s", err, out.String())
	}
	if body.Count != 1 || len(body.Results) != 1 || body.Results[0]["peer_id"] != "peer-a" {
		t.Fatalf("provider limit result = %#v", body)
	}
}

func TestSearchProvidersCSVUsesStableColumns(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSyncCLITestData(t, store)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSearchProviders(&out, searchProviderOptions{
		Query:  "CelesTrak",
		Format: "csv",
	})
	if err != nil {
		t.Fatalf("runSearchProviders failed: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("decode provider CSV: %v\n%s", err, out.String())
	}
	if len(records) != 2 || records[0][0] != "peer_id" || records[1][0] != "16Uiu2HCelesTrak" {
		t.Fatalf("provider CSV = %#v", records)
	}
	wantHeader := []string{
		"peer_id", "dn", "legal_name", "bitcoin_address", "epm_cid", "source", "updated_at",
		"schema_name", "provider_peer_id", "provider_public_key", "provider_id", "source_name", "batch_id", "query_profile",
		"local_rows", "pinned_rows", "cached_bytes", "pinned_bytes", "snapshot_id", "head", "high_water_mark", "last_synced_at",
	}
	if !reflect.DeepEqual(records[0], wantHeader) {
		t.Fatalf("provider CSV header = %#v, want %#v", records[0], wantHeader)
	}
	headerIndex := map[string]int{}
	for i, field := range records[0] {
		headerIndex[field] = i
	}
	if records[1][headerIndex["provider_peer_id"]] != "16Uiu2HCelesTrak" ||
		records[1][headerIndex["provider_public_key"]] != "provider-public-key" ||
		records[1][headerIndex["snapshot_id"]] != "head-2" {
		t.Fatalf("provider CSV row missing replica identity fields: %#v", records)
	}
}

func TestSearchDataFiltersBySchemaAndProvider(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSyncCLITestData(t, store)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSearchData(&out, searchDataOptions{
		Schema:       "OMM",
		ProviderID:   "space-data-network-02",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Format:       "json",
	})
	if err != nil {
		t.Fatalf("runSearchData failed: %v", err)
	}
	var body searchResult
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("decode data search JSON: %v\n%s", err, out.String())
	}
	if body.Count != 1 || body.Results[0]["source_name"] != "celestrak-gp" {
		t.Fatalf("unexpected data search body: %#v", body)
	}
}

func seedSearchCLIDottedAlias(t *testing.T, store *storage.FlatSQLStore) {
	t.Helper()

	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind:           "node",
		PeerID:         "16Uiu2HCelesTrak",
		DN:             "CelesTrak",
		LegalName:      "CelesTrak",
		BitcoinAddress: "bc1qspacedatanetwork000000000000000000000000",
		EPMCID:         "bafkreigh2akiscaildcagqrb7hf7vsgkl2kpdx3obxxm2pvshpwrsp7m2a",
		Source:         "test",
		EPMJSON: `{
			"xpub": "xpub661MyMwAqRbcFexample",
			"aliases": ["sdn.spaceaware"],
			"ens_names": ["celestrak.eth"]
		}`,
		UpdatedAt: 1779689334,
	}); err != nil {
		t.Fatalf("upsert dotted alias directory record failed: %v", err)
	}
}

func seedSearchCLIDirectoryWithoutProviderID(t *testing.T, store *storage.FlatSQLStore) {
	t.Helper()

	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind:           "node",
		PeerID:         "16Uiu2HCelesTrak",
		DN:             "CelesTrak No Provider ID",
		LegalName:      "CelesTrak",
		BitcoinAddress: "bc1qspacedatanetwork000000000000000000000000",
		EPMCID:         "bafkreigh2akiscaildcagqrb7hf7vsgkl2kpdx3obxxm2pvshpwrsp7m2a",
		Source:         "test",
		EPMJSON: `{
			"xpub": "xpub661MyMwAqRbcFexample",
			"ens_names": ["celestrak.eth"]
		}`,
		UpdatedAt: 1779689334,
	}); err != nil {
		t.Fatalf("upsert directory record without provider id failed: %v", err)
	}
}

func seedSearchCLIReplicaWithoutProviderID(t *testing.T, store *storage.FlatSQLStore) {
	t.Helper()

	payload := sds.NewOMMBuilder().
		WithNoradCatID(56775).
		WithObjectName("STARLINK-6292").
		WithEpoch("2026-05-25T06:08:54Z").
		Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", payload, "16Uiu2HCelesTrak", nil, storage.SourceTags{
		ProviderID:        "source-tags-provider-only",
		SourceName:        "celestrak-gp",
		BatchID:           "test-batch",
		ProducerPeerID:    "16Uiu2HCelesTrak",
		ProducerPublicKey: "provider-public-key",
	}); err != nil {
		t.Fatalf("store OMM failed: %v", err)
	}
	verifiedAt := time.Date(2026, 5, 25, 6, 8, 54, 0, time.UTC)
	if err := store.UpsertPinLedgerEntry(storage.PinLedgerEntry{
		CID:               "bafkshard-omm-peer-only",
		SchemaName:        "OMM.fbs",
		ProviderPeerID:    "16Uiu2HCelesTrak",
		ProviderPublicKey: "provider-public-key",
		SourceName:        "celestrak-gp",
		BatchID:           "test-batch",
		QueryProfile:      storage.DatasetPublicationQueryProfile,
		SnapshotID:        "head-peer-only",
		Head:              "head-peer-only",
		HighWaterMark:     "published-feed-v1:1779689334:1:1:1024",
		ByteHash:          "sha256:shard",
		Role:              "shard",
		RowCount:          1,
		ByteCount:         1024,
		VerificationState: "verified",
		VerifiedAt:        verifiedAt,
	}); err != nil {
		t.Fatalf("upsert pin ledger entry failed: %v", err)
	}
	if err := store.UpsertDirectoryRecord(storage.DirectoryRecord{
		Kind:           "node",
		PeerID:         "16Uiu2HCelesTrak",
		DN:             "CelesTrak",
		LegalName:      "CelesTrak",
		BitcoinAddress: "bc1qspacedatanetwork000000000000000000000000",
		EPMCID:         "bafkreigh2akiscaildcagqrb7hf7vsgkl2kpdx3obxxm2pvshpwrsp7m2a",
		Source:         "test",
		EPMJSON: `{
			"provider_id": "space-data-network-02",
			"peer_id": "16Uiu2HCelesTrak",
			"signing_public_key": "provider-public-key"
		}`,
		UpdatedAt: verifiedAt.Unix(),
	}); err != nil {
		t.Fatalf("upsert directory record failed: %v", err)
	}
}
