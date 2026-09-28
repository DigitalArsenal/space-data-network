package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spf13/cobra"
)

// reconcile-ingest-identity repairs a lane that re-ingested the same upstream
// records under new fetch stamps before ingest identity existed (graph:
// sdn-publication-hygiene-20260928; host-02 IQC: 532,945 rows for 36,636
// captures). Dry run by default.
var reconcileIngestIdentityCmd = &cobra.Command{
	Use:   "reconcile-ingest-identity",
	Short: "Collapse re-fetched copies of the same records in one ingest lane",
	Long: `Walk one ingest lane (schema, provider, source) oldest record first and keep
the first record of every ingest identity — the record hash with the fetch
stamps a parser writes (for $IQC: RETRIEVED_AT, CREATED_AT, UPDATED_AT)
blanked. Later copies are removed from the lane (and from the store when no
other lane tags them); the kept record inherits their batch tags. The lane's
identity rows are backfilled, so the next replay of the same upstream payload
lands nothing.

Dry run by default: it reads the lane and reports the counts. --apply writes.
Needs exclusive store access: stop the daemon first.`,
	RunE: runReconcileIngestIdentity,
}

var (
	reconcileIdentitySchema   string
	reconcileIdentityProvider string
	reconcileIdentitySource   string
	reconcileIdentityApply    bool
)

func init() {
	reconcileIngestIdentityCmd.Flags().StringVar(&reconcileIdentitySchema, "schema", "", "schema name, e.g. IQC.fbs")
	reconcileIngestIdentityCmd.Flags().StringVar(&reconcileIdentityProvider, "provider", "", "provider ID of the lane")
	reconcileIngestIdentityCmd.Flags().StringVar(&reconcileIdentitySource, "source", "", "source name of the lane")
	reconcileIngestIdentityCmd.Flags().BoolVar(&reconcileIdentityApply, "apply", false, "remove the copies instead of reporting them")
	rootCmd.AddCommand(reconcileIngestIdentityCmd)
}

func runReconcileIngestIdentity(cmd *cobra.Command, args []string) error {
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return fmt.Errorf("failed to initialize schema validator: %w", err)
	}
	store, err := storage.NewFlatSQLStore(cfg.Storage.Path, validator)
	if err != nil {
		if errors.Is(err, storage.ErrStoreLocked) {
			return fmt.Errorf("reconcile-ingest-identity needs EXCLUSIVE store access — stop the daemon first: %w", err)
		}
		return fmt.Errorf("failed to open storage: %w", err)
	}
	defer store.Close()

	result, err := store.ReconcileLaneIngestIdentities(reconcileIdentitySchema, reconcileIdentityProvider, reconcileIdentitySource, reconcileIdentityApply)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
