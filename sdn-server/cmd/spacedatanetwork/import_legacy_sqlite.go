package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	MPEFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	"github.com/google/flatbuffers/go"
	_ "github.com/mattn/go-sqlite3"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spf13/cobra"
)

var importLegacySQLiteCmd = &cobra.Command{
	Use:   "import-legacy-sqlite",
	Short: "Import legacy satellite_data.db rows into FlatSQL",
	Long: `Streams rows from a legacy SQLite table (default: satellite_data) and stores
them as OMM (and optionally MPE) FlatBuffers in the current FlatSQL storage.`,
	RunE: runImportLegacySQLite,
}

var (
	importLegacySourceDB        string
	importLegacySourceTable     string
	importLegacyStoragePath     string
	importLegacySourcePeer      string
	importLegacyBatchSize       int
	importLegacyCheckpointPath  string
	importLegacyResetCheckpoint bool
	importLegacyMaxRows         int64
	importLegacyStoreMPE        bool
)

func init() {
	importLegacySQLiteCmd.Flags().StringVar(&importLegacySourceDB, "source-db", "", "path to legacy SQLite database file (required)")
	importLegacySQLiteCmd.Flags().StringVar(&importLegacySourceTable, "source-table", "satellite_data", "legacy source table name")
	importLegacySQLiteCmd.Flags().StringVar(&importLegacyStoragePath, "storage-path", "", "override destination storage path (defaults to config.storage.path)")
	importLegacySQLiteCmd.Flags().StringVar(&importLegacySourcePeer, "source-peer", "source:legacy-sqlite", "peer_id to store on imported records")
	importLegacySQLiteCmd.Flags().IntVar(&importLegacyBatchSize, "batch-size", 2000, "rows to process per batch")
	importLegacySQLiteCmd.Flags().StringVar(&importLegacyCheckpointPath, "checkpoint-file", "", "checkpoint file path (default: <storage-path>/legacy-import-checkpoint.json)")
	importLegacySQLiteCmd.Flags().BoolVar(&importLegacyResetCheckpoint, "reset-checkpoint", false, "start import from rowid=0 and overwrite existing checkpoint")
	importLegacySQLiteCmd.Flags().Int64Var(&importLegacyMaxRows, "max-rows", 0, "stop after scanning this many rows (0 = unlimited)")
	importLegacySQLiteCmd.Flags().BoolVar(&importLegacyStoreMPE, "store-mpe", false, "also store MPE records derived from legacy rows (optional)")
	_ = importLegacySQLiteCmd.MarkFlagRequired("source-db")

	rootCmd.AddCommand(importLegacySQLiteCmd)
}

type legacyImportCheckpoint struct {
	LastRowID   int64  `json:"last_row_id"`
	RowsScanned int64  `json:"rows_scanned"`
	OMMStored   int64  `json:"omm_stored"`
	MPEStored   int64  `json:"mpe_stored"`
	UpdatedAt   string `json:"updated_at"`
}

var legacyTableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func runImportLegacySQLite(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	storagePath := strings.TrimSpace(importLegacyStoragePath)
	if storagePath == "" {
		storagePath = strings.TrimSpace(cfg.Storage.Path)
	}
	if storagePath == "" {
		return fmt.Errorf("storage path is required")
	}

	sourceDB := strings.TrimSpace(importLegacySourceDB)
	if sourceDB == "" {
		return fmt.Errorf("--source-db is required")
	}

	tableName := strings.TrimSpace(importLegacySourceTable)
	if !legacyTableNamePattern.MatchString(tableName) {
		return fmt.Errorf("invalid --source-table %q", tableName)
	}

	if importLegacyBatchSize <= 0 {
		return fmt.Errorf("--batch-size must be > 0")
	}

	checkpointPath := strings.TrimSpace(importLegacyCheckpointPath)
	if checkpointPath == "" {
		checkpointPath = filepath.Join(storagePath, "legacy-import-checkpoint.json")
	}

	if importLegacyResetCheckpoint {
		if err := os.Remove(checkpointPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to reset checkpoint: %w", err)
		}
	}

	checkpoint, err := loadLegacyCheckpoint(checkpointPath)
	if err != nil {
		return err
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return fmt.Errorf("failed to initialize schema validator: %w", err)
	}

	store, err := storage.NewFlatSQLStore(storagePath, validator)
	if err != nil {
		return fmt.Errorf("failed to open destination storage: %w", err)
	}
	defer store.Close()

	src, err := sql.Open("sqlite3", "file:"+sourceDB+"?mode=ro")
	if err != nil {
		return fmt.Errorf("failed to open source db: %w", err)
	}
	defer src.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Infof(
		"Starting legacy import: source=%s table=%s storage=%s batch=%d checkpoint=%s start_rowid=%d store_mpe=%v",
		sourceDB, tableName, storagePath, importLegacyBatchSize, checkpointPath, checkpoint.LastRowID, importLegacyStoreMPE,
	)

	query := fmt.Sprintf(`
		SELECT rowid, OBJECT_ID, EPOCH, MEAN_MOTION, ECCENTRICITY, INCLINATION,
		       RA_OF_ASC_NODE, ARG_OF_PERICENTER, MEAN_ANOMALY, NORAD_CAT_ID, BSTAR
		FROM "%s"
		WHERE rowid > ?
		ORDER BY rowid
		LIMIT ?
	`, tableName)

	started := time.Now()
	lastProgress := started
	writeCheckpoint := func() error {
		checkpoint.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return saveLegacyCheckpoint(checkpointPath, checkpoint)
	}

	var storeErrors int64

	for {
		if err := ctx.Err(); err != nil {
			if saveErr := writeCheckpoint(); saveErr != nil {
				log.Warnf("Failed to save checkpoint on cancellation: %v", saveErr)
			}
			return fmt.Errorf("import cancelled: %w", err)
		}

		rows, err := src.QueryContext(ctx, query, checkpoint.LastRowID, importLegacyBatchSize)
		if err != nil {
			return fmt.Errorf("failed to query source rows: %w", err)
		}

		var batchCount int64
		var lastRowID int64

		for rows.Next() {
			var (
				rowID       int64
				objectID    sql.NullString
				epoch       sql.NullString
				meanMotion  sql.NullFloat64
				ecc         sql.NullFloat64
				incl        sql.NullFloat64
				raan        sql.NullFloat64
				argp        sql.NullFloat64
				meanAnomaly sql.NullFloat64
				noradID     sql.NullInt64
				bstar       sql.NullFloat64
			)

			if err := rows.Scan(
				&rowID,
				&objectID,
				&epoch,
				&meanMotion,
				&ecc,
				&incl,
				&raan,
				&argp,
				&meanAnomaly,
				&noradID,
				&bstar,
			); err != nil {
				rows.Close()
				return fmt.Errorf("failed to scan legacy row: %w", err)
			}

			if importLegacyMaxRows > 0 && checkpoint.RowsScanned >= importLegacyMaxRows {
				break
			}

			batchCount++
			checkpoint.RowsScanned++
			lastRowID = rowID

			if !noradID.Valid || noradID.Int64 <= 0 || noradID.Int64 > math.MaxUint32 {
				continue
			}
			norad := uint32(noradID.Int64)

			objectIDValue := strings.TrimSpace(objectID.String)
			if objectIDValue == "" {
				objectIDValue = fmt.Sprintf("NORAD-%d", norad)
			}

			builder := sds.NewOMMBuilder().
				WithNoradCatID(norad).
				WithObjectName(fmt.Sprintf("SAT-%d", norad)).
				WithObjectID(objectIDValue)

			epochString := normalizeLegacyEpoch(epoch.String)
			if epochString != "" {
				builder = builder.WithEpoch(epochString)
			}
			if meanMotion.Valid {
				builder = builder.WithMeanMotion(meanMotion.Float64)
			}
			if ecc.Valid {
				builder = builder.WithEccentricity(ecc.Float64)
			}
			if incl.Valid {
				builder = builder.WithInclination(incl.Float64)
			}
			if raan.Valid {
				builder = builder.WithRaOfAscNode(raan.Float64)
			}
			if argp.Valid {
				builder = builder.WithArgOfPericenter(argp.Float64)
			}
			if meanAnomaly.Valid {
				builder = builder.WithMeanAnomaly(meanAnomaly.Float64)
			}

			ommBytes := builder.Build()
			if _, err := store.Store("OMM.fbs", ommBytes, importLegacySourcePeer, nil); err != nil {
				storeErrors++
				if storeErrors <= 5 || storeErrors%1000 == 0 {
					log.Warnf("OMM store error at rowid=%d: %v (total store errors=%d)", rowID, err, storeErrors)
				}
				continue
			}
			checkpoint.OMMStored++

			if importLegacyStoreMPE {
				epochUnix := int64(0)
				if t, err := parseLegacyEpoch(epochString); err == nil {
					epochUnix = t.Unix()
				}
				mpeBytes := buildLegacyMPE(
					objectIDValue,
					epochUnix,
					valueOrZero(meanMotion),
					valueOrZero(ecc),
					valueOrZero(incl),
					valueOrZero(raan),
					valueOrZero(argp),
					valueOrZero(meanAnomaly),
					valueOrZero(bstar),
				)
				if _, err := store.Store("MPE.fbs", mpeBytes, importLegacySourcePeer, nil); err != nil {
					storeErrors++
					if storeErrors <= 5 || storeErrors%1000 == 0 {
						log.Warnf("MPE store error at rowid=%d: %v (total store errors=%d)", rowID, err, storeErrors)
					}
					continue
				}
				checkpoint.MPEStored++
			}
		}

		if err := rows.Close(); err != nil {
			return fmt.Errorf("failed closing source rows: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("source rows iteration error: %w", err)
		}

		if lastRowID > 0 {
			checkpoint.LastRowID = lastRowID
		}

		if err := writeCheckpoint(); err != nil {
			return fmt.Errorf("failed to save checkpoint: %w", err)
		}

		now := time.Now()
		if now.Sub(lastProgress) >= 10*time.Second || batchCount == 0 {
			elapsed := now.Sub(started).Seconds()
			rate := float64(checkpoint.RowsScanned)
			if elapsed > 0 {
				rate = rate / elapsed
			}
			log.Infof(
				"Legacy import progress: rowid=%d scanned=%d omm=%d mpe=%d rate=%.1f rows/s",
				checkpoint.LastRowID, checkpoint.RowsScanned, checkpoint.OMMStored, checkpoint.MPEStored, rate,
			)
			lastProgress = now
		}

		if batchCount == 0 {
			break
		}
		if importLegacyMaxRows > 0 && checkpoint.RowsScanned >= importLegacyMaxRows {
			log.Infof("Stopped due to --max-rows=%d", importLegacyMaxRows)
			break
		}
	}

	if storeErrors > 0 {
		log.Warnf("Legacy import completed with %d store errors", storeErrors)
	}

	log.Infof(
		"Legacy import complete: scanned=%d omm=%d mpe=%d checkpoint=%s",
		checkpoint.RowsScanned, checkpoint.OMMStored, checkpoint.MPEStored, checkpointPath,
	)
	return nil
}

func loadLegacyCheckpoint(path string) (*legacyImportCheckpoint, error) {
	cp := &legacyImportCheckpoint{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cp, nil
		}
		return nil, fmt.Errorf("failed reading checkpoint %s: %w", path, err)
	}
	if len(data) == 0 {
		return cp, nil
	}
	if err := json.Unmarshal(data, cp); err != nil {
		return nil, fmt.Errorf("failed parsing checkpoint %s: %w", path, err)
	}
	return cp, nil
}

func saveLegacyCheckpoint(path string, cp *legacyImportCheckpoint) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func valueOrZero(v sql.NullFloat64) float64 {
	if v.Valid {
		return v.Float64
	}
	return 0
}

func normalizeLegacyEpoch(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if t, err := parseLegacyEpoch(raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return raw
}

func parseLegacyEpoch(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000000",
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}

	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}

	if f, err := strconv.ParseFloat(raw, 64); err == nil && f > 0 {
		sec := int64(f)
		nsec := int64((f - float64(sec)) * float64(time.Second))
		return time.Unix(sec, nsec).UTC(), nil
	}

	return time.Time{}, fmt.Errorf("unsupported epoch format: %q", raw)
}

func buildLegacyMPE(entityID string, epochUnix int64, meanMotion, ecc, incl, raan, argp, meanAnomaly, bstar float64) []byte {
	builder := flatbuffers.NewBuilder(256)
	entityIDOffset := builder.CreateString(entityID)

	MPEFB.MPEStart(builder)
	MPEFB.MPEAddENTITY_ID(builder, entityIDOffset)
	if epochUnix > 0 {
		MPEFB.MPEAddEPOCH(builder, float64(epochUnix))
	}
	if meanMotion != 0 {
		MPEFB.MPEAddMEAN_MOTION(builder, meanMotion)
	}
	if ecc != 0 {
		MPEFB.MPEAddECCENTRICITY(builder, ecc)
	}
	if incl != 0 {
		MPEFB.MPEAddINCLINATION(builder, incl)
	}
	if raan != 0 {
		MPEFB.MPEAddRA_OF_ASC_NODE(builder, raan)
	}
	if argp != 0 {
		MPEFB.MPEAddARG_OF_PERICENTER(builder, argp)
	}
	if meanAnomaly != 0 {
		MPEFB.MPEAddMEAN_ANOMALY(builder, meanAnomaly)
	}
	if bstar != 0 {
		MPEFB.MPEAddBSTAR(builder, bstar)
	}
	mpe := MPEFB.MPEEnd(builder)
	MPEFB.FinishSizePrefixedMPEBuffer(builder, mpe)

	out := make([]byte, len(builder.FinishedBytes()))
	copy(out, builder.FinishedBytes())
	return out
}
