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
	BaseURL         string
	PrimarySchema   string
	SecondarySchema string
	Encrypted       bool
	GrantID         string
	ChannelID       string
	AssessorPeerID  string
	Format          string
	Limit           int
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
	cmd.Flags().StringVar(&options.AssessorPeerID, "assessor-peer-id", "", "mutually chosen assessor peer ID")
	cmd.Flags().StringVar(&options.Format, "format", "table", "output format: table, json, csv")
	cmd.Flags().IntVar(&options.Limit, "limit", 100, "maximum events")
}

func runConjunctionScreen(ctx context.Context, out io.Writer, options conjunctionScreenOptions) error {
	baseURL, err := conjunctionBaseURL(options.BaseURL)
	if err != nil {
		return err
	}
	primarySchema, err := normalizeSyncSchemaName(options.PrimarySchema)
	if err != nil {
		return err
	}
	secondarySchema, err := normalizeSyncSchemaName(options.SecondarySchema)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"primary_schema":     primarySchema,
		"secondary_schema":   secondarySchema,
		"encrypted":          options.Encrypted,
		"include_provenance": true,
	}
	if value := strings.TrimSpace(options.GrantID); value != "" {
		payload["grant_id"] = value
	}
	if value := strings.TrimSpace(options.ChannelID); value != "" {
		payload["channel_id"] = value
	}
	if value := strings.TrimSpace(options.AssessorPeerID); value != "" {
		payload["assessor_peer_id"] = value
	}
	if options.Limit > 0 {
		payload["limit"] = options.Limit
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

func conjunctionBaseURL(explicit string) (string, error) {
	if value := strings.TrimSpace(explicit); value != "" {
		return value, nil
	}
	cfg, err := config.Load(configPath)
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
