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
