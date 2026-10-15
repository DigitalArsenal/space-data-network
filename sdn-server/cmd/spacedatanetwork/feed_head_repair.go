package main

// feed_head_repair.go: `spacedatanetwork store-repair-feed-heads` removes the
// residue a forged dataset feed head left in the publication ledger (security
// audit SEC-031): rows whose feed_sequence or published_at no honest producer
// emits. Such a row ranks first in its lane's retention forever, so the lane
// stays frozen on a stale batch even after the node is upgraded. Dry run by
// default; refuses while a daemon holds the store.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

var (
	feedHeadRepairApply   bool
	feedHeadRepairProfile string
)

var storeRepairFeedHeadsCmd = &cobra.Command{
	Use:   "store-repair-feed-heads",
	Short: "Remove dataset publication rows left by forged feed heads (implausible sequence or future time)",
	Long: `Find dataset publication rows whose feed_sequence is outside [0, 2^24] or
whose published_at is in the future — values only a forged feed head writes —
and, with --apply, delete them and their cached shard/index files so each lane
ranks its genuine batches again. Records the forged head made the node evict
come back with the provider's next genuine batch (or a catch-up).

Stop the daemon first; the command refuses while the store is in use.`,
	RunE: runStoreRepairFeedHeads,
}

func init() {
	storeRepairFeedHeadsCmd.Flags().BoolVar(&feedHeadRepairApply, "apply", false, "delete the rows (without it the plan is printed and nothing changes)")
	storeRepairFeedHeadsCmd.Flags().StringVar(&feedHeadRepairProfile, "query-profile", storage.DatasetPublicationQueryProfile, "publication query profile to scan")
	rootCmd.AddCommand(storeRepairFeedHeadsCmd)
}

func runStoreRepairFeedHeads(cmd *cobra.Command, args []string) error {
	if strings.TrimSpace(configPath) == "" {
		return errors.New("--config is required")
	}
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return fmt.Errorf("initialize schema validator: %w", err)
	}
	store, err := storage.NewFlatSQLStore(cfg.Storage.Path, validator)
	if err != nil {
		return fmt.Errorf("open storage (is the daemon running?): %w", err)
	}
	defer store.Close()

	rows, err := node.RepairImplausibleDatasetFeedPublications(store, feedHeadRepairProfile, time.Now(), feedHeadRepairApply)
	out := cmd.OutOrStdout()
	for _, row := range rows {
		pub := row.Publication
		fmt.Fprintf(out, "%s %s/%s batch=%s offset=%d head=%s: %s\n", pub.SchemaName, pub.ProviderID, pub.SourceName, pub.BatchID, pub.Offset, pub.FeedHead, row.Reason)
	}
	if err != nil {
		return err
	}
	switch {
	case len(rows) == 0:
		fmt.Fprintln(out, "no implausible publication rows")
	case feedHeadRepairApply:
		fmt.Fprintf(out, "removed %d implausible publication row(s)\n", len(rows))
	default:
		fmt.Fprintf(out, "dry run: %d row(s) would be removed (pass --apply)\n", len(rows))
	}
	return nil
}
