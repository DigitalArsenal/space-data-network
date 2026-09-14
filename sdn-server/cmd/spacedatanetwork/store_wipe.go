package main

// store_wipe.go: `spacedatanetwork store-wipe` removes a node's RECORD layers
// and nothing else.
//
// There is no rm -rf here and no "everything except". The verb names every
// file and directory it removes, one by one, and refuses to run while a
// daemon holds the store. Everything it does not name survives by
// construction: the node's identity (keys/, which is outside the store
// anyway), the auxiliary journal (the node's own EPM, pin ledger, dataset
// publications, licences), the operator auth database, credentials, module
// and licence state, flows, TLS material.
//
// The record layers are re-derivable from their publishers; the rest is not.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spf13/cobra"
)

// storeWipeTargets are the record layers, relative to storage.path. A name
// ending in "/" is a directory.
var storeWipeTargets = []string{
	"control.flatsqldb",
	"control.flatsqldb-journal",
	"control.flatsqldb.fsdata",
	"flatsql-streams/",
	"record-catalog.flatsqlmeta",
	"record-catalog.flatsqlmeta.prefix",
	"dataset-publication-replay/",
	"dataset-feed-head-sync/",
	"ui-cache/",
}

// storeWipeMustSurvive are the entries the wipe REFUSES to touch even if a
// future edit of the target list names one of them by mistake.
var storeWipeMustSurvive = []string{
	"auxiliary.flatsqlmeta",
	"auth.db", "auth.db-wal", "auth.db-shm",
	"keys/", "secrets/", "modules/", "customer-modules/", "license/", "flows/",
	"p2p-forge-certs/", "tls/", "field-encryption-identity.json",
}

var storeWipeYes bool

var storeWipeCmd = &cobra.Command{
	Use:   "store-wipe",
	Short: "Remove the node's record layers (control database, engine arena, legacy journal and streams) and nothing else",
	Long: `Remove the record layers of the store named by --config, one named file
or directory at a time, and leave every other file in place.

Removed:  control.flatsqldb (+ -journal, .fsdata), flatsql-streams/,
          record-catalog.flatsqlmeta (+ .prefix), dataset-publication-replay/,
          dataset-feed-head-sync/, ui-cache/
Kept:     everything else — auxiliary.flatsqlmeta (the node's own EPM, pin
          ledger, dataset publications, licences), auth.db, secrets/,
          modules/, customer-modules/, license/, flows/, TLS material, and the
          identity in keys/ (outside the store).

The verb refuses to run while a daemon holds the store, prints the plan, and
does nothing without --yes. Records come back from their publishers; the
next daemon start opens an empty record store and serves.`,
	RunE: runStoreWipe,
}

func init() {
	storeWipeCmd.Flags().BoolVar(&storeWipeYes, "yes", false, "actually remove the record layers (without it the plan is printed and nothing is touched)")
	rootCmd.AddCommand(storeWipeCmd)
}

func runStoreWipe(cmd *cobra.Command, args []string) error {
	if strings.TrimSpace(configPath) == "" {
		return errors.New("--config is required: the wipe removes files under the configured storage.path")
	}
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	storagePath := strings.TrimSpace(cfg.Storage.Path)
	if storagePath == "" {
		return errors.New("config has no storage.path")
	}
	authDBPath, err := resolveAuthDBPath(cfg)
	if err != nil {
		return err
	}
	return wipeStoreRecordLayers(cmd.OutOrStdout(), storagePath, authDBPath, storeWipeYes)
}

// wipeStoreRecordLayers is the whole operation, separated from the command so
// it can be exercised against a fixture store.
func wipeStoreRecordLayers(out io.Writer, storagePath, authDBPath string, apply bool) error {
	info, err := os.Stat(storagePath)
	if err != nil {
		return fmt.Errorf("storage.path %s: %w", storagePath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("storage.path %s is not a directory", storagePath)
	}
	for _, target := range storeWipeTargets {
		if err := storeWipeRefuse(storagePath, target, authDBPath); err != nil {
			return err
		}
	}

	// The store must not be open: a daemon that keeps its file descriptors
	// would keep writing into files this verb has just unlinked.
	lock, err := storage.LockStoreForMaintenance(storagePath)
	if err != nil {
		return fmt.Errorf("the store is in use — stop the daemon first: %w", err)
	}
	defer lock.Release()

	entries, err := os.ReadDir(storagePath)
	if err != nil {
		return fmt.Errorf("list %s: %w", storagePath, err)
	}
	targets := map[string]bool{}
	for _, t := range storeWipeTargets {
		targets[strings.TrimSuffix(t, "/")] = true
	}
	var remove, keep []string
	for _, e := range entries {
		name := e.Name()
		if name == storage.StoreLockFileName {
			continue // this verb holds it; the next opener recreates it
		}
		if targets[name] {
			remove = append(remove, name)
		} else {
			keep = append(keep, name)
		}
	}
	sort.Strings(remove)
	sort.Strings(keep)

	fmt.Fprintf(out, "store: %s\n", storagePath)
	fmt.Fprintf(out, "auth database (kept): %s\n", authDBPath)
	fmt.Fprintf(out, "REMOVE (%d):\n", len(remove))
	for _, name := range remove {
		fmt.Fprintf(out, "  - %s (%s)\n", name, storeWipeSize(filepath.Join(storagePath, name)))
	}
	fmt.Fprintf(out, "KEEP (%d):\n", len(keep))
	for _, name := range keep {
		fmt.Fprintf(out, "  + %s\n", name)
	}
	if !apply {
		fmt.Fprintln(out, "dry run: nothing removed (pass --yes to apply)")
		return nil
	}
	for _, name := range remove {
		path := filepath.Join(storagePath, name)
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		fmt.Fprintf(out, "removed %s\n", name)
	}
	fmt.Fprintf(out, "record layers removed; the next daemon start opens an empty record store\n")
	return nil
}

// storeWipeRefuse fails the whole verb before anything is touched when a
// target names something that must survive, or the auth database.
func storeWipeRefuse(storagePath, target, authDBPath string) error {
	name := strings.TrimSuffix(target, "/")
	for _, keep := range storeWipeMustSurvive {
		if strings.TrimSuffix(keep, "/") == name {
			return fmt.Errorf("refusing to wipe %s: it is not a record layer", name)
		}
	}
	if abs := filepath.Join(storagePath, name); abs == filepath.Clean(authDBPath) ||
		strings.HasPrefix(filepath.Clean(authDBPath), abs+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to wipe %s: it holds the auth database %s", name, authDBPath)
	}
	return nil
}

// storeWipeSize renders a target's size for the plan.
func storeWipeSize(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return "absent"
	}
	if !info.IsDir() {
		return storeWipeHuman(info.Size())
	}
	var total int64
	_ = filepath.Walk(path, func(_ string, fi os.FileInfo, walkErr error) error {
		if walkErr == nil && !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	return storeWipeHuman(total) + " dir"
}

func storeWipeHuman(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
