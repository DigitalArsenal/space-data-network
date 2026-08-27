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
// stated about its IDL (no file_identifier; an `(encrypted)` field — see
// below) or about the store it is opening (its code is already a plain control
// table there). 228 embedded standards, 226 routed.
//
// SCHEMA-SPECIFIC DECORATIONS STAY OPT-IN AND ARE NEVER THE GATE. $OMM keeps
// the engine-native epoch profiles (QueryEpochRawStream refuses any other
// schema by construction) and $TBS keeps the lat/lon R-Tree the engine derives
// from its column names — both sit ON TOP of the generic route.
//
// FIELD-ENCRYPTED STANDARDS ARE THE ONE THING THE DEFAULT DOES NOT SWALLOW.
// A standard whose IDL declares an `(encrypted)` field is SEALED AT REST
// (internal/encfield, field_encryption.go): the stream file holds an envelope
// and the plaintext lives only in the caller's buffer — which is exactly the
// buffer this file mirrors into the engine, and the engine is the PUBLIC
// /api/v1/query surface where `SELECT _data` returns the whole record. Routing
// such a standard would therefore publish the plaintext the seal exists to
// protect. It is refused in THREE places, and they are not interchangeable:
//
//   - The generated catalog never emits a table OR a file id for it
//     (enginecatalog.SkipEncryptedField). This is the refusal that removes the
//     NAME from SQL: `SELECT ... FROM KMF` answers "no such table" because the
//     schema text never declared the table, so CreateUnifiedViews — which
//     builds a view for every table the schema DOES declare, whatever file ids
//     are registered — has nothing to build. Pinned by
//     TestFieldEncryptedStandardsAreNeverEngineRouted.
//   - buildEngineRoutedSchemas drops anything the catalog declares unroutable.
//   - Every routing decision re-asks the live encfield registry
//     (engineSchemaSealsFields), so a schema registered in Go without a
//     catalog regeneration still gets no plaintext mirrored into the engine.
//
// The last two stop DATA from entering; only the first also stops the name
// from resolving, and this file no longer claims otherwise. FAIL CLOSED: an
// unrecognised standard is not routed rather than routed-and-hoped-about.
//
// The engine vtab is a pure cache: control rows + stream files remain the
// source of truth, so ingest failures are logged and skipped — only a trapped
// (poisoned) runtime is fatal.
//
// DELETE IS NOT MIRRORED. Store.Delete removes the control rows and the index
// entry; it does NOT tombstone the engine row, so a deleted record keeps
// answering from the sandboxed query surface until the next boot rebuild
// (which reads sdn_record_index and therefore cannot see it). Residency is not
// decremented either, so s.engineResident drifts HIGH after deletes. See
// FlatSQLStore.Delete for the full statement of the semantics.

//go:generate go run ./gen -schemas ../sds/schemas -out engine_standard_catalog.go

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
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
	// sized for a catalog-scale feed; multiplied across 226 standards it is
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
	// A DECLARED EXCLUSION WINS OVER EVERY OTHER SOURCE. engineUnroutableSchemas
	// is generated from the IDLs alongside the bindings, so the two agree by
	// construction — but the decorated set above is hand-written, and a
	// hand-edited or half-regenerated catalog that listed a standard in both
	// maps would otherwise route the very standard the generator refused. The
	// declaration is the answer; the routing table follows it.
	for name := range engineUnroutableSchemas {
		delete(routed, name)
	}
	return routed
}

// engineSchemaSealsFields reports whether a standard's records are FIELD
// SEALED at rest (encfield.RegisterSchema, keyed by the table name exactly as
// field_encryption.go registers it). Asked at every routing decision rather
// than folded into engineRoutedSchemas because the registry is populated by
// package init() and package-level VARS are initialized BEFORE init() runs: a
// var-time answer would be "no encrypted schemas" forever. A runtime lookup is
// one RWMutex read over a map the write path already consults per record
// (sealRecordFields).
func engineSchemaSealsFields(binding engineRoutedSchema) bool {
	return encfield.HasEncryptedFields(binding.Table)
}

// engineRoutedTableNames / engineRoutedFileIDs are set lookups over the same
// map. They exist because their callers run PER RECORD (engineRecordPayload on
// every stored buffer) and per table name: a linear scan was free over two
// entries and is 226 string compares per record now.
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

// engineRoutedSchemaFor resolves a schema name to its engine binding. The name
// may be written with or without the ".fbs" suffix — normalizeSchemaNameForEpoch
// appends it — but the CODE ITSELF IS CASE-SENSITIVE, exactly like every SDS
// standard code and every stored schema_name ("irm" is not $IRM here).
//
// A standard whose records are sealed at rest is refused even if it somehow
// appears in the table (engineSchemaSealsFields — see this file's header).
func engineRoutedSchemaFor(schemaName string) (engineRoutedSchema, bool) {
	binding, ok := engineRoutedSchemas[normalizeSchemaNameForEpoch(schemaName)]
	if !ok || engineSchemaSealsFields(binding) {
		return engineRoutedSchema{}, false
	}
	return binding, true
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
	for name, binding := range engineRoutedSchemas {
		if engineSchemaSealsFields(binding) {
			// NEVER REGISTER ITS FILE ID. registerEngineFileIDs reads this
			// list, so no sealed record can ever be mirrored into a table:
			// the engine places a buffer by its file identifier, and an
			// unregistered identifier has nowhere to go. That is the
			// property this line owns. It is NOT what makes the name
			// disappear from SQL — CreateUnifiedViews creates a view for
			// every table the schema TEXT declares, registered or not — and
			// the seal does not lean on it for that: the generated catalog
			// never writes a `table` for a sealed standard in the first
			// place (see the file header).
			continue
		}
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
// probeControlDatabase). Registering an excluded standard would be worse
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
	if !engineRoutedTableNames[tableName] {
		return false
	}
	// The name-set vars are built at package-var time, which is BEFORE the
	// encfield registrations run (see engineSchemaSealsFields), so the sealed
	// check happens here instead. It keeps every package-level accessor
	// agreeing: a standard that is not routed does not own its table name
	// either, and its plain control table is an ordinary control table.
	return !encfield.HasEncryptedFields(tableName)
}

// ==================== Per-store routing (exclusions + windows) ====================

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

// ==================== Declared columns (the listing is free) ====================

// engineRelationMetaColumns are the columns the ENGINE appends to EVERY record
// relation — base vtab, per-source shadow vtab and unified view alike — after
// the standard's projected fields, in this order. `_source` is a real vtab
// column (not something the view synthesizes), which is why a base table on a
// store with no source partitions declares it too.
var engineRelationMetaColumns = []string{"_source", "_rowid", "_offset", "_data"}

// engineDeclaredColumns maps each engine table name to the fields the schema
// text declares for it, in declaration order — which IS the column order,
// because FlatBuffers vtable slots are positional (see enginecatalog).
var engineDeclaredColumns = parseEngineDeclaredColumns(engineDatabaseSchema)

// parseEngineDeclaredColumns reads the engine schema text the database is
// created from. It is the same line shape engineSchemaTextExcluding walks:
// `table X {`, one `NAME:type;` per line, `}`.
func parseEngineDeclaredColumns(schema string) map[string][]string {
	out := make(map[string][]string, 256)
	table := ""
	var cols []string
	for _, line := range strings.Split(schema, "\n") {
		trimmed := strings.TrimSpace(line)
		if table == "" {
			if strings.HasPrefix(trimmed, "table ") && strings.HasSuffix(trimmed, "{") {
				table = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "table "), "{"))
				cols = nil
			}
			continue
		}
		if trimmed == "}" {
			out[table] = cols
			table, cols = "", nil
			continue
		}
		name, _, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		if name = strings.TrimSpace(name); name == "" || strings.HasPrefix(name, "//") {
			continue
		}
		cols = append(cols, name)
	}
	return out
}

// engineRelationColumns returns what `SELECT *` yields for a routed standard's
// relation — the declared fields followed by the engine meta columns.
//
// IT IS DERIVED, NOT PROBED, AND THAT IS THE POINT. The public query surface
// lists EVERY routed standard, so probing columns cost one prepared statement
// per standard against a UNION ALL view with one branch per source: 226
// statements holding the single-threaded engine (and s.mu) for an
// UNAUTHENTICATED listing — measured at ~76-99 ms per call versus 0.4 ms for
// the `SELECT _data ... LIMIT 10` it is supposed to describe, which is exactly
// the "asking WHAT is queryable costs more than querying it" shape
// PublicQuerySurface's own doc rules out. The schema text the database was
// created from already says what the columns are.
//
// The derivation stays honest because a persisted view that projects anything
// else is REBUILT at boot: engineViewsCoverSources compares each persisted
// view's projection against this list, so an upgraded binary whose catalog
// changed a standard's columns never serves the old view's column list (and
// TestSurfaceColumnsAreTheEngineColumns pins derived == engine for every
// routed standard, base relation and shadow partition alike).
func engineRelationColumns(table string) ([]string, bool) {
	declared, ok := engineDeclaredColumns[table]
	if !ok {
		return nil, false
	}
	cols := make([]string, 0, len(declared)+len(engineRelationMetaColumns))
	cols = append(cols, declared...)
	cols = append(cols, engineRelationMetaColumns...)
	return cols, true
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
// the split, 226 schemas x engine_hot_window is not a bound on anything.
func (s *FlatSQLStore) engineWindowFor(schemaName string) int {
	if _, decorated := engineDecoratedSchemas[normalizeSchemaNameForEpoch(schemaName)]; decorated {
		return s.engineHotWindow
	}
	if s.engineGenericHotWindow > 0 {
		// HONOURED EVEN WHEN IT EXCEEDS THE DECORATED WINDOW. This used to
		// require `< s.engineHotWindow`, which silently handed an operator who
		// RAISED storage.engine_generic_hot_window the decorated window
		// instead — a config key that quietly means something else for half
		// its range is worse than one that costs memory. NewFlatSQLStore warns
		// at open when it is the larger of the two, because that is where the
		// memory implication belongs (every routed standard shares one 4 GiB
		// engine).
		return s.engineGenericHotWindow
	}
	return s.engineHotWindow
}

// engineSchemaNameAliases lists every spelling of a schema name that a WRITER
// may have persisted for it.
//
// The store writes sdn_record_index.schema_name and the record-catalog journal
// frame VERBATIM, in whatever spelling the caller passed, while every engine
// route is keyed by the canonical ".fbs" name. Both spellings are in
// production use — caps/storage.go passes the module's `schema` string through
// untouched, and the modules' provider_source.hpp sets record_schema to the
// bare code ("OMM", "OEM") — so a rebuild that matched only the canonical
// spelling brought back ZERO rows for every record written the bare way. That
// is the exact failure the routing flip exists to eliminate, and it is a READ
// mismatch: nothing needs migrating, the boot rebuild just has to ask for both
// names.
func engineSchemaNameAliases(schemaName string) []string {
	canonical := normalizeSchemaNameForEpoch(schemaName)
	bare := strings.TrimSuffix(canonical, ".fbs")
	if bare == "" || bare == canonical {
		return []string{canonical}
	}
	return []string{canonical, bare}
}

// ==================== Engine residency bookkeeping ====================
//
// engineResident is keyed by the CANONICAL schema name, always. The write path
// is reached with the caller's spelling ("IRM") and the read paths — above all
// PublicQuerySurface — look it up by the routed name ("IRM.fbs"), so an
// unnormalized map reported Records: 0 and omitted every `<CODE>@<source>`
// partition for any standard written the bare way, while the rows were
// provably readable from the view. Every touch goes through these three
// accessors so the two spellings can never disagree again. Caller holds s.mu.

func (s *FlatSQLStore) engineResidentCount(schemaName string) int64 {
	return s.engineResident[normalizeSchemaNameForEpoch(schemaName)]
}

func (s *FlatSQLStore) engineResidentSet(schemaName string, n int64) {
	s.engineResident[normalizeSchemaNameForEpoch(schemaName)] = n
}

func (s *FlatSQLStore) engineResidentAdd(schemaName string, delta int64) {
	s.engineResident[normalizeSchemaNameForEpoch(schemaName)] += delta
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

// engineIngestablePayload returns the bytes to hand the engine for one record
// of binding's standard, or a stated reason why those bytes CANNOT ingest into
// that standard's table.
//
// IT EXISTS BECAUSE A REFUSED INGEST LOOKED LIKE A SUCCESSFUL ONE. The engine
// routes a buffer by the four identifier bytes at offset 4 and drops anything
// it cannot place — WITHOUT returning an error (flatsql_ingest_one_with_source
// answers a non-negative count either way). Both callers then counted the
// record as resident, so a store could report N records in a table holding
// zero: live-readable, then empty after a restart, which is the exact failure
// the routing flip exists to eliminate. Checking the identifier the engine is
// about to route on makes the count mean what it says, and turns a silent drop
// into a logged skip.
//
// The sealed-frame check is the LAST of the three refusals of a
// field-encrypted standard (see this file's header): whatever the routing
// table says, an encfield envelope — which is not a FlatBuffer at all, it
// starts "SDF1" — never reaches the engine.
func engineIngestablePayload(binding engineRoutedSchema, data []byte) ([]byte, string, bool) {
	if encfield.IsSealed(data) {
		return nil, "record is a field-encryption envelope, not a FlatBuffer", false
	}
	payload := engineRecordPayload(data)
	if len(payload) < 8 {
		return nil, fmt.Sprintf("record is %d bytes, too short to carry a file identifier", len(payload)), false
	}
	if id := string(payload[4:8]); id != binding.FileID {
		return nil, fmt.Sprintf("file identifier %q does not route to %q (want %q)", id, binding.Table, binding.FileID), false
	}
	return payload, "", true
}

// dropExcludedStandardViews removes the unified views a PREVIOUS boot left
// behind for standards THIS boot does not route.
//
// A unified view is a PERSISTED schema object. Excluding a standard takes its
// table out of the schema and stops its vtab module from ever being
// registered, but the `CREATE VIEW "<CODE>" AS SELECT ... FROM "<CODE>@src"`
// an earlier boot wrote survives in sqlite_master. Every plain-table path then
// resolves that leftover view instead of the canonical control table and
// answers `no such module: __flatsql_module_<code>_<src>`.
//
// That is not cosmetic: it is how the legacy blob migration — whose entire
// purpose is to CREATE the canonical table for an excluded standard — fails
// the open outright, and it is what a boot that fell back to the decorated
// standards after an unreadable probe would leave behind on all 224 others.
// Dropping the leftovers is what makes an exclusion MEAN "this standard is an
// ordinary control table in this store", which is exactly what it claims.
//
// The per-source shadow vtabs are deliberately left alone: DROP TABLE on a
// virtual table needs its module registered (which is precisely what an
// excluded standard does not have), and they are already invisible to every
// plain-table path (tableExists and schemasWithRecordTables both filter
// `CREATE VIRTUAL%`). A later boot that re-routes the standard adopts them.
//
// A PER-STORE EXCLUSION IS NOT THE ONLY WAY A STANDARD STOPS BEING ROUTED. A
// standard can LEAVE THE CATALOG ENTIRELY between binaries — that is what
// happened to $KMF when it gained an `(encrypted)` field, and it is what any
// SDS bump does when an IDL loses its file_identifier or gains a sealed field.
// Such a name is not in engineRoutedSchemas at all, so the per-store exclusion
// set never mentions it and a leftover view for it would survive with no vtab
// module behind it — the exact failure this function exists to prevent, one
// input class short. The sweep is therefore over the PERSISTED VIEWS: any view
// this binary does not route is dropped, identified by its own definition
// (`... FROM "<NAME>@<source>"`, the shape CreateUnifiedViews writes) so a view
// an operator or a migration created for some other purpose is never touched.
func dropExcludedStandardViews(engineDB *flatsqlrt.Database, excluded map[string]bool) error {
	routedTables := make(map[string]bool, len(engineRoutedSchemas))
	for name, binding := range engineRoutedSchemas {
		if excluded[name] {
			continue
		}
		routedTables[binding.Table] = true
	}
	res, err := engineDB.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'view'`)
	if err != nil {
		return fmt.Errorf("enumerate persisted unified views: %w", err)
	}
	leftover := []string{}
	for _, row := range res.Rows {
		if len(row) != 2 {
			continue
		}
		name, ok := row[0].(string)
		if !ok || routedTables[name] {
			continue
		}
		text, _ := row[1].(string)
		if !isEngineUnifiedViewText(name, text) {
			continue
		}
		leftover = append(leftover, name)
	}
	if len(leftover) == 0 {
		return nil
	}
	sort.Strings(leftover)
	// ONE TRANSACTION, for the same reason rebuildUnifiedViews uses one:
	// SQLite re-reads the whole schema after every schema-changing statement,
	// and the fail-closed path can have 224 of them.
	batched := false
	if _, err := engineDB.Query("BEGIN"); err == nil {
		batched = true
	}
	for _, name := range leftover {
		if _, err := engineDB.Query(`DROP VIEW IF EXISTS ` + quoteEngineRelation(name)); err != nil {
			if batched {
				if _, rbErr := engineDB.Query("ROLLBACK"); rbErr != nil {
					log.Warnf("FlatSQL boot: roll back leftover view cleanup: %v", rbErr)
				}
			}
			return fmt.Errorf("drop leftover unified view %q: %w", name, err)
		}
	}
	if batched {
		if _, err := engineDB.Query("COMMIT"); err != nil {
			return fmt.Errorf("commit leftover unified-view cleanup: %w", err)
		}
	}
	log.Warnf("FlatSQL boot: dropped %d leftover unified view(s) for standard(s) this store does not route: %v", len(leftover), leftover)
	return nil
}

// isEngineUnifiedViewText reports whether a persisted view is one the engine's
// CreateUnifiedViews wrote for the record table `name`: its definition selects
// from that name's per-source shadow partitions. A view with any other
// definition belongs to somebody else and is left alone.
func isEngineUnifiedViewText(name, text string) bool {
	return strings.Contains(text, `FROM "`+strings.ReplaceAll(name, `"`, `""`)+`@`)
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
// standards and one source (the catalog before $KMF was taken off the engine —
// the number is the measurement's, not today's count): 10.1 s un-batched,
// 0.086 s inside one transaction.
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
	binding, routed := s.engineRoutedSchemaFor(schemaName)
	if !routed {
		// Callers gate on engineRoutesSchema before queueing, so this is the
		// fail-closed backstop for a schema that stopped being routed between
		// the queue and the flush (an exclusion, or a field-encrypted standard).
		if len(pending) > 0 {
			log.Warnf("FlatSQL engine: %d %s record(s) queued for a schema this store does not route — not mirrored", len(pending), schemaName)
		}
		return nil
	}
	// Project once: an unplaceable payload is dropped here so neither the
	// batched nor the per-record path has to re-derive it.
	payloads := make([][]byte, 0, len(pending))
	sources := make([]string, 0, len(pending))
	for _, p := range pending {
		payload, reason, ok := engineIngestablePayload(binding, p.data)
		if !ok {
			log.Warnf("FlatSQL engine: skip %s record for %q: %s", schemaName, p.source, reason)
			continue
		}
		payloads = append(payloads, payload)
		sources = append(sources, p.source)
	}

	// Sources are registered BEFORE any transaction opens: RegisterSource is
	// DDL plus a unified-view rebuild.
	unusable, err := s.ensureEngineSourcesFor(schemaName, sources)
	if err != nil {
		return err
	}
	if len(unusable) > 0 {
		keptPayloads := payloads[:0]
		keptSources := sources[:0]
		for i := range payloads {
			if unusable[sources[i]] {
				continue
			}
			keptPayloads = append(keptPayloads, payloads[i])
			keptSources = append(keptSources, sources[i])
		}
		payloads, sources = keptPayloads, keptSources
	}

	// FAST PATH: one transaction, one guest call per source run. Measured at
	// 65,414 rows/s against 47 rows/s for the per-record path below — the cost
	// this removes is an implicit transaction (journal write + fsync) per
	// record, not the call count. See engine_bulk_ingest.go.
	ingested := int64(0)
	if n, ok := s.bulkIngestEngineGroups(groupEngineIngests(sources, payloads)); ok {
		ingested = n
	} else {
		// FALLBACK: the exact per-record path the batched one replaced. It
		// runs when the batch is too small to be worth a transaction, when
		// the transaction could not be opened, or when it was rolled back.
		for i, payload := range payloads {
			if _, err := s.engineDB.IngestOneWithSource(payload, sources[i]); err != nil {
				log.Warnf("FlatSQL engine: ingest %s record into %q: %v", schemaName, sources[i], err)
				if s.engine.Poisoned() {
					return fmt.Errorf("FlatSQL engine poisoned during %s ingest: %w", schemaName, err)
				}
				continue
			}
			ingested++
		}
	}
	s.engineResidentAdd(schemaName, ingested)
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
	overflow := s.engineResidentCount(schemaName) - int64(window)
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
	s.engineResidentAdd(schemaName, -evicted)
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
	present, err := s.schemasWithRecordTables()
	if err != nil {
		log.Warnf("FlatSQL engine rebuild: enumerate schemas with record tables: %v", err)
		present = nil
	}
	for _, schemaName := range s.engineRoutedSchemaNames() {
		// ONE schema-catalog query answers "which schemas could have a record
		// at all", so a store that has never seen 219 of the 226 routed
		// standards does not pay 219 hot-window queries at every boot. A nil
		// map means the enumeration itself failed; fall back to probing each
		// schema, which is what this always did.
		if present != nil && !present[schemaName] {
			s.engineResidentSet(schemaName, 0)
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
// standard routed it would pay 226 x sources DROP/DROP/CREATE statements —
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

// schemasWithRecordTables lists the routed schema names that HAVE A BACKING
// RECORD TABLE in this control database — the cheap, exact answer to "which
// standards could possibly have a record here".
//
// IT READS THE SCHEMA CATALOG, NOT THE DATA. The obvious formulation,
// `SELECT DISTINCT schema_name FROM sdn_record_index`, is the shape this engine
// has already been measured to be pathological at: rebuildSourceSummaryScope
// (flatsql.go) records `SELECT DISTINCT schema_name` costing 29.7 s of held
// engine lock on host-01, a plan EXPLAIN called a covering-index SEARCH costing
// 28.7 s, and native sqlite3 answering the same statement on the same file in
// 1 ms — the engine's cost tracked DATABASE SIZE, not rows. Per-schema probes
// are no better: the same measurement charged 500-680 ms PER SEEK at 2.8 GB, so
// 226 of them is minutes. sqlite_master is a few thousand rows on the largest
// box in the fleet and does not grow with records at all.
//
// It is EXACT, not approximate: recordReadSourceFiltered unions exactly the
// legacy per-standard table plus the sds_p_<producer>__<STANDARD> tables, so a
// standard with no table in that set has no readable row by construction, and
// the hot-window query for it can only return zero rows. Virtual tables are
// excluded the same way tableExists excludes them — the engine's own record
// vtabs live in this SQLite context under the bare standard name.
func (s *FlatSQLStore) schemasWithRecordTables() (map[string]bool, error) {
	rows, err := s.db.Query(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND COALESCE(sql, '') NOT LIKE 'CREATE VIRTUAL%'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	standards := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if _, standard, ok := parseProducerStandardTable(name); ok {
			standards[standard] = true
			continue
		}
		standards[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, schemaName := range s.engineRoutedSchemaNames() {
		table, err := sds.SchemaNameToTable(schemaName)
		if err != nil {
			continue
		}
		if standards[table] {
			present[schemaName] = true
		}
	}
	return present, nil
}

func (s *FlatSQLStore) rebuildEngineRecordsForSchema(schemaName string) error {
	binding, routed := s.engineRoutedSchemaFor(schemaName)
	if !routed {
		s.engineResidentSet(schemaName, 0)
		return nil
	}
	readSource, err := s.recordReadSource(schemaName)
	if err != nil {
		log.Warnf("FlatSQL engine rebuild: record read source: %v", err)
		return nil
	}

	// BOTH SPELLINGS. sdn_record_index.schema_name is written verbatim in
	// whatever spelling the writer used, and the bare code is the shape the
	// module SDK and the wasm provider sources actually pass
	// (engineSchemaNameAliases). Matching only the canonical ".fbs" name meant
	// a record stored as "IRM" was live-readable and then came back as ZERO
	// rows after a restart.
	aliases := engineSchemaNameAliases(schemaName)
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(aliases)), ", ")
	args := make([]any, 0, len(aliases)+1)
	for _, alias := range aliases {
		args = append(args, alias)
	}
	args = append(args, s.engineWindowFor(schemaName))
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
			WHERE idx.schema_name IN (%s)
			ORDER BY idx.rowid DESC
			LIMIT ?
		) ORDER BY rid ASC
	`, readSource, placeholders), args...)
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
	pendingPayloads := make([][]byte, 0, engineRebuildFlushSize)
	pendingSources := make([]string, 0, engineRebuildFlushSize)
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
		// readStreamRecordCached is the RAW reader: unlike
		// readFlatSQLStreamRecord it does NOT run the field-decryption pass, so
		// a sealed standard's frame arrives here as an encfield ENVELOPE. That
		// envelope cannot ingest, and counting it as resident is what made a
		// restart report rows it did not have.
		payload, reason, ok := engineIngestablePayload(binding, data)
		if !ok {
			log.Warnf("FlatSQL engine rebuild: skip %s record at %s@%d: %s", schemaName, streamPath, streamOffset, reason)
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
		// The rebuild pays the SAME per-record implicit transaction the live
		// mirror did (engine_bulk_ingest.go), and it pays it for the whole hot
		// window on every boot. Accumulate and flush in batches: sources are
		// already registered above, so the flush opens nothing but the
		// transaction.
		pendingPayloads = append(pendingPayloads, payload)
		pendingSources = append(pendingSources, source)
		if len(pendingPayloads) >= engineRebuildFlushSize {
			done, skip := s.flushEngineRebuildBatch(pendingPayloads, pendingSources)
			rebuilt += done
			skipped += skip
			pendingPayloads = pendingPayloads[:0]
			pendingSources = pendingSources[:0]
		}
	}
	if err := rows.Err(); err != nil {
		log.Warnf("FlatSQL engine rebuild: iterate hot window: %v", err)
	}
	if len(pendingPayloads) > 0 {
		done, skip := s.flushEngineRebuildBatch(pendingPayloads, pendingSources)
		rebuilt += done
		skipped += skip
	}
	s.engineResidentSet(schemaName, int64(rebuilt))
	if rebuilt > 0 || skipped > 0 {
		log.Infof("FlatSQL engine rebuild: loaded %d %s records into the hot window (%d skipped, window %d)", rebuilt, schemaName, skipped, s.engineWindowFor(schemaName))
	}
	return nil
}

// readStreamRecordCached is readFlatSQLStreamRecord's RAW sibling with a
// caller-owned open file cache (boot rebuild reads the same append-only stream
// files record by record).
//
// RAW IS THE DIFFERENCE THAT MATTERS. readFlatSQLStreamRecord runs the
// transparent field-level DECRYPTION pass (E1, the mirror of Append's seal);
// this does not. For a sealed standard the bytes it returns are an encfield
// envelope, not a FlatBuffer — which is precisely why every caller passes them
// through engineIngestablePayload instead of straight to the engine.
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
