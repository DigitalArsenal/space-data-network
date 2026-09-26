package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	CATFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	MPEFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	"github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spf13/cobra"
)

const (
	gpArchiveDefaultZip     = "/opt/data/sdn-archive/historical/space-track-gp-history-1959-2022-11-22/Archives2.zip"
	gpArchiveDefaultWindows = "/opt/data/sdn-archive/spacetrack/gp_history/by-creation"
	gpArchiveDefaultLedger  = "/opt/data/sdn-archive/state/spacetrack-ledger.json"
	gpArchiveDefaultOut     = "/opt/data/sdn-archive/flatsql"
	gpArchiveSourcePeer     = "source:space-track"
	gpArchiveHistorySource  = "gp-history-archive"
	gpArchiveCatalogSource  = "gp-catalog"
	gpArchiveOEMSourcePeer  = "source:sdn-epoch-state"
	gpArchiveSaveInterval   = 60 * time.Second
	gpArchiveShardBatchSize = 100000
)

var errGPArchiveStopped = errors.New("GP archive import stopped")

var (
	importGPArchiveOut, importGPArchiveZipPath, importGPArchiveWindowsRoot                string
	importGPArchiveLedger, importGPArchiveCheckpoint, importGPArchiveReport               string
	importGPArchiveSATCAT, importGPArchiveEpochStateModule                                string
	importGPArchiveBatchSize, importGPArchiveMaxZipEntries, importGPArchiveMaxWindowFiles int
	importGPArchiveSkipZip, importGPArchiveHistoryOnly, importGPArchiveCatalogOnly        bool
	importGPArchiveReindexCatalog                                                         bool
	importGPArchiveZipShard, importGPArchiveJSONShard                                     string
)

var importGPArchiveCmd = &cobra.Command{
	Use: "import-gp-archive", Short: "Import Space-Track GP history archives into FlatSQL",
	Long: "Streams the historical Space-Track GP zip and verified gp_history windows into history and catalog tiers without extracting the archive.",
	RunE: runImportGPArchive,
}

func init() {
	f := importGPArchiveCmd.Flags()
	f.StringVar(&importGPArchiveOut, "out", gpArchiveDefaultOut, "FlatSQL output directory")
	f.StringVar(&importGPArchiveZipPath, "zip", gpArchiveDefaultZip, "historical GP archive zip (empty disables zip import)")
	f.BoolVar(&importGPArchiveSkipZip, "skip-zip", false, "skip the historical zip and import JSON sources only")
	f.StringVar(&importGPArchiveZipShard, "zip-shard", "", "import ZIP shard i/N using zero-based CSV entry ordinals")
	f.StringVar(&importGPArchiveJSONShard, "json-shard", "", "import JSON history shard i/N using NORAD_CAT_ID modulo N")
	f.BoolVar(&importGPArchiveHistoryOnly, "history-only", false, "write history only; skip JSON, catalog, and OEM tiers")
	f.BoolVar(&importGPArchiveCatalogOnly, "catalog-only", false, "read JSON sources and materialize catalog and OEM tiers without writing history")
	f.BoolVar(&importGPArchiveReindexCatalog, "reindex-catalog", false, "rebuild record indexes for the gp-catalog MPE/CAT and epoch-state OEM datastores")
	f.StringVar(&importGPArchiveWindowsRoot, "windows", gpArchiveDefaultWindows, "gp_history window root")
	f.StringVar(&importGPArchiveLedger, "ledger", gpArchiveDefaultLedger, "Space-Track ledger")
	f.StringVar(&importGPArchiveSATCAT, "satcat", "", "SATCAT snapshot (default: newest snapshot under archive root)")
	f.StringVar(&importGPArchiveEpochStateModule, "epoch-state-module", "", "epoch-state WASM module (empty disables OEM derivation)")
	f.StringVar(&importGPArchiveCheckpoint, "checkpoint", "", "checkpoint path (default: <out>/gp-archive-checkpoint.json)")
	f.StringVar(&importGPArchiveReport, "report", "", "run report path (default: <out>/gp-archive-report.json)")
	f.IntVar(&importGPArchiveBatchSize, "batch-size", 2000, "records per FlatSQL batch (shard default: 100000)")
	f.IntVar(&importGPArchiveMaxZipEntries, "max-zip-entries", 0, "process at most this many new CSV entries")
	f.IntVar(&importGPArchiveMaxWindowFiles, "max-window-files", 0, "process at most this many new window files")
	rootCmd.AddCommand(importGPArchiveCmd)
}

type gpArchiveOptions struct {
	Out, ZipPath, WindowsRoot, LedgerPath, SATCATPath, EpochStateModule string
	CheckpointPath, ReportPath                                          string
	BatchSize, MaxZipEntries, MaxWindowFiles                            int
	SkipZip, HistoryOnly, CatalogOnly, ReindexCatalog                   bool
	ZipShard                                                            string
	ZipShardIndex, ZipShardCount                                        int
	JSONShard                                                           string
	JSONShardIndex, JSONShardCount                                      int
	deriver                                                             gpEpochDeriver
	moduleHash                                                          string
}

type gpLatestState struct {
	EntityID     string  `json:"entity_id"`
	NORAD        uint32  `json:"norad"`
	Epoch        float64 `json:"epoch"`
	TieKind      string  `json:"tie_kind"`
	TieValue     uint64  `json:"tie_value"`
	HistoryCID   string  `json:"history_cid"`
	SourceID     string  `json:"source_id"`
	MPE          []byte  `json:"mpe"`
	GPObjectName string  `json:"gp_object_name,omitempty"`
	CatalogCID   string  `json:"catalog_cid,omitempty"`
	OEMCID       string  `json:"oem_cid,omitempty"`
	OEMParentCID string  `json:"oem_parent_cid,omitempty"`
}

type gpArchiveCheckpoint struct {
	Version              int                       `json:"version"`
	ZipSHA256            string                    `json:"zip_sha256,omitempty"`
	ZipSize              int64                     `json:"zip_size,omitempty"`
	ZipModTimeUnixNano   int64                     `json:"zip_mod_time_unix_nano,omitempty"`
	ZipCSVEntries        int                       `json:"zip_csv_entries,omitempty"`
	CompletedZipEntries  map[string]string         `json:"completed_zip_entries,omitempty"`
	CompletedWindowFiles map[string]string         `json:"completed_window_files,omitempty"`
	CompletedGPFiles     map[string]string         `json:"completed_gp_files,omitempty"`
	SnapshotGPIDs        map[string]bool           `json:"snapshot_gp_ids,omitempty"`
	LastWindowGPIDs      []string                  `json:"last_window_gp_ids,omitempty"`
	ZipRowOrder          uint64                    `json:"zip_row_order,omitempty"`
	CanonicalIDs         map[string]string         `json:"canonical_ids,omitempty"`
	Latest               map[string]*gpLatestState `json:"latest,omitempty"`
	CATNames             map[string]string         `json:"cat_names,omitempty"`
	SATCATSHA256         string                    `json:"satcat_sha256,omitempty"`
	FoldedShardMTime     map[string]int64          `json:"folded_shard_mtime,omitempty"`
	UpdatedAt            string                    `json:"updated_at"`
}

type gpArchiveSource struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	Entry        string `json:"entry,omitempty"`
	RetrievedAt  string `json:"retrieved_at,omitempty"`
	RowsRead     int64  `json:"rows_read"`
	RowsStored   int64  `json:"rows_stored"`
	RowsRejected int64  `json:"rows_rejected"`
}

type gpArchiveSourcesFile struct {
	Sources []gpArchiveSource `json:"sources"`
}

type gpArchiveHashMismatch struct{ Path, Expected, Actual string }
type gpArchiveShardSATCATMismatch struct {
	Checkpoint  string `json:"checkpoint"`
	MainSHA256  string `json:"main_sha256"`
	ShardSHA256 string `json:"shard_sha256"`
}
type gpArchiveObjectIDDisagreements struct{ Blank, OldStyle, Different int64 }
type gpArchiveAmbiguousDesignator struct {
	INTLDES string   `json:"intldes"`
	NORADs  []uint32 `json:"norads"`
}
type gpArchiveCanonicalChange struct {
	NORAD             uint32 `json:"norad"`
	Previous, Current string
}
type gpEpochFailure struct {
	Index            int `json:"index"`
	EntityID, Reason string
	SGP4Error        int `json:"sgp4Error"`
}

type gpArchiveRunReport struct {
	StartedAt                                                                        string                         `json:"started_at"`
	FinishedAt                                                                       string                         `json:"finished_at"`
	Status                                                                           string                         `json:"status"`
	RecordsRead                                                                      int64                          `json:"records_read"`
	MPEStored                                                                        int64                          `json:"mpe_stored"`
	DuplicatesSkipped                                                                int64                          `json:"duplicates_skipped"`
	CATObjects                                                                       int64                          `json:"cat_objects"`
	CatalogEntities                                                                  int64                          `json:"catalog_entities"`
	CatalogEntitiesChanged                                                           int64                          `json:"catalog_entities_changed"`
	ObjectsLackingDesignator                                                         int64                          `json:"objects_lacking_designator"`
	ObjectsWithoutDesignator                                                         []string                       `json:"objects_without_designator,omitempty"`
	NORADsWithoutSATCAT                                                              []uint32                       `json:"norads_without_satcat_designator,omitempty"`
	ObjectIDDisagreements                                                            gpArchiveObjectIDDisagreements `json:"object_id_disagreements"`
	AmbiguousDesignators                                                             []gpArchiveAmbiguousDesignator `json:"ambiguous_designators,omitempty"`
	CanonicalIDChanges                                                               []gpArchiveCanonicalChange     `json:"canonical_id_changes,omitempty"`
	RowsRejected                                                                     int64                          `json:"rows_rejected"`
	RejectedReasons                                                                  map[string]int64               `json:"rejected_reasons,omitempty"`
	HashMismatches                                                                   []gpArchiveHashMismatch        `json:"hash_mismatches,omitempty"`
	ShardSATCATMismatches                                                            []gpArchiveShardSATCATMismatch `json:"shard_satcat_mismatches,omitempty"`
	Sources                                                                          []gpArchiveSource              `json:"sources,omitempty"`
	SATCATPath                                                                       string                         `json:"satcat_path,omitempty"`
	SATCATSHA256                                                                     string                         `json:"satcat_sha256,omitempty"`
	EpochStateModuleSHA256                                                           string                         `json:"epoch_state_module_sha256,omitempty"`
	EpochStatesDerived                                                               int64                          `json:"epoch_states_derived"`
	EpochStatesFailed                                                                int64                          `json:"epoch_states_failed"`
	EpochsOutsideLeapSecondTable                                                     int64                          `json:"epochs_outside_leap_second_table"`
	MaxRoundTripPositionKm                                                           float64                        `json:"max_round_trip_position_km"`
	MaxRoundTripVelocityKmPerS                                                       float64                        `json:"max_round_trip_velocity_km_per_s"`
	EpochStateFailures                                                               []gpEpochFailure               `json:"epoch_state_failures,omitempty"`
	RateRecordsPerSecond                                                             float64                        `json:"rate_records_per_second"`
	ZipEntriesImported, WindowFilesImported, GPFilesImported                         int
	ShardCheckpointsFolded, ShardCheckpointsSkipped, ShardEntitiesUpdated            int
	CompletedZipEntriesSkipped, CompletedWindowFilesSkipped, CompletedGPFilesSkipped int
	started                                                                          time.Time
	withoutDesignatorSet                                                             map[string]struct{}
	noSATCATSet                                                                      map[uint32]struct{}
}

type gpArchiveLedger struct {
	Windows []gpArchiveLedgerWindow `json:"windows"`
	SATCAT  []gpArchiveLedgerSATCAT `json:"satcat"`
	GP      []gpArchiveLedgerSATCAT `json:"gp"`
}
type gpArchiveLedgerWindow struct {
	CreationFrom, CreationTo, File string
	Records, Bytes                 int64
	SHA256, RetrievedAt            string
}
type gpArchiveLedgerSATCAT struct {
	Date, File          string
	Records, Bytes      int64
	SHA256, RetrievedAt string
}

type gpArchiveRecord struct {
	GPID, ObjectID, ObjectName, EntityID                                                           string
	NORAD                                                                                          uint32
	Epoch, MeanMotion, Eccentricity, Inclination, RAOfAscNode, ArgOfPericenter, MeanAnomaly, BSTAR float64
}

type gpJSONScalar string

func (s *gpJSONScalar) UnmarshalJSON(data []byte) error {
	t := strings.TrimSpace(string(data))
	if t == "" || t == "null" {
		*s = ""
		return nil
	}
	if strings.HasPrefix(t, "\"") {
		var v string
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*s = gpJSONScalar(v)
		return nil
	}
	*s = gpJSONScalar(t)
	return nil
}

type gpJSONRecord struct {
	GPID            gpJSONScalar `json:"GP_ID"`
	CreationDate    gpJSONScalar `json:"CREATION_DATE"`
	ObjectID        gpJSONScalar `json:"OBJECT_ID"`
	ObjectName      gpJSONScalar `json:"OBJECT_NAME"`
	NORAD           gpJSONScalar `json:"NORAD_CAT_ID"`
	Epoch           gpJSONScalar `json:"EPOCH"`
	MeanMotion      gpJSONScalar `json:"MEAN_MOTION"`
	Eccentricity    gpJSONScalar `json:"ECCENTRICITY"`
	Inclination     gpJSONScalar `json:"INCLINATION"`
	RAOfAscNode     gpJSONScalar `json:"RA_OF_ASC_NODE"`
	ArgOfPericenter gpJSONScalar `json:"ARG_OF_PERICENTER"`
	MeanAnomaly     gpJSONScalar `json:"MEAN_ANOMALY"`
	BSTAR           gpJSONScalar `json:"BSTAR"`
}
type gpSATCATRecord struct {
	NORAD      gpJSONScalar `json:"NORAD_CAT_ID"`
	INTLDES    gpJSONScalar `json:"INTLDES"`
	ObjectName gpJSONScalar `json:"OBJECT_NAME"`
	SATNAME    gpJSONScalar `json:"SATNAME"`
}
type gpSATCATEntry struct{ EntityID, ObjectName string }

type gpArchiveSink interface {
	StoreHistoryMPE([][]byte) (int, error)
	UpsertHistoryLicense(storage.SourceBatchLicense) error
	StoreCatalogMPE([][]byte, storage.SourceTags) (int, error)
	DeleteCatalogMPE(string) error
	StoreCatalogCAT([][]byte, storage.SourceTags) (int, error)
	StoreOEMBatch([]gpOEMWrite) ([]string, error)
	DeleteOEM(string) error
	Close() error
}

type gpOEMWrite struct {
	Record []byte
	Tags   storage.SourceTags
}

type gpFlatSQLArchiveSink struct{ history, catalogMPE, catalogCAT, oem *storage.FlatSQLStore }

func (s *gpFlatSQLArchiveSink) StoreHistoryMPE(r [][]byte) (int, error) {
	return s.history.StoreBatch("MPE.fbs", r, gpArchiveSourcePeer, nil)
}
func (s *gpFlatSQLArchiveSink) UpsertHistoryLicense(l storage.SourceBatchLicense) error {
	if l.IsEmpty() {
		return nil
	}
	return s.history.UpsertSourceBatchLicense(l)
}
func (s *gpFlatSQLArchiveSink) StoreCatalogMPE(r [][]byte, t storage.SourceTags) (int, error) {
	return s.catalogMPE.StoreBatchWithSourceTags("MPE.fbs", r, gpArchiveSourcePeer, nil, t)
}
func (s *gpFlatSQLArchiveSink) DeleteCatalogMPE(cid string) error {
	return s.catalogMPE.Delete("MPE.fbs", cid)
}
func (s *gpFlatSQLArchiveSink) StoreCatalogCAT(r [][]byte, t storage.SourceTags) (int, error) {
	return s.catalogCAT.StoreBatchWithSourceTags("CAT.fbs", r, gpArchiveSourcePeer, nil, t)
}
func (s *gpFlatSQLArchiveSink) StoreOEMBatch(writes []gpOEMWrite) ([]string, error) {
	records := make([][]byte, len(writes))
	cids := make([]string, len(writes))
	for i, w := range writes {
		records[i] = w.Record
		cids[i] = storage.ComputeCID(w.Record)
	}
	if _, err := s.oem.StoreBatch("OEM.fbs", records, gpArchiveOEMSourcePeer, nil); err != nil {
		return nil, err
	}
	for i, w := range writes {
		if err := s.oem.UpsertSourceTags("OEM.fbs", cids[i], w.Tags); err != nil {
			return nil, err
		}
	}
	return cids, nil
}
func (s *gpFlatSQLArchiveSink) DeleteOEM(cid string) error { return s.oem.Delete("OEM.fbs", cid) }
func (s *gpFlatSQLArchiveSink) Close() error {
	var first error
	for _, st := range []*storage.FlatSQLStore{s.history, s.catalogMPE, s.catalogCAT, s.oem} {
		if st != nil {
			if e := st.Close(); first == nil {
				first = e
			}
		}
	}
	return first
}

type gpEpochDeriver interface {
	Derive(context.Context, [][]byte) ([][]byte, gpEpochReport, error)
	Close() error
}
type gpEpochReport struct {
	Records, Derived, Failed, EpochsOutsideLeapSecondTable int64
	MaxRoundTripPositionKm, MaxRoundTripVelocityKmPerS     float64
	Failures                                               []gpEpochFailure
}
type gpWASMEpochDeriver struct{ module *modulert.Module }

func runImportGPArchive(cmd *cobra.Command, _ []string) error {
	shard := importGPArchiveZipShard
	if strings.TrimSpace(shard) == "" {
		shard = importGPArchiveJSONShard
	}
	batchSize := effectiveGPArchiveBatchSize(shard, importGPArchiveBatchSize, cmd.Flags().Changed("batch-size"))
	opts := gpArchiveOptions{Out: importGPArchiveOut, ZipPath: importGPArchiveZipPath, WindowsRoot: importGPArchiveWindowsRoot, LedgerPath: importGPArchiveLedger, SATCATPath: importGPArchiveSATCAT, EpochStateModule: importGPArchiveEpochStateModule, CheckpointPath: importGPArchiveCheckpoint, ReportPath: importGPArchiveReport, BatchSize: batchSize, MaxZipEntries: importGPArchiveMaxZipEntries, MaxWindowFiles: importGPArchiveMaxWindowFiles, SkipZip: importGPArchiveSkipZip, HistoryOnly: importGPArchiveHistoryOnly, CatalogOnly: importGPArchiveCatalogOnly, ReindexCatalog: importGPArchiveReindexCatalog, ZipShard: importGPArchiveZipShard, JSONShard: importGPArchiveJSONShard}
	if err := opts.normalize(); err != nil {
		return err
	}
	locked, releaseLock, err := acquireGPArchiveRunLock(opts.Out)
	if err != nil {
		return err
	}
	if !locked {
		fmt.Fprintln(cmd.OutOrStdout(), "another import holds the lock")
		return nil
	}
	defer releaseLock()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return err
	}
	if opts.ReindexCatalog {
		return reindexGPArchiveCatalog(opts.Out, validator)
	}
	if opts.EpochStateModule != "" && !opts.HistoryOnly {
		wasm, err := os.ReadFile(opts.EpochStateModule)
		if err != nil {
			return fmt.Errorf("read epoch-state module: %w", err)
		}
		sum := sha256.Sum256(wasm)
		opts.moduleHash = hex.EncodeToString(sum[:])
		m, err := modulert.NewModule(wasm, modulert.NewCapabilityRegistry(), &modulert.NodeContext{})
		if err != nil {
			return fmt.Errorf("load epoch-state module: %w", err)
		}
		opts.deriver = &gpWASMEpochDeriver{module: m}
		defer opts.deriver.Close()
	}
	open := func(id storage.DatastoreIdentity) (*storage.FlatSQLStore, error) {
		return storage.NewFlatSQLStoreForIdentity(opts.Out, validator, id)
	}
	s := &gpFlatSQLArchiveSink{}
	if !opts.CatalogOnly {
		if s.history, err = open(gpArchiveDatastoreIdentity("MPE.fbs", opts.historySourceName(), gpArchiveSourcePeer, "space-track")); err != nil {
			return err
		}
	}
	defer s.Close()
	if opts.HistoryOnly {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		report, runErr := importGPArchive(ctx, opts, s)
		if report != nil {
			log.Infof("GP archive shard import: read=%d history=%d duplicates=%d rejected=%d rate=%.1f records/s report=%s", report.RecordsRead, report.MPEStored, report.DuplicatesSkipped, report.RowsRejected, report.RateRecordsPerSecond, opts.ReportPath)
		}
		return runErr
	}
	if s.catalogMPE, err = open(gpArchiveDatastoreIdentity("MPE.fbs", gpArchiveCatalogSource, gpArchiveSourcePeer, "space-track")); err != nil {
		return err
	}
	if s.catalogCAT, err = open(gpArchiveDatastoreIdentity("CAT.fbs", gpArchiveCatalogSource, gpArchiveSourcePeer, "space-track")); err != nil {
		return err
	}
	if s.oem, err = open(gpArchiveDatastoreIdentity("OEM.fbs", "epoch-state", gpArchiveOEMSourcePeer, "sdn")); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := importGPArchive(ctx, opts, s)
	if report != nil {
		log.Infof("GP archive import: read=%d history=%d duplicates=%d catalog=%d changed=%d derived=%d failed=%d rejected=%d hash_mismatches=%d rate=%.1f records/s report=%s", report.RecordsRead, report.MPEStored, report.DuplicatesSkipped, report.CatalogEntities, report.CatalogEntitiesChanged, report.EpochStatesDerived, report.EpochStatesFailed, report.RowsRejected, len(report.HashMismatches), report.RateRecordsPerSecond, opts.ReportPath)
		if report.Status == "stopped" {
			log.Infof("stopped")
		}
	}
	return err
}

func effectiveGPArchiveBatchSize(zipShard string, requested int, explicitlySet bool) int {
	if strings.TrimSpace(zipShard) != "" && !explicitlySet {
		return gpArchiveShardBatchSize
	}
	return requested
}

func acquireGPArchiveRunLock(out string) (bool, func(), error) {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return false, nil, err
	}
	path := filepath.Join(out, "gp-archive-import.lock")
	pidText := strconv.Itoa(os.Getpid())
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			if _, err = f.WriteString(pidText + "\n"); err != nil {
				f.Close()
				_ = os.Remove(path)
				return false, nil, err
			}
			if err = f.Close(); err != nil {
				_ = os.Remove(path)
				return false, nil, err
			}
			release := func() {
				contents, readErr := os.ReadFile(path)
				if readErr == nil && strings.TrimSpace(string(contents)) == pidText {
					_ = os.Remove(path)
				}
			}
			return true, release, nil
		}
		if !os.IsExist(err) {
			return false, nil, err
		}
		contents, readErr := os.ReadFile(path)
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(contents)))
		if readErr == nil && parseErr == nil && pid > 0 && gpArchiveProcessAlive(pid) {
			return false, func() {}, nil
		}
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return false, nil, err
		}
	}
}

func gpArchiveProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (o *gpArchiveOptions) normalize() error {
	o.Out = strings.TrimSpace(o.Out)
	o.ZipPath = strings.TrimSpace(o.ZipPath)
	o.WindowsRoot = strings.TrimSpace(o.WindowsRoot)
	o.LedgerPath = strings.TrimSpace(o.LedgerPath)
	o.SATCATPath = strings.TrimSpace(o.SATCATPath)
	o.EpochStateModule = strings.TrimSpace(o.EpochStateModule)
	o.ZipShard = strings.TrimSpace(o.ZipShard)
	o.JSONShard = strings.TrimSpace(o.JSONShard)
	if o.Out == "" {
		return fmt.Errorf("--out is required")
	}
	if o.BatchSize <= 0 {
		return fmt.Errorf("--batch-size must be > 0")
	}
	if o.MaxZipEntries < 0 || o.MaxWindowFiles < 0 {
		return fmt.Errorf("sample limits must be >= 0")
	}
	if o.ZipShard != "" && o.JSONShard != "" {
		return fmt.Errorf("--zip-shard and --json-shard are mutually exclusive")
	}
	if o.ReindexCatalog && (o.HistoryOnly || o.CatalogOnly || o.ZipShard != "" || o.JSONShard != "") {
		return fmt.Errorf("--reindex-catalog cannot be combined with import modes")
	}
	if o.CatalogOnly && (o.HistoryOnly || o.ZipShard != "" || o.JSONShard != "") {
		return fmt.Errorf("--catalog-only cannot be combined with history-only or shard modes")
	}
	if o.CatalogOnly {
		o.SkipZip = true
		if o.LedgerPath == "" {
			return fmt.Errorf("--catalog-only requires --ledger")
		}
	}
	if o.ZipShard != "" {
		index, count, err := parseGPArchiveShard("--zip-shard", o.ZipShard)
		if err != nil {
			return err
		}
		o.ZipShardIndex, o.ZipShardCount = index, count
		o.HistoryOnly = true
		if o.SkipZip || o.ZipPath == "" {
			return fmt.Errorf("--zip-shard requires ZIP import")
		}
	}
	if o.JSONShard != "" {
		index, count, err := parseGPArchiveShard("--json-shard", o.JSONShard)
		if err != nil {
			return err
		}
		o.JSONShardIndex, o.JSONShardCount = index, count
		o.HistoryOnly = true
		o.SkipZip = true
		if o.LedgerPath == "" {
			return fmt.Errorf("--json-shard requires --ledger")
		}
	}
	if o.CheckpointPath == "" {
		o.CheckpointPath = filepath.Join(o.Out, "gp-archive-checkpoint.json")
	}
	if o.ReportPath == "" {
		o.ReportPath = filepath.Join(o.Out, "gp-archive-report.json")
	}
	return nil
}

func parseGPArchiveShard(flag, value string) (int, int, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%s must be i/N", flag)
	}
	index, indexErr := strconv.Atoi(parts[0])
	count, countErr := strconv.Atoi(parts[1])
	if indexErr != nil || countErr != nil || count <= 0 || index < 0 || index >= count {
		return 0, 0, fmt.Errorf("%s must satisfy 0 <= i < N", flag)
	}
	return index, count, nil
}

func (o gpArchiveOptions) historySourceName() string {
	if o.ZipShardCount > 0 {
		return fmt.Sprintf("gp-history-zip-shard-%d-of-%d", o.ZipShardIndex, o.ZipShardCount)
	}
	if o.JSONShardCount > 0 {
		return fmt.Sprintf("gp-history-json-shard-%d-of-%d", o.JSONShardIndex, o.JSONShardCount)
	}
	return gpArchiveHistorySource
}

func reindexGPArchiveCatalog(out string, validator *sds.Validator) error {
	targets := []struct {
		schema   string
		identity storage.DatastoreIdentity
	}{
		{"MPE.fbs", gpArchiveDatastoreIdentity("MPE.fbs", gpArchiveCatalogSource, gpArchiveSourcePeer, "space-track")},
		{"CAT.fbs", gpArchiveDatastoreIdentity("CAT.fbs", gpArchiveCatalogSource, gpArchiveSourcePeer, "space-track")},
		{"OEM.fbs", gpArchiveDatastoreIdentity("OEM.fbs", "epoch-state", gpArchiveOEMSourcePeer, "sdn")},
	}
	for _, target := range targets {
		store, err := storage.NewFlatSQLStoreForIdentity(out, validator, target.identity)
		if err != nil {
			return fmt.Errorf("open %s archive datastore for reindex: %w", target.schema, err)
		}
		summary, rebuildErr := store.RebuildIndex()
		closeErr := store.Close()
		if rebuildErr != nil {
			return fmt.Errorf("reindex %s archive datastore: %w", target.schema, rebuildErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s archive datastore after reindex: %w", target.schema, closeErr)
		}
		log.Infof("Reindexed GP archive %s datastore: %d records", target.schema, summary[target.schema])
	}
	return nil
}

func gpArchiveDatastoreIdentity(schema, source, peer, provider string) storage.DatastoreIdentity {
	return storage.DatastoreIdentity{SchemaName: schema, SourcePeerID: peer, ProviderID: provider, SourceName: source, QueryProfile: storage.DatasetPublicationQueryProfile}
}

func newGPArchiveRunReport() *gpArchiveRunReport {
	return &gpArchiveRunReport{StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Status: "complete", started: time.Now(), RejectedReasons: map[string]int64{}, withoutDesignatorSet: map[string]struct{}{}, noSATCATSet: map[uint32]struct{}{}}
}
func (r *gpArchiveRunReport) finalize() {
	r.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if d := time.Since(r.started).Seconds(); d > 0 {
		r.RateRecordsPerSecond = float64(r.RecordsRead) / d
	}
	for e := range r.withoutDesignatorSet {
		r.ObjectsWithoutDesignator = append(r.ObjectsWithoutDesignator, e)
	}
	sort.Strings(r.ObjectsWithoutDesignator)
	r.ObjectsLackingDesignator = int64(len(r.ObjectsWithoutDesignator))
	for n := range r.noSATCATSet {
		r.NORADsWithoutSATCAT = append(r.NORADsWithoutSATCAT, n)
	}
	sort.Slice(r.NORADsWithoutSATCAT, func(i, j int) bool { return r.NORADsWithoutSATCAT[i] < r.NORADsWithoutSATCAT[j] })
}

func importGPArchive(ctx context.Context, opts gpArchiveOptions, sink gpArchiveSink) (*gpArchiveRunReport, error) {
	if err := opts.normalize(); err != nil {
		return nil, err
	}
	cp, err := loadGPArchiveCheckpoint(opts.CheckpointPath)
	if err != nil {
		return nil, err
	}
	sourcesPath := filepath.Join(filepath.Dir(opts.CheckpointPath), "sources.json")
	sources, err := loadGPArchiveSources(sourcesPath)
	if err != nil {
		return nil, err
	}
	report := newGPArchiveRunReport()
	report.EpochStateModuleSHA256 = opts.moduleHash
	progress := newGPArchiveProgress(opts.CheckpointPath, sourcesPath, cp, sources)
	finish := func(runErr error) (*gpArchiveRunReport, error) {
		report.finalize()
		if e := saveGPArchiveReport(opts.ReportPath, report); e != nil && runErr == nil {
			runErr = e
		}
		return report, runErr
	}
	stopImport := func() (*gpArchiveRunReport, error) {
		report.Status = "stopped"
		if e := progress.save(true); e != nil {
			return finish(e)
		}
		report.Sources = sortedGPArchiveSources(sources)
		return finish(nil)
	}
	handlePhaseError := func(e error) (*gpArchiveRunReport, error, bool) {
		if errors.Is(e, errGPArchiveStopped) || errors.Is(e, context.Canceled) {
			r, stopErr := stopImport()
			return r, stopErr, true
		}
		return nil, e, false
	}
	ledger := gpArchiveLedger{}
	if opts.LedgerPath != "" {
		b, e := os.ReadFile(opts.LedgerPath)
		if e != nil {
			return finish(fmt.Errorf("read ledger: %w", e))
		}
		if e = json.Unmarshal(b, &ledger); e != nil {
			return finish(fmt.Errorf("decode ledger: %w", e))
		}
	}
	satcat, err := loadGPArchiveSATCAT(opts, &ledger, cp, report, sources)
	if err != nil {
		return finish(err)
	}
	if err = progress.save(true); err != nil {
		return finish(err)
	}
	if ctx.Err() != nil {
		return stopImport()
	}
	if opts.ZipPath != "" && !opts.SkipZip {
		if err = importGPArchiveZip(ctx, opts, cp, satcat, sources, report, sink, progress); err != nil {
			if r, e, stopped := handlePhaseError(err); stopped {
				return r, e
			}
			return finish(err)
		}
		if err = progress.save(true); err != nil {
			return finish(err)
		}
		if ctx.Err() != nil {
			return stopImport()
		}
	}
	if opts.HistoryOnly && opts.JSONShardCount == 0 {
		if err = progress.save(true); err != nil {
			return finish(err)
		}
		report.Sources = sortedGPArchiveSources(sources)
		return finish(nil)
	}
	if opts.LedgerPath != "" {
		if err = importGPArchiveWindows(ctx, opts, ledger.Windows, cp, satcat, sources, report, sink, progress); err != nil {
			if r, e, stopped := handlePhaseError(err); stopped {
				return r, e
			}
			return finish(err)
		}
		if err = progress.save(true); err != nil {
			return finish(err)
		}
		if ctx.Err() != nil {
			return stopImport()
		}
		if err = importGPArchiveSnapshots(ctx, opts, ledger.GP, cp, satcat, sources, report, sink, progress); err != nil {
			if r, e, stopped := handlePhaseError(err); stopped {
				return r, e
			}
			return finish(err)
		}
		if err = progress.save(true); err != nil {
			return finish(err)
		}
		if ctx.Err() != nil {
			return stopImport()
		}
	}
	if opts.HistoryOnly {
		if err = progress.save(true); err != nil {
			return finish(err)
		}
		report.Sources = sortedGPArchiveSources(sources)
		return finish(nil)
	}
	if err = foldGPArchiveShardCheckpoints(opts, cp, sources, report); err != nil {
		return finish(err)
	}
	if err = progress.save(true); err != nil {
		return finish(err)
	}
	if err = materializeGPArchiveCatalog(ctx, opts, cp, satcat, sources, report, sink, progress); err != nil {
		if r, e, stopped := handlePhaseError(err); stopped {
			return r, e
		}
		return finish(err)
	}
	if err = progress.save(true); err != nil {
		return finish(err)
	}
	if ctx.Err() != nil {
		return stopImport()
	}
	if opts.deriver != nil {
		if err = materializeGPArchiveOEM(ctx, opts, cp, report, sink, progress); err != nil {
			if r, e, stopped := handlePhaseError(err); stopped {
				return r, e
			}
			return finish(err)
		}
		if err = progress.save(true); err != nil {
			return finish(err)
		}
		if ctx.Err() != nil {
			return stopImport()
		}
	}
	if err = progress.save(true); err != nil {
		return finish(err)
	}
	report.Sources = sortedGPArchiveSources(sources)
	return finish(nil)
}

type gpArchiveProgress struct {
	checkpointPath, sourcesPath string
	cp                          *gpArchiveCheckpoint
	sources                     map[string]gpArchiveSource
	lastSaved                   time.Time
	now                         func() time.Time
	saveCheckpoint              func(string, *gpArchiveCheckpoint) error
	saveSources                 func(string, map[string]gpArchiveSource) error
}

func newGPArchiveProgress(checkpointPath, sourcesPath string, cp *gpArchiveCheckpoint, sources map[string]gpArchiveSource) *gpArchiveProgress {
	return &gpArchiveProgress{checkpointPath: checkpointPath, sourcesPath: sourcesPath, cp: cp, sources: sources, lastSaved: time.Now(), now: time.Now, saveCheckpoint: saveGPArchiveCheckpoint, saveSources: saveGPArchiveSources}
}

func (p *gpArchiveProgress) save(force bool) error {
	now := p.now()
	if !force && now.Sub(p.lastSaved) < gpArchiveSaveInterval {
		return nil
	}
	// Sources are written first. Only the checkpoint makes an entry complete, so a
	// crash can at worst leave harmless lineage ahead of the checkpoint. Work after
	// the last saved checkpoint is replayed; CID dedupe and monotonic latest tracking
	// make that replay safe.
	if err := p.saveSources(p.sourcesPath, p.sources); err != nil {
		return err
	}
	if err := p.saveCheckpoint(p.checkpointPath, p.cp); err != nil {
		return err
	}
	p.lastSaved = now
	return nil
}

func loadGPArchiveCheckpoint(path string) (*gpArchiveCheckpoint, error) {
	cp := &gpArchiveCheckpoint{Version: 2, CompletedZipEntries: map[string]string{}, CompletedWindowFiles: map[string]string{}, CompletedGPFiles: map[string]string{}, SnapshotGPIDs: map[string]bool{}, CanonicalIDs: map[string]string{}, Latest: map[string]*gpLatestState{}, CATNames: map[string]string{}, FoldedShardMTime: map[string]int64{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cp, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, cp); err != nil {
		return nil, err
	}
	if cp.CompletedZipEntries == nil {
		cp.CompletedZipEntries = map[string]string{}
	}
	if cp.CompletedWindowFiles == nil {
		cp.CompletedWindowFiles = map[string]string{}
	}
	if cp.CompletedGPFiles == nil {
		cp.CompletedGPFiles = map[string]string{}
	}
	if cp.SnapshotGPIDs == nil {
		cp.SnapshotGPIDs = map[string]bool{}
	}
	if cp.CanonicalIDs == nil {
		cp.CanonicalIDs = map[string]string{}
	}
	if cp.Latest == nil {
		cp.Latest = map[string]*gpLatestState{}
	}
	if cp.CATNames == nil {
		cp.CATNames = map[string]string{}
	}
	if cp.FoldedShardMTime == nil {
		cp.FoldedShardMTime = map[string]int64{}
	}
	cp.Version = 2
	return cp, nil
}
func saveGPArchiveCheckpoint(path string, cp *gpArchiveCheckpoint) error {
	cp.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return writeGPArchiveJSON(path, cp)
}
func saveGPArchiveReport(path string, r *gpArchiveRunReport) error {
	return writeGPArchiveJSON(path, r)
}
func loadGPArchiveSources(path string) (map[string]gpArchiveSource, error) {
	out := map[string]gpArchiveSource{}
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	var f gpArchiveSourcesFile
	if e = json.Unmarshal(b, &f); e != nil {
		return nil, e
	}
	for _, s := range f.Sources {
		out[s.ID] = s
	}
	return out, nil
}
func saveGPArchiveSources(path string, m map[string]gpArchiveSource) error {
	return writeGPArchiveJSON(path, gpArchiveSourcesFile{Sources: sortedGPArchiveSources(m)})
}
func sortedGPArchiveSources(m map[string]gpArchiveSource) []gpArchiveSource {
	a := make([]gpArchiveSource, 0, len(m))
	for _, s := range m {
		a = append(a, s)
	}
	sort.Slice(a, func(i, j int) bool { return a[i].ID < a[j].ID })
	return a
}
func writeGPArchiveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, append(b, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var gpCanonicalINTLDES = regexp.MustCompile(`^[0-9]{4}-[0-9]{3}[A-Z]{1,3}$`)

func loadGPArchiveSATCAT(opts gpArchiveOptions, ledger *gpArchiveLedger, cp *gpArchiveCheckpoint, report *gpArchiveRunReport, sources map[string]gpArchiveSource) (map[uint32]gpSATCATEntry, error) {
	path := opts.SATCATPath
	if path == "" {
		root := filepath.Dir(filepath.Dir(opts.LedgerPath))
		matches, _ := filepath.Glob(filepath.Join(root, "spacetrack", "satcat", "*", "*.json.gz"))
		sort.Strings(matches)
		if len(matches) == 0 {
			return nil, fmt.Errorf("no SATCAT snapshot found under %s", filepath.Join(root, "spacetrack", "satcat"))
		}
		path = matches[len(matches)-1]
	}
	abs, _ := filepath.Abs(path)
	var le *gpArchiveLedgerSATCAT
	for i := range ledger.SATCAT {
		p := ledger.SATCAT[i].File
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(filepath.Dir(opts.LedgerPath)), p)
		}
		pa, _ := filepath.Abs(p)
		if pa == abs {
			le = &ledger.SATCAT[i]
			break
		}
	}
	if le == nil {
		return nil, fmt.Errorf("SATCAT snapshot %s is absent from ledger", path)
	}
	raw, actual, err := readGzipAndHash(path)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(actual, le.SHA256) {
		report.HashMismatches = append(report.HashMismatches, gpArchiveHashMismatch{path, le.SHA256, actual})
		return nil, fmt.Errorf("SATCAT hash mismatch: %s", path)
	}
	var rows []gpSATCATRecord
	if err = json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode SATCAT: %w", err)
	}
	type candidate struct {
		norad         uint32
		intldes, name string
	}
	candidates := make([]candidate, 0, len(rows))
	claims := map[string]map[uint32]struct{}{}
	rejected := int64(0)
	for _, r := range rows {
		n, e := parseUint32(string(r.NORAD))
		if e != nil {
			rejected++
			continue
		}
		id := strings.TrimSpace(string(r.INTLDES))
		name := strings.TrimSpace(string(r.ObjectName))
		if name == "" {
			name = strings.TrimSpace(string(r.SATNAME))
		}
		if gpCanonicalINTLDES.MatchString(id) {
			if claims[id] == nil {
				claims[id] = map[uint32]struct{}{}
			}
			claims[id][n] = struct{}{}
		}
		candidates = append(candidates, candidate{n, id, name})
	}
	ambiguous := map[string]bool{}
	for id, set := range claims {
		if len(set) > 1 {
			ambiguous[id] = true
			a := gpArchiveAmbiguousDesignator{INTLDES: id}
			for n := range set {
				a.NORADs = append(a.NORADs, n)
			}
			sort.Slice(a.NORADs, func(i, j int) bool { return a.NORADs[i] < a.NORADs[j] })
			report.AmbiguousDesignators = append(report.AmbiguousDesignators, a)
		}
	}
	sort.Slice(report.AmbiguousDesignators, func(i, j int) bool {
		return report.AmbiguousDesignators[i].INTLDES < report.AmbiguousDesignators[j].INTLDES
	})
	out := map[uint32]gpSATCATEntry{}
	for _, c := range candidates {
		id := ""
		if gpCanonicalINTLDES.MatchString(c.intldes) && !ambiguous[c.intldes] {
			id = c.intldes
		}
		old := cp.CanonicalIDs[strconv.FormatUint(uint64(c.norad), 10)]
		current := id
		if current == "" {
			current = fmt.Sprintf("NORAD:%d", c.norad)
		}
		if old != "" && old != current {
			report.CanonicalIDChanges = append(report.CanonicalIDChanges, gpArchiveCanonicalChange{c.norad, old, current})
		}
		cp.CanonicalIDs[strconv.FormatUint(uint64(c.norad), 10)] = current
		prev := out[c.norad]
		if prev.ObjectName == "" || c.name != "" {
			out[c.norad] = gpSATCATEntry{EntityID: id, ObjectName: c.name}
		}
	}
	src := gpArchiveSource{ID: "satcat:" + actual, Kind: "satcat", Path: path, SHA256: actual, RetrievedAt: le.RetrievedAt, RowsRead: int64(len(rows)), RowsStored: int64(len(out)), RowsRejected: rejected}
	sources[src.ID] = src
	report.SATCATPath = path
	report.SATCATSHA256 = actual
	cp.SATCATSHA256 = actual
	return out, nil
}

type gpHistoryBatch struct {
	records [][]byte
	sink    gpArchiveSink
	report  *gpArchiveRunReport
	enabled bool
}

func (b *gpHistoryBatch) add(data []byte) error {
	if b.enabled {
		b.records = append(b.records, data)
	}
	return nil
}
func (b *gpHistoryBatch) flush() error {
	if !b.enabled || len(b.records) == 0 {
		return nil
	}
	n, e := b.sink.StoreHistoryMPE(b.records)
	b.report.MPEStored += int64(n)
	b.records = b.records[:0]
	return e
}

func importGPArchiveZip(ctx context.Context, opts gpArchiveOptions, cp *gpArchiveCheckpoint, satcat map[uint32]gpSATCATEntry, sources map[string]gpArchiveSource, report *gpArchiveRunReport, sink gpArchiveSink, progress *gpArchiveProgress) error {
	st, err := os.Stat(opts.ZipPath)
	if err != nil {
		return err
	}
	unchanged := cp.ZipSize != 0 && cp.ZipSize == st.Size() && cp.ZipModTimeUnixNano == st.ModTime().UnixNano()
	if unchanged && cp.ZipCSVEntries > 0 && len(cp.CompletedZipEntries) == cp.ZipCSVEntries {
		report.CompletedZipEntriesSkipped += cp.ZipCSVEntries
		return nil
	}
	if cp.ZipSize != 0 && (cp.ZipSize != st.Size() || cp.ZipModTimeUnixNano != st.ModTime().UnixNano()) {
		return fmt.Errorf("zip changed since checkpoint")
	}
	sum, err := hashFile(opts.ZipPath)
	if err != nil {
		return err
	}
	if cp.ZipSHA256 != "" && cp.ZipSHA256 != sum {
		return fmt.Errorf("zip hash changed since checkpoint")
	}
	cp.ZipSHA256 = sum
	cp.ZipSize = st.Size()
	cp.ZipModTimeUnixNano = st.ModTime().UnixNano()
	zr, err := zip.OpenReader(opts.ZipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	cp.ZipCSVEntries = 0
	csvOrdinal := 0
	for _, entry := range zr.File {
		if strings.HasSuffix(strings.ToLower(entry.Name), ".csv") {
			if opts.ZipShardCount == 0 || csvOrdinal%opts.ZipShardCount == opts.ZipShardIndex {
				cp.ZipCSVEntries++
			}
			csvOrdinal++
		}
	}
	considered := 0
	csvOrdinal = 0
	for _, entry := range zr.File {
		if ctx.Err() != nil {
			return errGPArchiveStopped
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name), ".csv") {
			continue
		}
		ordinal := csvOrdinal
		csvOrdinal++
		if opts.ZipShardCount > 0 && ordinal%opts.ZipShardCount != opts.ZipShardIndex {
			continue
		}
		if opts.MaxZipEntries > 0 && considered >= opts.MaxZipEntries {
			break
		}
		considered++
		if _, ok := cp.CompletedZipEntries[entry.Name]; ok {
			report.CompletedZipEntriesSkipped++
			continue
		}
		src := gpArchiveSource{ID: "zip:" + sum + ":" + entry.Name, Kind: "zip", Path: opts.ZipPath, SHA256: sum, Entry: entry.Name}
		beforeRead, beforeStored, beforeRejected := report.RecordsRead, report.MPEStored, report.RowsRejected
		if err = importGPArchiveCSVEntry(ctx, entry, ordinal, opts, cp, satcat, src.ID, report, sink); err != nil {
			return fmt.Errorf("import %s: %w", entry.Name, err)
		}
		src.RowsRead = report.RecordsRead - beforeRead
		src.RowsStored = report.MPEStored - beforeStored
		src.RowsRejected = report.RowsRejected - beforeRejected
		sources[src.ID] = src
		if err = upsertGPHistoryLicense(sink, sourceTags(src, opts.historySourceName())); err != nil {
			return err
		}
		cp.CompletedZipEntries[entry.Name] = sum
		report.ZipEntriesImported++
		if err = progress.save(false); err != nil {
			return err
		}
	}
	return nil
}

func importGPArchiveCSVEntry(ctx context.Context, entry *zip.File, entryOrdinal int, opts gpArchiveOptions, cp *gpArchiveCheckpoint, satcat map[uint32]gpSATCATEntry, sourceID string, report *gpArchiveRunReport, sink gpArchiveSink) error {
	rc, err := entry.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	r := csv.NewReader(bufio.NewReaderSize(rc, 256*1024))
	header, err := r.Read()
	if err != nil {
		return err
	}
	columns := map[string]int{}
	for i, n := range header {
		columns[strings.TrimSpace(n)] = i
	}
	dedup := map[string]struct{}{}
	batch := &gpHistoryBatch{sink: sink, report: report, enabled: !opts.CatalogOnly}
	var entryRowOrder uint64
	for {
		if ctx.Err() != nil {
			if err = batch.flush(); err != nil {
				return err
			}
			return errGPArchiveStopped
		}
		row, e := r.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			report.RowsRejected++
			report.RejectedReasons["csv_read"]++
			continue
		}
		report.RecordsRead++
		cp.ZipRowOrder++
		entryRowOrder++
		rec, reason := gpArchiveRecordFromCSV(row, columns)
		if reason != "" {
			report.RowsRejected++
			report.RejectedReasons[reason]++
			continue
		}
		canonicalizeGPRecord(&rec, satcat, cp, report)
		key := gpArchiveCSVKey(rec)
		if _, ok := dedup[key]; ok {
			report.DuplicatesSkipped++
			continue
		}
		dedup[key] = struct{}{}
		mpe := buildGPArchiveMPE(rec.EntityID, rec)
		cid := storage.ComputeCID(mpe)
		// ZIP shards must retain the unsharded global ordering for equal-epoch
		// tie breaks. Archive entries are tiny relative to the uint32 row space.
		tie := uint64(entryOrdinal)<<32 | entryRowOrder
		observeGPLatest(cp, rec, mpe, cid, sourceID, "zip", tie)
		if err = batch.add(mpe); err != nil {
			return err
		}
		if len(batch.records) >= opts.BatchSize {
			if err = batch.flush(); err != nil {
				return err
			}
		}
	}
	return batch.flush()
}

func importGPArchiveWindows(ctx context.Context, opts gpArchiveOptions, windows []gpArchiveLedgerWindow, cp *gpArchiveCheckpoint, satcat map[uint32]gpSATCATEntry, sources map[string]gpArchiveSource, report *gpArchiveRunReport, sink gpArchiveSink, progress *gpArchiveProgress) error {
	sort.Slice(windows, func(i, j int) bool { return windows[i].File < windows[j].File })
	for index, w := range windows {
		if ctx.Err() != nil {
			return errGPArchiveStopped
		}
		if opts.MaxWindowFiles > 0 && index >= opts.MaxWindowFiles {
			break
		}
		if _, ok := cp.CompletedWindowFiles[w.File]; ok {
			report.CompletedWindowFilesSkipped++
			continue
		}
		path := w.File
		if !filepath.IsAbs(path) {
			root := filepath.Dir(filepath.Dir(opts.LedgerPath))
			path = filepath.Join(root, path)
		}
		raw, actual, err := readGzipAndHash(path)
		if err != nil {
			return err
		}
		if !strings.EqualFold(actual, w.SHA256) {
			report.HashMismatches = append(report.HashMismatches, gpArchiveHashMismatch{path, w.SHA256, actual})
			continue
		}
		src := gpArchiveSource{ID: "window:" + actual + ":" + w.File, Kind: "window", Path: w.File, SHA256: actual, RetrievedAt: w.RetrievedAt}
		beforeRead, beforeStored, beforeRejected := report.RecordsRead, report.MPEStored, report.RowsRejected
		if err = importGPArchiveJSONWindow(ctx, raw, opts, cp, satcat, src.ID, "window", report, sink); err != nil {
			return fmt.Errorf("import %s: %w", path, err)
		}
		src.RowsRead = report.RecordsRead - beforeRead
		src.RowsStored = report.MPEStored - beforeStored
		src.RowsRejected = report.RowsRejected - beforeRejected
		sources[src.ID] = src
		if !opts.CatalogOnly {
			if err = upsertGPHistoryLicense(sink, sourceTags(src, opts.historySourceName())); err != nil {
				return err
			}
		}
		cp.CompletedWindowFiles[w.File] = actual
		report.WindowFilesImported++
		if err = progress.save(false); err != nil {
			return err
		}
	}
	return nil
}

func importGPArchiveSnapshots(ctx context.Context, opts gpArchiveOptions, snapshots []gpArchiveLedgerSATCAT, cp *gpArchiveCheckpoint, satcat map[uint32]gpSATCATEntry, sources map[string]gpArchiveSource, report *gpArchiveRunReport, sink gpArchiveSink, progress *gpArchiveProgress) error {
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].RetrievedAt == snapshots[j].RetrievedAt {
			return snapshots[i].File < snapshots[j].File
		}
		return snapshots[i].RetrievedAt < snapshots[j].RetrievedAt
	})
	for _, snapshot := range snapshots {
		if ctx.Err() != nil {
			return errGPArchiveStopped
		}
		if _, ok := cp.CompletedGPFiles[snapshot.File]; ok {
			report.CompletedGPFilesSkipped++
			continue
		}
		path := snapshot.File
		if !filepath.IsAbs(path) {
			root := filepath.Dir(filepath.Dir(opts.LedgerPath))
			path = filepath.Join(root, path)
		}
		raw, actual, err := readGzipAndHash(path)
		if err != nil {
			return err
		}
		if !strings.EqualFold(actual, snapshot.SHA256) {
			report.HashMismatches = append(report.HashMismatches, gpArchiveHashMismatch{path, snapshot.SHA256, actual})
			continue
		}
		src := gpArchiveSource{ID: "gp:" + actual + ":" + snapshot.File, Kind: "gp", Path: snapshot.File, SHA256: actual, RetrievedAt: snapshot.RetrievedAt}
		beforeRead, beforeStored, beforeRejected := report.RecordsRead, report.MPEStored, report.RowsRejected
		if err = importGPArchiveJSONWindow(ctx, raw, opts, cp, satcat, src.ID, "gp", report, sink); err != nil {
			return fmt.Errorf("import %s: %w", path, err)
		}
		src.RowsRead = report.RecordsRead - beforeRead
		src.RowsStored = report.MPEStored - beforeStored
		src.RowsRejected = report.RowsRejected - beforeRejected
		sources[src.ID] = src
		if !opts.CatalogOnly {
			if err = upsertGPHistoryLicense(sink, sourceTags(src, opts.historySourceName())); err != nil {
				return err
			}
		}
		cp.CompletedGPFiles[snapshot.File] = actual
		report.GPFilesImported++
		if err = progress.save(false); err != nil {
			return err
		}
	}
	return nil
}

func importGPArchiveJSONWindow(ctx context.Context, raw []byte, opts gpArchiveOptions, cp *gpArchiveCheckpoint, satcat map[uint32]gpSATCATEntry, sourceID, sourceKind string, report *gpArchiveRunReport, sink gpArchiveSink) error {
	var rows []gpJSONRecord
	if err := json.Unmarshal(raw, &rows); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, id := range cp.LastWindowGPIDs {
		seen[id] = struct{}{}
	}
	for id := range cp.SnapshotGPIDs {
		seen[id] = struct{}{}
	}
	lastCreationDate := ""
	for _, raw := range rows {
		if value := string(raw.CreationDate); value > lastCreationDate {
			lastCreationDate = value
		}
	}
	currentIDs := make([]string, 0)
	batch := &gpHistoryBatch{sink: sink, report: report, enabled: !opts.CatalogOnly}
	for _, raw := range rows {
		if ctx.Err() != nil {
			if err := batch.flush(); err != nil {
				return err
			}
			return errGPArchiveStopped
		}
		report.RecordsRead++
		rec, reason := gpArchiveRecordFromJSON(raw)
		if reason != "" {
			report.RowsRejected++
			report.RejectedReasons[reason]++
			continue
		}
		if opts.JSONShardCount > 0 && int(rec.NORAD%uint32(opts.JSONShardCount)) != opts.JSONShardIndex {
			continue
		}
		if rec.GPID != "" {
			if string(raw.CreationDate) == lastCreationDate {
				currentIDs = append(currentIDs, rec.GPID)
			}
			if _, ok := seen[rec.GPID]; ok {
				report.DuplicatesSkipped++
				if sourceKind == "gp" {
					cp.SnapshotGPIDs[rec.GPID] = true
				}
				continue
			}
			seen[rec.GPID] = struct{}{}
		}
		canonicalizeGPRecord(&rec, satcat, cp, report)
		mpe := buildGPArchiveMPE(rec.EntityID, rec)
		cid := storage.ComputeCID(mpe)
		tie, _ := strconv.ParseUint(rec.GPID, 10, 64)
		if old := cp.Latest[rec.EntityID]; rec.GPID != "" && old != nil && old.TieKind == "json" && old.TieValue == tie && old.HistoryCID == cid {
			report.DuplicatesSkipped++
			if sourceKind == "gp" {
				cp.SnapshotGPIDs[rec.GPID] = true
			}
			continue
		}
		if opts.JSONShardCount == 0 {
			observeGPLatest(cp, rec, mpe, cid, sourceID, "json", tie)
		}
		if sourceKind == "gp" && rec.GPID != "" {
			cp.SnapshotGPIDs[rec.GPID] = true
		}
		batch.add(mpe)
		if len(batch.records) >= opts.BatchSize {
			if err := batch.flush(); err != nil {
				return err
			}
		}
	}
	if err := batch.flush(); err != nil {
		return err
	}
	if sourceKind == "window" {
		cp.LastWindowGPIDs = currentIDs
	}
	return nil
}

func canonicalizeGPRecord(r *gpArchiveRecord, satcat map[uint32]gpSATCATEntry, cp *gpArchiveCheckpoint, report *gpArchiveRunReport) {
	s, ok := satcat[r.NORAD]
	if ok && s.EntityID != "" {
		r.EntityID = s.EntityID
	} else {
		r.EntityID = fmt.Sprintf("NORAD:%d", r.NORAD)
		report.withoutDesignatorSet[r.EntityID] = struct{}{}
		report.noSATCATSet[r.NORAD] = struct{}{}
	}
	key := strconv.FormatUint(uint64(r.NORAD), 10)
	old := cp.CanonicalIDs[key]
	if old != "" && old != r.EntityID {
		already := false
		for _, change := range report.CanonicalIDChanges {
			if change.NORAD == r.NORAD && change.Previous == old && change.Current == r.EntityID {
				already = true
				break
			}
		}
		if !already {
			report.CanonicalIDChanges = append(report.CanonicalIDChanges, gpArchiveCanonicalChange{r.NORAD, old, r.EntityID})
		}
	}
	cp.CanonicalIDs[key] = r.EntityID
	row := strings.TrimSpace(r.ObjectID)
	switch {
	case row == "":
		report.ObjectIDDisagreements.Blank++
	case row == r.EntityID:
	case !gpCanonicalINTLDES.MatchString(row):
		report.ObjectIDDisagreements.OldStyle++
	default:
		report.ObjectIDDisagreements.Different++
	}
}
func observeGPLatest(cp *gpArchiveCheckpoint, r gpArchiveRecord, mpe []byte, cid, sourceID, kind string, tie uint64) {
	old := cp.Latest[r.EntityID]
	candidate := &gpLatestState{Epoch: r.Epoch, TieKind: kind, TieValue: tie}
	if gpLatestStateNewer(candidate, old) {
		name := r.ObjectName
		if name == "" && old != nil {
			name = old.GPObjectName
		}
		next := &gpLatestState{EntityID: r.EntityID, NORAD: r.NORAD, Epoch: r.Epoch, TieKind: kind, TieValue: tie, HistoryCID: cid, SourceID: sourceID, MPE: append([]byte(nil), mpe...), GPObjectName: name}
		if old != nil {
			next.CatalogCID = old.CatalogCID
			next.OEMCID = old.OEMCID
			next.OEMParentCID = old.OEMParentCID
		}
		cp.Latest[r.EntityID] = next
	} else if old != nil && old.GPObjectName == "" && r.ObjectName != "" {
		old.GPObjectName = r.ObjectName
	}
}
func kindRank(k string) int {
	if k == "json" {
		return 2
	}
	return 1
}

func foldGPArchiveShardCheckpoints(opts gpArchiveOptions, cp *gpArchiveCheckpoint, sources map[string]gpArchiveSource, report *gpArchiveRunReport) error {
	paths, err := filepath.Glob(filepath.Join(opts.Out, "zip-shards", "*", "gp-archive-checkpoint.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return statErr
		}
		abs, absErr := filepath.Abs(path)
		if absErr != nil {
			return absErr
		}
		mtime := info.ModTime().UnixNano()
		if cp.FoldedShardMTime[abs] >= mtime {
			report.ShardCheckpointsSkipped++
			continue
		}
		shard, loadErr := loadGPArchiveCheckpoint(path)
		if loadErr != nil {
			return fmt.Errorf("load shard checkpoint %s: %w", path, loadErr)
		}
		if cp.SATCATSHA256 == "" || shard.SATCATSHA256 == "" || !strings.EqualFold(cp.SATCATSHA256, shard.SATCATSHA256) {
			report.ShardSATCATMismatches = append(report.ShardSATCATMismatches, gpArchiveShardSATCATMismatch{Checkpoint: path, MainSHA256: cp.SATCATSHA256, ShardSHA256: shard.SATCATSHA256})
			continue
		}
		shardSources, loadErr := loadGPArchiveSources(filepath.Join(filepath.Dir(path), "sources.json"))
		if loadErr != nil {
			return fmt.Errorf("load shard sources for %s: %w", path, loadErr)
		}
		entities := make([]string, 0, len(shard.Latest))
		for entity := range shard.Latest {
			entities = append(entities, entity)
		}
		sort.Strings(entities)
		for _, entity := range entities {
			candidate := shard.Latest[entity]
			if candidate == nil {
				continue
			}
			old := cp.Latest[entity]
			if !gpLatestStateNewer(candidate, old) {
				if old != nil && old.GPObjectName == "" && candidate.GPObjectName != "" {
					old.GPObjectName = candidate.GPObjectName
				}
				continue
			}
			src, ok := shardSources[candidate.SourceID]
			if !ok {
				return fmt.Errorf("shard checkpoint %s source %q missing from sources.json", path, candidate.SourceID)
			}
			sources[src.ID] = src
			next := *candidate
			next.MPE = append([]byte(nil), candidate.MPE...)
			if old != nil {
				next.CatalogCID = old.CatalogCID
				next.OEMCID = old.OEMCID
				next.OEMParentCID = old.OEMParentCID
			}
			cp.Latest[entity] = &next
			report.ShardEntitiesUpdated++
		}
		cp.FoldedShardMTime[abs] = mtime
		report.ShardCheckpointsFolded++
	}
	return nil
}

func gpLatestStateNewer(candidate, old *gpLatestState) bool {
	if candidate == nil {
		return false
	}
	return old == nil || candidate.Epoch > old.Epoch || (candidate.Epoch == old.Epoch && (kindRank(candidate.TieKind) > kindRank(old.TieKind) || (candidate.TieKind == old.TieKind && candidate.TieValue > old.TieValue)))
}

func sourceTags(src gpArchiveSource, sourceName string) storage.SourceTags {
	q := url.Values{}
	q.Set("sha256", src.SHA256)
	q.Set("data_level", "3")
	if src.Entry != "" {
		q.Set("entry", src.Entry)
	}
	if src.RetrievedAt != "" {
		q.Set("retrieved_at", src.RetrievedAt)
	}
	return storage.SourceTags{ProviderID: "space-track", SourceName: sourceName, SourceURL: "file:" + src.Path + "?" + q.Encode(), BatchID: src.ID, ProducerPeerID: gpArchiveSourcePeer}
}

func upsertGPHistoryLicense(sink gpArchiveSink, t storage.SourceTags) error {
	return sink.UpsertHistoryLicense(storage.SourceBatchLicense{SchemaName: "MPE.fbs", ProviderID: t.ProviderID, SourceName: t.SourceName, BatchID: t.BatchID, License: t.License, LicenseURL: t.LicenseURL, Citation: t.Citation, ShareAlike: t.ShareAlike})
}

func materializeGPArchiveCatalog(ctx context.Context, opts gpArchiveOptions, cp *gpArchiveCheckpoint, satcat map[uint32]gpSATCATEntry, sources map[string]gpArchiveSource, report *gpArchiveRunReport, sink gpArchiveSink, progress *gpArchiveProgress) error {
	bySource := map[string][]*gpLatestState{}
	for _, st := range cp.Latest {
		if st.CatalogCID != st.HistoryCID {
			bySource[st.SourceID] = append(bySource[st.SourceID], st)
		}
	}
	sourceIDs := make([]string, 0, len(bySource))
	for id := range bySource {
		sourceIDs = append(sourceIDs, id)
	}
	sort.Strings(sourceIDs)
	for _, sid := range sourceIDs {
		if ctx.Err() != nil {
			return errGPArchiveStopped
		}
		src, ok := sources[sid]
		if !ok {
			return fmt.Errorf("source %q missing from sources ledger", sid)
		}
		states := bySource[sid]
		sort.Slice(states, func(i, j int) bool { return states[i].EntityID < states[j].EntityID })
		for start := 0; start < len(states); start += opts.BatchSize {
			if ctx.Err() != nil {
				return errGPArchiveStopped
			}
			end := start + opts.BatchSize
			if end > len(states) {
				end = len(states)
			}
			records := make([][]byte, 0, end-start)
			for _, st := range states[start:end] {
				records = append(records, st.MPE)
			}
			if _, err := sink.StoreCatalogMPE(records, sourceTags(src, gpArchiveCatalogSource)); err != nil {
				return err
			}
			for _, st := range states[start:end] {
				previous := st.CatalogCID
				st.CatalogCID = st.HistoryCID
				if previous != "" && previous != st.CatalogCID {
					if err := sink.DeleteCatalogMPE(previous); err != nil {
						return err
					}
				}
				report.CatalogEntitiesChanged++
			}
			if err := progress.save(false); err != nil {
				return err
			}
		}
	}
	report.CatalogEntities = int64(len(cp.Latest))
	report.CATObjects = report.CatalogEntities
	catBySource := map[string][][]byte{}
	catNames := map[string][]string{}
	entities := make([]string, 0, len(cp.Latest))
	for id := range cp.Latest {
		entities = append(entities, id)
	}
	sort.Strings(entities)
	for _, id := range entities {
		st := cp.Latest[id]
		name := st.GPObjectName
		if s, ok := satcat[st.NORAD]; ok && s.ObjectName != "" {
			name = s.ObjectName
		}
		if previous, ok := cp.CATNames[id]; ok && previous == name {
			continue
		}
		catBySource[st.SourceID] = append(catBySource[st.SourceID], buildGPArchiveCAT(id, st.NORAD, name))
		catNames[st.SourceID] = append(catNames[st.SourceID], id)
	}
	for _, sid := range sourceIDsUnion(catBySource) {
		if ctx.Err() != nil {
			return errGPArchiveStopped
		}
		src, ok := sources[sid]
		if !ok {
			return fmt.Errorf("CAT source %q missing", sid)
		}
		if _, err := sink.StoreCatalogCAT(catBySource[sid], sourceTags(src, gpArchiveCatalogSource)); err != nil {
			return err
		}
		for i, id := range catNames[sid] {
			cat := CATFB.GetSizePrefixedRootAsCAT(catBySource[sid][i], 0)
			cp.CATNames[id] = string(cat.OBJECT_NAME())
		}
	}
	return progress.save(false)
}
func sourceIDsUnion(m map[string][][]byte) []string {
	a := make([]string, 0, len(m))
	for k := range m {
		a = append(a, k)
	}
	sort.Strings(a)
	return a
}

func materializeGPArchiveOEM(ctx context.Context, opts gpArchiveOptions, cp *gpArchiveCheckpoint, report *gpArchiveRunReport, sink gpArchiveSink, progress *gpArchiveProgress) error {
	const invokeBatchSize = 100 // Well below the host's interactive WASM fuel ceiling; still <= the 1000-frame contract limit.
	pending := make([]*gpLatestState, 0)
	for _, st := range cp.Latest {
		if st.CatalogCID != "" && st.OEMParentCID != st.CatalogCID {
			pending = append(pending, st)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].EntityID < pending[j].EntityID })
	for start := 0; start < len(pending); start += invokeBatchSize {
		if ctx.Err() != nil {
			return errGPArchiveStopped
		}
		end := start + invokeBatchSize
		if end > len(pending) {
			end = len(pending)
		}
		states := pending[start:end]
		inputs := make([][]byte, len(states))
		for i, s := range states {
			inputs[i] = s.MPE
		}
		// Once a module batch starts, allow it to finish before honoring cancellation.
		outputs, mr, err := opts.deriver.Derive(context.WithoutCancel(ctx), inputs)
		if err != nil {
			return err
		}
		report.EpochStatesDerived += mr.Derived
		report.EpochStatesFailed += mr.Failed
		report.EpochsOutsideLeapSecondTable += mr.EpochsOutsideLeapSecondTable
		if mr.MaxRoundTripPositionKm > report.MaxRoundTripPositionKm {
			report.MaxRoundTripPositionKm = mr.MaxRoundTripPositionKm
		}
		if mr.MaxRoundTripVelocityKmPerS > report.MaxRoundTripVelocityKmPerS {
			report.MaxRoundTripVelocityKmPerS = mr.MaxRoundTripVelocityKmPerS
		}
		failed := map[int]gpEpochFailure{}
		for _, f := range mr.Failures {
			f.Index += start
			report.EpochStateFailures = append(report.EpochStateFailures, f)
			failed[f.Index-start] = f
		}
		oi := 0
		var successful []*gpLatestState
		var writes []gpOEMWrite
		for i, st := range states {
			if _, bad := failed[i]; bad {
				if st.OEMCID != "" {
					if err := sink.DeleteOEM(st.OEMCID); err != nil {
						return err
					}
					st.OEMCID = ""
				}
				st.OEMParentCID = st.CatalogCID
				continue
			}
			if oi >= len(outputs) {
				return fmt.Errorf("epoch-state emitted %d states for %d successful records", len(outputs), len(states)-len(failed))
			}
			tags := storage.SourceTags{ProviderID: "sdn", SourceName: "epoch-state", SourceURL: "sdn:mpe/" + st.CatalogCID, BatchID: "sha256:" + opts.moduleHash, ProducerPeerID: gpArchiveOEMSourcePeer}
			writes = append(writes, gpOEMWrite{Record: outputs[oi], Tags: tags})
			successful = append(successful, st)
			oi++
		}
		if oi != len(outputs) {
			return fmt.Errorf("epoch-state emitted %d extra states", len(outputs)-oi)
		}
		cids, err := sink.StoreOEMBatch(writes)
		if err != nil {
			return err
		}
		for i, st := range successful {
			previous := st.OEMCID
			st.OEMCID = cids[i]
			st.OEMParentCID = st.CatalogCID
			if previous != "" && previous != cids[i] {
				if err := sink.DeleteOEM(previous); err != nil {
					return err
				}
			}
		}
		if err := progress.save(false); err != nil {
			return err
		}
	}
	return nil
}

func (d *gpWASMEpochDeriver) Close() error {
	if d == nil || d.module == nil {
		return nil
	}
	return d.module.Close()
}
func (d *gpWASMEpochDeriver) Derive(ctx context.Context, records [][]byte) ([][]byte, gpEpochReport, error) {
	payload := make([]byte, 0)
	for _, r := range records {
		payload = append(payload, r...)
		for len(payload)%8 != 0 {
			payload = append(payload, 0)
		}
	}
	outputs, err := d.module.InvokeMethodOutputs(ctx, "derive", []modulert.InvokeInputFrame{{PortID: "elements", SchemaName: "MPE.fbs", FileIdentifier: "$MPE", RootTypeName: "MPE", WireFormat: 1, RequiredAlignment: 8, Alignment: 8, ByteLength: uint32(len(payload)), Payload: payload}})
	if err != nil {
		return nil, gpEpochReport{}, err
	}
	var statesPayload, reportPayload []byte
	for _, o := range outputs {
		switch o.PortID {
		case "states":
			statesPayload = o.Payload
		case "report":
			reportPayload = o.Payload
		}
	}
	var mr gpEpochReport
	if len(reportPayload) > 0 {
		if err = json.Unmarshal(reportPayload, &mr); err != nil {
			return nil, mr, fmt.Errorf("decode epoch-state report: %w", err)
		}
	}
	states, err := splitSizePrefixedAligned(statesPayload)
	return states, mr, err
}
func splitSizePrefixedAligned(payload []byte) ([][]byte, error) {
	var out [][]byte
	for off := 0; off < len(payload); {
		if len(payload)-off < 4 {
			return nil, fmt.Errorf("truncated size prefix at %d", off)
		}
		size := int(binary.LittleEndian.Uint32(payload[off:]))
		if size == 0 {
			off += 4
			continue
		}
		end := off + 4 + size
		if end > len(payload) {
			return nil, fmt.Errorf("frame at %d exceeds payload", off)
		}
		out = append(out, append([]byte(nil), payload[off:end]...))
		off = end
		for off%8 != 0 && off < len(payload) {
			if payload[off] != 0 {
				return nil, fmt.Errorf("nonzero alignment padding at %d", off)
			}
			off++
		}
	}
	return out, nil
}

func gpArchiveRecordFromCSV(row []string, columns map[string]int) (gpArchiveRecord, string) {
	get := func(name string) string {
		i, ok := columns[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	return parseGPArchiveRecord(get("OBJECT_ID"), get("OBJECT_NAME"), get("NORAD_CAT_ID"), get("EPOCH"), get("MEAN_MOTION"), get("ECCENTRICITY"), get("INCLINATION"), get("RA_OF_ASC_NODE"), get("ARG_OF_PERICENTER"), get("MEAN_ANOMALY"), get("BSTAR"), "")
}
func gpArchiveRecordFromJSON(r gpJSONRecord) (gpArchiveRecord, string) {
	return parseGPArchiveRecord(string(r.ObjectID), string(r.ObjectName), string(r.NORAD), string(r.Epoch), string(r.MeanMotion), string(r.Eccentricity), string(r.Inclination), string(r.RAOfAscNode), string(r.ArgOfPericenter), string(r.MeanAnomaly), string(r.BSTAR), string(r.GPID))
}
func parseGPArchiveRecord(objectID, name, noradS, epochS, mmS, eccS, incS, raanS, argS, maS, bstarS, gpid string) (gpArchiveRecord, string) {
	n, err := parseUint32(noradS)
	if err != nil {
		return gpArchiveRecord{}, "invalid NORAD_CAT_ID"
	}
	t, err := parseGPArchiveEpoch(epochS)
	if err != nil {
		return gpArchiveRecord{}, "invalid EPOCH"
	}
	vals := []string{mmS, eccS, incS, raanS, argS, maS, bstarS}
	out := make([]float64, len(vals))
	names := []string{"MEAN_MOTION", "ECCENTRICITY", "INCLINATION", "RA_OF_ASC_NODE", "ARG_OF_PERICENTER", "MEAN_ANOMALY", "BSTAR"}
	for i, v := range vals {
		out[i], err = strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || math.IsNaN(out[i]) || math.IsInf(out[i], 0) {
			return gpArchiveRecord{}, "invalid " + names[i]
		}
	}
	if out[1] < 0 || out[1] >= 1 {
		return gpArchiveRecord{}, "invalid ECCENTRICITY"
	}
	return gpArchiveRecord{GPID: strings.TrimSpace(gpid), ObjectID: strings.TrimSpace(objectID), ObjectName: strings.TrimSpace(name), NORAD: n, Epoch: float64(t.Unix()) + float64(t.Nanosecond())/1e9, MeanMotion: out[0], Eccentricity: out[1], Inclination: out[2], RAOfAscNode: out[3], ArgOfPericenter: out[4], MeanAnomaly: out[5], BSTAR: out[6]}, ""
}
func parseGPArchiveEpoch(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	layouts := []string{"2006-01-02T15:04:05.999999999", time.RFC3339Nano}
	for _, layout := range layouts {
		if t, e := time.Parse(layout, v); e == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid epoch %q", v)
}
func parseUint32(v string) (uint32, error) {
	n, e := strconv.ParseUint(strings.TrimSpace(v), 10, 32)
	return uint32(n), e
}

func gpArchiveCSVKey(r gpArchiveRecord) string {
	return fmt.Sprintf("%d|%.9f|%.17g|%.17g|%.17g|%.17g|%.17g|%.17g|%.17g", r.NORAD, r.Epoch, r.MeanMotion, r.Eccentricity, r.Inclination, r.RAOfAscNode, r.ArgOfPericenter, r.MeanAnomaly, r.BSTAR)
}
func buildGPArchiveMPE(entity string, r gpArchiveRecord) []byte {
	b := flatbuffers.NewBuilder(256)
	id := b.CreateString(entity)
	MPEFB.MPEStart(b)
	MPEFB.MPEAddENTITY_ID(b, id)
	forceGPFloat64Slot(b, 1, r.Epoch)
	forceGPFloat64Slot(b, 2, r.MeanMotion)
	forceGPFloat64Slot(b, 3, r.Eccentricity)
	forceGPFloat64Slot(b, 4, r.Inclination)
	forceGPFloat64Slot(b, 5, r.RAOfAscNode)
	forceGPFloat64Slot(b, 6, r.ArgOfPericenter)
	forceGPFloat64Slot(b, 7, r.MeanAnomaly)
	forceGPFloat64Slot(b, 8, r.BSTAR)
	b.PrependInt8(0)
	b.Slot(9) // SGP4 is enum value zero; force the field to be present.
	root := MPEFB.MPEEnd(b)
	MPEFB.FinishSizePrefixedMPEBuffer(b, root)
	return append([]byte(nil), b.FinishedBytes()...)
}

func forceGPFloat64Slot(b *flatbuffers.Builder, slot int, value float64) {
	b.PrependFloat64(value)
	b.Slot(slot)
}
func buildGPArchiveCAT(entity string, norad uint32, name string) []byte {
	b := flatbuffers.NewBuilder(128)
	id := b.CreateString(entity)
	n := b.CreateString(name)
	CATFB.CATStart(b)
	CATFB.CATAddOBJECT_ID(b, id)
	CATFB.CATAddNORAD_CAT_ID(b, norad)
	CATFB.CATAddOBJECT_NAME(b, n)
	root := CATFB.CATEnd(b)
	CATFB.FinishSizePrefixedCATBuffer(b, root)
	return append([]byte(nil), b.FinishedBytes()...)
}

func hashFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func readGzipAndHash(path string) ([]byte, string, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, "", e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return nil, "", e
	}
	defer gz.Close()
	h := sha256.New()
	var b bytes.Buffer
	if _, e = io.Copy(io.MultiWriter(&b, h), gz); e != nil {
		return nil, "", e
	}
	return b.Bytes(), hex.EncodeToString(h.Sum(nil)), nil
}
