// Package flatsqlrt hosts the FlatSQL WASI reactor (flatsql-wasi.wasm)
// in-process via WasmEdge and wraps its C ABI in a Go API.
//
// FlatSQL keeps data in native FlatBuffer format and provides SQL access via
// SQLite virtual tables; query results can be returned as aligned,
// size-prefixed FlatBuffer streams (zero-copy on the guest side, one copy out
// of linear memory on the host side). See README.md for ABI conventions and
// artifact provenance.
package flatsqlrt

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"fmt"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// The embedded engine is the NO-EXCEPTIONS WASI build (flatsql_wasi_noeh
// CMake target): identical sources and exports to flatsql-wasi.wasm, but
// C++ throws abort instead of unwinding. This is required server-side
// because WasmEdge's AOT compiler (0.14–0.17) cannot parse wasm-exceptions
// (exnref) modules, and interpreted execution is ~100x too slow for query
// workloads. Byte-parity with the browser artifact is enforced by
// parity_test.go against shared-test-vectors/flatsql-parity.json.
//
//go:embed flatsql-wasi-noeh.wasm
var flatsqlWasm []byte

// EmbeddedWasm returns the embedded flatsql-wasi.wasm bytes (for hashing /
// provenance checks and for hosts that want to instantiate it themselves).
func EmbeddedWasm() []byte { return flatsqlWasm }

const (
	// EngineImportModule is the wasm import-module name linked dependents
	// resolve the engine's exports against — and therefore the name the live
	// engine instance is registered under in its own VM (loop C.7).
	EngineImportModule = "flatsql"

	// DefaultMaxMemoryPages allows the engine to grow to the wasm32 maximum
	// (65536 pages x 64 KiB = 4 GiB). The catalog-scale store needs far more
	// than modulert's 64 MB plugin cap.
	DefaultMaxMemoryPages = 65536

	// cstringReadChunk is the granularity for scanning guest C strings.
	// WasmEdge's GetData validates the entire requested range, so we read in
	// small chunks until the NUL terminator instead of one large window.
	cstringReadChunk = 256

	// cstringMaxLen caps guest C-string reads (defensive bound).
	cstringMaxLen = 64 * 1024 * 1024
)

// Option configures a Runtime.
type Option func(*config)

type config struct {
	maxMemoryPages uint32
	wasmBytes      []byte
	aotCacheDir    string
}

// WithMaxMemoryPages overrides the default 4 GiB linear-memory growth cap.
func WithMaxMemoryPages(pages uint32) Option {
	return func(c *config) { c.maxMemoryPages = pages }
}

// WithWasmBytes hosts alternative engine bytes (e.g. a newer artifact loaded
// from disk) instead of the embedded copy.
func WithWasmBytes(b []byte) Option {
	return func(c *config) { c.wasmBytes = b }
}

// Runtime is one instantiated FlatSQL engine (one WasmEdge VM). It can hold
// multiple databases. All calls are serialized on the underlying module lock:
// the engine is compiled single-threaded (SQLITE_THREADSAFE=0).
type Runtime struct {
	mod      *wasmrt.Module
	aot      bool
	poisoned bool
}

// Poisoned reports whether this runtime has trapped.
//
// The embedded engine is built WITHOUT exception support (see README).
// User-triggerable errors (bad SQL, unknown template, param mismatch,
// duplicate source, bad schema) are non-throwing since the flatsql A.3c
// refactor and do NOT poison the runtime. A trap (wasm-level failure of an
// engine export — a C++ throw on some untouched internal path, OOM,
// unreachable) may leave guest state corrupted: a poisoned runtime must be
// discarded and recreated; continuing to use it can hang or corrupt results.
func (r *Runtime) Poisoned() bool { return r.poisoned }

// MarkPoisoned records an externally observed trap (e.g. a linked flow's
// direct engine dispatch trapping mid-call — the engine state may be
// corrupted exactly like a host-invoked trap). The runtime must be replaced.
func (r *Runtime) MarkPoisoned() { r.poisoned = true }

// WasmModule exposes the underlying wasmrt module so hosts can register the
// LIVE engine instance into dependent VMs (wasmrt.WithLinkedModuleFrom) —
// the loop C.7 direct-linkage wiring. The instance is named
// EngineImportModule.
func (r *Runtime) WasmModule() *wasmrt.Module { return r.mod }

// New instantiates the FlatSQL WASI reactor and runs its _initialize export.
func New(opts ...Option) (*Runtime, error) {
	cfg := &config{maxMemoryPages: DefaultMaxMemoryPages, wasmBytes: flatsqlWasm}
	for _, o := range opts {
		o(cfg)
	}

	runBytes := cfg.wasmBytes
	aot := false
	if cfg.aotCacheDir != "" {
		if compiled, err := ensureAOT(cfg.aotCacheDir, cfg.wasmBytes); err == nil {
			runBytes = compiled
			aot = true
		}
		// On compile failure fall back to interpreting the portable bytes;
		// callers can check Runtime.AOT() and warn.
	}

	mod, err := wasmrt.NewModule(runBytes,
		wasmrt.WithWASI(),
		wasmrt.WithMaxMemoryPages(cfg.maxMemoryPages),
		// The engine is routinely executed from INSIDE another module's host
		// function (storage.flatsql_* capability hostcalls issued by AOT flow
		// mounts). libwasmedge 0.14 corrupts per-thread executor state on
		// nested AOT-inside-AOT execution — the engine therefore always runs
		// on its own locked OS thread (docs/wasmedge-aot-nested-execution.md).
		wasmrt.WithDedicatedThread(),
		// The engine instantiates as the NAMED module "flatsql" so its LIVE
		// instance can be registered into dependent VMs (linked flow
		// artifacts import flatsql.malloc/free/flatsql_* directly — loop C.7,
		// docs/flatsql-component-linkage.md B-iv). Direct linked calls are
		// safe on the CALLER's thread (one contiguous executor invocation,
		// §7.1) but require the module lock (SQLITE_THREADSAFE=0).
		wasmrt.WithRegisteredName(EngineImportModule),
	)
	if err != nil {
		return nil, fmt.Errorf("flatsqlrt: instantiate engine: %w", err)
	}
	if _, err := mod.Execute("_initialize"); err != nil {
		mod.Release()
		return nil, fmt.Errorf("flatsqlrt: _initialize: %w", err)
	}
	return &Runtime{mod: mod, aot: aot}, nil
}

// Close releases the WasmEdge VM and all engine memory. Databases created on
// this runtime become invalid.
func (r *Runtime) Close() {
	if r.mod != nil {
		r.mod.Release()
		r.mod = nil
	}
}

// MemoryStats reports the engine's current/max linear memory.
func (r *Runtime) MemoryStats() (wasmrt.MemoryStats, error) { return r.mod.MemoryStats() }

// lastError reads flatsql_get_error. Must be called with the module lock held.
func (r *Runtime) lastError() string {
	res, err := r.mod.Execute("flatsql_get_error")
	if err != nil || len(res) == 0 {
		return fmt.Sprintf("(flatsql_get_error unavailable: %v)", err)
	}
	ptr := toUint32(res[0])
	if ptr == 0 {
		return "(no engine error message)"
	}
	msg, err := r.readCString(ptr)
	if err != nil {
		return fmt.Sprintf("(error message unreadable: %v)", err)
	}
	return msg
}

// readCString scans a NUL-terminated guest string in bounded chunks, clamped
// to the current linear-memory size. Must be called with the module lock held.
func (r *Runtime) readCString(ptr uint32) (string, error) {
	if ptr == 0 {
		return "", nil
	}
	stats, err := r.mod.MemoryStats()
	if err != nil {
		return "", err
	}
	memEnd := uint32(stats.Bytes)
	var buf []byte
	for off := ptr; off < memEnd && len(buf) < cstringMaxLen; {
		chunk := uint32(cstringReadChunk)
		if off+chunk > memEnd {
			chunk = memEnd - off
		}
		data, err := r.mod.ReadMemory(off, chunk)
		if err != nil {
			return "", err
		}
		if i := bytes.IndexByte(data, 0); i >= 0 {
			return string(append(buf, data[:i]...)), nil
		}
		buf = append(buf, data...)
		off += chunk
	}
	return string(buf), nil
}

// engineErr wraps a benign engine error (error-sentinel return + populated
// error latch). Since the flatsql no-throw refactor (loop A.3c),
// user-triggerable failures — bad SQL, param-count mismatch, unknown
// template, duplicate source, bad schema — take this path without throwing,
// so the runtime stays healthy and is NOT poisoned.
func (r *Runtime) engineErr(op string) error {
	return fmt.Errorf("flatsqlrt: %s: %s", op, r.lastError())
}

// execErr wraps a trap/host-level failure of an engine export call and marks
// the runtime poisoned.
func (r *Runtime) execErr(op string, err error) error {
	r.poisoned = true
	return fmt.Errorf("flatsqlrt: %s (runtime poisoned — recreate it): %w", op, err)
}

// allocCString copies s into guest memory as a NUL-terminated C string.
// Caller must Deallocate. Must be called with the module lock held.
func (r *Runtime) allocCString(s string) (uint32, error) {
	return r.mod.Allocate(append([]byte(s), 0))
}

// allocBytes copies b into guest memory; empty slices pass ptr 0 with len 0
// (matching the JS shim's withBytes convention). Caller must Deallocate
// non-zero pointers.
func (r *Runtime) allocBytes(b []byte) (uint32, error) {
	if len(b) == 0 {
		return 0, nil
	}
	return r.mod.Allocate(b)
}

// BuildQueryCacheKey returns the engine's deterministic cache key for a
// template query invocation (dataset + artifact version + query id + params).
func (r *Runtime) BuildQueryCacheKey(dataset, artifactVersion, queryID string, params ...interface{}) (string, error) {
	r.mod.Lock()
	defer r.mod.Unlock()

	blob, err := EncodeParams(params)
	if err != nil {
		return "", err
	}
	ptrs := make([]uint32, 0, 4)
	free := func() {
		for _, p := range ptrs {
			if p != 0 {
				r.mod.Deallocate(p)
			}
		}
	}
	defer free()
	alloc := func(s string) (uint32, error) {
		p, err := r.allocCString(s)
		if err == nil {
			ptrs = append(ptrs, p)
		}
		return p, err
	}

	dsPtr, err := alloc(dataset)
	if err != nil {
		return "", err
	}
	verPtr, err := alloc(artifactVersion)
	if err != nil {
		return "", err
	}
	idPtr, err := alloc(queryID)
	if err != nil {
		return "", err
	}
	blobPtr, err := r.allocBytes(blob)
	if err != nil {
		return "", err
	}
	if blobPtr != 0 {
		ptrs = append(ptrs, blobPtr)
	}

	res, err := r.mod.Execute("flatsql_build_query_cache_key",
		int32(dsPtr), int32(verPtr), int32(idPtr),
		int32(blobPtr), int32(len(blob)), int32(len(params)))
	if err != nil {
		return "", r.execErr("flatsql_build_query_cache_key", err)
	}
	keyPtr := toUint32(res[0])
	if keyPtr == 0 {
		return "", r.engineErr("build_query_cache_key")
	}
	return r.readCString(keyPtr)
}

// ResponseArtifactKeyOptions mirrors the JS buildResponseArtifactCacheKey
// options (format defaults to "json"; Projection is newline-joined).
type ResponseArtifactKeyOptions struct {
	Format          string
	PublishEventKey string
	Projection      []string
	Params          []interface{}
}

// BuildResponseArtifactCacheKey returns the engine's deterministic cache key
// for a response artifact (the ETag/conditional-GET identity of a query
// result).
func (r *Runtime) BuildResponseArtifactCacheKey(schemaName, schemaVersion, sql string, opts ResponseArtifactKeyOptions) (string, error) {
	r.mod.Lock()
	defer r.mod.Unlock()

	format := opts.Format
	if format == "" {
		format = "json"
	}
	projection := ""
	for i, p := range opts.Projection {
		if i > 0 {
			projection += "\n"
		}
		projection += p
	}
	blob, err := EncodeParams(opts.Params)
	if err != nil {
		return "", err
	}

	ptrs := make([]uint32, 0, 7)
	defer func() {
		for _, p := range ptrs {
			if p != 0 {
				r.mod.Deallocate(p)
			}
		}
	}()
	alloc := func(s string) (uint32, error) {
		p, err := r.allocCString(s)
		if err == nil {
			ptrs = append(ptrs, p)
		}
		return p, err
	}

	schemaPtr, err := alloc(schemaName)
	if err != nil {
		return "", err
	}
	verPtr, err := alloc(schemaVersion)
	if err != nil {
		return "", err
	}
	sqlPtr, err := alloc(sql)
	if err != nil {
		return "", err
	}
	fmtPtr, err := alloc(format)
	if err != nil {
		return "", err
	}
	evtPtr, err := alloc(opts.PublishEventKey)
	if err != nil {
		return "", err
	}
	projPtr, err := alloc(projection)
	if err != nil {
		return "", err
	}
	blobPtr, err := r.allocBytes(blob)
	if err != nil {
		return "", err
	}
	if blobPtr != 0 {
		ptrs = append(ptrs, blobPtr)
	}

	res, err := r.mod.Execute("flatsql_build_response_artifact_cache_key",
		int32(schemaPtr), int32(verPtr), int32(sqlPtr), int32(fmtPtr),
		int32(evtPtr), int32(projPtr),
		int32(blobPtr), int32(len(blob)), int32(len(opts.Params)))
	if err != nil {
		return "", r.execErr("flatsql_build_response_artifact_cache_key", err)
	}
	keyPtr := toUint32(res[0])
	if keyPtr == 0 {
		return "", r.engineErr("build_response_artifact_cache_key")
	}
	return r.readCString(keyPtr)
}

// CreateDatabase parses a FlatBuffers .fbs schema and creates one SQL table
// per schema table. name is a diagnostic label.
func (r *Runtime) CreateDatabase(schema, name string) (*Database, error) {
	r.mod.Lock()
	defer r.mod.Unlock()

	schemaPtr, err := r.allocCString(schema)
	if err != nil {
		return nil, err
	}
	defer r.mod.Deallocate(schemaPtr)
	namePtr, err := r.allocCString(name)
	if err != nil {
		return nil, err
	}
	defer r.mod.Deallocate(namePtr)

	res, err := r.mod.Execute("flatsql_create_db", int32(schemaPtr), int32(namePtr))
	if err != nil {
		return nil, r.execErr("flatsql_create_db", err)
	}
	handle := toUint32(res[0])
	if handle == 0 {
		return nil, r.engineErr("create_db")
	}
	return &Database{rt: r, handle: handle, name: name}, nil
}

// Database is one FlatSQL database (a set of FlatBuffer-backed tables) on a
// Runtime.
type Database struct {
	rt     *Runtime
	handle uint32
	name   string

	// rawMirror holds recently materialized raw streams host-side, keyed by
	// (generation, sql, params) — see rawstream_mirror.go. Lazily created;
	// guarded by the Runtime's module lock like all database state.
	rawMirror *rawStreamMirror

	// engineRefMirror holds bodies harvested from LINKED flows' engine
	// body-references, keyed by (generation, fnv1a64, size) — content
	// identity under the engine's own staleness authority. Same locking as
	// rawMirror.
	engineRefMirror *rawStreamMirror
}

// Name returns the diagnostic label the database was created with.
func (d *Database) Name() string { return d.name }

// Handle exposes the engine-side database handle — the value linked flow
// artifacts receive through sdn_flatsql_link_init and pass to their direct
// flatsql_* calls (loop C.7).
func (d *Database) Handle() uint32 { return d.handle }

// Destroy releases the engine-side database. The handle must not be used
// afterwards.
func (d *Database) Destroy() {
	if d.handle == 0 {
		return
	}
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	d.rt.mod.Execute("flatsql_destroy_db", int32(d.handle))
	d.handle = 0
}

// RegisterFileID maps a 4-byte FlatBuffer file identifier (e.g. "$OMM") to a
// schema table so ingest can route buffers by their embedded identifier.
func (d *Database) RegisterFileID(fileID, tableName string) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	fidPtr, err := d.rt.allocCString(fileID)
	if err != nil {
		return err
	}
	defer d.rt.mod.Deallocate(fidPtr)
	tblPtr, err := d.rt.allocCString(tableName)
	if err != nil {
		return err
	}
	defer d.rt.mod.Deallocate(tblPtr)

	if _, err := d.rt.mod.Execute("flatsql_register_file_id", int32(d.handle), int32(fidPtr), int32(tblPtr)); err != nil {
		return d.rt.execErr("flatsql_register_file_id", err)
	}
	return nil
}

// RegisterSource creates per-source shadow tables (`Base@source`) so ingest
// can be partitioned by provider/source.
func (d *Database) RegisterSource(source string) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	srcPtr, err := d.rt.allocCString(source)
	if err != nil {
		return err
	}
	defer d.rt.mod.Deallocate(srcPtr)

	// flatsql_register_source is a void export: failures (e.g. duplicate
	// source) only land in the error latch, so detect success by the source
	// count changing.
	before, err := d.rt.mod.Execute("flatsql_get_sources_count", int32(d.handle))
	if err != nil {
		return d.rt.execErr("flatsql_get_sources_count", err)
	}
	if _, err := d.rt.mod.Execute("flatsql_register_source", int32(d.handle), int32(srcPtr)); err != nil {
		return d.rt.execErr("flatsql_register_source", err)
	}
	after, err := d.rt.mod.Execute("flatsql_get_sources_count", int32(d.handle))
	if err != nil {
		return d.rt.execErr("flatsql_get_sources_count", err)
	}
	if wasmrt.ToInt32(after[0]) == wasmrt.ToInt32(before[0]) {
		return d.rt.engineErr("register_source")
	}
	return nil
}

// CreateUnifiedViews replaces each base table name with a UNION ALL view over
// its per-source shadow tables, adding a `_source` column.
func (d *Database) CreateUnifiedViews() error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	if _, err := d.rt.mod.Execute("flatsql_create_unified_views", int32(d.handle)); err != nil {
		return d.rt.execErr("flatsql_create_unified_views", err)
	}
	return nil
}

// MarkDeleted tombstones one record (identified by its vtab `_rowid`
// sequence) in a table or per-source shadow table (full name, e.g.
// "OMM@celestrak-gp"). Queries skip tombstoned records immediately; the
// underlying arena bytes are only reclaimed by a rebuild. CAUTION: the table
// name MUST be a registered source/table — an unknown name throws inside the
// engine, which traps (poisons) the no-EH build. Callers should pass names
// obtained from the engine itself (e.g. the unified view's `_source` column).
func (d *Database) MarkDeleted(tableName string, sequence uint64) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	tblPtr, err := d.rt.allocCString(tableName)
	if err != nil {
		return err
	}
	defer d.rt.mod.Deallocate(tblPtr)

	if _, err := d.rt.mod.Execute("flatsql_mark_deleted", int32(d.handle), int32(tblPtr), float64(sequence)); err != nil {
		return d.rt.execErr("flatsql_mark_deleted", err)
	}
	return nil
}

// DeletedCount reports how many records are tombstoned in a table (0 for
// unknown tables — the engine treats that case as empty, not an error).
func (d *Database) DeletedCount(tableName string) (int64, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	tblPtr, err := d.rt.allocCString(tableName)
	if err != nil {
		return 0, err
	}
	defer d.rt.mod.Deallocate(tblPtr)

	res, err := d.rt.mod.Execute("flatsql_get_deleted_count", int32(d.handle), int32(tblPtr))
	if err != nil {
		return 0, d.rt.execErr("flatsql_get_deleted_count", err)
	}
	return int64(toFloat64(res[0])), nil
}

// ClearTombstones forgets a table's tombstone set (used after a compaction
// rebuild re-ingested only the live records).
func (d *Database) ClearTombstones(tableName string) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	tblPtr, err := d.rt.allocCString(tableName)
	if err != nil {
		return err
	}
	defer d.rt.mod.Deallocate(tblPtr)

	if _, err := d.rt.mod.Execute("flatsql_clear_tombstones", int32(d.handle), int32(tblPtr)); err != nil {
		return d.rt.execErr("flatsql_clear_tombstones", err)
	}
	return nil
}

// Ingest appends a size-prefixed FlatBuffer stream. Returns the number of
// bytes consumed from the stream (== len(stream) on full success; the C ABI
// does not expose the per-call record count).
func (d *Database) Ingest(stream []byte) (int, error) {
	return d.ingest("flatsql_ingest", stream, "")
}

// IngestOne appends a single FlatBuffer (no size prefix). Returns the
// engine-assigned record sequence.
func (d *Database) IngestOne(buf []byte) (int, error) {
	return d.ingest("flatsql_ingest_one", buf, "")
}

// IngestWithSource appends a size-prefixed stream into the source's shadow
// tables (bytes consumed returned). The source must have been registered
// with RegisterSource.
func (d *Database) IngestWithSource(stream []byte, source string) (int, error) {
	return d.ingest("flatsql_ingest_with_source", stream, source)
}

// IngestOneWithSource appends one FlatBuffer into the source's shadow tables.
func (d *Database) IngestOneWithSource(buf []byte, source string) (int, error) {
	return d.ingest("flatsql_ingest_one_with_source", buf, source)
}

func (d *Database) ingest(export string, data []byte, source string) (int, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	dataPtr, err := d.rt.allocBytes(data)
	if err != nil {
		return 0, err
	}
	if dataPtr != 0 {
		defer d.rt.mod.Deallocate(dataPtr)
	}

	args := []interface{}{int32(d.handle), int32(dataPtr), int32(len(data))}
	if source != "" {
		srcPtr, err := d.rt.allocCString(source)
		if err != nil {
			return 0, err
		}
		defer d.rt.mod.Deallocate(srcPtr)
		args = append(args, int32(srcPtr))
	}

	res, err := d.rt.mod.Execute(export, args...)
	if err != nil {
		return 0, fmt.Errorf("flatsqlrt: %s: %w", export, err)
	}
	count := toFloat64(res[0])
	if count < 0 {
		return 0, d.rt.engineErr(export)
	}
	return int(count), nil
}

// Result is a materialized row/column query result.
type Result struct {
	Columns []string
	Rows    [][]interface{}
}

// Query runs SQL and materializes rows. Params (optional) bind `?`
// placeholders; supported Go types: nil, bool, all ints, float32/64, string,
// []byte.
func (d *Database) Query(sql string, params ...interface{}) (*Result, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	if err := d.execQuery(sql, params); err != nil {
		return nil, err
	}
	return d.readCurrentResult()
}

// execQuery runs flatsql_query or flatsql_query_params. Lock must be held.
func (d *Database) execQuery(sql string, params []interface{}) error {
	sqlPtr, err := d.rt.allocCString(sql)
	if err != nil {
		return err
	}
	defer d.rt.mod.Deallocate(sqlPtr)

	if len(params) == 0 {
		res, err := d.rt.mod.Execute("flatsql_query", int32(d.handle), int32(sqlPtr))
		if err != nil {
			return d.rt.execErr("flatsql_query", err)
		}
		if wasmrt.ToInt32(res[0]) == 0 {
			return d.rt.engineErr("query")
		}
		return nil
	}

	blob, err := EncodeParams(params)
	if err != nil {
		return err
	}
	blobPtr, err := d.rt.allocBytes(blob)
	if err != nil {
		return err
	}
	if blobPtr != 0 {
		defer d.rt.mod.Deallocate(blobPtr)
	}
	res, err := d.rt.mod.Execute("flatsql_query_params",
		int32(d.handle), int32(sqlPtr), int32(blobPtr), int32(len(blob)), int32(len(params)))
	if err != nil {
		return d.rt.execErr("flatsql_query_params", err)
	}
	if wasmrt.ToInt32(res[0]) == 0 {
		return d.rt.engineErr("query_params")
	}
	return nil
}

// Cell type tags returned by flatsql_result_cell_type.
const (
	cellNull   = 0
	cellBool   = 1
	cellInt32  = 2
	cellInt64  = 3
	cellFloat  = 4
	cellString = 5
	cellBlob   = 6
)

// readCurrentResult materializes the engine's current result. Lock must be held.
func (d *Database) readCurrentResult() (*Result, error) {
	m := d.rt.mod
	colsRes, err := m.Execute("flatsql_result_column_count")
	if err != nil {
		return nil, err
	}
	rowsRes, err := m.Execute("flatsql_result_row_count")
	if err != nil {
		return nil, err
	}
	nCols := int(wasmrt.ToInt32(colsRes[0]))
	nRows := int(wasmrt.ToInt32(rowsRes[0]))

	out := &Result{Columns: make([]string, nCols), Rows: make([][]interface{}, nRows)}
	for c := 0; c < nCols; c++ {
		nameRes, err := m.Execute("flatsql_result_column_name", int32(c))
		if err != nil {
			return nil, err
		}
		name, err := d.rt.readCString(toUint32(nameRes[0]))
		if err != nil {
			return nil, err
		}
		out.Columns[c] = name
	}
	for rIdx := 0; rIdx < nRows; rIdx++ {
		row := make([]interface{}, nCols)
		for c := 0; c < nCols; c++ {
			v, err := d.readCell(rIdx, c)
			if err != nil {
				return nil, err
			}
			row[c] = v
		}
		out.Rows[rIdx] = row
	}
	return out, nil
}

func (d *Database) readCell(row, col int) (interface{}, error) {
	m := d.rt.mod
	tRes, err := m.Execute("flatsql_result_cell_type", int32(row), int32(col))
	if err != nil {
		return nil, err
	}
	switch wasmrt.ToInt32(tRes[0]) {
	case cellNull:
		return nil, nil
	case cellBool:
		v, err := m.Execute("flatsql_result_cell_number", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		return toFloat64(v[0]) != 0, nil
	case cellInt32, cellInt64:
		v, err := m.Execute("flatsql_result_cell_number", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		return int64(toFloat64(v[0])), nil
	case cellFloat:
		v, err := m.Execute("flatsql_result_cell_number", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		return toFloat64(v[0]), nil
	case cellString:
		v, err := m.Execute("flatsql_result_cell_string", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		return d.rt.readCString(toUint32(v[0]))
	case cellBlob:
		ptrRes, err := m.Execute("flatsql_result_cell_blob", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		sizeRes, err := m.Execute("flatsql_result_cell_blob_size", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		size := uint32(wasmrt.ToInt32(sizeRes[0]))
		if size == 0 {
			return []byte{}, nil
		}
		return m.ReadMemory(toUint32(ptrRes[0]), size)
	default:
		return nil, fmt.Errorf("flatsqlrt: unknown cell type at (%d,%d)", row, col)
	}
}

// RegisterQueryTemplate registers a reusable (optionally cached) query.
func (d *Database) RegisterQueryTemplate(queryID, sql string, cacheable bool) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	idPtr, err := d.rt.allocCString(queryID)
	if err != nil {
		return err
	}
	defer d.rt.mod.Deallocate(idPtr)
	sqlPtr, err := d.rt.allocCString(sql)
	if err != nil {
		return err
	}
	defer d.rt.mod.Deallocate(sqlPtr)

	c := int32(0)
	if cacheable {
		c = 1
	}
	res, err := d.rt.mod.Execute("flatsql_register_query_template", int32(d.handle), int32(idPtr), int32(sqlPtr), c)
	if err != nil {
		return d.rt.execErr("flatsql_register_query_template", err)
	}
	if wasmrt.ToInt32(res[0]) == 0 {
		return d.rt.engineErr("register_query_template")
	}
	return nil
}

// QueryTemplate executes a registered template with bound params.
func (d *Database) QueryTemplate(queryID string, params ...interface{}) (*Result, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	idPtr, err := d.rt.allocCString(queryID)
	if err != nil {
		return nil, err
	}
	defer d.rt.mod.Deallocate(idPtr)

	blob, err := EncodeParams(params)
	if err != nil {
		return nil, err
	}
	blobPtr, err := d.rt.allocBytes(blob)
	if err != nil {
		return nil, err
	}
	if blobPtr != 0 {
		defer d.rt.mod.Deallocate(blobPtr)
	}

	res, err := d.rt.mod.Execute("flatsql_query_template",
		int32(d.handle), int32(idPtr), int32(blobPtr), int32(len(blob)), int32(len(params)))
	if err != nil {
		return nil, d.rt.execErr("flatsql_query_template", err)
	}
	if wasmrt.ToInt32(res[0]) == 0 {
		return nil, d.rt.engineErr("query_template")
	}
	return d.readCurrentResult()
}

// QueryMany executes a batch of queries in one guest call and materializes
// each result in order.
func (d *Database) QueryMany(reqs []QueryRequest) ([]*Result, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	blob, err := EncodeQueryRequests(reqs)
	if err != nil {
		return nil, err
	}
	blobPtr, err := d.rt.allocBytes(blob)
	if err != nil {
		return nil, err
	}
	if blobPtr != 0 {
		defer d.rt.mod.Deallocate(blobPtr)
	}

	res, err := d.rt.mod.Execute("flatsql_query_many",
		int32(d.handle), int32(blobPtr), int32(len(blob)), int32(len(reqs)))
	if err != nil {
		return nil, d.rt.execErr("flatsql_query_many", err)
	}
	if wasmrt.ToInt32(res[0]) == 0 {
		return nil, d.rt.engineErr("query_many")
	}

	countRes, err := d.rt.mod.Execute("flatsql_batch_result_count")
	if err != nil {
		return nil, err
	}
	n := int(wasmrt.ToInt32(countRes[0]))
	results := make([]*Result, 0, n)
	for i := 0; i < n; i++ {
		selRes, err := d.rt.mod.Execute("flatsql_select_batch_result", int32(i))
		if err != nil {
			return nil, err
		}
		if wasmrt.ToInt32(selRes[0]) == 0 {
			return nil, d.rt.engineErr(fmt.Sprintf("select_batch_result(%d)", i))
		}
		r, err := d.readCurrentResult()
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

// RawStream is an aligned, size-prefixed FlatBuffer stream query result:
// concatenated [u32le length][bytes] frames, plus the row/column counts the
// engine reported for the artifact.
type RawStream struct {
	Bytes   []byte
	Rows    int
	Columns int
	// CacheHit reports whether the engine served this stream from its
	// response-artifact cache (no SQL re-execution — loop C.5b).
	CacheHit bool
	// MirrorHit reports the stream was served from the HOST-side mirror
	// (zero engine execution, zero memory copies — loop C.5c). Implies the
	// bytes are identical to what the engine would return: the mirror is
	// keyed by the engine's own (generation, sql, params) identity.
	MirrorHit bool
	// FNV1a64 is the word-folded FNV-1a 64 content hash of Bytes (the
	// canonical body-reference / entity-tag identity — FNV1a64WordFolded).
	FNV1a64 uint64
	// FrameCount counts Bytes' size-prefixed frames, skipping zero-length
	// prefixes (the modules' x-sdn-record-count rule); -1 when the framing
	// is malformed.
	FrameCount int
}

// QueryRawFlatBufferStream executes SQL whose every result cell is a BLOB
// (e.g. `SELECT _data FROM OMM WHERE ...`) and returns the aligned
// size-prefixed frame stream — the wire format served to the network.
//
// Repeated queries whose engine generation is unchanged are served from the
// host-side mirror without executing anything in the engine (see
// rawstream_mirror.go). Callers MUST NOT mutate the returned Bytes.
func (d *Database) QueryRawFlatBufferStream(sql string, params ...interface{}) (*RawStream, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	blob, err := EncodeParams(params)
	if err != nil {
		return nil, err
	}

	// Host mirror first: (generation, sql, params) is the engine's own
	// staleness identity — a hit is byte-equivalent to re-reading the
	// engine's cached artifact, minus the round-trip and the copy.
	preGen, genErr := d.queryCacheGenerationLocked()
	mirrorKey := ""
	if genErr == nil {
		mirrorKey = rawMirrorKey(sql, blob, preGen)
		if d.rawMirror != nil {
			if cached := d.rawMirror.get(mirrorKey); cached != nil {
				hit := *cached
				hit.CacheHit = true
				hit.MirrorHit = true
				return &hit, nil
			}
		}
	}

	sqlPtr, err := d.rt.allocCString(sql)
	if err != nil {
		return nil, err
	}
	defer d.rt.mod.Deallocate(sqlPtr)

	blobPtr, err := d.rt.allocBytes(blob)
	if err != nil {
		return nil, err
	}
	if blobPtr != 0 {
		defer d.rt.mod.Deallocate(blobPtr)
	}

	res, err := d.rt.mod.Execute("flatsql_query_raw_flatbuffer_stream",
		int32(d.handle), int32(sqlPtr), int32(blobPtr), int32(len(blob)), int32(len(params)))
	if err != nil {
		return nil, d.rt.execErr("flatsql_query_raw_flatbuffer_stream", err)
	}
	if wasmrt.ToInt32(res[0]) == 0 {
		return nil, d.rt.engineErr("query_raw_flatbuffer_stream")
	}

	m := d.rt.mod
	ptrRes, err := m.Execute("flatsql_response_artifact_data")
	if err != nil {
		return nil, err
	}
	sizeRes, err := m.Execute("flatsql_response_artifact_size")
	if err != nil {
		return nil, err
	}
	rowsRes, err := m.Execute("flatsql_response_artifact_row_count")
	if err != nil {
		return nil, err
	}
	colsRes, err := m.Execute("flatsql_response_artifact_column_count")
	if err != nil {
		return nil, err
	}
	hitRes, err := m.Execute("flatsql_response_artifact_cache_hit")
	if err != nil {
		return nil, err
	}

	size := uint32(wasmrt.ToInt32(sizeRes[0]))
	var out []byte
	if size > 0 {
		out, err = m.ReadMemory(toUint32(ptrRes[0]), size)
		if err != nil {
			return nil, err
		}
	} else {
		out = []byte{}
	}
	stream := &RawStream{
		Bytes:      out,
		Rows:       int(toFloat64(rowsRes[0])),
		Columns:    int(toFloat64(colsRes[0])),
		CacheHit:   wasmrt.ToInt32(hitRes[0]) != 0,
		FNV1a64:    FNV1a64WordFolded(out),
		FrameCount: countStreamFrames(out),
	}

	// Mirror only when the generation is unchanged across the execution: a
	// mutating raw-stream statement (or any concurrent invalidation) bumps
	// it, and such results must never be replayed.
	if mirrorKey != "" {
		if postGen, err := d.queryCacheGenerationLocked(); err == nil && postGen == preGen {
			if d.rawMirror == nil {
				d.rawMirror = newRawStreamMirror()
			}
			d.rawMirror.put(mirrorKey, stream)
		}
	}
	return stream, nil
}

// queryCacheGenerationLocked reads the engine's query-cache generation
// counter (bumped by every mutator). Caller holds the module lock.
func (d *Database) queryCacheGenerationLocked() (uint64, error) {
	res, err := d.rt.mod.Execute("flatsql_query_cache_generation", int32(d.handle))
	if err != nil {
		return 0, d.rt.execErr("flatsql_query_cache_generation", err)
	}
	return uint64(toFloat64(res[0])), nil
}

// ExportData snapshots the raw arena stream (the durability primitive).
func (d *Database) ExportData() ([]byte, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	ptrRes, err := d.rt.mod.Execute("flatsql_export_data", int32(d.handle))
	if err != nil {
		return nil, d.rt.execErr("flatsql_export_data", err)
	}
	sizeRes, err := d.rt.mod.Execute("flatsql_export_size")
	if err != nil {
		return nil, err
	}
	size := uint32(wasmrt.ToInt32(sizeRes[0]))
	if size == 0 {
		return []byte{}, nil
	}
	return d.rt.mod.ReadMemory(toUint32(ptrRes[0]), size)
}

// LoadAndRebuild restores a snapshot produced by ExportData, rebuilding
// indexes.
func (d *Database) LoadAndRebuild(data []byte) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	dataPtr, err := d.rt.allocBytes(data)
	if err != nil {
		return err
	}
	if dataPtr != 0 {
		defer d.rt.mod.Deallocate(dataPtr)
	}
	if _, err := d.rt.mod.Execute("flatsql_load_and_rebuild", int32(d.handle), int32(dataPtr), int32(len(data))); err != nil {
		return d.rt.execErr("flatsql_load_and_rebuild", err)
	}
	return nil
}

// DecodeSizePrefixedStream splits a [u32le length][bytes] frame stream into
// individual FlatBuffer payloads (host-side helper for tests and adapters).
func DecodeSizePrefixedStream(stream []byte) ([][]byte, error) {
	var frames [][]byte
	off := 0
	for off < len(stream) {
		if off+4 > len(stream) {
			return nil, fmt.Errorf("flatsqlrt: truncated frame header at offset %d", off)
		}
		n := int(binary.LittleEndian.Uint32(stream[off : off+4]))
		off += 4
		if off+n > len(stream) {
			return nil, fmt.Errorf("flatsqlrt: truncated frame payload at offset %d (want %d bytes)", off, n)
		}
		frames = append(frames, stream[off:off+n])
		off += n
	}
	return frames, nil
}

func toUint32(v interface{}) uint32 {
	switch val := v.(type) {
	case int32:
		return uint32(val)
	case uint32:
		return val
	case int64:
		return uint32(val)
	case uint64:
		return uint32(val)
	default:
		return 0
	}
}

func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int32:
		return float64(val)
	case int64:
		return float64(val)
	case uint32:
		return float64(val)
	case uint64:
		return float64(val)
	default:
		return 0
	}
}
