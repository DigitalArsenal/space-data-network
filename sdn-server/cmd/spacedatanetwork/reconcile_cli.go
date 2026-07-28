package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spf13/cobra"
)

var reconcileSourceBatchCmd = &cobra.Command{
	Use:   "reconcile-source-batch",
	Short: "Prune source-tagged records outside an accepted batch",
	Long: `Prune source-tagged records outside an accepted provider/source batch.

The command is dry-run by default. Use --apply only after the reported matched
count matches the accepted DPM/source-batch reconciliation plan.`,
	RunE: runReconcileSourceBatch,
}

var (
	reconcileSchema    string
	reconcileProvider  string
	reconcileSource    string
	reconcileKeepBatch string
	reconcileApply     bool
)

func init() {
	reconcileSourceBatchCmd.Flags().StringVar(&reconcileSchema, "schema", "", "schema name, e.g. OMM.fbs")
	reconcileSourceBatchCmd.Flags().StringVar(&reconcileProvider, "provider", "", "provider ID to reconcile")
	reconcileSourceBatchCmd.Flags().StringVar(&reconcileSource, "source", "", "source name to reconcile")
	reconcileSourceBatchCmd.Flags().StringVar(&reconcileKeepBatch, "keep-batch", "", "accepted batch ID to keep")
	reconcileSourceBatchCmd.Flags().BoolVar(&reconcileApply, "apply", false, "apply deletion instead of dry-run")
	rootCmd.AddCommand(reconcileSourceBatchCmd)
}

func runReconcileSourceBatch(cmd *cobra.Command, args []string) error {
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
		return fmt.Errorf("failed to open storage: %w", err)
	}
	defer store.Close()

	result, err := store.ReconcileSourceBatch(reconcileSchema, reconcileProvider, reconcileSource, reconcileKeepBatch, reconcileApply)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
