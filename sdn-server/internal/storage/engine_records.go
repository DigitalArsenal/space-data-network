package storage

// engine_records.go wires SDS record vtabs into the store's FlatSQL-WASM
// engine (loop B.3, docs/flatsql-store-v2.md §4/§5): the write path mirrors
// every stored record of a ROUTED schema into a per-source shadow table
// (`<Base>@<source-name>`), boot rebuilds the hot window from the durable
// control tables + stream files, and epoch profile queries (nearest / as_of /
// forward) run natively inside the engine over the unified OMM view,
// streaming aligned size-prefixed FlatBuffer frames out.
//
// EVERY EMBEDDED STANDARD IS ROUTED (owner directive 2026-08-25: "The routing
// of $IRM just needs to be another one of the standards ingested like all the
// others in the standards engine"). engineRoutedSchemas used to be a two-entry
// literal — $OMM (loop B.3) and $TBS (the cellular slice,
// sdn-tbs-feed-sync-for-cache-lane) — so a new standard was readable through
// the sandboxed query surface only after someone typed its name here. It is
// now the DEFAULT for every standard the node embeds: the generated catalog
// (engine_standard_catalog.go) supplies the table graph and the file
// identifier for each, and a standard stops being routed only for a reason
// stated about its IDL (no file_identifier) or about the store it is opening
// (its code is already a plain control table there).
//
// SCHEMA-SPECIFIC DECORATIONS STAY OPT-IN AND ARE NEVER THE GATE. $OMM keeps
// the engine-native epoch profiles (QueryEpochRawStream refuses any other
// schema by construction) and $TBS keeps the lat/lon R-Tree the engine derives
// from its column names — both sit ON TOP of the generic route.
//
// The engine vtab is a pure cache: control rows + stream files remain the
// source of truth, so ingest failures are logged and skipped — only a trapped
// (poisoned) runtime is fatal.

//go:generate go run ./gen -schemas ../sds/schemas -out engine_standard_catalog.go

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

	// engineDefaultGenericHotWindow bounds the records resident per
	// GENERICALLY routed schema — every standard except the two the node
	// decorates and reads at provider scale. The per-schema budget above is
	// sized for a catalog-scale feed; multiplied across 227 standards it is
	// not a bound at all against the engine's 4 GiB, so a standard that is
	// routed merely because it exists gets a small window until something
	// actually reads it at scale. Configurable via WithEngineGenericHotWindow
	// (config storage.engine_generic_hot_window).
	engineDefaultGenericHotWindow = 10_000

	// engineOMMSchemaName is the loop-B.3 SDS schema routed into the engine.
	engineOMMSchemaName = "OMM.fbs"

	// engineTBSSchemaName is the cellular slice
	// (sdn-tbs-feed-sync-for-cache-lane): $TBS sites materialized by the
	// dataset-feed-head-sync lane must be readable through the engine's
	// record surface, because that is what storage.flatsql_query_stream ->
	// QueryRawStream serves to the aggregate cache flow.
	engineTBSSchemaName = "TBS.fbs"

	// engineDefaultSource partitions records stored without source tags.
	engineDefaultSource = "local"
)

// engineRoutedSchema is one SDS standard's engine binding: the SQL base table
// its records land in and the 4-byte FlatBuffer file identifier that routes a
// buffer to that table at ingest (RegisterFileID).
type engineRoutedSchema struct {
	Table  string
	FileID string
}

// engineDecoratedSchemas are the two standards this host decorates: $OMM with
// engine-native epoch profiles and the parity-pinned table text, $TBS with the
// cellular tile contract's flat point fields. They are routed exactly like
// every other standard — the only thing this set changes is that their table
// TEXT is hand-pinned rather than generated, and that they keep the full
// per-schema hot window instead of the generic one.
var engineDecoratedSchemas = map[string]engineRoutedSchema{
	engineOMMSchemaName: {Table: "OMM", FileID: "$OMM"},
	engineTBSSchemaName: {Table: "TBS", FileID: "$TBS"},
}

// engineRoutedSchemas is THE list of SDS schemas routed into the engine: the
// two decorated standards plus EVERY other embedded standard that declares a
// file identifier (engine_standard_catalog.go, generated from the embedded
// IDLs). File-id registration, table ownership, the public query surface,
// ingest gating, hot-window eviction and boot rebuild all read this map, so
// nothing anywhere compares against a schema constant.
var engineRoutedSchemas = buildEngineRoutedSchemas()

func buildEngineRoutedSchemas() map[string]engineRoutedSchema {
	routed := make(map[string]engineRoutedSchema, len(engineDecoratedSchemas)+len(engineGeneratedStandardBindings))
	for name, binding := range engineDecoratedSchemas {
		routed[name] = binding
	}
	for name, binding := range engineGeneratedStandardBindings {
		routed[name] = binding
	}
	return routed
}

// engineRoutedTableNames / engineRoutedFileIDs are set lookups over the same
// map. They exist because their callers run PER RECORD (engineRecordPayload on
// every stored buffer) and per table name: a linear scan was free over two
// entries and is 227 string compares per record now.
var (
	engineRoutedTableNames = engineRoutedNameSet(func(b engineRoutedSchema) string { return b.Table })
	engineRoutedFileIDs    = engineRoutedNameSet(func(b engineRoutedSchema) string { return b.FileID })
)

func engineRoutedNameSet(key func(engineRoutedSchema) string) map[string]bool {
	set := make(map[string]bool, len(engineRoutedSchemas))
	for _, binding := range engineRoutedSchemas {
		set[key(binding)] = true
	}
	return set
}

// engineRoutedSchemaFor resolves a schema name (with or without the ".fbs"
// suffix, any case) to its engine binding.
func engineRoutedSchemaFor(schemaName string) (engineRoutedSchema, bool) {
	binding, ok := engineRoutedSchemas[normalizeSchemaNameForEpoch(schemaName)]
	return binding, ok
}

// engineRoutesSchema reports whether stored records of this schema are
// mirrored into the engine.
func engineRoutesSchema(schemaName string) bool {
	_, ok := engineRoutedSchemaFor(schemaName)
	return ok
}

// engineRoutedSchemaNames lists the routed schema names in a stable order so
// boot rebuild and file-id registration are deterministic.
func engineRoutedSchemaNames() []string {
	names := make([]string, 0, len(engineRoutedSchemas))
	for name := range engineRoutedSchemas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// registerEngineFileIDs maps every routed standard's file identifier to its
// base table. A table only materializes in SQLite once its file id is
// registered (FlatSQLDatabase::initializeSQLiteEngine), so this is also what
// makes `SELECT ... FROM TBS` resolve on a store that has never ingested a
// $TBS record — an empty answer instead of "no such table".
//
// excluded names the standards this particular store must NOT route (see
// engineExcludedStandards). Registering an excluded standard would be worse
// than useless: its table is absent from the schema this database was created
// from, and FlatSQLDatabase::registerFileId THROWS on an unknown table —
// which, in the -fignore-exceptions engine, poisons the runtime at boot.
func registerEngineFileIDs(engineDB *flatsqlrt.Database, excluded map[string]bool) error {
	for _, name := range engineRoutedSchemaNames() {
		if excluded[name] {
			continue
		}
		binding := engineRoutedSchemas[name]
		if err := engineDB.RegisterFileID(binding.FileID, binding.Table); err != nil {
			return fmt.Errorf("register %s file identifier: %w", binding.FileID, err)
		}
	}
	return nil
}

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

// engineTBSTableGraph is the $TBS record table, appended to engineRecordSchema
// to form the schema the store actually creates its database from
// (engineDatabaseSchema).
//
// It is a SEPARATE constant on purpose. engineRecordSchema is a CROSS-HOST
// CONTRACT: shared-test-vectors/flatsql-parity.json pins that exact text and
// both hosts (WasmEdge via internal/flatsqlrt, V8 via sdn-js) build their
// parity database from it, so this host cannot unilaterally rewrite it.
// Appending keeps the pinned OMM surface byte-identical while the node gains
// the cellular read surface.
//
// The table mirrors internal/sds/schemas/TBS.fbs field-for-field IN
// DECLARATION ORDER — vtable slots are positional, so a reordered or elided
// field would decode a NEIGHBOURING value, not a missing one. The two
// trailing REQUIRED fields (SOURCES:[TBSProvenance], CONSENSUS:TBSConsensus)
// are deliberately NOT projected as columns: they are a table vector and a
// nested table, they sit last so omitting them cannot shift any preceding
// slot, and every column the cellular tile contract names
// (docs/cellular-density-tiles/contract.json pointFields) is flat. Their
// bytes are never lost — `_data` is the whole record, so attribution and
// consensus travel intact to any consumer that decodes the frame with the
// real $TBS binding.
const engineTBSTableGraph = `
  table TBS {
    ID:string;
    NATIVE_ID:string;
    RADIO:tbsRadioClass = UNKNOWN;
    MCC:uint;
    MNC:uint;
    LAC:uint;
    TAC:uint;
    CELL_ID:string;
    LATITUDE:double;
    LONGITUDE:double;
    RANGE_M:double;
    SAMPLES:uint;
    AVERAGE_SIGNAL_DBM:double;
    FIRST_OBSERVED:string;
    LAST_OBSERVED:string;
    OPERATOR:string;
    FREQUENCY_MHZ:double;
    SITE_NAME:string;
    COUNTRY_CODE:string;
  }
`

// engineDatabaseSchema is the schema every engine database is created from:
// the parity-pinned OMM surface, the pinned TBS surface, and the generated
// table graph for every OTHER embedded standard.
//
// ORDER IS PART OF THE CONTRACT. engineRecordSchema must stay FIRST and
// byte-identical (shared-test-vectors/flatsql-parity.json pins it and both
// hosts build their parity database from it); appending can never change what
// the OMM table means.
const engineDatabaseSchema = engineRecordSchema + engineTBSTableGraph + engineStandardCatalogGraph

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
	return engineRoutedTableNames[tableName]
}

// ==================== Per-store routing (exclusions + windows) ====================

// engineExcludedStandards reports the routed standards this store must NOT
// route, because their standard code is already a PLAIN table in its control
// database.
//
// THIS IS A DATA-DESTRUCTION GUARD, not tidiness. createUnifiedView issues
// `DROP TABLE IF EXISTS "<name>"` before it creates the view (flatsql
// cpp/src/sqlite_engine.cpp), so routing a standard whose code is an existing
// plain table would DELETE that table and every row in it — and those rows are
// reachable today, because recordReadSourceFiltered unions the bare canonical
// table when it exists. v1 stores are routed-only and never create such a
// table anew, so this can only fire on a database migrated from a pre-WS7.3d
// store. It fires FAIL-CLOSED: the standard is dropped from the schema the
// database is created from, its rows are left exactly where they are, and the
// exclusion is logged with the reason.
//
// The two DECORATED standards are deliberately not excludable: they have been
// engine-owned since loop B.3 / the cellular slice, so their canonical names
// were already reserved before this change and excluding them now would be a
// different regression, not a fix.
func engineExcludedStandards(engineDB *flatsqlrt.Database) (map[string]bool, error) {
	res, err := engineDB.Query(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND (sql IS NULL OR sql NOT LIKE '%__flatsql_module_%')`)
	if err != nil {
		return nil, err
	}
	plain := make(map[string]bool, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 1 {
			continue
		}
		if name, ok := row[0].(string); ok {
			plain[name] = true
		}
	}
	excluded := map[string]bool{}
	for name, binding := range engineRoutedSchemas {
		if _, decorated := engineDecoratedSchemas[name]; decorated {
			continue
		}
		if plain[binding.Table] {
			excluded[name] = true
		}
	}
	return excluded, nil
}

// engineSchemaTextExcluding removes the excluded standards' table blocks from
// the engine schema text. A table that is absent from the schema is never
// given a unified view, so the plain table of the same name survives untouched
// — which is the entire point.
func engineSchemaTextExcluding(excluded map[string]bool) string {
	if len(excluded) == 0 {
		return engineDatabaseSchema
	}
	drop := make(map[string]bool, len(excluded))
	for name := range excluded {
		if binding, ok := engineRoutedSchemas[name]; ok {
			drop[binding.Table] = true
		}
	}
	var out strings.Builder
	out.Grow(len(engineDatabaseSchema))
	skipping := false
	for _, line := range strings.Split(engineDatabaseSchema, "\n") {
		trimmed := strings.TrimSpace(line)
		if skipping {
			if trimmed == "}" {
				skipping = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "table ") && strings.HasSuffix(trimmed, "{") {
			name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "table "), "{"))
			if drop[name] {
				skipping = true
				continue
			}
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// engineRoutedSchemaFor is engineRoutedSchemaFor narrowed to THIS store's
// routed set (the catalog minus the store's exclusions).
func (s *FlatSQLStore) engineRoutedSchemaFor(schemaName string) (engineRoutedSchema, bool) {
	binding, ok := engineRoutedSchemaFor(schemaName)
	if !ok || s.engineExcluded[normalizeSchemaNameForEpoch(schemaName)] {
		return engineRoutedSchema{}, false
	}
	return binding, true
}

// engineRoutesSchema reports whether THIS store mirrors the schema's records
// into the engine.
func (s *FlatSQLStore) engineRoutesSchema(schemaName string) bool {
	_, ok := s.engineRoutedSchemaFor(schemaName)
	return ok
}

// engineOwnsTableName reports whether a SQL table name is reserved by one of
// THIS store's engine record vtabs. An excluded standard does not own its
// name here — that is precisely why it was excluded — so the legacy migration
// paths keep treating it as an ordinary control table.
func (s *FlatSQLStore) engineOwnsTableName(tableName string) bool {
	if !engineOwnsTableName(tableName) {
		return false
	}
	for name := range s.engineExcluded {
		if binding, ok := engineRoutedSchemas[name]; ok && binding.Table == tableName {
			return false
		}
	}
	return true
}

// engineRoutedSchemaNames lists THIS store's routed schema names in a stable
// order.
func (s *FlatSQLStore) engineRoutedSchemaNames() []string {
	all := engineRoutedSchemaNames()
	if len(s.engineExcluded) == 0 {
		return all
	}
	names := make([]string, 0, len(all))
	for _, name := range all {
		if !s.engineExcluded[name] {
			names = append(names, name)
		}
	}
	return names
}

// engineWindowFor is the hot-window budget for one schema: the full
// per-schema window for the two DECORATED standards (which are read at
// provider scale), the smaller generic window for everything else. Without
// the split, 227 schemas x engine_hot_window is not a bound on anything.
func (s *FlatSQLStore) engineWindowFor(schemaName string) int {
	if _, decorated := engineDecoratedSchemas[normalizeSchemaNameForEpoch(schemaName)]; decorated {
		return s.engineHotWindow
	}
	if s.engineGenericHotWindow > 0 && s.engineGenericHotWindow < s.engineHotWindow {
		return s.engineGenericHotWindow
	}
	return s.engineHotWindow
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
// The identifier check is over EVERY routed standard: a $TBS frame arriving
// with its prefix intact must be stripped exactly like an $OMM one, otherwise
// the engine reads the length word as the root offset and drops the record.
func engineRecordPayload(data []byte) []byte {
	if len(data) >= 12 &&
		int64(binary.LittleEndian.Uint32(data[:4]))+4 == int64(len(data)) &&
		engineRoutedFileID(string(data[8:12])) {
		return data[4:]
	}
	return data
}

// engineRoutedFileID reports whether a 4-byte FlatBuffer file identifier
// belongs to a routed standard.
func engineRoutedFileID(fileID string) bool {
	return engineRoutedFileIDs[fileID]
}

// rebuildUnifiedViews replaces every routed base name with a UNION ALL view
// over its per-source shadow tables, IN ONE TRANSACTION.
//
// THE TRANSACTION IS THE WHOLE POINT, and it is worth 100x. CreateUnifiedViews
// is all-or-nothing across the schema: DROP TABLE + DROP VIEW + CREATE VIEW for
// every routed table, plus a CREATE VIRTUAL TABLE per shadow. SQLite re-reads
// the ENTIRE schema after each schema-changing statement, and the disk-backed
// engine writes a rollback journal for each one, so the cost is quadratic in
// the number of routed standards. Measured on the disk-backed engine with 227
// standards and one source: 10.1 s un-batched, 0.086 s inside one transaction.
// It is also strictly safer — a crash mid-rebuild now rolls back instead of
// leaving half the record surface dropped.
//
// The BEGIN is best-effort: if the engine is already inside a transaction the
// rebuild simply runs un-batched, exactly as it always did. Callers hold s.mu.
func (s *FlatSQLStore) rebuildUnifiedViews() error {
	s.engineViewRebuilds++
	batched := false
	if _, err := s.engineDB.Query("BEGIN"); err == nil {
		batched = true
	}
	if err := s.engineDB.CreateUnifiedViews(); err != nil {
		if batched {
			if _, rbErr := s.engineDB.Query("ROLLBACK"); rbErr != nil {
				log.Warnf("FlatSQL engine: roll back failed unified-view rebuild: %v", rbErr)
			}
		}
		return err
	}
	if batched {
		if _, err := s.engineDB.Query("COMMIT"); err != nil {
			return fmt.Errorf("commit unified-view rebuild: %w", err)
		}
	}
	return nil
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
	if err := s.rebuildUnifiedViews(); err != nil {
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
	binding, routed := s.engineRoutedSchemaFor(schemaName)
	window := s.engineWindowFor(schemaName)
	if !routed || window <= 0 {
		return nil
	}
	overflow := s.engineResident[schemaName] - int64(window)
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
		`SELECT _source, _rowid FROM "`+binding.Table+`" ORDER BY _rowid ASC LIMIT ?1`, overflow)
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
		// source carries the FULL shadow-table name ("OMM@catalogfixture-gp",
		// "TBS@cell-tower-bulk") as
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
			evicted, schemaName, window)
	}
	return nil
}

// rebuildEngineRecords reloads the engine vtab hot window from durable state
// at boot, once per ROUTED schema: the newest engineHotWindow records of that
// schema (by global index rowid), replayed in ascending rowid order from the
// stream files. A no-op on empty stores. Never fails the open for per-record
// problems — only a poisoned runtime is fatal. Called from NewFlatSQLStore
// before the store is shared, so no locking is needed.
//
// The window is PER SCHEMA: a store that has ingested millions of $OMM must
// still come back with its $TBS sites resident, so the two never share (and
// never evict each other out of) one budget.
func (s *FlatSQLStore) rebuildEngineRecords() error {
	s.preregisterEngineSources()
	present, err := s.schemasWithIndexedRecords()
	if err != nil {
		log.Warnf("FlatSQL engine rebuild: enumerate indexed schemas: %v", err)
		present = nil
	}
	for _, schemaName := range s.engineRoutedSchemaNames() {
		// ONE query answers "which schemas have any records at all", so a
		// store that has never seen 220 of the 227 routed standards does not
		// pay 220 hot-window queries at every boot. A nil map means the
		// enumeration itself failed; fall back to probing each schema, which
		// is what this always did.
		if present != nil && !present[schemaName] {
			s.engineResident[schemaName] = 0
			continue
		}
		if err := s.rebuildEngineRecordsForSchema(schemaName); err != nil {
			return err
		}
	}
	return nil
}

// preregisterEngineSources registers every source the derived summary cache
// knows about, in ONE batch, before the hot-window rebuild starts feeding
// records in.
//
// WHY IT MATTERS NOW AND DID NOT BEFORE. ensureEngineSource rebuilds ALL
// unified views whenever it meets a source for the first time, and
// CreateUnifiedViews is all-or-nothing across the schema. With two routed
// standards a cold boot paid 2 x sources view rebuilds; with every embedded
// standard routed it would pay 227 x sources DROP/DROP/CREATE statements —
// O(tables x sources) write DDL on the path that is already the slow one.
// Registering the known set up front collapses that to a single rebuild.
//
// Best-effort by construction: a source this misses is simply registered the
// old way when its first record arrives. The summary cache is rebuilt
// immediately before this runs (RebuildDerivedState), and it holds ONE row per
// (schema, provider, source, batch) lane, so the scan is tens of rows — never
// the multi-million-row source-tag table.
func (s *FlatSQLStore) preregisterEngineSources() {
	rows, err := s.db.Query(`SELECT DISTINCT source_name FROM sdn_record_source_summary`)
	if err != nil {
		return
	}
	var sources []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return
		}
		if name = strings.TrimSpace(name); name != "" && !s.engineSources[name] {
			sources = append(sources, name)
		}
	}
	rows.Close()
	if rows.Err() != nil || len(sources) == 0 {
		return
	}
	sort.Strings(sources)
	registered := 0
	for _, source := range sources {
		if err := s.engineDB.RegisterSource(source); err != nil {
			log.Warnf("FlatSQL engine rebuild: pre-register source %q: %v", source, err)
			if s.engine.Poisoned() {
				return
			}
			continue
		}
		s.engineSources[source] = true
		registered++
	}
	if registered == 0 {
		return
	}
	if err := s.rebuildUnifiedViews(); err != nil {
		log.Warnf("FlatSQL engine rebuild: rebuild unified views after pre-registering %d source(s): %v", registered, err)
		return
	}
	log.Infof("FlatSQL engine rebuild: pre-registered %d engine source(s) in one view rebuild", registered)
}

// schemasWithIndexedRecords lists the schema names that have at least one row
// in the global record index. schema_name leads idx_sdn_record_index_lookup,
// so this is an index scan over distinct values, not a table scan.
func (s *FlatSQLStore) schemasWithIndexedRecords() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT schema_name FROM sdn_record_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		present[name] = true
	}
	return present, rows.Err()
}

func (s *FlatSQLStore) rebuildEngineRecordsForSchema(schemaName string) error {
	readSource, err := s.recordReadSource(schemaName)
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
	`, readSource), schemaName, s.engineWindowFor(schemaName))
	if err != nil {
		log.Warnf("FlatSQL engine rebuild: query %s hot window: %v", schemaName, err)
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
	s.engineResident[schemaName] = int64(rebuilt)
	if rebuilt > 0 || skipped > 0 {
		log.Infof("FlatSQL engine rebuild: loaded %d %s records into the hot window (%d skipped, window %d)", rebuilt, schemaName, skipped, s.engineWindowFor(schemaName))
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
// "catalogfixture-gp"); empty means all sources. limit <= 0 means no limit.
func (s *FlatSQLStore) QueryEpochRawStream(schemaName string, sourceName string, profile string, epochUnix float64, limit int) (*flatsqlrt.RawStream, error) {
	// Epoch profiles are $OMM-specific by construction, not by omission: the
	// three SQL shapes partition on NORAD_CAT_ID and order on
	// USER_DEFINED_EPOCH_TIMESTAMP, neither of which exists on any other
	// routed standard. A routed-but-non-OMM schema is refused here rather
	// than silently answered from the wrong columns.
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
		// name ("OMM@catalogfixture-gp"), not the bare source.
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
	binding, routed := s.engineRoutedSchemaFor(schemaName)
	if !routed {
		return 0, fmt.Errorf("%q is not routed into this store's engine", schemaName)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engineDB == nil {
		return 0, errors.New("store is closed")
	}
	res, err := s.engineDB.Query(`SELECT COUNT(*) FROM "` + binding.Table + `"`)
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
