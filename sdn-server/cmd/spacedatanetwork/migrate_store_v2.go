package main

// migrate_store_v2.go (loop B.7): `spacedatanetwork migrate-store-v2`
// generates the FlatSQL v2 compact record metadata from a deployed v1
// basePath containing sdn.db + flatsql-streams/. The legacy sdn.db is opened
// READ-ONLY through the pure-Go modernc.org/sqlite driver and is left
// untouched; record payload bytes stay in the unchanged append-only stream
// files. The load-bearing invariant: sdn_record_index rowids (the datasync
// cursor space deployed peers hold) are preserved exactly, including sparse
// gaps — see storage.MigrateLegacyControl.

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spf13/cobra"
)

var (
	migrateStoreV2Source      string
	migrateStoreV2Target      string
	migrateStoreV2WindowLimit int
)

const migrateStoreV2RecordCatalogFile = "record-catalog.flatsqlmeta"

var migrateStoreV2Cmd = &cobra.Command{
	Use:   "migrate-store-v2",
	Short: "Generate FlatSQL v2 compact metadata from a legacy sdn.db",
	Long: `Migrate a deployed v1 datastore (sdn.db + flatsql-streams/) to the
FlatSQL-WASM engine store by generating compact record metadata from the
legacy sqlite control tables.

The legacy sdn.db is opened read-only and never modified; record payload
bytes stay in the unchanged flatsql-streams/*.flatsql files. Every control
row is copied WITH ITS LEGACY ROWID (including gaps left by GC) so deployed
peers' datasync cursors remain valid byte-for-byte.

The migration is hot-window-bounded: only the newest --window-limit
sdn_record_index rows (plus their tags and stream metadata) are migrated —
a full-history control DB does not fit one engine (the wasm memory ceiling).
Rows below the window stay in the legacy sdn.db/streams as archive; peers
with cursors below the window page into the window start or snapshot-resync
(designed behavior). --window-limit 0 migrates the full history (small
stores only).

--target defaults to --source (in-place: record-catalog.flatsqlmeta is
created alongside the untouched sdn.db). For a dry run, point --target at a
scratch directory that already contains a copy or symlink of
<source>/flatsql-streams. The command refuses to run if the target already
has compact record metadata.`,
	RunE: runMigrateStoreV2,
}

func init() {
	migrateStoreV2Cmd.Flags().StringVar(&migrateStoreV2Source, "source", "", "legacy datastore basePath containing sdn.db and flatsql-streams/ (required)")
	migrateStoreV2Cmd.Flags().StringVar(&migrateStoreV2Target, "target", "", "destination basePath for the v2 store (default: --source, in-place)")
	migrateStoreV2Cmd.Flags().IntVar(&migrateStoreV2WindowLimit, "window-limit", 1500000, "migrate only the newest N sdn_record_index rows (the hot window); 0 = full history")
	_ = migrateStoreV2Cmd.MarkFlagRequired("source")
	rootCmd.AddCommand(migrateStoreV2Cmd)
}

func runMigrateStoreV2(cmd *cobra.Command, args []string) error {
	source := strings.TrimSpace(migrateStoreV2Source)
	if source == "" {
		return errors.New("--source is required")
	}
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("resolve --source: %w", err)
	}
	target := strings.TrimSpace(migrateStoreV2Target)
	if target == "" {
		target = sourceAbs
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve --target: %w", err)
	}

	legacyDBPath := filepath.Join(sourceAbs, "sdn.db")
	if _, err := os.Stat(legacyDBPath); err != nil {
		return fmt.Errorf("legacy sqlite database not found at %s: %w", legacyDBPath, err)
	}
	catalogPath := filepath.Join(targetAbs, migrateStoreV2RecordCatalogFile)
	if _, err := os.Stat(catalogPath); err == nil {
		return fmt.Errorf("refusing to migrate: %s already exists (the target already has v2 record metadata; remove it or choose another --target)", catalogPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", catalogPath, err)
	}
	if targetAbs != sourceAbs {
		streams := filepath.Join(targetAbs, "flatsql-streams")
		if info, err := os.Stat(streams); err != nil || !info.IsDir() {
			return fmt.Errorf("target %s has no flatsql-streams directory: the migration only rewrites control metadata — copy or symlink %s there first (record payload bytes stay in the stream files)",
				targetAbs, filepath.Join(sourceAbs, "flatsql-streams"))
		}
	}

	// Read-only, immutable open: the legacy database is input only.
	legacy, err := sql.Open("sqlite", "file:"+legacyDBPath+"?mode=ro&immutable=1")
	if err != nil {
		return fmt.Errorf("open legacy sqlite database: %w", err)
	}
	defer legacy.Close()
	if err := legacy.Ping(); err != nil {
		return fmt.Errorf("open legacy sqlite database %s: %w", legacyDBPath, err)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return fmt.Errorf("initialize SDS validator: %w", err)
	}
	store, err := storage.NewFlatSQLStore(targetAbs, validator)
	if err != nil {
		return fmt.Errorf("open v2 store at %s: %w", targetAbs, err)
	}
	defer store.Close()

	report, err := store.MigrateLegacyControl(legacy, storage.LegacyMigrationOptions{
		WindowLimit: migrateStoreV2WindowLimit,
	})
	if report != nil {
		printLegacyMigrationReport(cmd, targetAbs, catalogPath, report)
	}
	if err != nil {
		return fmt.Errorf("legacy migration failed: %w", err)
	}
	if problems := legacyMigrationProblems(report); len(problems) > 0 {
		return fmt.Errorf("migration verification failed: %s", strings.Join(problems, "; "))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "migration complete: %s\n", catalogPath)
	return nil
}

func legacyMigrationProblems(report *storage.LegacyMigrationReport) []string {
	var problems []string
	if report.NewRecordIndexMaxRowID != report.LegacyRecordIndexMaxRowID {
		problems = append(problems, fmt.Sprintf(
			"sdn_record_index MAX(rowid) mismatch: legacy=%d new=%d (datasync cursor space diverged)",
			report.LegacyRecordIndexMaxRowID, report.NewRecordIndexMaxRowID))
	}
	if report.NewTagsCount != report.ExpectedTagsCount {
		problems = append(problems, fmt.Sprintf(
			"sdn_record_source_tags count mismatch: expected=%d (of %d legacy) new=%d",
			report.ExpectedTagsCount, report.LegacyTagsCount, report.NewTagsCount))
	}
	if report.SampleMatched != report.SampleChecked {
		problems = append(problems, fmt.Sprintf(
			"record-index sample check failed: %d/%d matched", report.SampleMatched, report.SampleChecked))
		problems = append(problems, report.SampleMismatches...)
	}
	return problems
}

func printLegacyMigrationReport(cmd *cobra.Command, targetAbs, catalogPath string, report *storage.LegacyMigrationReport) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "legacy store migration report (target %s)\n", targetAbs)
	fmt.Fprintf(out, "  record catalog: %s\n", catalogPath)
	fmt.Fprintln(out, "  tables:")
	for _, t := range report.Tables {
		if t.SkippedReason != "" {
			fmt.Fprintf(out, "    %-48s legacy=%-9d SKIPPED: %s\n", t.Name, t.LegacyRows, t.SkippedReason)
			continue
		}
		note := ""
		if t.WindowFiltered {
			note = " (window-filtered)"
		}
		fmt.Fprintf(out, "    %-48s legacy=%-9d copied=%d%s\n", t.Name, t.LegacyRows, t.CopiedRows, note)
	}
	if report.WindowMinRowID > 0 {
		fmt.Fprintf(out, "  hot window: limit=%d minRowID=%d windowRows=%d of %d legacy index rows\n",
			report.WindowLimit, report.WindowMinRowID, report.WindowRows, report.LegacyRecordIndexTotalRows)
		fmt.Fprintf(out, "  NOTE: %d legacy rows below the window were not migrated; peers with older cursors will resync\n",
			report.TruncatedRows)
	} else {
		fmt.Fprintf(out, "  hot window: not truncated (%d legacy index rows, limit=%d)\n",
			report.LegacyRecordIndexTotalRows, report.WindowLimit)
	}
	fmt.Fprintf(out, "  sdn_record_index MAX(rowid): legacy=%d new=%d\n",
		report.LegacyRecordIndexMaxRowID, report.NewRecordIndexMaxRowID)
	fmt.Fprintf(out, "  sdn_record_source_tags rows: legacy=%d expected=%d new=%d\n",
		report.LegacyTagsCount, report.ExpectedTagsCount, report.NewTagsCount)
	fmt.Fprintf(out, "  record-index sample check: %d/%d matched\n",
		report.SampleMatched, report.SampleChecked)
	for _, mismatch := range report.SampleMismatches {
		fmt.Fprintf(out, "    MISMATCH: %s\n", mismatch)
	}
	fmt.Fprintf(out, "  engine hot-window records ingested: %d\n", report.EngineRecordsIngested)
}
