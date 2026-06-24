package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

type providersSharedOptions struct {
	Schema       string
	ProviderID   string
	SourceName   string
	BatchID      string
	QueryProfile string
	Mode         string
	Format       string
	Limit        int
	BaseURL      string
	SessionToken string
	ProviderURL  string
	Target       string
	IncludeData  bool
}

type providersDescriptorOptions struct {
	ProviderURL string
	Format      string
}

type providersConnectOptions struct {
	ProviderURL string
	Target      string
	Format      string
}

type providersQueryOptions struct {
	BaseURL      string
	Schema       string
	ProviderID   string
	SourceName   string
	BatchID      string
	QueryProfile string
	Format       string
	Limit        int
	IncludeData  bool
}

var providersOptionsState = providersSharedOptions{
	Format:      "table",
	Limit:       100,
	ProviderURL: defaultModulePublishProviderURL,
}

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "List, search, inspect, connect to, and query SDN providers",
	Long: `List, search, inspect, connect to, and query SDN providers.

Provider commands share the same provider/search filters as data search:
--schema, --provider-id, --source-name, --batch-id, --query-profile,
--mode local|daemon|live-dht, --limit, and --format table|json|csv.`,
}

var providersConnectCmd = &cobra.Command{
	Use:   "connect [provider-url]",
	Short: "Resolve a provider descriptor to a libp2p connection target",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		options := providersConnectOptions{
			ProviderURL: providerURLArg(args),
			Target:      providersOptionsState.Target,
			Format:      providersOptionsState.Format,
		}
		return runProvidersConnect(cmd.Context(), cmd.OutOrStdout(), options)
	},
}

var providersDescriptorCmd = &cobra.Command{
	Use:   "descriptor [provider-url]",
	Short: "Fetch a provider descriptor",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		options := providersDescriptorOptions{
			ProviderURL: providerURLArg(args),
			Format:      providersOptionsState.Format,
		}
		return runProvidersDescriptor(cmd.Context(), cmd.OutOrStdout(), options)
	},
}

var providersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List known provider directory records",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runProvidersList(cmd.OutOrStdout(), providersOptionsState)
	},
}

var providersQueryCmd = &cobra.Command{
	Use:   "query",
	Short: "Query provider data through the unified SDN data query endpoint",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		options := providersQueryOptions{
			BaseURL:      providersOptionsState.BaseURL,
			Schema:       providersOptionsState.Schema,
			ProviderID:   providersOptionsState.ProviderID,
			SourceName:   providersOptionsState.SourceName,
			BatchID:      providersOptionsState.BatchID,
			QueryProfile: providersOptionsState.QueryProfile,
			Format:       providersOptionsState.Format,
			Limit:        providersOptionsState.Limit,
			IncludeData:  providersOptionsState.IncludeData,
		}
		return runProvidersQuery(cmd.Context(), cmd.OutOrStdout(), options)
	},
}

var providersSearchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search provider directory records",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := ""
		if len(args) > 0 {
			query = args[0]
		}
		return runProvidersSearch(cmd.OutOrStdout(), query, providersOptionsState)
	},
}

var providersShowCmd = &cobra.Command{
	Use:   "show <provider>",
	Short: "Show one provider directory record",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runProvidersShow(cmd.OutOrStdout(), args[0], providersOptionsState)
	},
}

func init() {
	addProvidersSharedFlags(providersCmd, &providersOptionsState)
	providersCmd.AddCommand(
		providersConnectCmd,
		providersDescriptorCmd,
		providersListCmd,
		providersQueryCmd,
		providersSearchCmd,
		providersShowCmd,
	)
}

func addProvidersSharedFlags(cmd *cobra.Command, options *providersSharedOptions) {
	cmd.PersistentFlags().StringVar(&options.Schema, "schema", "", "schema name or three-letter abbreviation, for example OMM or OMM.fbs")
	cmd.PersistentFlags().StringVar(&options.ProviderID, "provider-id", "", "provider ID, peer ID, public key, xpub, address, ENS, SNS, IPFS CID, or IPNS")
	cmd.PersistentFlags().StringVar(&options.SourceName, "source-name", "", "provider source/feed name")
	cmd.PersistentFlags().StringVar(&options.BatchID, "batch-id", "", "source batch ID")
	cmd.PersistentFlags().StringVar(&options.QueryProfile, "query-profile", "", "sync query profile")
	cmd.PersistentFlags().StringVar(&options.Mode, "mode", "local", "search mode for list/search/show: local, daemon, live-dht")
	cmd.PersistentFlags().StringVar(&options.Format, "format", "table", "output format: table, json, csv")
	cmd.PersistentFlags().IntVar(&options.Limit, "limit", 100, "maximum results")
	cmd.PersistentFlags().StringVar(&options.BaseURL, "api", "", "local SDN daemon API base URL for provider query and daemon/live-dht search")
	cmd.PersistentFlags().StringVar(&options.BaseURL, "api-url", "", "alias for --api")
	cmd.PersistentFlags().StringVar(&options.SessionToken, "session-token", "", "SDN wallet session token for auth-enabled APIs (default: SDN_SESSION_TOKEN)")
	cmd.PersistentFlags().StringVar(&options.ProviderURL, "provider-url", defaultModulePublishProviderURL, "provider descriptor URL")
	cmd.PersistentFlags().StringVar(&options.Target, "target", "", "explicit provider multiaddr including /p2p/<peer-id>")
	cmd.PersistentFlags().BoolVar(&options.IncludeData, "include-data", false, "include data_base64 in provider query JSON responses")
}

func providerURLArg(args []string) string {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return strings.TrimSpace(args[0])
	}
	return providersOptionsState.ProviderURL
}

func runProvidersList(out io.Writer, options providersSharedOptions) error {
	return runProvidersSearch(out, "", options)
}

func runProvidersSearch(out io.Writer, query string, options providersSharedOptions) error {
	return runSearchProviders(out, searchProviderOptions{
		Query:        query,
		Schema:       options.Schema,
		ProviderID:   options.ProviderID,
		SourceName:   options.SourceName,
		BatchID:      options.BatchID,
		QueryProfile: options.QueryProfile,
		Mode:         options.Mode,
		APIURL:       options.BaseURL,
		SessionToken: options.SessionToken,
		Format:       options.Format,
		Limit:        options.Limit,
	})
}

func runProvidersShow(out io.Writer, provider string, options providersSharedOptions) error {
	options.ProviderID = strings.TrimSpace(provider)
	options.Limit = 1
	return runProvidersSearch(out, provider, options)
}

func runProvidersDescriptor(ctx context.Context, out io.Writer, options providersDescriptorOptions) error {
	descriptor, err := fetchProviderDescriptor(ctx, options.ProviderURL)
	if err != nil {
		return err
	}
	row := providerDescriptorRow(descriptor)
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	return writeSearchResult(out, searchResult{Count: 1, Results: []map[string]any{row}}, providerDescriptorFields(), format)
}

func runProvidersConnect(ctx context.Context, out io.Writer, options providersConnectOptions) error {
	target, err := resolveModulePublishTarget(ctx, options.ProviderURL, options.Target)
	if err != nil {
		return err
	}
	row := map[string]any{
		"target":       target,
		"provider_url": options.ProviderURL,
	}
	if descriptor, err := fetchProviderDescriptor(ctx, options.ProviderURL); err == nil {
		row["peer_id"] = descriptor.PeerID
		row["public_key"] = descriptor.PublicKey
	}
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	return writeSearchResult(out, searchResult{Count: 1, Results: []map[string]any{row}}, []string{"target", "peer_id", "public_key", "provider_url"}, format)
}

func runProvidersQuery(ctx context.Context, out io.Writer, options providersQueryOptions) error {
	baseURL, err := providersQueryBaseURL(options.BaseURL)
	if err != nil {
		return err
	}
	payload := map[string]any{}
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
	if options.Limit > 0 {
		payload["limit"] = options.Limit
	}
	if options.IncludeData {
		payload["include_data"] = true
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := strings.TrimRight(baseURL, "/") + "/api/v1/data/query"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("query provider data: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("query provider data: %s", resp.Status)
	}

	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("decode provider query response: %w", err)
	}
	records, _ := decoded["records"].([]any)
	rows := make([]map[string]any, 0, len(records))
	for _, record := range records {
		if row, ok := record.(map[string]any); ok {
			rows = append(rows, row)
		}
	}
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	return writeSearchResult(out, searchResult{Count: len(rows), Results: rows}, providerQueryFields(rows), format)
}

func providersQueryBaseURL(explicit string) (string, error) {
	if value := strings.TrimSpace(explicit); value != "" {
		return value, nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	return adminURL(cfg), nil
}

func providerDescriptorFields() []string {
	return []string{"peer_id", "public_key", "ipns", "relay_addresses"}
}

func providerDescriptorRow(descriptor *providerDescriptorResponse) map[string]any {
	row := map[string]any{
		"peer_id":    descriptor.PeerID,
		"public_key": descriptor.PublicKey,
	}
	if descriptor.IPNS != "" {
		row["ipns"] = descriptor.IPNS
	}
	if len(descriptor.RelayAddresses) > 0 {
		row["relay_addresses"] = strings.Join(descriptor.RelayAddresses, " ")
	}
	return row
}

func providerQueryFields(rows []map[string]any) []string {
	fields := []string{"schema_name", "provider_id", "source_name", "batch_id", "query_profile", "cid", "peer_id", "timestamp", "size_bytes", "data_base64"}
	seen := map[string]bool{}
	for _, field := range fields {
		seen[field] = true
	}
	for _, row := range rows {
		for field := range row {
			if seen[field] {
				continue
			}
			fields = append(fields, field)
			seen[field] = true
		}
	}
	return fields
}
