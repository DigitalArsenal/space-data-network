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

type conjunctionScreenOptions struct {
	BaseURL             string
	PrimarySchema       string
	SecondarySchema     string
	Encrypted           bool
	GrantID             string
	ChannelID           string
	ResultChannelID     string
	AssessorPeerID      string
	PrimaryProviderID   string
	PrimarySourceName   string
	PrimaryPNMCID       string
	PrimaryQuery        string
	SecondaryProviderID string
	SecondarySourceName string
	SecondaryPNMCID     string
	SecondaryQuery      string
	ModuleID            string
	ModuleVersion       string
	DryRun              bool
	Format              string
	Limit               int
}

var conjunctionOptionsState struct {
	Screen conjunctionScreenOptions
}

var conjunctionCmd = &cobra.Command{
	Use:   "conjunction",
	Short: "Run SDN conjunction assessment workflows",
}

var conjunctionScreenCmd = &cobra.Command{
	Use:   "screen",
	Short: "Screen conjunction risk, including encrypted maneuver ephemeris workflows",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConjunctionScreen(cmd.Context(), cmd.OutOrStdout(), conjunctionOptionsState.Screen)
	},
}

func init() {
	conjunctionOptionsState.Screen.Format = "table"
	conjunctionOptionsState.Screen.PrimarySchema = "MPE"
	conjunctionOptionsState.Screen.SecondarySchema = "OMM"
	conjunctionOptionsState.Screen.Limit = 100
	addConjunctionScreenFlags(conjunctionScreenCmd, &conjunctionOptionsState.Screen)
	conjunctionCmd.AddCommand(conjunctionScreenCmd)
}

func addConjunctionScreenFlags(cmd *cobra.Command, options *conjunctionScreenOptions) {
	cmd.Flags().StringVar(&options.BaseURL, "api", "", "local SDN daemon API base URL")
	cmd.Flags().StringVar(&options.PrimarySchema, "primary-schema", "MPE", "primary ephemeris schema, for example MPE or OEM")
	cmd.Flags().StringVar(&options.SecondarySchema, "secondary-schema", "OMM", "secondary/catalog ephemeris schema, for example OMM or OEM")
	cmd.Flags().BoolVar(&options.Encrypted, "encrypted", true, "screen encrypted private maneuver ephemeris")
	cmd.Flags().StringVar(&options.GrantID, "grant-id", "", "private data grant ID used to retrieve encrypted ephemeris")
	cmd.Flags().StringVar(&options.ChannelID, "channel-id", "", "private SDN channel ID for encrypted CA exchange")
	cmd.Flags().StringVar(&options.ResultChannelID, "result-channel-id", "", "private SDN channel ID for encrypted CA result delivery")
	cmd.Flags().StringVar(&options.AssessorPeerID, "assessor-peer-id", "", "mutually chosen assessor peer ID")
	cmd.Flags().StringVar(&options.PrimaryProviderID, "primary-provider-id", "", "provider ID for the primary ephemeris source")
	cmd.Flags().StringVar(&options.PrimarySourceName, "primary-source-name", "", "source name for the primary ephemeris source")
	cmd.Flags().StringVar(&options.PrimaryPNMCID, "primary-pnm-cid", "", "PNM CID for the primary ephemeris source")
	cmd.Flags().StringVar(&options.PrimaryQuery, "primary-query", "", "query used to retrieve primary ephemeris records")
	cmd.Flags().StringVar(&options.SecondaryProviderID, "secondary-provider-id", "", "provider ID for the secondary ephemeris source")
	cmd.Flags().StringVar(&options.SecondarySourceName, "secondary-source-name", "", "source name for the secondary ephemeris source")
	cmd.Flags().StringVar(&options.SecondaryPNMCID, "secondary-pnm-cid", "", "PNM CID for the secondary ephemeris source")
	cmd.Flags().StringVar(&options.SecondaryQuery, "secondary-query", "", "query used to retrieve secondary ephemeris records")
	cmd.Flags().StringVar(&options.ModuleID, "module-id", "com.space-data-network.conjunction-assessment", "conjunction assessment module ID")
	cmd.Flags().StringVar(&options.ModuleVersion, "module-version", "latest", "conjunction assessment module version")
	cmd.Flags().BoolVar(&options.DryRun, "dry-run", false, "print the encrypted CA request and provenance without contacting the daemon")
	cmd.Flags().StringVar(&options.Format, "format", "table", "output format: table, json, csv")
	cmd.Flags().IntVar(&options.Limit, "limit", 100, "maximum events")
}

func runConjunctionScreen(ctx context.Context, out io.Writer, options conjunctionScreenOptions) error {
	primarySchema, err := normalizeSyncSchemaName(options.PrimarySchema)
	if err != nil {
		return err
	}
	secondarySchema, err := normalizeSyncSchemaName(options.SecondarySchema)
	if err != nil {
		return err
	}

	payload := conjunctionScreenPayload(options, primarySchema, secondarySchema)

	if options.DryRun {
		format, err := normalizeSearchFormat(options.Format)
		if err != nil {
			return err
		}
		result := conjunctionDryRunResult(options, primarySchema, secondarySchema, payload)
		if format == searchOutputJSON {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}
		events := conjunctionEventRows(result["events"])
		return writeSearchResult(out, searchResult{Count: len(events), Results: events}, conjunctionEventFields(events), format)
	}

	baseURL, err := conjunctionBaseURL(options.BaseURL)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := strings.TrimRight(baseURL, "/") + "/api/v1/conjunction/screen"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("screen conjunctions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("screen conjunctions: %s", resp.Status)
	}

	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("decode conjunction screen response: %w", err)
	}
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	if format == searchOutputJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(decoded)
	}
	events := conjunctionEventRows(decoded["events"])
	return writeSearchResult(out, searchResult{Count: len(events), Results: events}, conjunctionEventFields(events), format)
}

func conjunctionScreenPayload(options conjunctionScreenOptions, primarySchema, secondarySchema string) map[string]any {
	payload := map[string]any{
		"primary_schema":     primarySchema,
		"secondary_schema":   secondarySchema,
		"encrypted":          options.Encrypted,
		"include_provenance": true,
		"sources": []map[string]any{
			conjunctionSourceSelection("primary", primarySchema, options.PrimaryProviderID, options.PrimarySourceName, options.PrimaryPNMCID, options.PrimaryQuery, options.Encrypted),
			conjunctionSourceSelection("secondary", secondarySchema, options.SecondaryProviderID, options.SecondarySourceName, options.SecondaryPNMCID, options.SecondaryQuery, false),
		},
		"module": map[string]any{
			"id":      firstConjunctionString(options.ModuleID, "com.space-data-network.conjunction-assessment"),
			"version": firstConjunctionString(options.ModuleVersion, "latest"),
		},
	}
	if value := strings.TrimSpace(options.GrantID); value != "" {
		payload["grant_id"] = value
	}
	if value := strings.TrimSpace(options.ChannelID); value != "" {
		payload["channel_id"] = value
	}
	if value := strings.TrimSpace(options.ResultChannelID); value != "" {
		payload["result_channel_id"] = value
	}
	if value := strings.TrimSpace(options.AssessorPeerID); value != "" {
		payload["assessor_peer_id"] = value
	}
	if options.Limit > 0 {
		payload["limit"] = options.Limit
	}
	return payload
}

func conjunctionDryRunResult(options conjunctionScreenOptions, primarySchema, secondarySchema string, payload map[string]any) map[string]any {
	sources, _ := payload["sources"].([]map[string]any)
	module, _ := payload["module"].(map[string]any)
	result := map[string]any{
		"workflow":          "encrypted-conjunction-assessment",
		"mode":              conjunctionWorkflowMode(options.Encrypted, primarySchema),
		"status":            "dry-run",
		"primary_schema":    primarySchema,
		"secondary_schema":  secondarySchema,
		"encrypted":         options.Encrypted,
		"count":             0,
		"events":            []map[string]any{},
		"sources":           sources,
		"grant_id":          strings.TrimSpace(options.GrantID),
		"channel_id":        strings.TrimSpace(options.ChannelID),
		"result_channel_id": strings.TrimSpace(options.ResultChannelID),
		"assessor_peer_id":  strings.TrimSpace(options.AssessorPeerID),
		"provenance": map[string]any{
			"run_at":            time.Now().UTC().Format(time.RFC3339),
			"dry_run":           true,
			"source_schemas":    []string{primarySchema, secondarySchema},
			"sources":           sources,
			"module":            module,
			"grant_id":          strings.TrimSpace(options.GrantID),
			"channel_id":        strings.TrimSpace(options.ChannelID),
			"result_channel_id": strings.TrimSpace(options.ResultChannelID),
			"assessor_peer_id":  strings.TrimSpace(options.AssessorPeerID),
			"ca_configuration": map[string]any{
				"primary_schema":   primarySchema,
				"secondary_schema": secondarySchema,
				"encrypted":        options.Encrypted,
				"limit":            options.Limit,
			},
		},
	}
	return result
}

func conjunctionSourceSelection(role, schema, providerID, sourceName, pnmCID, query string, encrypted bool) map[string]any {
	return map[string]any{
		"role":        role,
		"schema":      schema,
		"provider_id": strings.TrimSpace(providerID),
		"source_name": strings.TrimSpace(sourceName),
		"pnm_cid":     strings.TrimSpace(pnmCID),
		"query":       strings.TrimSpace(query),
		"encrypted":   encrypted,
	}
}

func conjunctionWorkflowMode(encrypted bool, primarySchema string) string {
	if encrypted && strings.EqualFold(primarySchema, "MPE.fbs") {
		return "private-maneuver-ephemeris"
	}
	if encrypted {
		return "private-ephemeris"
	}
	return "local-screening"
}

func firstConjunctionString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func conjunctionBaseURL(explicit string) (string, error) {
	if value := strings.TrimSpace(explicit); value != "" {
		return value, nil
	}
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return "", err
	}
	return adminURL(cfg), nil
}

func conjunctionEventRows(value any) []map[string]any {
	events, _ := value.([]any)
	rows := make([]map[string]any, 0, len(events))
	for _, event := range events {
		if row, ok := event.(map[string]any); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func conjunctionEventFields(rows []map[string]any) []string {
	fields := []string{"event_id", "primary_schema", "secondary_schema", "screening_result", "min_range_m", "tca", "provenance_hash"}
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
