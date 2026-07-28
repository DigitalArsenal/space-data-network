package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type searchOutputFormat string
type searchMode string

const (
	searchOutputTable searchOutputFormat = "table"
	searchOutputJSON  searchOutputFormat = "json"
	searchOutputCSV   searchOutputFormat = "csv"

	searchModeLocal   searchMode = "local"
	searchModeDaemon  searchMode = "daemon"
	searchModeLiveDHT searchMode = "live-dht"
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
	Mode         string
	APIURL       string
	SessionToken string
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
	Mode         string
	APIURL       string
	SessionToken string
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
		searchOptionsState.Provider.Query = ""
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
		searchOptionsState.Standard.Query = ""
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
		searchOptionsState.Data.Query = ""
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
}

func addSearchProviderFlags(cmd *cobra.Command, options *searchProviderOptions) {
	cmd.Flags().StringVar(&options.Schema, "schema", "", "schema name or three-letter abbreviation, for example OMM or OMM.fbs")
	cmd.Flags().StringVar(&options.ProviderID, "provider-id", "", "provider ID, peer ID, public key, xpub, address, ENS, SNS, IPFS CID, or IPNS")
	cmd.Flags().StringVar(&options.SourceName, "source-name", "", "provider source/feed name")
	cmd.Flags().StringVar(&options.BatchID, "batch-id", "", "source batch ID")
	cmd.Flags().StringVar(&options.QueryProfile, "query-profile", "", "sync query profile")
	cmd.Flags().StringVar(&options.Mode, "mode", "local", "search mode: local, daemon, live-dht")
	cmd.Flags().StringVar(&options.APIURL, "api", "", "local SDN daemon API base URL for daemon/live-dht search (default: SDN_API_URL or config admin address)")
	cmd.Flags().StringVar(&options.APIURL, "api-url", "", "alias for --api")
	cmd.Flags().StringVar(&options.SessionToken, "session-token", "", "SDN wallet session token for auth-enabled search APIs (default: SDN_SESSION_TOKEN)")
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
	cmd.Flags().StringVar(&options.Mode, "mode", "local", "search mode: local, daemon, live-dht")
	cmd.Flags().StringVar(&options.APIURL, "api", "", "local SDN daemon API base URL for daemon/live-dht search (default: SDN_API_URL or config admin address)")
	cmd.Flags().StringVar(&options.APIURL, "api-url", "", "alias for --api")
	cmd.Flags().StringVar(&options.SessionToken, "session-token", "", "SDN wallet session token for auth-enabled search APIs (default: SDN_SESSION_TOKEN)")
	cmd.Flags().StringVar(&options.Format, "format", "table", "output format: table, json, csv")
	cmd.Flags().IntVar(&options.Limit, "limit", 100, "maximum results")
}

func normalizeSearchMode(input string) (searchMode, error) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "", "local":
		return searchModeLocal, nil
	case "daemon", "api":
		return searchModeDaemon, nil
	case "live-dht", "livedht", "dht":
		return searchModeLiveDHT, nil
	default:
		return "", fmt.Errorf("unsupported search mode %q (use local, daemon, live-dht)", input)
	}
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
	case time.Time:
		return formatSearchTime(typed)
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

func runSearchProviders(out io.Writer, options searchProviderOptions) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	mode, err := normalizeSearchMode(options.Mode)
	if err != nil {
		return err
	}
	if mode != searchModeLocal {
		return runSearchProvidersViaAPI(context.Background(), out, options, mode, format)
	}
	store, err := openSearchStore()
	if err != nil {
		return err
	}
	defer store.Close()

	rows, err := buildSearchProviderRows(store, options)
	if err != nil {
		return err
	}
	sortSearchRows(rows, providerSearchSortFields()...)
	rows = applySearchLimit(rows, options.Limit)
	return writeSearchResult(out, searchResult{Count: len(rows), Results: rows}, providerSearchFields(), format)
}

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

	rows, err := buildSearchStandardsRows(store, options)
	if err != nil {
		return err
	}
	sortSearchStandardRows(rows, options.Query)
	rows = applySearchLimit(rows, options.Limit)
	return writeSearchResult(out, searchResult{Count: len(rows), Results: rows}, standardsSearchFields(), format)
}

func runSearchData(out io.Writer, options searchDataOptions) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	mode, err := normalizeSearchMode(options.Mode)
	if err != nil {
		return err
	}
	if mode != searchModeLocal {
		return runSearchDataViaAPI(context.Background(), out, options, mode, format)
	}
	store, err := openSearchStore()
	if err != nil {
		return err
	}
	defer store.Close()

	rows, err := buildSearchDataRows(store, options)
	if err != nil {
		return err
	}
	sortSearchRows(rows, dataSearchSortFields()...)
	rows = applySearchLimit(rows, options.Limit)
	return writeSearchResult(out, searchResult{Count: len(rows), Results: rows}, dataSearchFields(), format)
}

func runSearchProvidersViaAPI(ctx context.Context, out io.Writer, options searchProviderOptions, mode searchMode, format searchOutputFormat) error {
	result, err := postSearchAPI(ctx, options.APIURL, options.SessionToken, "/api/v1/search/providers", searchAPIPayload(searchAPIOptions{
		Query:        options.Query,
		Schema:       options.Schema,
		ProviderID:   options.ProviderID,
		SourceName:   options.SourceName,
		BatchID:      options.BatchID,
		QueryProfile: options.QueryProfile,
		Mode:         string(mode),
		Limit:        options.Limit,
	}))
	if err != nil {
		return err
	}
	return writeSearchResult(out, result, providerSearchFields(), format)
}

func runSearchDataViaAPI(ctx context.Context, out io.Writer, options searchDataOptions, mode searchMode, format searchOutputFormat) error {
	result, err := postSearchAPI(ctx, options.APIURL, options.SessionToken, "/api/v1/search/data", searchAPIPayload(searchAPIOptions{
		Query:        options.Query,
		Schema:       options.Schema,
		ProviderID:   options.ProviderID,
		SourceName:   options.SourceName,
		BatchID:      options.BatchID,
		QueryProfile: options.QueryProfile,
		Mode:         string(mode),
		Limit:        options.Limit,
	}))
	if err != nil {
		return err
	}
	return writeSearchResult(out, result, dataSearchFields(), format)
}

type searchAPIOptions struct {
	Query        string
	Schema       string
	ProviderID   string
	SourceName   string
	BatchID      string
	QueryProfile string
	Mode         string
	Limit        int
}

func searchAPIPayload(options searchAPIOptions) map[string]any {
	payload := map[string]any{}
	if value := strings.TrimSpace(options.Query); value != "" {
		payload["query"] = value
	}
	if value := strings.TrimSpace(options.Schema); value != "" {
		payload["schema"] = value
	}
	if value := strings.TrimSpace(options.ProviderID); value != "" {
		payload["provider_id"] = value
	}
	if value := strings.TrimSpace(options.SourceName); value != "" {
		payload["source_name"] = value
	}
	if value := strings.TrimSpace(options.BatchID); value != "" {
		payload["batch_id"] = value
	}
	if value := strings.TrimSpace(options.QueryProfile); value != "" {
		payload["query_profile"] = value
	}
	if value := strings.TrimSpace(options.Mode); value != "" {
		payload["mode"] = value
	}
	if options.Limit > 0 {
		payload["limit"] = options.Limit
	}
	return payload
}

func postSearchAPI(ctx context.Context, explicitAPIURL, explicitSessionToken, path string, payload map[string]any) (searchResult, error) {
	baseURL, err := searchAPIBaseURL(explicitAPIURL)
	if err != nil {
		return searchResult{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return searchResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return searchResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token := searchSessionToken(explicitSessionToken); token != "" {
		req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return searchResult{}, fmt.Errorf("search API request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return searchResult{}, fmt.Errorf("search API request: %s", resp.Status)
	}
	var decoded searchResult
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return searchResult{}, fmt.Errorf("decode search API response: %w", err)
	}
	if decoded.Results == nil {
		decoded.Results = []map[string]any{}
	}
	if decoded.Count == 0 && len(decoded.Results) > 0 {
		decoded.Count = len(decoded.Results)
	}
	return decoded, nil
}

func searchAPIBaseURL(explicit string) (string, error) {
	if value := strings.TrimSpace(explicit); value != "" {
		return value, nil
	}
	if value := strings.TrimSpace(os.Getenv("SDN_API_URL")); value != "" {
		return value, nil
	}
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return "", err
	}
	return adminURL(cfg), nil
}

func searchSessionToken(explicit string) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("SDN_SESSION_TOKEN"))
}

func providerSearchFields() []string {
	return []string{
		"peer_id", "dn", "legal_name", "bitcoin_address", "epm_cid", "source", "updated_at",
		"schema_name", "provider_peer_id", "provider_public_key", "provider_id", "source_name", "batch_id", "query_profile",
		"local_rows", "pinned_rows", "cached_bytes", "pinned_bytes", "snapshot_id", "head", "high_water_mark", "last_synced_at",
	}
}

func standardsSearchFields() []string {
	return []string{"schema_name", "code", "description", "record_count", "total_bytes"}
}

func dataSearchFields() []string {
	return []string{
		"schema_name", "provider_id", "source_name", "batch_id", "query_profile",
		"provider_peer_id", "provider_public_key", "local_rows", "pinned_rows",
		"cached_bytes", "pinned_bytes", "snapshot_id", "head", "high_water_mark", "last_synced_at",
	}
}

func providerSearchSortFields() []string {
	return searchSortFieldsWithLeading(
		[]string{"dn", "provider_id", "source_name", "schema_name"},
		providerSearchFields(),
	)
}

func dataSearchSortFields() []string {
	return searchSortFieldsWithLeading(
		[]string{"schema_name", "provider_id", "source_name", "batch_id"},
		dataSearchFields(),
	)
}

func searchSortFieldsWithLeading(leading, fields []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(leading)+len(fields))
	for _, field := range append(append([]string{}, leading...), fields...) {
		if strings.TrimSpace(field) == "" || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	return out
}

func sortSearchStandardRows(rows []map[string]any, query string) {
	query = strings.ToLower(strings.TrimSpace(query))
	sort.Slice(rows, func(i, j int) bool {
		leftDirect := searchStandardDirectMatch(rows[i], query)
		rightDirect := searchStandardDirectMatch(rows[j], query)
		if leftDirect != rightDirect {
			return leftDirect
		}
		return searchRowSortKey(rows[i], []string{"schema_name", "code"}) < searchRowSortKey(rows[j], []string{"schema_name", "code"})
	})
}

func searchStandardDirectMatch(row map[string]any, query string) bool {
	if query == "" {
		return false
	}
	code := strings.ToLower(searchValueString(row["code"]))
	schemaName := strings.ToLower(searchValueString(row["schema_name"]))
	return query == code || query == schemaName || query == strings.TrimSuffix(schemaName, ".fbs")
}

func openSearchStore() (*storage.FlatSQLStore, error) {
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize schema validator: %w", err)
	}
	store, err := openStoreForReading(cfg.Storage.Path, validator)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func buildSearchProviderRows(store *storage.FlatSQLStore, options searchProviderOptions) ([]map[string]any, error) {
	schemaName, err := normalizeSyncSchemaName(options.Schema)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(options.Query)
	providerQuery := strings.TrimSpace(options.ProviderID)
	if providerQuery == "" {
		providerQuery = query
	}
	resolution, err := resolveSyncProviderIdentifier(store, providerQuery)
	if err != nil {
		return nil, err
	}
	stats, err := localSearchReplicaStats(store, storage.LocalReplicaStatsQuery{
		SchemaName:   schemaName,
		SourceName:   strings.TrimSpace(options.SourceName),
		BatchID:      strings.TrimSpace(options.BatchID),
		QueryProfile: strings.TrimSpace(options.QueryProfile),
	})
	if err != nil {
		return nil, err
	}
	matchingStats := filterSearchStatsByResolution(stats, resolution)

	directoryRecords, err := store.QueryDirectory(storage.DirectoryQuery{
		Kind:   "node",
		Search: providerQuery,
		Limit:  providerDirectoryCandidateLimit(options.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("query provider directory: %w", err)
	}

	replicaFiltersActive := searchProviderReplicaFiltersActive(options)
	rows := make([]map[string]any, 0, len(directoryRecords)+len(matchingStats))
	seenStats := map[string]bool{}
	directoryByPeerID := map[string]storage.DirectoryRecord{}
	for _, record := range directoryRecords {
		if strings.TrimSpace(record.PeerID) != "" {
			directoryByPeerID[record.PeerID] = record
		}
		recordStats := searchStatsForDirectoryRecord(record, matchingStats)
		if len(recordStats) == 0 {
			if replicaFiltersActive {
				continue
			}
			row := searchDirectoryRow(record)
			rows = append(rows, row)
			continue
		}
		for _, stat := range recordStats {
			row := searchDirectoryRow(record)
			addSearchReplicaStats(row, stat)
			rows = append(rows, row)
			seenStats[searchStatKey(stat)] = true
		}
	}
	for _, stat := range matchingStats {
		if seenStats[searchStatKey(stat)] {
			continue
		}
		if record, ok, err := searchDirectoryRecordForStat(store, directoryByPeerID, stat); err != nil {
			return nil, err
		} else if ok {
			row := searchDirectoryRow(record)
			addSearchReplicaStats(row, stat)
			rows = append(rows, row)
			continue
		}
		row := map[string]any{}
		addSearchReplicaStats(row, stat)
		if searchRowContains(row, query) || providerQuery != "" {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func searchProviderReplicaFiltersActive(options searchProviderOptions) bool {
	return strings.TrimSpace(options.Schema) != "" ||
		strings.TrimSpace(options.SourceName) != "" ||
		strings.TrimSpace(options.BatchID) != "" ||
		strings.TrimSpace(options.QueryProfile) != ""
}

func providerDirectoryCandidateLimit(finalLimit int) int {
	// QueryDirectory orders by updated_at, while the CLI sorts enriched rows by
	// display fields. Keep a candidate window so small --limit values apply only
	// after the CLI's deterministic sort. Full correctness beyond the storage
	// candidate cap needs a paged or display-sorted directory query API.
	if finalLimit > 1000 {
		return finalLimit
	}
	return 1000
}

func buildSearchDataRows(store *storage.FlatSQLStore, options searchDataOptions) ([]map[string]any, error) {
	schemaName, err := normalizeSyncSchemaName(options.Schema)
	if err != nil {
		return nil, err
	}
	stats, err := localSearchReplicaStats(store, storage.LocalReplicaStatsQuery{
		SchemaName:   schemaName,
		ProviderID:   strings.TrimSpace(options.ProviderID),
		SourceName:   strings.TrimSpace(options.SourceName),
		BatchID:      strings.TrimSpace(options.BatchID),
		QueryProfile: strings.TrimSpace(options.QueryProfile),
	})
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(options.Query)
	rows := make([]map[string]any, 0, len(stats))
	for _, stat := range stats {
		row := map[string]any{}
		addSearchReplicaStats(row, stat)
		if !searchRowContains(row, query) {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func buildSearchStandardsRows(store *storage.FlatSQLStore, options searchStandardsOptions) ([]map[string]any, error) {
	registry, err := sds.NewSchemaRegistry()
	if err != nil {
		return nil, fmt.Errorf("load schema registry: %w", err)
	}
	summary, err := store.DataSummary()
	if err != nil {
		return nil, fmt.Errorf("query data summary: %w", err)
	}
	counts := map[string]storage.DataSchemaSummary{}
	for _, schema := range summary.Schemas {
		counts[schema.SchemaName] = schema
	}

	query := strings.TrimSpace(options.Query)
	schemas := registry.Info()
	rows := make([]map[string]any, 0, len(schemas))
	for _, info := range schemas {
		code := strings.TrimSuffix(info.Name, ".fbs")
		row := map[string]any{
			"schema_name":  info.Name,
			"code":         code,
			"description":  standardsSearchDescription(registry, info, code),
			"record_count": int64(0),
			"total_bytes":  int64(0),
		}
		if summary, ok := counts[info.Name]; ok {
			row["record_count"] = summary.Count
			row["total_bytes"] = summary.TotalBytes
		}
		if !searchStandardRowContains(row, query) {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func standardsSearchDescription(registry *sds.SchemaRegistry, info sds.SchemaInfo, code string) string {
	if content, ok := registry.Get(info.Name); ok {
		if description := standardsSearchTableDescription(content, code); description != "" {
			return description
		}
	}
	return info.Description
}

func standardsSearchTableDescription(content []byte, code string) string {
	var comment []string
	tablePrefix := "table " + code
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "///") {
			comment = append(comment, strings.TrimSpace(strings.TrimPrefix(line, "///")))
			continue
		}
		if strings.HasPrefix(line, tablePrefix) {
			return strings.TrimSpace(strings.Join(comment, " "))
		}
		comment = nil
	}
	return ""
}

func searchStandardRowContains(row map[string]any, query string) bool {
	return searchRowContains(map[string]any{
		"schema_name": row["schema_name"],
		"code":        row["code"],
		"description": row["description"],
	}, query)
}

func localSearchReplicaStats(store *storage.FlatSQLStore, query storage.LocalReplicaStatsQuery) ([]storage.LocalReplicaStats, error) {
	stats, err := store.LocalReplicaStats(query)
	if err != nil {
		return nil, fmt.Errorf("query local replica stats: %w", err)
	}
	filtered := stats[:0]
	for _, stat := range stats {
		if searchReplicaStatHasEvidence(stat) {
			filtered = append(filtered, stat)
		}
	}
	return filtered, nil
}

func filterSearchStatsByResolution(stats []storage.LocalReplicaStats, resolution syncProviderResolution) []storage.LocalReplicaStats {
	filtered := make([]storage.LocalReplicaStats, 0, len(stats))
	for _, stat := range stats {
		if _, ok := resolution.matchStat(stat); ok {
			filtered = append(filtered, stat)
		}
	}
	return filtered
}

func searchStatsForDirectoryRecord(record storage.DirectoryRecord, stats []storage.LocalReplicaStats) []storage.LocalReplicaStats {
	resolution := syncProviderResolution{
		input:      record.PeerID,
		kind:       syncProviderKindProviderID,
		providers:  newSyncIdentifierSet(),
		peers:      newSyncIdentifierSet(),
		publicKeys: newSyncIdentifierSet(),
	}
	addDirectoryRecordToSyncResolution(&resolution, record)
	matches := make([]storage.LocalReplicaStats, 0, len(stats))
	for _, stat := range stats {
		if _, ok := resolution.matchStat(stat); ok {
			matches = append(matches, stat)
		}
	}
	return matches
}

func searchDirectoryRecordForStat(store *storage.FlatSQLStore, directoryByPeerID map[string]storage.DirectoryRecord, stat storage.LocalReplicaStats) (storage.DirectoryRecord, bool, error) {
	peerID := strings.TrimSpace(stat.ProviderPeerID)
	if peerID == "" {
		return storage.DirectoryRecord{}, false, nil
	}
	if record, ok := directoryByPeerID[peerID]; ok {
		return record, true, nil
	}
	records, err := store.QueryDirectory(storage.DirectoryQuery{
		Kind:   "node",
		PeerID: peerID,
		Limit:  1,
	})
	if err != nil {
		return storage.DirectoryRecord{}, false, fmt.Errorf("query provider directory by peer id: %w", err)
	}
	if len(records) == 0 {
		return storage.DirectoryRecord{}, false, nil
	}
	directoryByPeerID[peerID] = records[0]
	return records[0], true, nil
}

func searchDirectoryRow(record storage.DirectoryRecord) map[string]any {
	return map[string]any{
		"peer_id":         record.PeerID,
		"dn":              record.DN,
		"legal_name":      record.LegalName,
		"bitcoin_address": record.BitcoinAddress,
		"epm_cid":         record.EPMCID,
		"source":          record.Source,
		"updated_at":      formatSearchUnix(record.UpdatedAt),
	}
}

func addSearchReplicaStats(row map[string]any, stat storage.LocalReplicaStats) {
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
	row["last_synced_at"] = formatSearchTime(stat.LastSyncedAt)
}

func searchReplicaStatHasEvidence(stat storage.LocalReplicaStats) bool {
	return stat.LocalRows > 0 || stat.PinnedRows > 0 || stat.CachedBytes > 0 || stat.PinnedBytes > 0 ||
		strings.TrimSpace(stat.Head) != "" || strings.TrimSpace(stat.SnapshotID) != "" || !stat.LastSyncedAt.IsZero()
}

func searchRowContains(row map[string]any, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, value := range row {
		if strings.Contains(strings.ToLower(searchValueString(value)), query) {
			return true
		}
	}
	return false
}

func searchStatKey(stat storage.LocalReplicaStats) string {
	return strings.Join([]string{
		stat.SchemaName,
		stat.ProviderPeerID,
		stat.ProviderPublicKey,
		stat.ProviderID,
		stat.SourceName,
		stat.BatchID,
		stat.QueryProfile,
	}, "\x00")
}

func formatSearchUnix(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func formatSearchTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
