package main

import (
	"fmt"
	"strings"

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

func init() {
	rootCmd.AddCommand(newChannelsCommand())
}

func newChannelsCommand() *cobra.Command {
	listOptions := channelsListOptions{}
	subscribeOptions := channelSubscriptionOptions{}
	unsubscribeOptions := channelSubscriptionOptions{}
	subscriptions := channels.NewSubscriptionRegistry()
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
	cmd.AddCommand(&cobra.Command{
		Use:   "monitor <channelId>",
		Short: "Print channel synchronization status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChannelsMonitor(cmd, args[0])
		},
	})
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
	cmd.AddCommand(failClosedChannelCommand("publish <channelId>", "Publish a native FlatBuffers channel stream"))
	grantsCmd := &cobra.Command{
		Use:   "grants",
		Short: "Manage private channel grants",
	}
	grantsCmd.AddCommand(failClosedChannelCommand("issue <channelId>", "Issue a private channel grant"))
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

func runChannelsMonitor(cmd *cobra.Command, channelID string) error {
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		return err
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

func failClosedChannelCommand(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed, err := channels.ParseChannelID(args[0])
			if err != nil {
				return err
			}
			return fmt.Errorf("%s requires verified channel API and grant support before changing %s", cmd.Name(), parsed.ChannelID)
		},
	}
}
