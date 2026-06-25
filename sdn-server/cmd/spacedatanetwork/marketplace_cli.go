package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/storefront"
)

type marketplaceOptions struct {
	APIURL       string
	Format       string
	ProviderID   string
	DataTypes    []string
	ListingKinds []string
	Tags         []string
	AccessTypes  []string
	Limit        int
	Offset       int
}

type marketplaceListingsResponse struct {
	Listings []storefront.Listing `json:"listings"`
	Total    int                  `json:"total"`
}

func init() {
	rootCmd.AddCommand(newMarketplaceCommand())
}

func newMarketplaceCommand() *cobra.Command {
	options := marketplaceOptions{Format: "table", Limit: 50}
	cmd := &cobra.Command{
		Use:   "marketplace",
		Short: "Search and inspect SDN marketplace listings",
	}
	cmd.PersistentFlags().StringVar(&options.APIURL, "api", "", "local SDN daemon API base URL (default: SDN_API_URL or config admin address)")
	cmd.PersistentFlags().StringVar(&options.APIURL, "api-url", "", "alias for --api")
	cmd.PersistentFlags().String("session-token", "", "SDN wallet session token for auth-enabled APIs (default: SDN_SESSION_TOKEN)")
	cmd.PersistentFlags().StringVar(&options.Format, "format", "table", "output format: table, json, csv")
	cmd.PersistentFlags().StringVar(&options.ProviderID, "provider-id", "", "provider peer ID filter")
	cmd.PersistentFlags().StringArrayVar(&options.DataTypes, "standard", nil, "Space Data Standards code/data type filter, repeatable")
	cmd.PersistentFlags().StringArrayVar(&options.DataTypes, "data-type", nil, "alias for --standard")
	cmd.PersistentFlags().StringArrayVar(&options.ListingKinds, "kind", nil, "listing kind filter: data_stream or wasm_module, repeatable")
	cmd.PersistentFlags().StringArrayVar(&options.Tags, "tag", nil, "listing tag filter, repeatable")
	cmd.PersistentFlags().StringArrayVar(&options.AccessTypes, "access-type", nil, "access type filter: one-time, subscription, streaming, query")
	cmd.PersistentFlags().IntVar(&options.Limit, "limit", 50, "maximum results")
	cmd.PersistentFlags().IntVar(&options.Offset, "offset", 0, "result offset")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List marketplace listings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMarketplaceSearch(cmd.Context(), cmd.OutOrStdout(), cmd, "", options)
		},
	}
	searchCmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search marketplace listings",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) > 0 {
				query = args[0]
			}
			return runMarketplaceSearch(cmd.Context(), cmd.OutOrStdout(), cmd, query, options)
		},
	}
	showCmd := &cobra.Command{
		Use:   "show <listingId>",
		Short: "Show one marketplace listing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMarketplaceShow(cmd.Context(), cmd.OutOrStdout(), cmd, args[0], options)
		},
	}
	cmd.AddCommand(listCmd, searchCmd, showCmd)
	return cmd
}

func runMarketplaceSearch(ctx context.Context, out io.Writer, cmd *cobra.Command, searchText string, options marketplaceOptions) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	query, err := marketplaceSearchQuery(searchText, options)
	if err != nil {
		return err
	}
	body, err := json.Marshal(query)
	if err != nil {
		return err
	}
	baseURL, err := searchAPIBaseURL(options.APIURL)
	if err != nil {
		return err
	}
	endpoint, err := marketplaceAPIURL(baseURL, "/api/storefront/listings/search")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	prepareChannelAPIRequest(cmd, req)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("search marketplace listings: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("search marketplace listings: %s", resp.Status)
	}
	var decoded marketplaceListingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("decode marketplace listings: %w", err)
	}
	rows := marketplaceListingRows(decoded.Listings)
	count := decoded.Total
	if count == 0 {
		count = len(rows)
	}
	return writeSearchResult(out, searchResult{Count: count, Results: rows}, marketplaceListingFields(), format)
}

func runMarketplaceShow(ctx context.Context, out io.Writer, cmd *cobra.Command, listingID string, options marketplaceOptions) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	baseURL, err := searchAPIBaseURL(options.APIURL)
	if err != nil {
		return err
	}
	endpoint, err := marketplaceAPIURL(baseURL, "/api/storefront/listings/"+url.PathEscape(strings.TrimSpace(listingID)))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	prepareChannelAPIRequest(cmd, req)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("show marketplace listing: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("show marketplace listing: %s", resp.Status)
	}
	var listing storefront.Listing
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return fmt.Errorf("decode marketplace listing: %w", err)
	}
	return writeSearchResult(out, searchResult{Count: 1, Results: marketplaceListingRows([]storefront.Listing{listing})}, marketplaceListingFields(), format)
}

func marketplaceSearchQuery(searchText string, options marketplaceOptions) (storefront.SearchQuery, error) {
	kinds, err := marketplaceListingKinds(options.ListingKinds)
	if err != nil {
		return storefront.SearchQuery{}, err
	}
	accessTypes, err := marketplaceAccessTypes(options.AccessTypes)
	if err != nil {
		return storefront.SearchQuery{}, err
	}
	providers := splitMarketplaceValues([]string{options.ProviderID})
	return storefront.SearchQuery{
		SearchText:      strings.TrimSpace(searchText),
		ProviderPeerIDs: providers,
		DataTypes:       splitMarketplaceValues(options.DataTypes),
		ListingKinds:    kinds,
		Tags:            splitMarketplaceValues(options.Tags),
		AccessTypes:     accessTypes,
		Limit:           options.Limit,
		Offset:          options.Offset,
	}, nil
}

func marketplaceListingKinds(values []string) ([]storefront.ListingKind, error) {
	normalized := splitMarketplaceValues(values)
	kinds := make([]storefront.ListingKind, 0, len(normalized))
	for _, value := range normalized {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "data", "stream", "data-stream", "data_stream":
			kinds = append(kinds, storefront.ListingKindDataStream)
		case "module", "wasm", "wasm-module", "wasm_module":
			kinds = append(kinds, storefront.ListingKindWASMModule)
		default:
			return nil, fmt.Errorf("unsupported marketplace listing kind %q (use data_stream or wasm_module)", value)
		}
	}
	return kinds, nil
}

func marketplaceAccessTypes(values []string) ([]storefront.AccessType, error) {
	normalized := splitMarketplaceValues(values)
	accessTypes := make([]storefront.AccessType, 0, len(normalized))
	for _, value := range normalized {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "0", "one-time", "one_time", "onetime":
			accessTypes = append(accessTypes, storefront.AccessTypeOneTime)
		case "1", "subscription":
			accessTypes = append(accessTypes, storefront.AccessTypeSubscription)
		case "2", "streaming", "stream":
			accessTypes = append(accessTypes, storefront.AccessTypeStreaming)
		case "3", "query":
			accessTypes = append(accessTypes, storefront.AccessTypeQuery)
		default:
			return nil, fmt.Errorf("unsupported access type %q (use one-time, subscription, streaming, query)", value)
		}
	}
	return accessTypes, nil
}

func splitMarketplaceValues(values []string) []string {
	var result []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				result = append(result, trimmed)
			}
		}
	}
	return result
}

func marketplaceAPIURL(baseURL string, path string) (string, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid api-url: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid api-url: absolute URL required")
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), nil
}

func marketplaceListingRows(listings []storefront.Listing) []map[string]any {
	rows := make([]map[string]any, 0, len(listings))
	for _, listing := range listings {
		price, currency := marketplaceListingPrice(listing)
		policy := listing.ProtectedDelivery.FieldStreamPolicy
		row := map[string]any{
			"listing_id":          listing.ListingID,
			"listing_kind":        string(listing.ListingKind),
			"provider_peer_id":    listing.ProviderPeerID,
			"title":               listing.Title,
			"data_types":          listing.DataTypes,
			"tags":                listing.Tags,
			"access_type":         marketplaceAccessTypeString(listing.AccessType),
			"encryption_required": listing.EncryptionRequired,
			"delivery_methods":    listing.DeliveryMethods,
			"module_id":           listing.ProtectedDelivery.ModuleID,
			"stream_id":           "",
			"schema_code":         "",
			"policy_id":           "",
			"key_epoch":           "",
			"price":               price,
			"currency":            currency,
			"active":              listing.Active,
		}
		if policy != nil {
			row["stream_id"] = policy.StreamID
			row["schema_code"] = policy.SchemaCode
			row["policy_id"] = policy.PolicyID
			row["key_epoch"] = policy.KeyEpoch
		}
		rows = append(rows, row)
	}
	return rows
}

func marketplaceListingPrice(listing storefront.Listing) (uint64, string) {
	if len(listing.Pricing) == 0 {
		return 0, ""
	}
	return listing.Pricing[0].PriceAmount, listing.Pricing[0].PriceCurrency
}

func marketplaceAccessTypeString(accessType storefront.AccessType) string {
	switch accessType {
	case storefront.AccessTypeOneTime:
		return "one-time"
	case storefront.AccessTypeSubscription:
		return "subscription"
	case storefront.AccessTypeStreaming:
		return "streaming"
	case storefront.AccessTypeQuery:
		return "query"
	default:
		return "access_type(" + strconv.Itoa(int(accessType)) + ")"
	}
}

func marketplaceListingFields() []string {
	return []string{
		"listing_id",
		"listing_kind",
		"provider_peer_id",
		"title",
		"data_types",
		"tags",
		"access_type",
		"encryption_required",
		"delivery_methods",
		"module_id",
		"stream_id",
		"schema_code",
		"policy_id",
		"key_epoch",
		"price",
		"currency",
		"active",
	}
}
