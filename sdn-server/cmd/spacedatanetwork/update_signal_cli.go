package main

// The operator surface of the push lane (owner ruling 2026-08-09):
//
//	spacedatanetwork update signal    — publish is not push until this runs
//	spacedatanetwork update slots     — what this box can reverse to
//	spacedatanetwork update rollback  — reverse to a retained build
//
// `signal` is the client half of POST /api/v1/admin/updates/signal and follows
// the same shape as `update sign-manifest`: it runs on the publisher host,
// obtains Admin authority through the §19 admit ceremony with the node's root
// key (or a --session-token), and the signing key never leaves the node. It
// submits only a SELECTOR — which published update to announce — because the
// node derives the signal from its own feed index; see internal/api/update_signal.go.
//
// `slots` and `rollback` are the local half of the five-slot retention rule.
// They run against the bundle this executable belongs to and need no session:
// reading and reversing your own install is not an admin-API operation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

var (
	updateSignalChannel  string
	updateSignalUpdateID string
	updateSignalVersion  string
	updateSignalPlatform string
	updateSignalArch     string
	updateSignalKind     string
	updateSignalTopic    string
	updateSignalDryRun   bool
	updateSignalNodeURL  string
	updateSignalJSON     bool
)

type updateSignalCLIResponse struct {
	Published   bool            `json:"published"`
	DryRun      bool            `json:"dry_run"`
	Topic       string          `json:"topic"`
	UpdateID    string          `json:"update_id"`
	Version     string          `json:"version"`
	Sequence    int64           `json:"sequence"`
	Channel     string          `json:"channel"`
	Target      string          `json:"target"`
	ManifestURL string          `json:"manifest_url"`
	CarrierURL  string          `json:"carrier_url"`
	BundleHash  string          `json:"bundle_hash"`
	KeyID       string          `json:"key_id"`
	PublicKey   string          `json:"public_key"`
	SignedAt    string          `json:"signed_at"`
	Bytes       int             `json:"bytes"`
	Signal      json.RawMessage `json:"signal"`
}

var updateSignalCmd = &cobra.Command{
	Use:   "signal",
	Short: "Push a signed update signal so every install upgrades itself in place",
	Long: "Announces an already-published feed artifact on the fleet's update topic. The node " +
		"reads its own feed index, builds the signal from that entry, signs it with the publisher " +
		"key under SDN-UPDATE-SIGNAL-V1, and broadcasts it. Every subscribed install then fetches, " +
		"verifies and upgrades itself. Publishing to the feed WITHOUT this is not a push: the " +
		"artifact is available, and nothing is told to go and get it.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(updateSignalChannel) == "" {
			return errors.New("--channel is required: a signal names one feed lane")
		}
		payload := map[string]any{"channel": strings.TrimSpace(updateSignalChannel)}
		for key, value := range map[string]string{
			"update_id": updateSignalUpdateID,
			"version":   updateSignalVersion,
			"platform":  updateSignalPlatform,
			"arch":      updateSignalArch,
			"kind":      updateSignalKind,
			"topic":     updateSignalTopic,
		} {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				payload[key] = trimmed
			}
		}
		if updateSignalDryRun {
			payload["dry_run"] = true
		}

		client, err := newSignalClient(cmd)
		if err != nil {
			return err
		}
		var result updateSignalCLIResponse
		if err := client.postJSON(context.Background(), "/api/v1/admin/updates/signal", payload, &result, ""); err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		if updateSignalJSON {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}
		if result.DryRun {
			fmt.Fprintln(out, "dry_run=yes nothing_signed=yes nothing_published=yes")
		}
		fmt.Fprintf(out, "published=%t\n", result.Published)
		fmt.Fprintf(out, "topic=%s\n", result.Topic)
		fmt.Fprintf(out, "update_id=%s version=%s sequence=%d channel=%s target=%s\n",
			result.UpdateID, result.Version, result.Sequence, result.Channel, result.Target)
		fmt.Fprintf(out, "manifest_url=%s\n", result.ManifestURL)
		fmt.Fprintf(out, "carrier_url=%s\n", result.CarrierURL)
		fmt.Fprintf(out, "bundle_hash=%s\n", result.BundleHash)
		fmt.Fprintf(out, "key_id=%s signal_bytes=%d\n", result.KeyID, result.Bytes)
		if result.Published {
			fmt.Fprintln(out, "next=every subscribed install fetches, verifies and upgrades itself; watch each box's journal for 'Update signal accepted'")
		}
		return nil
	},
}

// newSignalClient mirrors newUpdateSigningClient: the ceremony needs the node's
// seed, so this runs on the publisher host, and --node-url exists because a
// daemon that also serves the public web has a certificate for its public name
// and none that can be valid for 127.0.0.1.
func newSignalClient(cmd *cobra.Command) (*adminClient, error) {
	if nodeURL := strings.TrimSpace(updateSignalNodeURL); nodeURL != "" {
		saved := updateSignManifestNodeURL
		updateSignManifestNodeURL = nodeURL
		defer func() { updateSignManifestNodeURL = saved }()
		return newUpdateSigningClient(cmd)
	}
	return newAdminClient(cmd)
}

var updateSlotsJSON bool

var updateSlotsCmd = &cobra.Command{
	Use:   "slots",
	Short: "List the builds this box can roll back to (retention limit five)",
	Long: "Reports the running build and the retained reverse targets, newest first, reconciled " +
		"against what is actually on disk. Owner ruling 2026-08-09 sets the retention at five. " +
		"A feed-retention policy MUST NOT reap any version listed here on any box.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		layout := bundle.ResolveCurrent()
		if layout.Root == "" {
			return errors.New("current executable is not running from a self-contained SDN bundle")
		}
		inventory, err := update.Inventory(update.PathsFor(layout.Root))
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if updateSlotsJSON {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return encoder.Encode(inventory)
		}
		fmt.Fprintf(out, "retention_limit=%d\n", inventory.Limit)
		fmt.Fprintf(out, "current update_id=%s version=%s sequence=%d applied_at=%s\n",
			inventory.Current.UpdateID, inventory.Current.Version, inventory.Current.Sequence, inventory.Current.RecordedAt)
		if len(inventory.Slots) == 0 {
			fmt.Fprintln(out, "slots=NONE — this box has NO reverse target. The next update has nothing to fall back to.")
		}
		for i, slot := range inventory.Slots {
			marker := ""
			if i == 0 {
				marker = " (default rollback target)"
			}
			fmt.Fprintf(out, "slot[%d] update_id=%s version=%s sequence=%d path=%s%s\n",
				i, slot.UpdateID, slot.Version, slot.Sequence, slot.Path, marker)
		}
		for _, slot := range inventory.Missing {
			fmt.Fprintf(out, "MISSING update_id=%s path=%s — recorded as a reverse target but not on disk\n", slot.UpdateID, slot.Path)
		}
		for _, dir := range inventory.Unmanaged {
			fmt.Fprintf(out, "UNMANAGED %s — no slot record claims this directory; retention will prune it on the next apply\n", dir)
		}
		return nil
	},
}

var (
	updateRollbackSlot   string
	updateRollbackReason string
)

var updateRollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Restore a retained build (default: the immediately-previous one)",
	Long: "Swaps the bundle back to a retained build and moves the displaced payload to " +
		"updates/failed/<update-id>/. With no --slot this restores the immediately-previous " +
		"verified build, which is the only reverse target whose behaviour is known. An older " +
		"slot must be NAMED: skipping generations silently reverts every lane that landed in " +
		"between. Stop the daemon first; a rollback rewrites the binary it is running.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		layout := bundle.ResolveCurrent()
		if layout.Root == "" {
			return errors.New("current executable is not running from a self-contained SDN bundle")
		}
		if strings.TrimSpace(updateRollbackReason) == "" {
			return errors.New("--reason is required: an unexplained reversal is the defect this lane exists to end")
		}
		result, err := update.Rollback(update.PathsFor(layout.Root), update.RollbackOptions{
			Slot:   strings.TrimSpace(updateRollbackSlot),
			Reason: strings.TrimSpace(updateRollbackReason),
		})
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "restored_update=%s\n", result.RestoredUpdateID)
		fmt.Fprintf(out, "version=%s\n", result.RestoredVersion)
		fmt.Fprintf(out, "sequence=%d\n", result.RestoredSequence)
		fmt.Fprintf(out, "restored_from=%s\n", result.RestoredFrom)
		fmt.Fprintf(out, "displaced_to=%s\n", result.FailedPath)
		fmt.Fprintf(out, "slots_remaining=%d\n", len(result.Slots))
		fmt.Fprintln(out, "next=restart the SDN daemon to run the restored version")
		return nil
	},
}

func init() {
	updateSignalCmd.Flags().StringVar(&updateSignalChannel, "channel", "", "feed channel to announce (required)")
	updateSignalCmd.Flags().StringVar(&updateSignalUpdateID, "update-id", "", "announce this update id (default: the newest entry in the lane)")
	updateSignalCmd.Flags().StringVar(&updateSignalVersion, "version", "", "announce this version")
	updateSignalCmd.Flags().StringVar(&updateSignalPlatform, "platform", "", "target platform (default: this host's)")
	updateSignalCmd.Flags().StringVar(&updateSignalArch, "arch", "", "target arch (default: this host's)")
	updateSignalCmd.Flags().StringVar(&updateSignalKind, "kind", "", "target kind (default: cli-bundle)")
	updateSignalCmd.Flags().StringVar(&updateSignalTopic, "topic", "", "override the pub/sub topic (default: /sdn/updates/v1/<channel>)")
	updateSignalCmd.Flags().BoolVar(&updateSignalDryRun, "dry-run", false, "show the exact signal that would be broadcast; sign nothing, publish nothing")
	updateSignalCmd.Flags().BoolVar(&updateSignalJSON, "json", false, "emit the full response as JSON")
	updateSignalCmd.Flags().StringVar(&updateSignalNodeURL, "node-url", "",
		"explicit base URL of the publishing node (set this when the daemon's certificate is issued for a public hostname rather than loopback)")
	updateSignalCmd.Flags().String("session-token", "",
		"session token for the admin API (default: $SDN_SESSION_TOKEN; omit to sign in with the node's root key)")

	updateSlotsCmd.Flags().BoolVar(&updateSlotsJSON, "json", false, "emit the inventory as JSON")

	updateRollbackCmd.Flags().StringVar(&updateRollbackSlot, "slot", "",
		"which retained build to restore: an update id, a version, or a rollback path (default: the immediately-previous build)")
	updateRollbackCmd.Flags().StringVar(&updateRollbackReason, "reason", "", "why this box is being reversed (required; recorded in the deploy ledger)")

	updateCmd.AddCommand(updateSignalCmd)
	updateCmd.AddCommand(updateSlotsCmd)
	updateCmd.AddCommand(updateRollbackCmd)
}
