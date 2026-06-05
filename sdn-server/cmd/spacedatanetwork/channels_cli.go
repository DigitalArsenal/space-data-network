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

func init() {
	rootCmd.AddCommand(newChannelsCommand())
}

func newChannelsCommand() *cobra.Command {
	listOptions := channelsListOptions{}
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
	cmd.AddCommand(failClosedChannelCommand("subscribe <channelId>", "Subscribe to a channel"))
	cmd.AddCommand(failClosedChannelCommand("unsubscribe <channelId>", "Unsubscribe from a channel"))
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
		fmt.Fprintf(out, "standardCode=%s topic=%s visibility=unknown\n", code, channels.DiscoveryTopic(code))
		return nil
	}
	for _, schemaName := range sds.SupportedSchemas {
		code, err := channels.StandardCodeFromSchemaName(schemaName)
		if err != nil {
			continue
		}
		fmt.Fprintf(out, "standardCode=%s topic=%s visibility=unknown\n", code, channels.DiscoveryTopic(code))
	}
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
