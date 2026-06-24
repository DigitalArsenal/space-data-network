# CLI Search And EPM Wizard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build CLI search for providers, standards, and local data-source metadata, plus an EPM/vCard wizard and FlatBuffer identity export.

**Architecture:** Keep search daemon-independent by opening the configured FlatSQL store directly, matching `sync status`. Put search code in a new focused CLI file, put EPM wizard code in a separate identity CLI file, and share deterministic output helpers for table, JSON, and CSV. The wizard updates `epm.Profile` through `epm.Service.UpdateProfile` and `storage.SaveLocalEPM`, so exports and directory indexing use the same local EPM source.

**Tech Stack:** Go, Cobra CLI, FlatSQL storage, SDS schema registry/validator, EPM service, vCard/QR helpers, existing WasmEdge-backed Go test wrapper.

---

## File Structure

- Create `sdn-server/cmd/spacedatanetwork/search_cli.go`: command registration, search option structs, result structs, snapshot builders, and table/JSON/CSV output for `search providers`, `search standards`, and `search data`.
- Create `sdn-server/cmd/spacedatanetwork/search_cli_test.go`: CLI search tests with seeded FlatSQL directory, source summaries, and local replica stats.
- Create `sdn-server/cmd/spacedatanetwork/identity_wizard_cli.go`: `identity wizard`, `identity export --format flatbuffer`, prompt handling, `--set key=value`, output writing, and local EPM persistence.
- Create `sdn-server/cmd/spacedatanetwork/identity_wizard_cli_test.go`: wizard prompt/set tests, flatbuffer export tests, and private material guard tests.
- Modify `sdn-server/cmd/spacedatanetwork/main.go`: wire new flags only where shared globals already live, or move existing identity export helpers into the new identity file.
- Modify `sdn-server/cmd/spacedatanetwork/main_test.go`: keep existing identity export tests passing and add root-help assertions when not better placed in new tests.
- Modify `README.md`: document search commands, output formats, wizard usage, and flatbuffer export.
- Modify `deployment/release/install-script.test.mjs` or existing release smoke tests only if a CLI help/export smoke assertion already exists there.

## Task 1: Add Shared Search Command Surface And Formatters

**Files:**
- Create: `sdn-server/cmd/spacedatanetwork/search_cli.go`
- Create: `sdn-server/cmd/spacedatanetwork/search_cli_test.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`

- [ ] **Step 1: Write failing tests for command registration and formats**

Add this to `sdn-server/cmd/spacedatanetwork/search_cli_test.go`:

```go
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
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
cd sdn-server && ../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestRootHelpListsSearchCommand|TestSearchCommandHelpListsSubcommands|TestNormalizeSearchFormat|TestWriteSearchResultJSONAndCSV' -count=1
```

Expected: FAIL because `searchCmd`, `searchOutputFormat`, `normalizeSearchFormat`, `searchResult`, and `writeSearchResult` do not exist.

- [ ] **Step 3: Implement the search command shell and formatters**

Create `sdn-server/cmd/spacedatanetwork/search_cli.go`:

```go
package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type searchOutputFormat string

const (
	searchOutputTable searchOutputFormat = "table"
	searchOutputJSON  searchOutputFormat = "json"
	searchOutputCSV   searchOutputFormat = "csv"
)

type searchResult struct {
	Count   int              `json:"count"`
	Results []map[string]any `json:"results"`
}

var searchOptionsState struct {
	Provider searchProviderOptions
	Standard searchStandardsOptions
	Data     searchDataOptions
}

type searchProviderOptions struct {
	Query        string
	Schema       string
	ProviderID   string
	SourceName   string
	BatchID      string
	QueryProfile string
	Format       string
	Limit        int
}

type searchStandardsOptions struct {
	Query  string
	Format string
	Limit  int
}

type searchDataOptions struct {
	Query        string
	Schema       string
	ProviderID   string
	SourceName   string
	BatchID      string
	QueryProfile string
	Format       string
	Limit        int
}

var searchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search SDN providers, standards, and local data metadata",
}

var searchProvidersCmd = &cobra.Command{
	Use:   "providers [query]",
	Short: "Search provider EPM directory records",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			searchOptionsState.Provider.Query = args[0]
		}
		return runSearchProviders(cmd.OutOrStdout(), searchOptionsState.Provider)
	},
}

var searchStandardsCmd = &cobra.Command{
	Use:   "standards [query]",
	Short: "Search Space Data Standards schemas",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			searchOptionsState.Standard.Query = args[0]
		}
		return runSearchStandards(cmd.OutOrStdout(), searchOptionsState.Standard)
	},
}

var searchDataCmd = &cobra.Command{
	Use:   "data [query]",
	Short: "Search local data-source metadata",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			searchOptionsState.Data.Query = args[0]
		}
		return runSearchData(cmd.OutOrStdout(), searchOptionsState.Data)
	},
}

func init() {
	addSearchProviderFlags(searchProvidersCmd, &searchOptionsState.Provider)
	addSearchStandardsFlags(searchStandardsCmd, &searchOptionsState.Standard)
	addSearchDataFlags(searchDataCmd, &searchOptionsState.Data)
	searchCmd.AddCommand(searchProvidersCmd)
	searchCmd.AddCommand(searchStandardsCmd)
	searchCmd.AddCommand(searchDataCmd)
	rootCmd.AddCommand(searchCmd)
}

func addSearchProviderFlags(cmd *cobra.Command, options *searchProviderOptions) {
	cmd.Flags().StringVar(&options.Schema, "schema", "", "schema name or three-letter abbreviation, for example OMM or OMM.fbs")
	cmd.Flags().StringVar(&options.ProviderID, "provider-id", "", "provider ID, peer ID, public key, xpub, address, ENS, SNS, IPFS CID, or IPNS")
	cmd.Flags().StringVar(&options.SourceName, "source-name", "", "provider source/feed name")
	cmd.Flags().StringVar(&options.BatchID, "batch-id", "", "source batch ID")
	cmd.Flags().StringVar(&options.QueryProfile, "query-profile", "", "sync query profile")
	cmd.Flags().StringVar(&options.Format, "format", "table", "output format: table, json, csv")
	cmd.Flags().IntVar(&options.Limit, "limit", 100, "maximum results")
}

func addSearchStandardsFlags(cmd *cobra.Command, options *searchStandardsOptions) {
	cmd.Flags().StringVar(&options.Format, "format", "table", "output format: table, json, csv")
	cmd.Flags().IntVar(&options.Limit, "limit", 100, "maximum results")
}

func addSearchDataFlags(cmd *cobra.Command, options *searchDataOptions) {
	cmd.Flags().StringVar(&options.Schema, "schema", "", "schema name or three-letter abbreviation, for example OMM or OMM.fbs")
	cmd.Flags().StringVar(&options.ProviderID, "provider-id", "", "provider ID filter")
	cmd.Flags().StringVar(&options.SourceName, "source-name", "", "source/feed name filter")
	cmd.Flags().StringVar(&options.BatchID, "batch-id", "", "source batch ID filter")
	cmd.Flags().StringVar(&options.QueryProfile, "query-profile", "", "sync query profile filter")
	cmd.Flags().StringVar(&options.Format, "format", "table", "output format: table, json, csv")
	cmd.Flags().IntVar(&options.Limit, "limit", 100, "maximum results")
}

func normalizeSearchFormat(input string) (searchOutputFormat, error) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "", "table", "row", "rows":
		return searchOutputTable, nil
	case "json":
		return searchOutputJSON, nil
	case "csv":
		return searchOutputCSV, nil
	default:
		return "", fmt.Errorf("unsupported search output format %q (use table, json, csv)", input)
	}
}

func writeSearchResult(out io.Writer, result searchResult, fields []string, format searchOutputFormat) error {
	if result.Results == nil {
		result.Results = []map[string]any{}
	}
	switch format {
	case searchOutputJSON:
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	case searchOutputCSV:
		writer := csv.NewWriter(out)
		if err := writer.Write(fields); err != nil {
			return err
		}
		for _, row := range result.Results {
			values := make([]string, 0, len(fields))
			for _, field := range fields {
				values = append(values, searchValueString(row[field]))
			}
			if err := writer.Write(values); err != nil {
				return err
			}
		}
		writer.Flush()
		return writer.Error()
	case searchOutputTable:
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(tw, strings.Join(fields, "\t")); err != nil {
			return err
		}
		for _, row := range result.Results {
			values := make([]string, 0, len(fields))
			for _, field := range fields {
				values = append(values, searchValueString(row[field]))
			}
			if _, err := fmt.Fprintln(tw, strings.Join(values, "\t")); err != nil {
				return err
			}
		}
		return tw.Flush()
	default:
		return fmt.Errorf("unsupported search output format %q", format)
	}
}

func searchValueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	case []string:
		return strings.Join(typed, ";")
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(raw)
	}
}

func applySearchLimit(rows []map[string]any, limit int) []map[string]any {
	if limit <= 0 || limit >= len(rows) {
		return rows
	}
	return rows[:limit]
}

func sortSearchRows(rows []map[string]any, keys ...string) {
	sort.Slice(rows, func(i, j int) bool {
		return searchRowSortKey(rows[i], keys) < searchRowSortKey(rows[j], keys)
	})
}

func searchRowSortKey(row map[string]any, keys []string) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, strings.ToLower(searchValueString(row[key])))
	}
	return strings.Join(parts, "\x00")
}
```

- [ ] **Step 4: Add temporary run function stubs to keep command registration compiling**

Append these stubs to `search_cli.go`; later tasks replace them:

```go
func runSearchProviders(out io.Writer, options searchProviderOptions) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	return writeSearchResult(out, searchResult{Results: []map[string]any{}}, providerSearchFields(), format)
}

func runSearchStandards(out io.Writer, options searchStandardsOptions) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	return writeSearchResult(out, searchResult{Results: []map[string]any{}}, standardsSearchFields(), format)
}

func runSearchData(out io.Writer, options searchDataOptions) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	return writeSearchResult(out, searchResult{Results: []map[string]any{}}, dataSearchFields(), format)
}

func providerSearchFields() []string {
	return []string{"peer_id", "dn", "legal_name", "provider_id", "source_name", "schema_name", "local_rows", "pinned_rows", "updated_at"}
}

func standardsSearchFields() []string {
	return []string{"schema_name", "code", "description", "record_count", "total_bytes"}
}

func dataSearchFields() []string {
	return []string{"schema_name", "provider_id", "source_name", "batch_id", "query_profile", "local_rows", "pinned_rows", "cached_bytes", "pinned_bytes"}
}
```

- [ ] **Step 5: Run focused tests and verify GREEN**

Run:

```bash
cd sdn-server && ../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestRootHelpListsSearchCommand|TestSearchCommandHelpListsSubcommands|TestNormalizeSearchFormat|TestWriteSearchResultJSONAndCSV' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit Task 1**

```bash
git add sdn-server/cmd/spacedatanetwork/search_cli.go sdn-server/cmd/spacedatanetwork/search_cli_test.go
git commit -m "Add CLI search command shell"
```

## Task 2: Implement Provider And Data Search From Local FlatSQL

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/search_cli.go`
- Modify: `sdn-server/cmd/spacedatanetwork/search_cli_test.go`

- [ ] **Step 1: Write failing provider and data search tests**

Append to `search_cli_test.go`:

```go
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
```

Add imports to `search_cli_test.go`:

```go
import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)
```

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
cd sdn-server && ../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestSearchProvidersJSONEnrichesDirectoryWithReplicaStats|TestSearchProvidersCSVUsesStableColumns|TestSearchDataFiltersBySchemaAndProvider' -count=1
```

Expected: FAIL because the Task 1 stubs return zero rows.

- [ ] **Step 3: Add local store opener and provider/data snapshot builders**

Replace the `runSearchProviders` and `runSearchData` stubs in `search_cli.go` with:

```go
func runSearchProviders(out io.Writer, options searchProviderOptions) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	store, err := openSearchStore()
	if err != nil {
		return err
	}
	defer store.Close()

	result, err := buildProviderSearchResult(store, options)
	if err != nil {
		return err
	}
	return writeSearchResult(out, result, providerSearchFields(), format)
}

func runSearchData(out io.Writer, options searchDataOptions) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	store, err := openSearchStore()
	if err != nil {
		return err
	}
	defer store.Close()

	result, err := buildDataSearchResult(store, options)
	if err != nil {
		return err
	}
	return writeSearchResult(out, result, dataSearchFields(), format)
}
```

Add these helpers and imports:

```go
import (
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func openSearchStore() (*storage.FlatSQLStore, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize schema validator: %w", err)
	}
	store, err := storage.NewFlatSQLStore(cfg.Storage.Path, validator)
	if err != nil {
		return nil, fmt.Errorf("failed to open storage: %w", err)
	}
	return store, nil
}

func buildProviderSearchResult(store *storage.FlatSQLStore, options searchProviderOptions) (searchResult, error) {
	schemaName, err := normalizeSyncSchemaName(options.Schema)
	if err != nil {
		return searchResult{}, err
	}
	query := strings.TrimSpace(options.Query)
	if strings.TrimSpace(options.ProviderID) != "" {
		query = strings.TrimSpace(options.ProviderID)
	}
	records, err := store.QueryDirectory(storage.DirectoryQuery{
		Kind:   "node",
		Search: query,
		Limit:  options.Limit,
	})
	if err != nil {
		return searchResult{}, fmt.Errorf("query provider directory: %w", err)
	}
	stats, err := store.LocalReplicaStats(storage.LocalReplicaStatsQuery{
		SchemaName:   schemaName,
		ProviderID:   strings.TrimSpace(options.ProviderID),
		SourceName:   strings.TrimSpace(options.SourceName),
		BatchID:      strings.TrimSpace(options.BatchID),
		QueryProfile: strings.TrimSpace(options.QueryProfile),
	})
	if err != nil {
		return searchResult{}, fmt.Errorf("query local replica stats: %w", err)
	}

	resolution, err := resolveSyncProviderIdentifier(store, query)
	if err != nil {
		return searchResult{}, err
	}
	rows := make([]map[string]any, 0)
	for _, record := range records {
		matchedStats := providerStatsForRecord(record, stats, resolution)
		if len(matchedStats) == 0 {
			rows = append(rows, providerDirectoryRow(record, storage.LocalReplicaStats{}))
			continue
		}
		for _, stat := range matchedStats {
			rows = append(rows, providerDirectoryRow(record, stat))
		}
	}
	sortSearchRows(rows, "dn", "provider_id", "source_name", "schema_name")
	rows = applySearchLimit(rows, options.Limit)
	return searchResult{Count: len(rows), Results: rows}, nil
}

func providerStatsForRecord(record storage.DirectoryRecord, stats []storage.LocalReplicaStats, resolution syncProviderResolution) []storage.LocalReplicaStats {
	rows := make([]storage.LocalReplicaStats, 0)
	for _, stat := range stats {
		if stat.ProviderPeerID == record.PeerID || stat.ProviderID == record.PeerID {
			rows = append(rows, stat)
			continue
		}
		if _, ok := resolution.matchStat(stat); ok && strings.TrimSpace(resolution.input) != "" {
			rows = append(rows, stat)
		}
	}
	return rows
}

func providerDirectoryRow(record storage.DirectoryRecord, stat storage.LocalReplicaStats) map[string]any {
	row := map[string]any{
		"peer_id":         record.PeerID,
		"dn":              record.DN,
		"legal_name":      record.LegalName,
		"bitcoin_address": record.BitcoinAddress,
		"epm_cid":         record.EPMCID,
		"source":          record.Source,
		"updated_at":      formatUnixTime(record.UpdatedAt),
	}
	if stat.SchemaName != "" {
		addReplicaStatFields(row, stat)
	}
	return row
}

func buildDataSearchResult(store *storage.FlatSQLStore, options searchDataOptions) (searchResult, error) {
	schemaName, err := normalizeSyncSchemaName(options.Schema)
	if err != nil {
		return searchResult{}, err
	}
	stats, err := store.LocalReplicaStats(storage.LocalReplicaStatsQuery{
		SchemaName:   schemaName,
		ProviderID:   strings.TrimSpace(options.ProviderID),
		SourceName:   strings.TrimSpace(options.SourceName),
		BatchID:      strings.TrimSpace(options.BatchID),
		QueryProfile: strings.TrimSpace(options.QueryProfile),
	})
	if err != nil {
		return searchResult{}, fmt.Errorf("query local replica stats: %w", err)
	}
	needle := strings.ToLower(strings.TrimSpace(options.Query))
	rows := make([]map[string]any, 0, len(stats))
	for _, stat := range stats {
		row := map[string]any{}
		addReplicaStatFields(row, stat)
		if needle == "" || searchRowContains(row, needle) {
			rows = append(rows, row)
		}
	}
	sortSearchRows(rows, "schema_name", "provider_id", "source_name", "batch_id")
	rows = applySearchLimit(rows, options.Limit)
	return searchResult{Count: len(rows), Results: rows}, nil
}

func addReplicaStatFields(row map[string]any, stat storage.LocalReplicaStats) {
	row["schema_name"] = stat.SchemaName
	row["provider_peer_id"] = stat.ProviderPeerID
	row["provider_public_key"] = stat.ProviderPublicKey
	row["provider_id"] = stat.ProviderID
	row["source_name"] = stat.SourceName
	row["batch_id"] = stat.BatchID
	row["query_profile"] = stat.QueryProfile
	row["local_rows"] = stat.LocalRows
	row["pinned_rows"] = stat.PinnedRows
	row["cached_bytes"] = stat.CachedBytes
	row["pinned_bytes"] = stat.PinnedBytes
	row["snapshot_id"] = stat.SnapshotID
	row["head"] = stat.Head
	row["high_water_mark"] = stat.HighWaterMark
	if !stat.LastSyncedAt.IsZero() {
		row["last_synced_at"] = stat.LastSyncedAt.UTC().Format(time.RFC3339)
	}
}

func formatUnixTime(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}

func searchRowContains(row map[string]any, needle string) bool {
	for _, value := range row {
		if strings.Contains(strings.ToLower(searchValueString(value)), needle) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Expand provider/data columns**

Replace the field helpers in `search_cli.go`:

```go
func providerSearchFields() []string {
	return []string{
		"peer_id", "dn", "legal_name", "bitcoin_address", "epm_cid", "source", "updated_at",
		"schema_name", "provider_id", "source_name", "batch_id", "query_profile",
		"local_rows", "pinned_rows", "cached_bytes", "pinned_bytes", "head", "high_water_mark", "last_synced_at",
	}
}

func dataSearchFields() []string {
	return []string{
		"schema_name", "provider_id", "source_name", "batch_id", "query_profile",
		"provider_peer_id", "provider_public_key", "local_rows", "pinned_rows",
		"cached_bytes", "pinned_bytes", "snapshot_id", "head", "high_water_mark", "last_synced_at",
	}
}
```

- [ ] **Step 5: Run focused tests and verify GREEN**

Run:

```bash
cd sdn-server && ../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestSearchProvidersJSONEnrichesDirectoryWithReplicaStats|TestSearchProvidersCSVUsesStableColumns|TestSearchDataFiltersBySchemaAndProvider' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit Task 2**

```bash
git add sdn-server/cmd/spacedatanetwork/search_cli.go sdn-server/cmd/spacedatanetwork/search_cli_test.go
git commit -m "Add provider and data search CLI"
```

## Task 3: Implement Standards Search From SDS Registry

**Files:**
- Modify: `sdn-server/cmd/spacedatanetwork/search_cli.go`
- Modify: `sdn-server/cmd/spacedatanetwork/search_cli_test.go`

- [ ] **Step 1: Write failing standards search tests**

Append to `search_cli_test.go`:

```go
func TestSearchStandardsFindsSchemaAndLocalCounts(t *testing.T) {
	cfgPath, store := newSyncCLITestStore(t)
	seedSyncCLITestData(t, store)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSearchStandards(&out, searchStandardsOptions{
		Query:  "omm",
		Format: "json",
	})
	if err != nil {
		t.Fatalf("runSearchStandards failed: %v", err)
	}
	var body searchResult
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatalf("decode standards JSON: %v\n%s", err, out.String())
	}
	if body.Count == 0 {
		t.Fatalf("expected at least one standard result: %#v", body)
	}
	row := body.Results[0]
	if row["schema_name"] != "OMM.fbs" || row["code"] != "OMM" {
		t.Fatalf("unexpected standards row: %#v", row)
	}
	if row["record_count"] != float64(1) && row["record_count"] != int64(1) {
		t.Fatalf("record_count = %#v, want 1", row["record_count"])
	}
}

func TestSearchStandardsCSVHasStableHeader(t *testing.T) {
	cfgPath, _ := newSyncCLITestStore(t)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runSearchStandards(&out, searchStandardsOptions{
		Query:  "catalog",
		Format: "csv",
		Limit:  5,
	})
	if err != nil {
		t.Fatalf("runSearchStandards failed: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("decode standards CSV: %v\n%s", err, out.String())
	}
	if len(records) < 2 || strings.Join(records[0], ",") != "schema_name,code,description,record_count,total_bytes" {
		t.Fatalf("standards CSV = %#v", records)
	}
}
```

- [ ] **Step 2: Run focused standards tests and verify RED**

Run:

```bash
cd sdn-server && ../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestSearchStandardsFindsSchemaAndLocalCounts|TestSearchStandardsCSVHasStableHeader' -count=1
```

Expected: FAIL because the Task 1 standards stub returns zero rows.

- [ ] **Step 3: Implement standards search**

Replace the `runSearchStandards` stub in `search_cli.go`:

```go
func runSearchStandards(out io.Writer, options searchStandardsOptions) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	store, err := openSearchStore()
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := buildStandardsSearchResult(store, options)
	if err != nil {
		return err
	}
	return writeSearchResult(out, result, standardsSearchFields(), format)
}

func buildStandardsSearchResult(store *storage.FlatSQLStore, options searchStandardsOptions) (searchResult, error) {
	registry, err := sds.NewSchemaRegistry()
	if err != nil {
		return searchResult{}, fmt.Errorf("load schema registry: %w", err)
	}
	summary, err := store.DataSummary()
	if err != nil {
		return searchResult{}, fmt.Errorf("query data summary: %w", err)
	}
	counts := map[string]storage.DataSchemaSummary{}
	for _, schema := range summary.Schemas {
		counts[schema.SchemaName] = schema
	}

	needle := strings.ToLower(strings.TrimSpace(options.Query))
	rows := make([]map[string]any, 0)
	for _, info := range registry.Info() {
		code := strings.TrimSuffix(info.Name, ".fbs")
		row := map[string]any{
			"schema_name":  info.Name,
			"code":         code,
			"description":  info.Description,
			"record_count": int64(0),
			"total_bytes":  int64(0),
		}
		if summary, ok := counts[info.Name]; ok {
			row["record_count"] = summary.Count
			row["total_bytes"] = summary.TotalBytes
		}
		if needle == "" || searchRowContains(row, needle) {
			rows = append(rows, row)
		}
	}
	sortSearchRows(rows, "schema_name")
	rows = applySearchLimit(rows, options.Limit)
	return searchResult{Count: len(rows), Results: rows}, nil
}

func standardsSearchFields() []string {
	return []string{"schema_name", "code", "description", "record_count", "total_bytes"}
}
```

- [ ] **Step 4: Run focused standards tests and verify GREEN**

Run:

```bash
cd sdn-server && ../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestSearchStandardsFindsSchemaAndLocalCounts|TestSearchStandardsCSVHasStableHeader' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 3**

```bash
git add sdn-server/cmd/spacedatanetwork/search_cli.go sdn-server/cmd/spacedatanetwork/search_cli_test.go
git commit -m "Add standards search CLI"
```

## Task 4: Add Identity Wizard And FlatBuffer Export

**Files:**
- Create: `sdn-server/cmd/spacedatanetwork/identity_wizard_cli.go`
- Create: `sdn-server/cmd/spacedatanetwork/identity_wizard_cli_test.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main.go`
- Modify: `sdn-server/cmd/spacedatanetwork/main_test.go`

- [ ] **Step 1: Write failing wizard and flatbuffer export tests**

Create `sdn-server/cmd/spacedatanetwork/identity_wizard_cli_test.go`:

```go
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestIdentityWizardSetValuesWritesLocalEPMAndJSON(t *testing.T) {
	cfgPath, store, peerID, dataDir := newIdentityWizardTestStore(t)
	withSyncCLITestConfig(t, cfgPath)

	var out bytes.Buffer
	err := runIdentityWizardWithIO(strings.NewReader("y\n"), &out, identityWizardOptions{
		Sets: []string{
			"dn=CelesTrak Provider",
			"legal_name=CelesTrak",
			"email=ops@celestrak.test",
			"telephone=+1-555-0100",
			"alternate_names=celestrak.eth,provider.sol",
		},
		Format: "json",
	}, store, peerID, dataDir)
	if err != nil {
		t.Fatalf("runIdentityWizardWithIO failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("wizard JSON invalid: %v\n%s", err, out.String())
	}
	if payload["dn"] != "CelesTrak Provider" || payload["legal_name"] != "CelesTrak" {
		t.Fatalf("wizard JSON = %#v", payload)
	}
	if strings.Contains(out.String(), "mnemonic") || strings.Contains(out.String(), "xpriv") || strings.Contains(out.String(), "private_key") {
		t.Fatalf("wizard printed private material: %s", out.String())
	}
	records, err := store.QueryDirectory(storage.DirectoryQuery{Kind: "node", Search: "CelesTrak"})
	if err != nil {
		t.Fatalf("query directory: %v", err)
	}
	if len(records) != 1 || records[0].PeerID != peerID.String() {
		t.Fatalf("directory records = %#v", records)
	}
}

func TestIdentityWizardCSVAndFlatBufferOutputs(t *testing.T) {
	cfgPath, store, peerID, dataDir := newIdentityWizardTestStore(t)
	withSyncCLITestConfig(t, cfgPath)

	var csvOut bytes.Buffer
	err := runIdentityWizardWithIO(strings.NewReader("y\n"), &csvOut, identityWizardOptions{
		Sets:   []string{"dn=CSV Provider", "legal_name=CSV Legal"},
		Format: "csv",
	}, store, peerID, dataDir)
	if err != nil {
		t.Fatalf("wizard CSV failed: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(csvOut.String())).ReadAll()
	if err != nil {
		t.Fatalf("wizard CSV invalid: %v\n%s", err, csvOut.String())
	}
	if len(records) != 2 || records[0][0] != "peer_id" || records[1][1] != "CSV Provider" {
		t.Fatalf("wizard CSV records = %#v", records)
	}

	outPath := filepath.Join(t.TempDir(), "epm.fbs")
	err = runIdentityWizardWithIO(strings.NewReader("y\n"), io.Discard, identityWizardOptions{
		Sets:       []string{"dn=FlatBuffer Provider", "legal_name=FlatBuffer Legal"},
		Format:     "flatbuffer",
		OutputPath: outPath,
	}, store, peerID, dataDir)
	if err != nil {
		t.Fatalf("wizard flatbuffer failed: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read flatbuffer output: %v", err)
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(raw) {
		t.Fatalf("flatbuffer output is not EPM bytes: %x", raw[:min(len(raw), 16)])
	}
}

func TestExportIdentityFlatBufferUsesNodeEPMEndpoint(t *testing.T) {
	server := newIdentityExportTestServer(t)
	epmBytes := sds.NewEPMBuilder().WithDN("FlatBuffer Export").Build()
	server.Config.Handler.(*http.ServeMux).HandleFunc("/api/node/epm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-flatbuffers")
		_, _ = w.Write(epmBytes)
	})
	var out bytes.Buffer
	if err := exportIdentity(context.Background(), &out, server.URL, "flatbuffer"); err != nil {
		t.Fatalf("export flatbuffer failed: %v", err)
	}
	if !bytes.Equal(out.Bytes(), epmBytes) {
		t.Fatalf("flatbuffer export mismatch")
	}
}

func newIdentityWizardTestStore(t *testing.T) (string, *storage.FlatSQLStore, peer.ID, string) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(tmpDir, "data")
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config failed: %v", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("create validator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(cfg.Storage.Path, validator)
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	peerID, err := peer.Decode("12D3KooWQecWYcDWL8z8VhFBWkXU1L6XfWZp8fH7jzv6y7gVn3Vh")
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}
	return cfgPath, store, peerID, cfg.Storage.Path
}
```

Fix the import block after writing the test so it includes:

```go
import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)
```

- [ ] **Step 2: Run focused wizard tests and verify RED**

Run:

```bash
cd sdn-server && ../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestIdentityWizardSetValuesWritesLocalEPMAndJSON|TestIdentityWizardCSVAndFlatBufferOutputs|TestExportIdentityFlatBufferUsesNodeEPMEndpoint' -count=1
```

Expected: FAIL because `runIdentityWizardWithIO`, `identityWizardOptions`, and flatbuffer export support do not exist.

- [ ] **Step 3: Add wizard command, flags, and options**

Create `sdn-server/cmd/spacedatanetwork/identity_wizard_cli.go`:

```go
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	qrgen "github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/directory"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	sdnvcard "github.com/spacedatanetwork/sdn-server/internal/vcard"
)

type identityWizardOptions struct {
	Format     string
	OutputPath string
	Sets       []string
	Yes        bool
}

var identityWizardState identityWizardOptions

var identityWizardCmd = &cobra.Command{
	Use:   "wizard",
	Short: "Create or update the node EPM/vCard contact record",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIdentityWizard(cmd.InOrStdin(), cmd.OutOrStdout(), identityWizardState)
	},
}

func init() {
	identityWizardCmd.Flags().StringVar(&identityWizardState.Format, "format", "text", "output format: text, json, csv, flatbuffer, qrcode")
	identityWizardCmd.Flags().StringVarP(&identityWizardState.OutputPath, "output", "o", "", "write flatbuffer output to this path")
	identityWizardCmd.Flags().StringArrayVar(&identityWizardState.Sets, "set", nil, "set a public EPM field as key=value; may be repeated")
	identityWizardCmd.Flags().BoolVarP(&identityWizardState.Yes, "yes", "y", false, "accept generated EPM without confirmation prompt")
	identityCmd.AddCommand(identityWizardCmd)
}
```

- [ ] **Step 4: Implement wizard execution and EPM persistence**

Append to `identity_wizard_cli.go`:

```go
func runIdentityWizard(in io.Reader, out io.Writer, options identityWizardOptions) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return fmt.Errorf("failed to initialize schema validator: %w", err)
	}
	store, err := storage.NewFlatSQLStore(cfg.Storage.Path, validator)
	if err != nil {
		return fmt.Errorf("failed to open storage: %w", err)
	}
	defer store.Close()
	peerID, err := loadWizardPeerID(cfg, store)
	if err != nil {
		return err
	}
	return runIdentityWizardWithIO(in, out, options, store, peerID, cfg.Storage.Path)
}

func runIdentityWizardWithIO(in io.Reader, out io.Writer, options identityWizardOptions, store *storage.FlatSQLStore, peerID peer.ID, dataDir string) error {
	existing := loadWizardProfile(store, peerID)
	profile := cloneEPMProfile(existing)
	if err := applyWizardSets(profile, options.Sets); err != nil {
		return err
	}
	if len(options.Sets) == 0 {
		if err := promptWizardProfile(in, out, profile); err != nil {
			return err
		}
	}
	if !options.Yes {
		ok, err := confirmWizardProfile(in, out)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	service := epm.NewService(nil, nil, peerID, "", dataDir)
	service.SetProfileStore(store)
	if err := service.UpdateProfile(profile); err != nil {
		return err
	}
	epmBytes := service.GetNodeEPM()
	epmJSON, err := epm.DirectoryRecordJSONFromEPM(epmBytes, peerID.String())
	if err != nil {
		return err
	}
	dirSvc := directory.NewService(store)
	if err := dirSvc.UpsertNodeEPMJSON(epmJSON, computeWizardEPMCID(epmBytes), "local-node"); err != nil {
		return err
	}
	return writeWizardEPMOutput(out, epmBytes, options)
}
```

- [ ] **Step 5: Implement prompts, `--set`, and output helpers**

Append to `identity_wizard_cli.go`:

```go
func loadWizardProfile(store *storage.FlatSQLStore, peerID peer.ID) *epm.Profile {
	raw, err := store.LoadLocalEPM(peerID.String())
	if err != nil {
		return &epm.Profile{DN: "SDN Node " + peerID.ShortString()}
	}
	info, err := epm.DirectoryRecordJSONFromEPM(raw, peerID.String())
	if err != nil {
		return &epm.Profile{DN: "SDN Node " + peerID.ShortString()}
	}
	return profileFromDirectoryJSON(info, peerID)
}

func cloneEPMProfile(profile *epm.Profile) *epm.Profile {
	if profile == nil {
		return &epm.Profile{}
	}
	cp := *profile
	if profile.Address != nil {
		addr := *profile.Address
		cp.Address = &addr
	}
	cp.AlternateNames = append([]string(nil), profile.AlternateNames...)
	return &cp
}

func applyWizardSets(profile *epm.Profile, sets []string) error {
	for _, assignment := range sets {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok {
			return fmt.Errorf("invalid --set %q (expected key=value)", assignment)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "dn", "display_name":
			profile.DN = value
		case "legal_name":
			profile.LegalName = value
		case "email":
			profile.Email = value
		case "telephone", "tel":
			profile.Telephone = value
		case "website", "url", "provider_id", "bitcoin_address", "ethereum_address", "solana_address", "ens", "sns", "alternate_names":
			profile.AlternateNames = mergeWizardAlternateNames(profile.AlternateNames, splitWizardList(value)...)
		default:
			return fmt.Errorf("unsupported EPM wizard field %q", key)
		}
	}
	return nil
}

func promptWizardProfile(in io.Reader, out io.Writer, profile *epm.Profile) error {
	reader := bufio.NewReader(in)
	profile.DN = promptWizardValue(reader, out, "Display name / DN", profile.DN)
	profile.LegalName = promptWizardValue(reader, out, "Legal name", profile.LegalName)
	profile.Email = promptWizardValue(reader, out, "Email", profile.Email)
	profile.Telephone = promptWizardValue(reader, out, "Telephone", profile.Telephone)
	aliases := promptWizardValue(reader, out, "Aliases, URLs, provider IDs, chain addresses, ENS, SNS (comma separated)", strings.Join(profile.AlternateNames, ","))
	profile.AlternateNames = splitWizardList(aliases)
	return nil
}

func promptWizardValue(reader *bufio.Reader, out io.Writer, label string, current string) string {
	if current != "" {
		fmt.Fprintf(out, "%s [%s]: ", label, current)
	} else {
		fmt.Fprintf(out, "%s: ", label)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return current
	}
	return line
}

func confirmWizardProfile(in io.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, "Save EPM profile? [y/N]: ")
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func writeWizardEPMOutput(out io.Writer, epmBytes []byte, options identityWizardOptions) error {
	switch normalizeIdentityExportFormat(options.Format) {
	case "json":
		info, err := epm.DirectoryRecordJSONFromEPM(epmBytes, "")
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(info)
	case "csv":
		info, err := epm.DirectoryRecordJSONFromEPM(epmBytes, "")
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(info)
		return writeIdentityCSV(out, raw)
	case "flatbuffer":
		return writeIdentityFlatBufferOutput(out, epmBytes, options.OutputPath)
	case "qrcode":
		vcardText, err := sdnvcard.EPMToVCard(epmBytes)
		if err != nil {
			return err
		}
		qr, err := qrgen.New(vcardText, qrgen.Medium)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, qr.ToSmallString(false))
		return err
	default:
		vcardText, err := sdnvcard.EPMToVCard(epmBytes)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, vcardText)
		return err
	}
}
```

Add missing helper functions in the same file: `profileFromDirectoryJSON`, `mergeWizardAlternateNames`, `splitWizardList`, `computeWizardEPMCID`, and `writeIdentityFlatBufferOutput`. Implement `computeWizardEPMCID` by calling `epm.ComputeEPMCID` and returning an empty string on error only for directory metadata.

- [ ] **Step 6: Add flatbuffer support to existing identity export**

Move or modify `exportIdentity` in `main.go` so `normalizeIdentityExportFormat` maps `flatbuffer`, `fbs`, and `epm` to `flatbuffer`, and add this case:

```go
case "flatbuffer":
	epmBytes, err := fetchLocalIdentityEndpoint(ctx, baseURL, "/api/node/epm")
	if err != nil {
		return err
	}
	return writeIdentityFlatBufferOutput(out, epmBytes, "")
```

Add:

```go
func writeIdentityFlatBufferOutput(out io.Writer, epmBytes []byte, outputPath string) error {
	if len(epmBytes) == 0 {
		return fmt.Errorf("empty EPM FlatBuffer payload")
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return fmt.Errorf("identity endpoint returned invalid EPM FlatBuffer bytes")
	}
	if strings.TrimSpace(outputPath) != "" {
		return os.WriteFile(outputPath, epmBytes, 0o600)
	}
	_, err := out.Write(epmBytes)
	return err
}
```

Import `github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM` in the file containing this helper.

- [ ] **Step 7: Run focused wizard tests and verify GREEN**

Run:

```bash
cd sdn-server && ../scripts/go-with-wasmedge.sh test ./cmd/spacedatanetwork -run 'TestIdentityWizardSetValuesWritesLocalEPMAndJSON|TestIdentityWizardCSVAndFlatBufferOutputs|TestExportIdentityFlatBufferUsesNodeEPMEndpoint' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit Task 4**

```bash
git add sdn-server/cmd/spacedatanetwork/identity_wizard_cli.go sdn-server/cmd/spacedatanetwork/identity_wizard_cli_test.go sdn-server/cmd/spacedatanetwork/main.go sdn-server/cmd/spacedatanetwork/main_test.go
git commit -m "Add EPM wizard and flatbuffer export"
```

## Task 5: Documentation And Release Smoke Coverage

**Files:**
- Modify: `README.md`
- Modify: `deployment/release/install-script.test.mjs`
- Test: `deployment/release/install-script.test.mjs`

- [ ] **Step 1: Write failing README/release smoke assertions**

In `deployment/release/install-script.test.mjs`, add assertions near existing CLI command guidance checks:

```js
assert.match(output, /spacedatanetwork search standards OMM --format json/)
assert.match(output, /spacedatanetwork identity wizard/)
```

If that file does not expose installer output text for CLI examples, add a Node test that reads `README.md`:

```js
test('README documents CLI search and EPM wizard', async () => {
  const readme = await fs.readFile(new URL('../../README.md', import.meta.url), 'utf8')
  assert.match(readme, /spacedatanetwork search providers/)
  assert.match(readme, /spacedatanetwork search standards OMM --format json/)
  assert.match(readme, /spacedatanetwork identity wizard/)
  assert.match(readme, /identity export --format flatbuffer/)
})
```

- [ ] **Step 2: Run release doc smoke and verify RED**

Run:

```bash
node --test deployment/release/install-script.test.mjs
```

Expected: FAIL because README or installer guidance does not yet document the new commands.

- [ ] **Step 3: Update README CLI documentation**

Add this under the CLI quick start section in `README.md`:

```md
Search local SDN providers, standards, and data-source metadata:

```sh
spacedatanetwork search providers celestrak --schema OMM
spacedatanetwork search standards OMM --format json
spacedatanetwork search data --schema CAT --provider-id space-data-network-02 --format csv
```

Search output defaults to aligned table rows. Use `--format json` for scripts or `--format csv` for spreadsheets.

Create or update your public EPM/vCard contact record:

```sh
spacedatanetwork identity wizard
spacedatanetwork identity wizard --set dn="CelesTrak Provider" --set legal_name="CelesTrak" --format json --yes
spacedatanetwork identity export --format flatbuffer --output epm.fbs
spacedatanetwork identity export --format qrcode
```

The wizard stores only public EPM contact fields. It never prints mnemonic, xpriv, private signing key, or private encryption key material.
```

- [ ] **Step 4: Run doc smoke and verify GREEN**

Run:

```bash
node --test deployment/release/install-script.test.mjs
```

Expected: PASS.

- [ ] **Step 5: Commit Task 5**

```bash
git add README.md deployment/release/install-script.test.mjs
git commit -m "Document CLI search and EPM wizard"
```

## Task 6: Full Verification And Public Installer Smoke

**Files:**
- Verify only unless a preceding task exposes a failure.

- [ ] **Step 1: Run full focused CLI package tests**

Run:

```bash
cd sdn-server && ../scripts/go-with-wasmedge.sh test -count=1 ./cmd/spacedatanetwork
```

Expected: PASS.

- [ ] **Step 2: Run storage and EPM focused package tests**

Run:

```bash
cd sdn-server && ../scripts/go-with-wasmedge.sh test -count=1 ./internal/storage ./internal/epm ./internal/vcard
```

Expected: PASS.

- [ ] **Step 3: Run release JS tests touched by docs/smoke**

Run:

```bash
node --test deployment/release/install-script.test.mjs deployment/release/build-self-contained-cli.test.mjs
```

Expected: PASS.

- [ ] **Step 4: Build the CLI and smoke new help output**

Run:

```bash
cd sdn-server
../scripts/go-with-wasmedge.sh build -o /tmp/spacedatanetwork-search-wizard ./cmd/spacedatanetwork
/tmp/spacedatanetwork-search-wizard --help | grep -E 'search|identity'
/tmp/spacedatanetwork-search-wizard search --help | grep -E 'providers|standards|data'
/tmp/spacedatanetwork-search-wizard identity wizard --help | grep -E 'format|set|output'
```

Expected: each grep exits 0.

- [ ] **Step 5: Run a local temp-HOME functional smoke**

Run:

```bash
rm -rf /tmp/sdn-search-wizard-home
mkdir -p /tmp/sdn-search-wizard-home
HOME=/tmp/sdn-search-wizard-home /tmp/spacedatanetwork-search-wizard init
HOME=/tmp/sdn-search-wizard-home /tmp/spacedatanetwork-search-wizard search standards OMM --format json
HOME=/tmp/sdn-search-wizard-home /tmp/spacedatanetwork-search-wizard identity wizard \
  --set dn="Smoke Provider" \
  --set legal_name="Smoke Provider LLC" \
  --format json \
  --yes
HOME=/tmp/sdn-search-wizard-home /tmp/spacedatanetwork-search-wizard identity export --format flatbuffer --output /tmp/sdn-search-wizard-home/epm.fbs
test -s /tmp/sdn-search-wizard-home/epm.fbs
```

Expected: all commands exit 0 and `epm.fbs` is non-empty.

- [ ] **Step 6: Check git diff and status**

Run:

```bash
git diff --check
git status --short --branch
```

Expected: `git diff --check` exits 0. Status shows only intended committed changes or a clean branch ahead of origin.

- [ ] **Step 7: Push and run release workflow**

Run:

```bash
git push origin main
gh workflow run "SDN Beta Release Artifacts" --repo DigitalArsenal/space-data-network --ref main
gh run list --repo DigitalArsenal/space-data-network --workflow "SDN Beta Release Artifacts" --limit 1 --json databaseId,status,conclusion,url,headSha
```

Watch the returned run:

```bash
gh run watch <run-id> --repo DigitalArsenal/space-data-network --interval 30 --exit-status
```

Expected: workflow conclusion `success`.

- [ ] **Step 8: Public installer smoke after release publishes**

Run:

```bash
rm -rf /tmp/sdn-search-wizard-bin /tmp/sdn-search-wizard-public-home
mkdir -p /tmp/sdn-search-wizard-bin /tmp/sdn-search-wizard-public-home
HOME=/tmp/sdn-search-wizard-public-home PATH="/tmp/sdn-search-wizard-bin:$PATH" \
  curl -fsSL https://spacedatanetwork.org/install.sh |
  HOME=/tmp/sdn-search-wizard-public-home PATH="/tmp/sdn-search-wizard-bin:$PATH" \
  SDN_INSTALL_DIR=/tmp/sdn-search-wizard-bin bash
HOME=/tmp/sdn-search-wizard-public-home PATH="/tmp/sdn-search-wizard-bin:$PATH" spacedatanetwork search standards OMM --format json
HOME=/tmp/sdn-search-wizard-public-home PATH="/tmp/sdn-search-wizard-bin:$PATH" spacedatanetwork identity wizard \
  --set dn="Public Smoke Provider" \
  --set legal_name="Public Smoke Provider LLC" \
  --format csv \
  --yes
HOME=/tmp/sdn-search-wizard-public-home PATH="/tmp/sdn-search-wizard-bin:$PATH" spacedatanetwork identity export --format flatbuffer --output /tmp/sdn-search-wizard-public-home/epm.fbs
test -s /tmp/sdn-search-wizard-public-home/epm.fbs
```

Expected: all commands exit 0.
