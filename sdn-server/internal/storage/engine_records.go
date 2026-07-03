package storage

// engine_records.go wires SDS record vtabs into the store's FlatSQL-WASM
// engine (loop B.3, docs/flatsql-store-v2.md §4/§5): the write path mirrors
// every stored OMM record into a per-source shadow table
// (`OMM@<source-name>`), boot rebuilds the hot window from the durable
// control tables + stream files, and epoch profile queries (nearest / as_of /
// forward) run natively inside the engine over the unified OMM view,
// streaming aligned size-prefixed FlatBuffer frames out.
//
// The engine vtab is a pure cache: control rows + stream files remain the
// source of truth, so ingest failures are logged and skipped — only a trapped
// (poisoned) runtime is fatal.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

const (
	// engineDefaultHotWindow bounds the records resident in the engine per
	// schema. A.4 measured the raw record ceiling at ~1.5M resident $OMM, but
	// the record vtabs share the 4 GiB engine with the control tables (~4
	// control rows per record, loop B.7), so the practical default is the
	// migration guidance's ~400K records. Configurable via
	// WithEngineHotWindow (config storage.engine_hot_window). Enforced at
	// boot rebuild (LIMIT) and at ingest (tombstone eviction of the oldest
	// resident records). Eviction never touches stream files, the control
	// journal, or datasync cursor rowids.
	engineDefaultHotWindow = 400_000

	// engineOMMSchemaName is the only SDS schema routed into the engine so
	// far (loop B.3 slice); further standards land with their own tables.
	engineOMMSchemaName = "OMM.fbs"

	// engineDefaultSource partitions records stored without source tags.
	engineDefaultSource = "local"
)

// engineRecordSchema is the FlatBuffer schema handed to the engine. The OMM
// table mirrors the SDS OMM layout exactly (parity-tested against real $OMM
// buffers in internal/flatsqlrt/flatsqlrt_test.go); control tables
// (sdn_record_index & co.) stay plain SQLite tables created via DDL through
// the driver.
const engineRecordSchema = `
  table OMM {
    CCSDS_OMM_VERS:double;
    CREATION_DATE:string;
    ORIGINATOR:string;
    OBJECT_NAME:string;
    OBJECT_ID:string;
    CENTER_NAME:string;
    REFERENCE_FRAME:RFM;
    REFERENCE_FRAME_EPOCH:string;
    TIME_SYSTEM:timingStandard = UTC;
    MEAN_ELEMENT_THEORY:meanElementSource = SGP4;
    COMMENT:string;
    EPOCH:string;
    SEMI_MAJOR_AXIS:double;
    MEAN_MOTION:double;
    ECCENTRICITY:double;
    INCLINATION:double;
    RA_OF_ASC_NODE:double;
    ARG_OF_PERICENTER:double;
    MEAN_ANOMALY:double;
    GM:double;
    MASS:double;
    SOLAR_RAD_AREA:double;
    SOLAR_RAD_COEFF:double;
    DRAG_AREA:double;
    DRAG_COEFF:double;
    EPHEMERIS_TYPE:ephemerisFormat = SGP4;
    CLASSIFICATION_TYPE:string;
    NORAD_CAT_ID:uint32;
    ELEMENT_SET_NO:uint32;
    REV_AT_EPOCH:double;
    BSTAR:double;
    MEAN_MOTION_DOT:double;
    MEAN_MOTION_DDOT:double;
    COV_REFERENCE_FRAME:RFM;
    COVARIANCE:[double];
    USER_DEFINED_BIP_0044_TYPE:uint;
    USER_DEFINED_OBJECT_DESIGNATOR:string;
    USER_DEFINED_EARTH_MODEL:string;
    USER_DEFINED_EPOCH_TIMESTAMP: double;
    USER_DEFINED_MICROSECONDS: double;
  }
  root_type OMM;
  file_identifier "$OMM";
`

// Engine-native epoch query shapes over the unified OMM view. Positional
// params, in this exact order: ?1 source shadow name (” = all sources),
// ?2 epoch unix seconds (float), ?3 limit.
const (
	engineEpochNearestSQL = `SELECT _data FROM (SELECT _data, ROW_NUMBER() OVER (PARTITION BY NORAD_CAT_ID ORDER BY ABS(USER_DEFINED_EPOCH_TIMESTAMP - ?2)) rn FROM OMM WHERE (?1 = '' OR _source = ?1)) WHERE rn = 1 LIMIT ?3`
	engineEpochAsOfSQL    = `SELECT _data FROM (SELECT _data, ROW_NUMBER() OVER (PARTITION BY NORAD_CAT_ID ORDER BY USER_DEFINED_EPOCH_TIMESTAMP DESC) rn FROM OMM WHERE (?1 = '' OR _source = ?1) AND USER_DEFINED_EPOCH_TIMESTAMP <= ?2) WHERE rn = 1 LIMIT ?3`
	engineEpochForwardSQL = `SELECT _data FROM (SELECT _data, ROW_NUMBER() OVER (PARTITION BY NORAD_CAT_ID ORDER BY USER_DEFINED_EPOCH_TIMESTAMP ASC) rn FROM OMM WHERE (?1 = '' OR _source = ?1) AND USER_DEFINED_EPOCH_TIMESTAMP >= ?2) WHERE rn = 1 LIMIT ?3`
)

// engineOwnsTableName reports whether a SQL table name is reserved by an
// engine record vtab (plus its unified view). Plain control/metadata tables
// can never use these names: the vtab occupies them in the shared SQLite
// context.
func engineOwnsTableName(tableName string) bool {
	return tableName == "OMM"
}

// engineIngest is one record queued for the engine vtab after its control
// row committed.
type engineIngest struct {
	data   []byte
	source string
}

// engineSourceName maps a record's source tags to the engine partition name.
func engineSourceName(tags *SourceTags) string {
	if tags != nil {
		if name := strings.TrimSpace(tags.SourceName); name != "" {
			return name
		}
	}
	return engineDefaultSource
}

// engineRecordPayload returns the unprefixed FlatBuffer bytes the engine's
// IngestOne* API expects. Store accepts both bare and size-prefixed buffers
// (see parseOMM), so strip a 4-byte size prefix when the buffer carries one.
func engineRecordPayload(data []byte) []byte {
	if len(data) >= 12 &&
		int64(binary.LittleEndian.Uint32(data[:4]))+4 == int64(len(data)) &&
		OMM.SizePrefixedOMMBufferHasIdentifier(data) {
		return data[4:]
	}
	return data
}

// ensureEngineSource registers a per-source shadow table once and refreshes
// the unified views so queries see the new partition. Caller holds s.mu for
// writing (s.engineSources is guarded by it).
func (s *FlatSQLStore) ensureEngineSource(source string) error {
	if s.engineSources[source] {
		return nil
	}
	if err := s.engineDB.RegisterSource(source); err != nil {
		return err
	}
	s.engineSources[source] = true
	if err := s.engineDB.CreateUnifiedViews(); err != nil {
		// Ingest into the registered shadow table still works; queries just
		// miss the partition until the next successful view rebuild.
		log.Warnf("FlatSQL engine: rebuild unified views after registering source %q: %v", source, err)
		if s.engine.Poisoned() {
			return err
		}
	}
	return nil
}

// ingestEngineRecords mirrors committed records into the engine vtab. The
// control row + stream file are the source of truth, so per-record failures
// are logged and skipped; only a poisoned (trapped) runtime is returned as an
// error. Caller holds s.mu for writing.
func (s *FlatSQLStore) ingestEngineRecords(schemaName string, pending []engineIngest) error {
	ingested := int64(0)
	for _, p := range pending {
		if err := s.ensureEngineSource(p.source); err != nil {
			log.Warnf("FlatSQL engine: register source %q for %s: %v", p.source, schemaName, err)
			if s.engine.Poisoned() {
				return fmt.Errorf("FlatSQL engine poisoned registering source %q: %w", p.source, err)
			}
			continue
		}
		if _, err := s.engineDB.IngestOneWithSource(engineRecordPayload(p.data), p.source); err != nil {
			log.Warnf("FlatSQL engine: ingest %s record into %q: %v", schemaName, p.source, err)
			if s.engine.Poisoned() {
				return fmt.Errorf("FlatSQL engine poisoned during %s ingest: %w", schemaName, err)
			}
			continue
		}
		ingested++
	}
	s.engineResident[schemaName] += ingested
	return s.enforceEngineHotWindowLocked(schemaName)
}

// enforceEngineHotWindowLocked evicts the OLDEST resident engine records
// beyond the configured hot window by tombstoning them in their per-source
// shadow tables (flatsql_mark_deleted): queries stop returning them
// immediately, and the next boot rebuild (which loads at most the window)
// reclaims the memory. The durable substrate is untouched — control rows,
// stream files, and the datasync cursor rowid space keep every record.
// Failures are logged and skipped (the window is a cache bound, not
// correctness-critical state); only a poisoned runtime is fatal. Caller
// holds s.mu for writing.
func (s *FlatSQLStore) enforceEngineHotWindowLocked(schemaName string) error {
	if schemaName != engineOMMSchemaName || s.engineHotWindow <= 0 {
		return nil
	}
	overflow := s.engineResident[schemaName] - int64(s.engineHotWindow)
	if overflow <= 0 {
		return nil
	}
	if len(s.engineSources) == 0 {
		return nil
	}

	// Oldest live rows first. `_rowid` is the engine's per-source ingest
	// sequence and both boot rebuild and live mirroring insert in ascending
	// global-index order, so ascending `_rowid` is ingest order (exactly so
	// for the dominant single-source case; near-ingest-order across
	// concurrently written sources). Tombstoned rows are already skipped by
	// the vtab scan.
	res, err := s.engineDB.Query(
		`SELECT _source, _rowid FROM OMM ORDER BY _rowid ASC LIMIT ?1`, overflow)
	if err != nil {
		log.Warnf("FlatSQL engine: hot-window eviction scan (%s): %v", schemaName, err)
		if s.engine.Poisoned() {
			return fmt.Errorf("FlatSQL engine poisoned during hot-window eviction scan: %w", err)
		}
		return nil
	}

	evicted := int64(0)
	for _, row := range res.Rows {
		if len(row) != 2 {
			continue
		}
		source, ok := row[0].(string)
		if !ok || source == "" {
			continue
		}
		seq, ok := row[1].(int64)
		if !ok {
			continue
		}
		// source carries the FULL shadow-table name ("OMM@celestrak-gp") as
		// reported by the unified view — exactly the name MarkDeleted keys
		// on, and guaranteed registered (it came from the engine itself).
		if err := s.engineDB.MarkDeleted(source, uint64(seq)); err != nil {
			log.Warnf("FlatSQL engine: tombstone %s seq %d: %v", source, seq, err)
			if s.engine.Poisoned() {
				return fmt.Errorf("FlatSQL engine poisoned during hot-window eviction: %w", err)
			}
			continue
		}
		evicted++
	}
	s.engineResident[schemaName] -= evicted
	if evicted > 0 {
		log.Infof("FlatSQL engine: hot window evicted %d oldest %s records (window %d)",
			evicted, schemaName, s.engineHotWindow)
	}
	return nil
}

// rebuildEngineRecords reloads the engine vtab hot window from durable state
// at boot: the newest engineHotWindow OMM records (by global index rowid),
// replayed in ascending rowid order from the stream files. A no-op on empty
// stores. Never fails the open for per-record problems — only a poisoned
// runtime is fatal. Called from NewFlatSQLStore before the store is shared,
// so no locking is needed.
func (s *FlatSQLStore) rebuildEngineRecords() error {
	readSource, err := s.recordReadSource(engineOMMSchemaName)
	if err != nil {
		log.Warnf("FlatSQL engine rebuild: record read source: %v", err)
		return nil
	}

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT stream_path, stream_offset, record_length, source_name FROM (
			SELECT idx.rowid AS rid,
			       rr.stream_path AS stream_path,
			       rr.stream_offset AS stream_offset,
			       rr.record_length AS record_length,
			       COALESCE((
			           SELECT tags.source_name
			           FROM sdn_record_source_tags tags
			           WHERE tags.schema_name = idx.schema_name AND tags.cid = idx.cid
			           ORDER BY tags.created_at DESC
			           LIMIT 1
			       ), '') AS source_name
			FROM sdn_record_index idx
			JOIN %s rr ON rr.cid = idx.cid
			WHERE idx.schema_name = ?
			ORDER BY idx.rowid DESC
			LIMIT ?
		) ORDER BY rid ASC
	`, readSource), engineOMMSchemaName, s.engineHotWindow)
	if err != nil {
		log.Warnf("FlatSQL engine rebuild: query %s hot window: %v", engineOMMSchemaName, err)
		return nil
	}
	defer rows.Close()

	// Cache stream file handles across records: the hot window reads the same
	// few append-only files millions of times otherwise.
	files := map[string]*os.File{}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()

	rebuilt, skipped := 0, 0
	for rows.Next() {
		var streamPath, sourceName string
		var streamOffset, recordLength int64
		if err := rows.Scan(&streamPath, &streamOffset, &recordLength, &sourceName); err != nil {
			log.Warnf("FlatSQL engine rebuild: scan hot-window row: %v", err)
			skipped++
			continue
		}
		data, err := s.readStreamRecordCached(files, streamPath, streamOffset, recordLength)
		if err != nil {
			log.Warnf("FlatSQL engine rebuild: read %s@%d: %v", streamPath, streamOffset, err)
			skipped++
			continue
		}
		source := strings.TrimSpace(sourceName)
		if source == "" {
			source = engineDefaultSource
		}
		if err := s.ensureEngineSource(source); err != nil {
			log.Warnf("FlatSQL engine rebuild: register source %q: %v", source, err)
			if s.engine.Poisoned() {
				return fmt.Errorf("FlatSQL engine poisoned registering source %q: %w", source, err)
			}
			skipped++
			continue
		}
		if _, err := s.engineDB.IngestOneWithSource(engineRecordPayload(data), source); err != nil {
			log.Warnf("FlatSQL engine rebuild: ingest record: %v", err)
			if s.engine.Poisoned() {
				return fmt.Errorf("FlatSQL engine poisoned during rebuild ingest: %w", err)
			}
			skipped++
			continue
		}
		rebuilt++
	}
	if err := rows.Err(); err != nil {
		log.Warnf("FlatSQL engine rebuild: iterate hot window: %v", err)
	}
	s.engineResident[engineOMMSchemaName] = int64(rebuilt)
	if rebuilt > 0 || skipped > 0 {
		log.Infof("FlatSQL engine rebuild: loaded %d %s records into the hot window (%d skipped, window %d)", rebuilt, engineOMMSchemaName, skipped, s.engineHotWindow)
	}
	return nil
}

// readStreamRecordCached is readFlatSQLStreamRecord with a caller-owned open
// file cache (boot rebuild reads the same append-only stream files record by
// record).
func (s *FlatSQLStore) readStreamRecordCached(files map[string]*os.File, streamPath string, streamOffset, recordLength int64) ([]byte, error) {
	if streamOffset < 0 {
		return nil, fmt.Errorf("negative FlatSQL stream offset %d", streamOffset)
	}
	if recordLength < 0 || recordLength > int64(^uint32(0)) {
		return nil, fmt.Errorf("invalid FlatSQL record length %d", recordLength)
	}
	clean := filepath.Clean(streamPath)
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("invalid FlatSQL stream path %q", streamPath)
	}
	file, ok := files[clean]
	if !ok {
		var err error
		file, err = os.Open(filepath.Join(s.basePath, clean))
		if err != nil {
			return nil, err
		}
		files[clean] = file
	}
	var sizePrefix [4]byte
	if _, err := file.ReadAt(sizePrefix[:], streamOffset); err != nil {
		return nil, err
	}
	if length := int64(binary.LittleEndian.Uint32(sizePrefix[:])); length != recordLength {
		return nil, fmt.Errorf("FlatSQL stream frame length = %d, want %d", length, recordLength)
	}
	data := make([]byte, recordLength)
	if _, err := file.ReadAt(data, streamOffset+4); err != nil {
		return nil, err
	}
	return data, nil
}

// QueryEpochRawStream runs an engine-native epoch profile (nearest, as_of,
// forward) over the unified OMM view and returns the aligned size-prefixed
// FlatBuffer frame stream — the wire format served to the network — without
// hydrating anything from stream files. sourceName is the bare source (e.g.
// "celestrak-gp"); empty means all sources. limit <= 0 means no limit.
func (s *FlatSQLStore) QueryEpochRawStream(schemaName string, sourceName string, profile string, epochUnix float64, limit int) (*flatsqlrt.RawStream, error) {
	if normalizeSchemaNameForEpoch(schemaName) != engineOMMSchemaName {
		return nil, fmt.Errorf("engine epoch queries support only %s (got %q)", engineOMMSchemaName, schemaName)
	}

	var sqlText string
	switch strings.TrimPrefix(strings.TrimSpace(profile), "epoch.") {
	case "nearest":
		sqlText = engineEpochNearestSQL
	case "as_of":
		sqlText = engineEpochAsOfSQL
	case "forward":
		sqlText = engineEpochForwardSQL
	default:
		return nil, fmt.Errorf("unsupported engine epoch profile %q (want nearest, as_of, or forward)", profile)
	}

	sourceShadow := ""
	if src := strings.TrimSpace(sourceName); src != "" {
		// The unified view's _source column carries the FULL shadow-table
		// name ("OMM@celestrak-gp"), not the bare source.
		sourceShadow = "OMM@" + src
	}
	limitParam := int64(limit)
	if limit <= 0 {
		limitParam = -1 // SQLite: LIMIT -1 = unlimited
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engineDB == nil {
		return nil, errors.New("store is closed")
	}
	if len(s.engineSources) == 0 {
		// No sources registered yet: the unified view (and its _source
		// column) does not exist. Nothing ingested, nothing to return.
		return &flatsqlrt.RawStream{Bytes: []byte{}}, nil
	}
	return s.engineDB.QueryRawFlatBufferStream(sqlText, sourceShadow, epochUnix, limitParam)
}

// EngineRecordCount reports how many records are resident in the engine's
// unified view for a schema (the hot window), 0 when nothing was ingested yet.
func (s *FlatSQLStore) EngineRecordCount(schemaName string) (int64, error) {
	if normalizeSchemaNameForEpoch(schemaName) != engineOMMSchemaName {
		return 0, fmt.Errorf("engine record counts support only %s (got %q)", engineOMMSchemaName, schemaName)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engineDB == nil {
		return 0, errors.New("store is closed")
	}
	res, err := s.engineDB.Query(`SELECT COUNT(*) FROM OMM`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "no such view") {
			return 0, nil
		}
		return 0, err
	}
	if len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
		return 0, fmt.Errorf("unexpected engine count result shape: %d rows", len(res.Rows))
	}
	count, ok := res.Rows[0][0].(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected engine count cell type %T", res.Rows[0][0])
	}
	return count, nil
}
