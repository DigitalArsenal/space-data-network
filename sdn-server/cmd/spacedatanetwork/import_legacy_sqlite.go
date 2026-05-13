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
	importLegacySourceDB           string
	importLegacySourceTable        string
	importLegacyStoragePath        string
	importLegacySourcePeer         string
	importLegacyBatchSize          int
	importLegacyCheckpointPath     string
	importLegacyResetCheckpoint    bool
	importLegacyMaxRows            int64
	importLegacyStoreMPE           bool
	importLegacyProviderID         string
	importLegacySourceNameForTags  string
	importLegacySourceURL          string
	importLegacyBatchID            string
	importLegacyContentKeyID       string
	importLegacyProducerPeerID     string
	importLegacyProducerPublicKey  string
	importLegacyDatastoreNamespace bool
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
	importLegacySQLiteCmd.Flags().StringVar(&importLegacyProviderID, "provider-id", "space-data-network-02", "SDN provider ID for imported source tags")
	importLegacySQLiteCmd.Flags().StringVar(&importLegacySourceNameForTags, "source-name", "celestrak-gp-historical", "SDN source name for imported source tags")
	importLegacySQLiteCmd.Flags().StringVar(&importLegacySourceURL, "source-url", "", "source URL for imported source tags (default: file://<absolute source-db>)")
	importLegacySQLiteCmd.Flags().StringVar(&importLegacyBatchID, "batch-id", "", "batch ID for imported source tags (default: source DB metadata fingerprint)")
	importLegacySQLiteCmd.Flags().StringVar(&importLegacyContentKeyID, "content-key-id", "public", "content key ID for imported source tags")
	importLegacySQLiteCmd.Flags().StringVar(&importLegacyProducerPeerID, "producer-peer-id", "", "producer peer ID for imported source tags (default: --source-peer)")
	importLegacySQLiteCmd.Flags().StringVar(&importLegacyProducerPublicKey, "producer-public-key", "", "producer public key for imported source tags")
	importLegacySQLiteCmd.Flags().BoolVar(&importLegacyDatastoreNamespace, "datastore-namespace", false, "store OMM rows in an isolated SDN datastore namespace instead of adding per-record source tags")
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

	if importLegacyDatastoreNamespace && importLegacyStoreMPE {
		return fmt.Errorf("--datastore-namespace currently supports OMM import only; run MPE as a separate source datastore")
	}

	sourceTags, err := legacyImportSourceTags(sourceDB, importLegacySourcePeer)
	if err != nil {
		return err
	}

	destinationPath := storagePath
	var datastoreIdentity storage.DatastoreIdentity
	if importLegacyDatastoreNamespace {
		datastoreIdentity = legacyImportDatastoreIdentity(sourceTags)
		destinationPath, err = storage.DatastoreIdentityPath(storagePath, datastoreIdentity)
		if err != nil {
			return err
		}
	}

	checkpointPath := strings.TrimSpace(importLegacyCheckpointPath)
	if checkpointPath == "" {
		checkpointPath = filepath.Join(destinationPath, "legacy-import-checkpoint.json")
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

	var store *storage.FlatSQLStore
	if importLegacyDatastoreNamespace {
		store, err = storage.NewFlatSQLStoreForIdentity(storagePath, validator, datastoreIdentity)
		if err != nil {
			return fmt.Errorf("failed to open namespaced destination storage: %w", err)
		}
	} else {
		store, err = storage.NewFlatSQLStore(storagePath, validator)
		if err != nil {
			return fmt.Errorf("failed to open destination storage: %w", err)
		}
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
		sourceDB, tableName, destinationPath, importLegacyBatchSize, checkpointPath, checkpoint.LastRowID, importLegacyStoreMPE,
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
		ommBatch := make([][]byte, 0, importLegacyBatchSize)
		mpeBatch := make([][]byte, 0, importLegacyBatchSize)

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
			ommBatch = append(ommBatch, ommBytes)
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
				mpeBatch = append(mpeBatch, mpeBytes)
				checkpoint.MPEStored++
			}
		}

		if err := rows.Close(); err != nil {
			return fmt.Errorf("failed closing source rows: %w", err)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("source rows iteration error: %w", err)
		}
		if importLegacyDatastoreNamespace {
			if _, err := store.StoreBatch("OMM.fbs", ommBatch, importLegacySourcePeer, nil); err != nil {
				return fmt.Errorf("store OMM legacy namespace batch after rowid=%d: %w", lastRowID, err)
			}
		} else if _, err := store.StoreBatchWithSourceTags("OMM.fbs", ommBatch, importLegacySourcePeer, nil, sourceTags); err != nil {
			return fmt.Errorf("store OMM legacy batch after rowid=%d: %w", lastRowID, err)
		}
		if importLegacyStoreMPE {
			if _, err := store.StoreBatchWithSourceTags("MPE.fbs", mpeBatch, importLegacySourcePeer, nil, sourceTags); err != nil {
				return fmt.Errorf("store MPE legacy batch after rowid=%d: %w", lastRowID, err)
			}
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

	log.Infof(
		"Legacy import complete: scanned=%d omm=%d mpe=%d checkpoint=%s",
		checkpoint.RowsScanned, checkpoint.OMMStored, checkpoint.MPEStored, checkpointPath,
	)
	return nil
}

func legacyImportSourceTags(sourceDB, sourcePeer string) (storage.SourceTags, error) {
	sourceDB = strings.TrimSpace(sourceDB)
	sourcePeer = strings.TrimSpace(sourcePeer)
	if sourcePeer == "" {
		sourcePeer = "source:legacy-sqlite"
	}
	providerID := strings.TrimSpace(importLegacyProviderID)
	if providerID == "" {
		providerID = sourcePeer
	}
	sourceName := strings.TrimSpace(importLegacySourceNameForTags)
	if sourceName == "" {
		sourceName = "celestrak-gp-historical"
	}
	sourceURL := strings.TrimSpace(importLegacySourceURL)
	if sourceURL == "" {
		absSourceDB, err := filepath.Abs(sourceDB)
		if err != nil {
			absSourceDB = sourceDB
		}
		sourceURL = "file://" + absSourceDB
	}
	batchID := strings.TrimSpace(importLegacyBatchID)
	if batchID == "" {
		var err error
		batchID, err = legacyImportBatchID(sourceDB)
		if err != nil {
			return storage.SourceTags{}, err
		}
	}
	contentKeyID := strings.TrimSpace(importLegacyContentKeyID)
	if contentKeyID == "" {
		contentKeyID = "public"
	}
	producerPeerID := strings.TrimSpace(importLegacyProducerPeerID)
	if producerPeerID == "" {
		producerPeerID = sourcePeer
	}
	producerPublicKey := strings.TrimSpace(importLegacyProducerPublicKey)
	return storage.SourceTags{
		ProviderID:        providerID,
		SourceName:        sourceName,
		SourceURL:         sourceURL,
		BatchID:           batchID,
		ContentKeyID:      contentKeyID,
		ProducerPeerID:    producerPeerID,
		ProducerPublicKey: producerPublicKey,
	}, nil
}

func legacyImportDatastoreIdentity(tags storage.SourceTags) storage.DatastoreIdentity {
	return storage.DatastoreIdentity{
		SchemaName:      "OMM.fbs",
		SourcePeerID:    tags.ProducerPeerID,
		SourcePublicKey: tags.ProducerPublicKey,
		ProviderID:      tags.ProviderID,
		SourceName:      tags.SourceName,
		BatchHead:       tags.BatchID,
		QueryProfile:    storage.DatasetPublicationQueryProfile,
		SnapshotID:      tags.BatchID,
		HighWaterMark:   tags.BatchID,
		ArtifactHash:    tags.BatchID,
	}
}

func legacyImportBatchID(sourceDB string) (string, error) {
	info, err := os.Stat(sourceDB)
	if err != nil {
		return "", fmt.Errorf("stat legacy source db: %w", err)
	}
	return fmt.Sprintf("legacy-sqlite:%s:%d:%d", filepath.Base(sourceDB), info.Size(), info.ModTime().UTC().UnixNano()), nil
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
