package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

type channelsListOptions struct {
	StandardCode string
}

type channelSubscriptionOptions struct {
	Visibility string
}

type channelGrantIssueOptions struct {
	To        string
	Scopes    []string
	ExpiresAt string
}

type channelPublishOptions struct {
	From   string
	APIURL string
}

type channelMonitorOptions struct {
	APIURL string
}

func init() {
	rootCmd.AddCommand(newChannelsCommand())
}

func newChannelsCommand() *cobra.Command {
	listOptions := channelsListOptions{}
	subscribeOptions := channelSubscriptionOptions{}
	unsubscribeOptions := channelSubscriptionOptions{}
	monitorOptions := channelMonitorOptions{}
	subscriptions := channels.NewSubscriptionRegistry()
	grants := channels.NewChannelGrantRegistry()
	cmd := &cobra.Command{
		Use:   "channels",
		Short: "List, inspect, subscribe, and monitor SDN data channels",
	}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List discoverable SDN channels by standardCode",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsList(cmd, listOptions)
		},
	}
	listCmd.Flags().StringVar(&listOptions.StandardCode, "standard", "", "three-letter Space Data Standards record code")

	cmd.AddCommand(listCmd)
	cmd.AddCommand(&cobra.Command{
		Use:   "show <channelId>",
		Short: "Show channel metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsShow(cmd, args[0])
		},
	})
	monitorCmd := &cobra.Command{
		Use:   "monitor <channelId>",
		Short: "Print channel synchronization status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsMonitor(cmd, monitorOptions, args[0])
		},
	}
	monitorCmd.Flags().StringVar(&monitorOptions.APIURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	cmd.AddCommand(monitorCmd)
	subscribeCmd := &cobra.Command{
		Use:   "subscribe <channelId>",
		Short: "Subscribe to a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsSubscribe(cmd, subscriptions, subscribeOptions, args[0])
		},
	}
	subscribeCmd.Flags().StringVar(&subscribeOptions.Visibility, "visibility", "public", "channel visibility")
	cmd.AddCommand(subscribeCmd)
	unsubscribeCmd := &cobra.Command{
		Use:   "unsubscribe <channelId>",
		Short: "Unsubscribe from a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsUnsubscribe(cmd, subscriptions, unsubscribeOptions, args[0])
		},
	}
	unsubscribeCmd.Flags().StringVar(&unsubscribeOptions.Visibility, "visibility", "public", "channel visibility")
	cmd.AddCommand(unsubscribeCmd)
	publishOptions := channelPublishOptions{}
	publishCmd := &cobra.Command{
		Use:   "publish <channelId>",
		Short: "Publish a native FlatBuffers channel stream",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsPublish(cmd, publishOptions, args[0])
		},
	}
	publishCmd.Flags().StringVar(&publishOptions.From, "from", "", "native FlatBuffers stream file to publish")
	publishCmd.Flags().StringVar(&publishOptions.APIURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	cmd.AddCommand(publishCmd)
	grantsCmd := &cobra.Command{
		Use:   "grants",
		Short: "Manage private channel grants",
	}
	grantIssueOptions := channelGrantIssueOptions{}
	grantIssueCmd := &cobra.Command{
		Use:   "issue <channelId>",
		Short: "Issue a private channel grant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsGrantIssue(cmd, grants, grantIssueOptions, args[0])
		},
	}
	grantIssueCmd.Flags().StringVar(&grantIssueOptions.To, "to", "", "subscriber peer or EPM subject")
	grantIssueCmd.Flags().StringArrayVar(&grantIssueOptions.Scopes, "scope", nil, "private channel access scope")
	grantIssueCmd.Flags().StringVar(&grantIssueOptions.ExpiresAt, "expires-at", "", "grant expiration as RFC3339")
	grantsCmd.AddCommand(grantIssueCmd)
	cmd.AddCommand(grantsCmd)
	return cmd
}

func runChannelsList(cmd *cobra.Command, options channelsListOptions) error {
	out := cmd.OutOrStdout()
	if strings.TrimSpace(options.StandardCode) != "" {
		code, err := channels.AssertStandardCode(options.StandardCode)
		if err != nil {
			return err
		}
		printChannelListRow(out, code)
		return nil
	}
	for _, schemaName := range sds.SupportedSchemas {
		code, err := channels.StandardCodeFromSchemaName(schemaName)
		if err != nil {
			continue
		}
		printChannelListRow(out, code)
	}
	return nil
}

func runChannelsPublish(cmd *cobra.Command, options channelPublishOptions, channelID string) error {
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
	}
	from := strings.TrimSpace(options.From)
	if from == "" {
		return fmt.Errorf("--from is required")
	}
	streamBytes, err := os.ReadFile(from)
	if err != nil {
		return fmt.Errorf("read native FlatBuffers stream: %w", err)
	}
	frames, err := channels.SplitNativeStreamFrames(streamBytes)
	if err != nil {
		return fmt.Errorf("invalid native FlatBuffers stream: %w", err)
	}
	if apiURL := firstNonEmptyChannelOption(strings.TrimSpace(options.APIURL), strings.TrimSpace(os.Getenv("SDN_API_URL"))); apiURL != "" {
		return runChannelsPublishToAPI(cmd, parsed, apiURL, streamBytes)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "channelId=%s\n", parsed.ChannelID)
	fmt.Fprintf(out, "sourceId=%s\n", parsed.SourceID)
	fmt.Fprintf(out, "standardCode=%s\n", parsed.StandardCode)
	if parsed.FeedUUID != "" {
		fmt.Fprintf(out, "feedUuid=%s\n", parsed.FeedUUID)
	}
	fmt.Fprintln(out, "contentType=application/vnd.sdn.flatbuffers.stream")
	fmt.Fprintf(out, "streamBytes=%d\n", len(streamBytes))
	fmt.Fprintf(out, "streamFrames=%d\n", len(frames))
	return nil
}

func runChannelsPublishToAPI(cmd *cobra.Command, parsed channels.ChannelID, apiURL string, streamBytes []byte) error {
	publishURL, err := channelPublishURL(apiURL, parsed.ChannelID)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, publishURL, bytes.NewReader(streamBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("publish native channel stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("publish native channel stream: %s", resp.Status)
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("decode channel publish response: %w", err)
	}
	if _, ok := payload["channelId"]; !ok {
		payload["channelId"] = parsed.ChannelID
	}
	if _, ok := payload["sourceId"]; !ok {
		payload["sourceId"] = parsed.SourceID
	}
	if _, ok := payload["standardCode"]; !ok {
		payload["standardCode"] = parsed.StandardCode
	}
	printChannelPublishPayload(cmd.OutOrStdout(), payload)
	return nil
}

func printChannelListRow(out interface {
	Write([]byte) (int, error)
}, standardCode string) {
	fmt.Fprintf(out, "standardCode=%s topic=%s visibility=public subscribed=false grantState=not-required encryptionState=none\n", standardCode, channels.DiscoveryTopic(standardCode))
}

func runChannelsSubscribe(cmd *cobra.Command, registry *channels.SubscriptionRegistry, options channelSubscriptionOptions, channelID string) error {
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(options.Visibility), "private") {
		return fmt.Errorf("verified channel grant required for %s", parsed.ChannelID)
	}
	return printChannelSubscriptionState(cmd, registry.Subscribe(parsed))
}

func runChannelsUnsubscribe(cmd *cobra.Command, registry *channels.SubscriptionRegistry, options channelSubscriptionOptions, channelID string) error {
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(options.Visibility), "private") {
		return fmt.Errorf("verified channel grant required for %s", parsed.ChannelID)
	}
	return printChannelSubscriptionState(cmd, registry.Unsubscribe(parsed))
}

func printChannelSubscriptionState(cmd *cobra.Command, state channels.SubscriptionState) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "channelId=%s\n", state.ChannelID)
	fmt.Fprintf(out, "subscribed=%t\n", state.Subscribed)
	fmt.Fprintf(out, "visibility=%s\n", state.Visibility)
	fmt.Fprintf(out, "grantState=%s\n", state.GrantState)
	fmt.Fprintf(out, "encryptionState=%s\n", state.EncryptionState)
	return nil
}

func runChannelsGrantIssue(cmd *cobra.Command, registry *channels.ChannelGrantRegistry, options channelGrantIssueOptions, channelID string) error {
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
	}
	scopes, err := parseChannelGrantScopes(options.Scopes)
	if err != nil {
		return err
	}
	expiresAt := time.Time{}
	if strings.TrimSpace(options.ExpiresAt) != "" {
		expiresAt, err = time.Parse(time.RFC3339, strings.TrimSpace(options.ExpiresAt))
		if err != nil {
			return fmt.Errorf("invalid expires-at: %w", err)
		}
	}
	grant, err := registry.Issue(channels.ChannelGrantIssueRequest{
		Channel:   parsed,
		Subject:   options.To,
		Scopes:    scopes,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "grantId=%s\n", grant.GrantID)
	fmt.Fprintf(out, "channelId=%s\n", grant.ChannelID)
	fmt.Fprintf(out, "subject=%s\n", grant.Subject)
	fmt.Fprintln(out, "grantState=verified")
	for _, scope := range grant.Scopes {
		fmt.Fprintf(out, "scope=%s\n", scope)
	}
	fmt.Fprintf(out, "expiresAt=%s\n", grant.ExpiresAt.Format(time.RFC3339Nano))
	return nil
}

func parseChannelGrantScopes(values []string) ([]channels.AccessBoundary, error) {
	if len(values) == 0 {
		return nil, nil
	}
	allowed := map[string]channels.AccessBoundary{
		string(channels.BoundarySubscribe):          channels.BoundarySubscribe,
		string(channels.BoundaryUnsubscribe):        channels.BoundaryUnsubscribe,
		string(channels.BoundaryPublish):            channels.BoundaryPublish,
		string(channels.BoundaryStreamOpen):         channels.BoundaryStreamOpen,
		string(channels.BoundaryByteRangeRead):      channels.BoundaryByteRangeRead,
		string(channels.BoundaryKeyUnwrap):          channels.BoundaryKeyUnwrap,
		string(channels.BoundaryShardImport):        channels.BoundaryShardImport,
		string(channels.BoundaryModuleFeedDelivery): channels.BoundaryModuleFeedDelivery,
		string(channels.BoundaryLocalCacheRead):     channels.BoundaryLocalCacheRead,
	}
	scopes := make([]channels.AccessBoundary, 0, len(values))
	for _, value := range values {
		scope := strings.TrimSpace(value)
		boundary, ok := allowed[scope]
		if !ok {
			return nil, fmt.Errorf("invalid channel grant scope %q", value)
		}
		scopes = append(scopes, boundary)
	}
	return scopes, nil
}

func runChannelsShow(cmd *cobra.Command, channelID string) error {
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "channelId=%s\n", parsed.ChannelID)
	fmt.Fprintf(out, "sourceId=%s\n", parsed.SourceID)
	fmt.Fprintf(out, "standardCode=%s\n", parsed.StandardCode)
	if parsed.FeedUUID != "" {
		fmt.Fprintf(out, "feedUuid=%s\n", parsed.FeedUUID)
	}
	fmt.Fprintln(out, "visibility=unknown")
	fmt.Fprintln(out, "pnmVerified=false")
	return nil
}

func runChannelsMonitor(cmd *cobra.Command, options channelMonitorOptions, channelID string) error {
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
	}
	if apiURL := firstNonEmptyChannelOption(strings.TrimSpace(options.APIURL), strings.TrimSpace(os.Getenv("SDN_API_URL"))); apiURL != "" {
		return runChannelsMonitorFromAPI(cmd, parsed, apiURL)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "channelId=%s\n", parsed.ChannelID)
	fmt.Fprintf(out, "sourceId=%s\n", parsed.SourceID)
	fmt.Fprintf(out, "standardCode=%s\n", parsed.StandardCode)
	fmt.Fprintln(out, "channelHead=")
	fmt.Fprintln(out, "pnmVerified=false")
	fmt.Fprintln(out, "providerPeer=")
	fmt.Fprintln(out, "localRows=0")
	fmt.Fprintln(out, "remoteRows=0")
	fmt.Fprintln(out, "syncedRows=0")
	fmt.Fprintln(out, "missingRows=0")
	fmt.Fprintln(out, "pinnedRows=0")
	fmt.Fprintln(out, "syncedBytes=0")
	fmt.Fprintln(out, "throughputBytesPerSecond=0")
	fmt.Fprintln(out, "wireSpeedUtilization=")
	fmt.Fprintln(out, "grantState=unknown")
	fmt.Fprintln(out, "encryptionState=unknown")
	fmt.Fprintln(out, "lastVerifiedUpdate=")
	return nil
}

func runChannelsMonitorFromAPI(cmd *cobra.Command, parsed channels.ChannelID, apiURL string) error {
	monitorURL, err := channelMonitorURL(apiURL, parsed.ChannelID)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, monitorURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch channel monitor: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fetch channel monitor: %s", resp.Status)
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("decode channel monitor: %w", err)
	}
	if _, ok := payload["channelId"]; !ok {
		payload["channelId"] = parsed.ChannelID
	}
	if _, ok := payload["sourceId"]; !ok {
		payload["sourceId"] = parsed.SourceID
	}
	if _, ok := payload["standardCode"]; !ok {
		payload["standardCode"] = parsed.StandardCode
	}
	printChannelMonitorPayload(cmd.OutOrStdout(), payload)
	return nil
}

func channelMonitorURL(apiURL string, channelID string) (string, error) {
	return channelAPIURL(apiURL, channelID, "/monitor")
}

func channelPublishURL(apiURL string, channelID string) (string, error) {
	return channelAPIURL(apiURL, channelID, "/publish?stream=1")
}

func channelAPIURL(apiURL string, channelID string, suffix string) (string, error) {
	base, err := url.Parse(strings.TrimRight(apiURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid api-url: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid api-url: absolute URL required")
	}
	pathSuffix, rawQuery, _ := strings.Cut(suffix, "?")
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/channels/" + url.PathEscape(channelID) + pathSuffix
	base.RawQuery = rawQuery
	base.Fragment = ""
	return base.String(), nil
}

func printChannelPublishPayload(out interface {
	Write([]byte) (int, error)
}, payload map[string]interface{}) {
	for _, key := range []string{
		"channelId",
		"sourceId",
		"standardCode",
		"feedUuid",
		"contentType",
		"streamBytes",
		"streamFrames",
		"throughputBytesPerSecond",
		"wireSpeedUtilization",
		"importedRows",
	} {
		if value, ok := payload[key]; ok {
			fmt.Fprintf(out, "%s=%v\n", key, monitorValue(value))
		}
	}
}

func printChannelMonitorPayload(out interface {
	Write([]byte) (int, error)
}, payload map[string]interface{}) {
	for _, key := range []string{
		"channelId",
		"sourceId",
		"standardCode",
		"channelHead",
		"pnmVerified",
		"providerPeer",
		"localRows",
		"remoteRows",
		"syncedRows",
		"missingRows",
		"pinnedRows",
		"syncedBytes",
		"throughputBytesPerSecond",
		"wireSpeedUtilization",
		"grantState",
		"encryptionState",
		"lastVerifiedUpdate",
	} {
		fmt.Fprintf(out, "%s=%v\n", key, monitorValue(payload[key]))
	}
}

func monitorValue(value interface{}) interface{} {
	if value == nil {
		return ""
	}
	return value
}

func firstNonEmptyChannelOption(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
