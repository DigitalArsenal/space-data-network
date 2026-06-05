package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	StandardCode          string
	Visibility            string
	Subject               string
	GrantID               string
	APIURL                string
	InsecureSkipTLSVerify bool
}

type channelShowOptions struct {
	APIURL                string
	InsecureSkipTLSVerify bool
}

type channelSubscriptionOptions struct {
	Visibility            string
	Subject               string
	GrantID               string
	APIURL                string
	InsecureSkipTLSVerify bool
}

type channelGrantIssueOptions struct {
	To                    string
	Scopes                []string
	ExpiresAt             string
	APIURL                string
	InsecureSkipTLSVerify bool
}

type channelPublishOptions struct {
	From                  string
	Subject               string
	GrantID               string
	Visibility            string
	APIURL                string
	InsecureSkipTLSVerify bool
}

type channelStreamOptions struct {
	Out                   string
	Subject               string
	GrantID               string
	Visibility            string
	APIURL                string
	InsecureSkipTLSVerify bool
}

type channelPNMOptions struct {
	Out                   string
	Subject               string
	GrantID               string
	Visibility            string
	APIURL                string
	InsecureSkipTLSVerify bool
}

type channelMonitorOptions struct {
	Subject               string
	GrantID               string
	Visibility            string
	APIURL                string
	InsecureSkipTLSVerify bool
}

func init() {
	rootCmd.AddCommand(newChannelsCommand())
}

func newChannelsCommand() *cobra.Command {
	listOptions := channelsListOptions{}
	showOptions := channelShowOptions{}
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
	listCmd.Flags().StringVar(&listOptions.Visibility, "visibility", "", "channel visibility filter")
	listCmd.Flags().StringVar(&listOptions.Subject, "subject", "", "subscriber EPM subject for private channel access")
	listCmd.Flags().StringVar(&listOptions.GrantID, "grant-id", "", "private channel grant ID")
	listCmd.Flags().StringVar(&listOptions.APIURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	addChannelInsecureTLSFlag(listCmd, &listOptions.InsecureSkipTLSVerify)

	cmd.AddCommand(listCmd)
	showCmd := &cobra.Command{
		Use:   "show <channelId>",
		Short: "Show channel metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsShow(cmd, showOptions, args[0])
		},
	}
	showCmd.Flags().StringVar(&showOptions.APIURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	addChannelInsecureTLSFlag(showCmd, &showOptions.InsecureSkipTLSVerify)
	cmd.AddCommand(showCmd)
	monitorCmd := &cobra.Command{
		Use:   "monitor <channelId>",
		Short: "Print channel synchronization status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsMonitor(cmd, monitorOptions, args[0])
		},
	}
	monitorCmd.Flags().StringVar(&monitorOptions.Subject, "subject", "", "subscriber EPM subject for private channel access")
	monitorCmd.Flags().StringVar(&monitorOptions.GrantID, "grant-id", "", "private channel grant ID")
	monitorCmd.Flags().StringVar(&monitorOptions.Visibility, "visibility", "", "channel visibility for private access checks")
	monitorCmd.Flags().StringVar(&monitorOptions.APIURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	addChannelInsecureTLSFlag(monitorCmd, &monitorOptions.InsecureSkipTLSVerify)
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
	subscribeCmd.Flags().StringVar(&subscribeOptions.Subject, "subject", "", "subscriber EPM subject for private channel access")
	subscribeCmd.Flags().StringVar(&subscribeOptions.GrantID, "grant-id", "", "private channel grant ID")
	subscribeCmd.Flags().StringVar(&subscribeOptions.APIURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	addChannelInsecureTLSFlag(subscribeCmd, &subscribeOptions.InsecureSkipTLSVerify)
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
	unsubscribeCmd.Flags().StringVar(&unsubscribeOptions.Subject, "subject", "", "subscriber EPM subject for private channel access")
	unsubscribeCmd.Flags().StringVar(&unsubscribeOptions.GrantID, "grant-id", "", "private channel grant ID")
	unsubscribeCmd.Flags().StringVar(&unsubscribeOptions.APIURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	addChannelInsecureTLSFlag(unsubscribeCmd, &unsubscribeOptions.InsecureSkipTLSVerify)
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
	publishCmd.Flags().StringVar(&publishOptions.Subject, "subject", "", "subscriber EPM subject for private channel access")
	publishCmd.Flags().StringVar(&publishOptions.GrantID, "grant-id", "", "private channel grant ID")
	publishCmd.Flags().StringVar(&publishOptions.Visibility, "visibility", "", "channel visibility for private access checks")
	publishCmd.Flags().StringVar(&publishOptions.APIURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	addChannelInsecureTLSFlag(publishCmd, &publishOptions.InsecureSkipTLSVerify)
	cmd.AddCommand(publishCmd)
	streamOptions := channelStreamOptions{}
	streamCmd := &cobra.Command{
		Use:   "stream <channelId>",
		Short: "Open and save a native FlatBuffers channel stream",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsStream(cmd, streamOptions, args[0])
		},
	}
	streamCmd.Flags().StringVar(&streamOptions.Out, "out", "", "file path for native FlatBuffers stream bytes")
	streamCmd.Flags().StringVar(&streamOptions.Subject, "subject", "", "subscriber EPM subject for private channel access")
	streamCmd.Flags().StringVar(&streamOptions.GrantID, "grant-id", "", "private channel grant ID")
	streamCmd.Flags().StringVar(&streamOptions.Visibility, "visibility", "", "channel visibility for private access checks")
	streamCmd.Flags().StringVar(&streamOptions.APIURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	addChannelInsecureTLSFlag(streamCmd, &streamOptions.InsecureSkipTLSVerify)
	cmd.AddCommand(streamCmd)
	pnmOptions := channelPNMOptions{}
	pnmCmd := &cobra.Command{
		Use:   "pnm <channelId>",
		Short: "Fetch verified PNM bytes for a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsPNM(cmd, pnmOptions, args[0])
		},
	}
	pnmCmd.Flags().StringVar(&pnmOptions.Out, "out", "", "file path for verified PNM bytes")
	pnmCmd.Flags().StringVar(&pnmOptions.Subject, "subject", "", "subscriber EPM subject for private channel access")
	pnmCmd.Flags().StringVar(&pnmOptions.GrantID, "grant-id", "", "private channel grant ID")
	pnmCmd.Flags().StringVar(&pnmOptions.Visibility, "visibility", "", "channel visibility for private access checks")
	pnmCmd.Flags().StringVar(&pnmOptions.APIURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	addChannelInsecureTLSFlag(pnmCmd, &pnmOptions.InsecureSkipTLSVerify)
	cmd.AddCommand(pnmCmd)
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
	grantIssueCmd.Flags().StringVar(&grantIssueOptions.APIURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	addChannelInsecureTLSFlag(grantIssueCmd, &grantIssueOptions.InsecureSkipTLSVerify)
	grantsCmd.AddCommand(grantIssueCmd)
	cmd.AddCommand(grantsCmd)
	return cmd
}

func addChannelInsecureTLSFlag(cmd *cobra.Command, target *bool) {
	cmd.Flags().BoolVar(target, "insecure-skip-tls-verify", false, "allow self-signed loopback HTTPS SDN API certificates")
}

func runChannelsList(cmd *cobra.Command, options channelsListOptions) error {
	if apiURL := firstNonEmptyChannelOption(strings.TrimSpace(options.APIURL), strings.TrimSpace(os.Getenv("SDN_API_URL"))); apiURL != "" {
		return runChannelsListFromAPI(cmd, options, apiURL)
	}
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

func runChannelsListFromAPI(cmd *cobra.Command, options channelsListOptions, apiURL string) error {
	listURL, err := channelListURL(apiURL, options)
	if err != nil {
		return err
	}
	client, err := newChannelAPIClient(apiURL, 10*time.Second, options.InsecureSkipTLSVerify)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, listURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("list channels: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("list channels: %s", resp.Status)
	}
	rows, err := decodeChannelListResponse(resp.Body)
	if err != nil {
		return fmt.Errorf("decode channel list: %w", err)
	}
	out := cmd.OutOrStdout()
	for _, row := range rows {
		printChannelRowPayload(out, row)
	}
	return nil
}

func decodeChannelListResponse(body io.Reader) ([]map[string]interface{}, error) {
	var payload interface{}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return nil, err
	}
	switch value := payload.(type) {
	case []interface{}:
		return channelRowsFromInterfaces(value), nil
	case map[string]interface{}:
		rows, ok := value["results"].([]interface{})
		if !ok {
			return nil, fmt.Errorf("results array missing")
		}
		return channelRowsFromInterfaces(rows), nil
	default:
		return nil, fmt.Errorf("unexpected channel list payload %T", payload)
	}
}

func channelRowsFromInterfaces(values []interface{}) []map[string]interface{} {
	rows := make([]map[string]interface{}, 0, len(values))
	for _, value := range values {
		row, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	return rows
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
	frames, err := channels.SplitNativeStreamFramesForChannel(parsed, streamBytes)
	if err != nil {
		return fmt.Errorf("invalid native FlatBuffers stream: %w", err)
	}
	if apiURL := firstNonEmptyChannelOption(strings.TrimSpace(options.APIURL), strings.TrimSpace(os.Getenv("SDN_API_URL"))); apiURL != "" {
		return runChannelsPublishToAPI(cmd, parsed, apiURL, streamBytes, channelAccessQuery{
			Subject:    options.Subject,
			GrantID:    options.GrantID,
			Visibility: options.Visibility,
		}, options.InsecureSkipTLSVerify)
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

func runChannelsPublishToAPI(cmd *cobra.Command, parsed channels.ChannelID, apiURL string, streamBytes []byte, access channelAccessQuery, insecureSkipTLSVerify bool) error {
	publishURL, err := channelPublishURL(apiURL, parsed.ChannelID, access)
	if err != nil {
		return err
	}
	client, err := newChannelAPIClient(apiURL, 10*time.Second, insecureSkipTLSVerify)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, publishURL, bytes.NewReader(streamBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
	if isPrivateChannelVisibility(access.Visibility) {
		req.Header.Set("X-SDN-Encrypted-Stream", "true")
	}
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

func runChannelsStream(cmd *cobra.Command, options channelStreamOptions, channelID string) error {
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
	}
	outPath := strings.TrimSpace(options.Out)
	if outPath == "" {
		return fmt.Errorf("--out is required")
	}
	apiURL := firstNonEmptyChannelOption(strings.TrimSpace(options.APIURL), strings.TrimSpace(os.Getenv("SDN_API_URL")))
	if apiURL == "" {
		return fmt.Errorf("--api-url or SDN_API_URL is required to open a channel stream")
	}
	streamBytes, contentType, err := readChannelsStreamFromAPI(cmd, parsed, options, apiURL)
	if err != nil {
		return err
	}
	frames, err := channels.SplitNativeStreamFramesForChannel(parsed, streamBytes)
	if err != nil {
		return fmt.Errorf("invalid native FlatBuffers stream: %w", err)
	}
	if err := os.WriteFile(outPath, streamBytes, 0o600); err != nil {
		return fmt.Errorf("write native FlatBuffers stream: %w", err)
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/vnd.sdn.flatbuffers.stream"
	}
	payload := map[string]interface{}{
		"channelId":    parsed.ChannelID,
		"sourceId":     parsed.SourceID,
		"standardCode": parsed.StandardCode,
		"contentType":  contentType,
		"streamBytes":  len(streamBytes),
		"streamFrames": len(frames),
		"out":          outPath,
	}
	if parsed.FeedUUID != "" {
		payload["feedUuid"] = parsed.FeedUUID
	}
	printChannelStreamPayload(cmd.OutOrStdout(), payload)
	return nil
}

func readChannelsStreamFromAPI(cmd *cobra.Command, parsed channels.ChannelID, options channelStreamOptions, apiURL string) ([]byte, string, error) {
	streamURL, err := channelStreamURL(apiURL, parsed.ChannelID, channelAccessQuery{
		Subject:    options.Subject,
		GrantID:    options.GrantID,
		Visibility: options.Visibility,
	})
	if err != nil {
		return nil, "", err
	}
	client, err := newChannelAPIClient(apiURL, 30*time.Second, options.InsecureSkipTLSVerify)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.sdn.flatbuffers.stream")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("open native channel stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("open native channel stream: %s", resp.Status)
	}
	streamBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read native channel stream: %w", err)
	}
	return streamBytes, resp.Header.Get("Content-Type"), nil
}

func runChannelsPNM(cmd *cobra.Command, options channelPNMOptions, channelID string) error {
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
	}
	outPath := strings.TrimSpace(options.Out)
	if outPath == "" {
		return fmt.Errorf("--out is required")
	}
	apiURL := firstNonEmptyChannelOption(strings.TrimSpace(options.APIURL), strings.TrimSpace(os.Getenv("SDN_API_URL")))
	if apiURL == "" {
		return fmt.Errorf("--api-url or SDN_API_URL is required to fetch channel PNM")
	}
	pnmBytes, contentType, err := readChannelsPNMFromAPI(cmd, parsed, options, apiURL)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, pnmBytes, 0o600); err != nil {
		return fmt.Errorf("write verified PNM: %w", err)
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/vnd.sdn.pnm"
	}
	payload := map[string]interface{}{
		"channelId":    parsed.ChannelID,
		"sourceId":     parsed.SourceID,
		"standardCode": parsed.StandardCode,
		"contentType":  contentType,
		"pnmBytes":     len(pnmBytes),
		"out":          outPath,
	}
	if parsed.FeedUUID != "" {
		payload["feedUuid"] = parsed.FeedUUID
	}
	printChannelPNMPayload(cmd.OutOrStdout(), payload)
	return nil
}

func readChannelsPNMFromAPI(cmd *cobra.Command, parsed channels.ChannelID, options channelPNMOptions, apiURL string) ([]byte, string, error) {
	pnmURL, err := channelPNMURL(apiURL, parsed.ChannelID, channelAccessQuery{
		Subject:    options.Subject,
		GrantID:    options.GrantID,
		Visibility: options.Visibility,
	})
	if err != nil {
		return nil, "", err
	}
	client, err := newChannelAPIClient(apiURL, 10*time.Second, options.InsecureSkipTLSVerify)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, pnmURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.sdn.pnm")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch verified channel PNM: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("fetch verified channel PNM: %s", resp.Status)
	}
	pnmBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read verified channel PNM: %w", err)
	}
	return pnmBytes, resp.Header.Get("Content-Type"), nil
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
	if apiURL := firstNonEmptyChannelOption(strings.TrimSpace(options.APIURL), strings.TrimSpace(os.Getenv("SDN_API_URL"))); apiURL != "" {
		return runChannelsSubscriptionToAPI(cmd, parsed, apiURL, "subscribe", channelAccessQuery{
			Subject:    options.Subject,
			GrantID:    options.GrantID,
			Visibility: options.Visibility,
		}, options.InsecureSkipTLSVerify)
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
	if apiURL := firstNonEmptyChannelOption(strings.TrimSpace(options.APIURL), strings.TrimSpace(os.Getenv("SDN_API_URL"))); apiURL != "" {
		return runChannelsSubscriptionToAPI(cmd, parsed, apiURL, "unsubscribe", channelAccessQuery{
			Subject:    options.Subject,
			GrantID:    options.GrantID,
			Visibility: options.Visibility,
		}, options.InsecureSkipTLSVerify)
	}
	if strings.EqualFold(strings.TrimSpace(options.Visibility), "private") {
		return fmt.Errorf("verified channel grant required for %s", parsed.ChannelID)
	}
	return printChannelSubscriptionState(cmd, registry.Unsubscribe(parsed))
}

func runChannelsSubscriptionToAPI(cmd *cobra.Command, parsed channels.ChannelID, apiURL string, action string, access channelAccessQuery, insecureSkipTLSVerify bool) error {
	subscriptionURL, err := channelSubscriptionURL(apiURL, parsed.ChannelID, action, access)
	if err != nil {
		return err
	}
	client, err := newChannelAPIClient(apiURL, 10*time.Second, insecureSkipTLSVerify)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, subscriptionURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s channel: %w", action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s channel: %s", action, resp.Status)
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("decode channel %s response: %w", action, err)
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
	printChannelSubscriptionPayload(cmd.OutOrStdout(), payload)
	return nil
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
	if apiURL := firstNonEmptyChannelOption(strings.TrimSpace(options.APIURL), strings.TrimSpace(os.Getenv("SDN_API_URL"))); apiURL != "" {
		return runChannelsGrantIssueToAPI(cmd, parsed, apiURL, options, scopes)
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

func runChannelsGrantIssueToAPI(cmd *cobra.Command, parsed channels.ChannelID, apiURL string, options channelGrantIssueOptions, scopes []channels.AccessBoundary) error {
	grantURL, err := channelGrantURL(apiURL, parsed.ChannelID)
	if err != nil {
		return err
	}
	scopeValues := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scopeValues = append(scopeValues, string(scope))
	}
	payload := map[string]interface{}{
		"subject": strings.TrimSpace(options.To),
		"scopes":  scopeValues,
	}
	if strings.TrimSpace(options.ExpiresAt) != "" {
		payload["expiresAt"] = strings.TrimSpace(options.ExpiresAt)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client, err := newChannelAPIClient(apiURL, 10*time.Second, options.InsecureSkipTLSVerify)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, grantURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("issue channel grant: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("issue channel grant: %s", resp.Status)
	}
	var response map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return fmt.Errorf("decode channel grant response: %w", err)
	}
	if _, ok := response["channelId"]; !ok {
		response["channelId"] = parsed.ChannelID
	}
	printChannelGrantPayload(cmd.OutOrStdout(), response)
	return nil
}

func parseChannelGrantScopes(values []string) ([]channels.AccessBoundary, error) {
	if len(values) == 0 {
		return nil, nil
	}
	allowed := map[string]channels.AccessBoundary{
		string(channels.BoundaryListPrivate):        channels.BoundaryListPrivate,
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

func runChannelsShow(cmd *cobra.Command, options channelShowOptions, channelID string) error {
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
	}
	if apiURL := firstNonEmptyChannelOption(strings.TrimSpace(options.APIURL), strings.TrimSpace(os.Getenv("SDN_API_URL"))); apiURL != "" {
		return runChannelsShowFromAPI(cmd, parsed, apiURL, options.InsecureSkipTLSVerify)
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

func runChannelsShowFromAPI(cmd *cobra.Command, parsed channels.ChannelID, apiURL string, insecureSkipTLSVerify bool) error {
	showURL, err := channelDetailURL(apiURL, parsed.ChannelID)
	if err != nil {
		return err
	}
	client, err := newChannelAPIClient(apiURL, 10*time.Second, insecureSkipTLSVerify)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, showURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("show channel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("show channel: %s", resp.Status)
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("decode channel detail: %w", err)
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
	printChannelDetailPayload(cmd.OutOrStdout(), payload)
	return nil
}

func runChannelsMonitor(cmd *cobra.Command, options channelMonitorOptions, channelID string) error {
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
	}
	if apiURL := firstNonEmptyChannelOption(strings.TrimSpace(options.APIURL), strings.TrimSpace(os.Getenv("SDN_API_URL"))); apiURL != "" {
		return runChannelsMonitorFromAPI(cmd, parsed, apiURL, channelAccessQuery{
			Subject:    options.Subject,
			GrantID:    options.GrantID,
			Visibility: options.Visibility,
		}, options.InsecureSkipTLSVerify)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "channelId=%s\n", parsed.ChannelID)
	fmt.Fprintf(out, "sourceId=%s\n", parsed.SourceID)
	fmt.Fprintf(out, "standardCode=%s\n", parsed.StandardCode)
	fmt.Fprintln(out, "channelHead=")
	fmt.Fprintln(out, "pnmVerified=false")
	fmt.Fprintln(out, "dpmVerified=false")
	fmt.Fprintln(out, "providerPeer=")
	fmt.Fprintln(out, "localRows=0")
	fmt.Fprintln(out, "remoteRows=0")
	fmt.Fprintln(out, "syncedRows=0")
	fmt.Fprintln(out, "missingRows=0")
	fmt.Fprintln(out, "pinnedCount=0")
	fmt.Fprintln(out, "pinnedRows=0")
	fmt.Fprintln(out, "syncedBytes=0")
	fmt.Fprintln(out, "throughputBytesPerSecond=0")
	fmt.Fprintln(out, "wireSpeedUtilization=")
	fmt.Fprintln(out, "grantState=unknown")
	fmt.Fprintln(out, "encryptionState=unknown")
	fmt.Fprintln(out, "lastVerifiedUpdate=")
	return nil
}

func runChannelsMonitorFromAPI(cmd *cobra.Command, parsed channels.ChannelID, apiURL string, access channelAccessQuery, insecureSkipTLSVerify bool) error {
	monitorURL, err := channelMonitorURL(apiURL, parsed.ChannelID, access)
	if err != nil {
		return err
	}
	client, err := newChannelAPIClient(apiURL, 10*time.Second, insecureSkipTLSVerify)
	if err != nil {
		return err
	}
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

func channelMonitorURL(apiURL string, channelID string, access channelAccessQuery) (string, error) {
	return channelAPIURLWithQuery(apiURL, channelID, "/monitor", access.queryValues())
}

func channelListURL(apiURL string, options channelsListOptions) (string, error) {
	base, err := url.Parse(strings.TrimRight(apiURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid api-url: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid api-url: absolute URL required")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/channels"
	query := base.Query()
	standardCode := options.StandardCode
	if strings.TrimSpace(standardCode) != "" {
		code, err := channels.AssertStandardCode(standardCode)
		if err != nil {
			return "", err
		}
		query.Set("standardCode", code)
	}
	if visibility := strings.TrimSpace(options.Visibility); visibility != "" {
		query.Set("visibility", visibility)
	}
	if subject := strings.TrimSpace(options.Subject); subject != "" {
		query.Set("subject", subject)
	}
	if grantID := strings.TrimSpace(options.GrantID); grantID != "" {
		query.Set("grantId", grantID)
	}
	base.RawQuery = query.Encode()
	base.Fragment = ""
	return base.String(), nil
}

func channelDetailURL(apiURL string, channelID string) (string, error) {
	return channelAPIURL(apiURL, channelID, "")
}

func channelPublishURL(apiURL string, channelID string, access channelAccessQuery) (string, error) {
	query := access.queryValues()
	query.Set("stream", "1")
	return channelAPIURLWithQuery(apiURL, channelID, "/publish", query)
}

type channelAccessQuery struct {
	Subject    string
	GrantID    string
	Visibility string
}

func channelStreamURL(apiURL string, channelID string, access channelAccessQuery) (string, error) {
	return channelAPIURLWithQuery(apiURL, channelID, "/stream", access.queryValues())
}

func channelPNMURL(apiURL string, channelID string, access channelAccessQuery) (string, error) {
	return channelAPIURLWithQuery(apiURL, channelID, "/pnm", access.queryValues())
}

func (access channelAccessQuery) queryValues() url.Values {
	query := url.Values{}
	if subject := strings.TrimSpace(access.Subject); subject != "" {
		query.Set("subject", subject)
	}
	if grantID := strings.TrimSpace(access.GrantID); grantID != "" {
		query.Set("grantId", grantID)
	}
	if visibility := strings.TrimSpace(access.Visibility); visibility != "" {
		query.Set("visibility", visibility)
	}
	return query
}

func isPrivateChannelVisibility(visibility string) bool {
	value := strings.ToLower(strings.TrimSpace(visibility))
	return value == "private" || strings.HasPrefix(value, "private-")
}

func channelSubscriptionURL(apiURL string, channelID string, action string, access channelAccessQuery) (string, error) {
	switch action {
	case "subscribe", "unsubscribe":
	default:
		return "", fmt.Errorf("invalid channel subscription action %q", action)
	}
	return channelAPIURLWithQuery(apiURL, channelID, "/"+action, access.queryValues())
}

func channelGrantURL(apiURL string, channelID string) (string, error) {
	return channelAPIURL(apiURL, channelID, "/grants")
}

func channelAPIURL(apiURL string, channelID string, suffix string) (string, error) {
	return channelAPIURLWithQuery(apiURL, channelID, suffix, nil)
}

func channelAPIURLWithQuery(apiURL string, channelID string, suffix string, extraQuery url.Values) (string, error) {
	base, err := url.Parse(strings.TrimRight(apiURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid api-url: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid api-url: absolute URL required")
	}
	pathSuffix, rawQuery, _ := strings.Cut(suffix, "?")
	base.Path = strings.TrimRight(base.Path, "/") + "/api/v1/channels/" + url.PathEscape(channelID) + pathSuffix
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("invalid channel query: %w", err)
	}
	for key, values := range extraQuery {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	base.RawQuery = query.Encode()
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
		"grantState",
		"encryptionState",
	} {
		if value, ok := payload[key]; ok {
			fmt.Fprintf(out, "%s=%v\n", key, monitorValue(value))
		}
	}
}

func printChannelStreamPayload(out interface {
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
		"out",
	} {
		if value, ok := payload[key]; ok {
			fmt.Fprintf(out, "%s=%v\n", key, monitorValue(value))
		}
	}
}

func printChannelPNMPayload(out interface {
	Write([]byte) (int, error)
}, payload map[string]interface{}) {
	for _, key := range []string{
		"channelId",
		"sourceId",
		"standardCode",
		"feedUuid",
		"contentType",
		"pnmBytes",
		"out",
	} {
		if value, ok := payload[key]; ok {
			fmt.Fprintf(out, "%s=%v\n", key, monitorValue(value))
		}
	}
}

func printChannelRowPayload(out interface {
	Write([]byte) (int, error)
}, payload map[string]interface{}) {
	for _, key := range []string{
		"channelId",
		"sourceId",
		"standardCode",
		"feedUuid",
		"topic",
		"visibility",
		"subscribed",
		"grantState",
		"encryptionState",
	} {
		if value, ok := payload[key]; ok {
			fmt.Fprintf(out, "%s=%v ", key, monitorValue(value))
		}
	}
	fmt.Fprintln(out)
}

func printChannelDetailPayload(out interface {
	Write([]byte) (int, error)
}, payload map[string]interface{}) {
	for _, key := range []string{
		"channelId",
		"sourceId",
		"standardCode",
		"feedUuid",
		"visibility",
		"subscribed",
		"pnmVerified",
		"dpmVerified",
		"pnmCid",
		"grantState",
		"encryptionState",
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
		"visibility",
		"channelHead",
		"pnmVerified",
		"dpmVerified",
		"providerPeer",
		"localRows",
		"remoteRows",
		"syncedRows",
		"missingRows",
		"pinnedCount",
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

func printChannelSubscriptionPayload(out interface {
	Write([]byte) (int, error)
}, payload map[string]interface{}) {
	for _, key := range []string{
		"channelId",
		"sourceId",
		"standardCode",
		"feedUuid",
		"subscribed",
		"visibility",
		"grantState",
		"encryptionState",
		"lastUpdated",
	} {
		if value, ok := payload[key]; ok {
			fmt.Fprintf(out, "%s=%v\n", key, monitorValue(value))
		}
	}
}

func printChannelGrantPayload(out interface {
	Write([]byte) (int, error)
}, payload map[string]interface{}) {
	for _, key := range []string{
		"grantId",
		"channelId",
		"subject",
		"grantState",
	} {
		if value, ok := payload[key]; ok {
			fmt.Fprintf(out, "%s=%v\n", key, monitorValue(value))
		}
	}
	if scopes, ok := payload["scopes"].([]interface{}); ok {
		for _, scope := range scopes {
			fmt.Fprintf(out, "scope=%v\n", monitorValue(scope))
		}
	}
	for _, key := range []string{"issuedAt", "expiresAt"} {
		if value, ok := payload[key]; ok {
			fmt.Fprintf(out, "%s=%v\n", key, monitorValue(value))
		}
	}
}

func monitorValue(value interface{}) interface{} {
	if value == nil {
		return ""
	}
	return value
}

func newChannelAPIClient(apiURL string, timeout time.Duration, insecureSkipTLSVerify bool) (*http.Client, error) {
	if !insecureSkipTLSVerify {
		return &http.Client{Timeout: timeout}, nil
	}
	if !isLoopbackChannelAPIURL(apiURL) {
		return nil, fmt.Errorf("--insecure-skip-tls-verify is only allowed for loopback SDN API URLs")
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}, nil
}

func isLoopbackChannelAPIURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func firstNonEmptyChannelOption(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
