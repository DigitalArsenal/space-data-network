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
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
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
	store, err := openSearchStore()
	if err != nil {
		return err
	}
	defer store.Close()

	rows, err := buildSearchProviderRows(store, options)
	if err != nil {
		return err
	}
	sortSearchRows(rows, "dn", "provider_id", "source_name", "schema_name")
	rows = applySearchLimit(rows, options.Limit)
	return writeSearchResult(out, searchResult{Count: len(rows), Results: rows}, providerSearchFields(), format)
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
	store, err := openSearchStore()
	if err != nil {
		return err
	}
	defer store.Close()

	rows, err := buildSearchDataRows(store, options)
	if err != nil {
		return err
	}
	sortSearchRows(rows, "schema_name", "provider_id", "source_name", "batch_id")
	rows = applySearchLimit(rows, options.Limit)
	return writeSearchResult(out, searchResult{Count: len(rows), Results: rows}, dataSearchFields(), format)
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
		ProviderID:   searchProviderIDStatsFilter(options.ProviderID),
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
	for _, record := range directoryRecords {
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
		row := map[string]any{}
		addSearchReplicaStats(row, stat)
		if searchRowContains(row, query) || providerQuery != "" {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func searchProviderIDStatsFilter(input string) string {
	value := strings.TrimSpace(input)
	if value == "" || classifySyncProviderIdentifier(value) != syncProviderKindProviderID {
		return ""
	}
	if looksLikeSearchPeerID(value) {
		return ""
	}
	if looksLikeSearchProviderPublicKey(value) {
		return ""
	}
	return value
}

func looksLikeSearchPeerID(value string) bool {
	if _, err := peer.Decode(value); err == nil {
		return true
	}
	return strings.HasPrefix(value, "12D3KooW") ||
		strings.HasPrefix(value, "16Uiu") ||
		(strings.HasPrefix(value, "Qm") && len(value) == 46)
}

func looksLikeSearchProviderPublicKey(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "public-key") || strings.Contains(lower, "pubkey") {
		return true
	}
	if len(trimmed) == 64 || len(trimmed) == 66 || len(trimmed) == 128 || len(trimmed) == 130 {
		for _, r := range trimmed {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
		return true
	}
	return false
}

func searchProviderReplicaFiltersActive(options searchProviderOptions) bool {
	return strings.TrimSpace(options.Schema) != "" ||
		strings.TrimSpace(options.ProviderID) != "" ||
		strings.TrimSpace(options.SourceName) != "" ||
		strings.TrimSpace(options.BatchID) != "" ||
		strings.TrimSpace(options.QueryProfile) != ""
}

func providerDirectoryCandidateLimit(finalLimit int) int {
	// QueryDirectory orders by updated_at, while the CLI sorts enriched rows by
	// display fields. Keep a candidate window so small --limit values apply only
	// after the CLI's deterministic sort.
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
