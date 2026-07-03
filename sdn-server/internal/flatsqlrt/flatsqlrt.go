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

//go:embed flatsql-wasi.wasm
var flatsqlWasm []byte

// EmbeddedWasm returns the embedded flatsql-wasi.wasm bytes (for hashing /
// provenance checks and for hosts that want to instantiate it themselves).
func EmbeddedWasm() []byte { return flatsqlWasm }

const (
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
	mod *wasmrt.Module
}

// New instantiates the FlatSQL WASI reactor and runs its _initialize export.
func New(opts ...Option) (*Runtime, error) {
	cfg := &config{maxMemoryPages: DefaultMaxMemoryPages, wasmBytes: flatsqlWasm}
	for _, o := range opts {
		o(cfg)
	}
	mod, err := wasmrt.NewModule(cfg.wasmBytes,
		wasmrt.WithWASI(),
		wasmrt.WithMaxMemoryPages(cfg.maxMemoryPages),
	)
	if err != nil {
		return nil, fmt.Errorf("flatsqlrt: instantiate engine: %w", err)
	}
	if _, err := mod.Execute("_initialize"); err != nil {
		mod.Release()
		return nil, fmt.Errorf("flatsqlrt: _initialize: %w", err)
	}
	return &Runtime{mod: mod}, nil
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

// engineErr wraps a failed engine call with flatsql_get_error detail.
func (r *Runtime) engineErr(op string) error {
	return fmt.Errorf("flatsqlrt: %s: %s", op, r.lastError())
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
		return nil, fmt.Errorf("flatsqlrt: flatsql_create_db: %w", err)
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
}

// Name returns the diagnostic label the database was created with.
func (d *Database) Name() string { return d.name }

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
		return fmt.Errorf("flatsqlrt: flatsql_register_file_id: %w", err)
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

	if _, err := d.rt.mod.Execute("flatsql_register_source", int32(d.handle), int32(srcPtr)); err != nil {
		return fmt.Errorf("flatsqlrt: flatsql_register_source: %w", err)
	}
	return nil
}

// CreateUnifiedViews replaces each base table name with a UNION ALL view over
// its per-source shadow tables, adding a `_source` column.
func (d *Database) CreateUnifiedViews() error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	if _, err := d.rt.mod.Execute("flatsql_create_unified_views", int32(d.handle)); err != nil {
		return fmt.Errorf("flatsqlrt: flatsql_create_unified_views: %w", err)
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
			return fmt.Errorf("flatsqlrt: flatsql_query: %w", err)
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
		return fmt.Errorf("flatsqlrt: flatsql_query_params: %w", err)
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
		return fmt.Errorf("flatsqlrt: flatsql_register_query_template: %w", err)
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
		return nil, fmt.Errorf("flatsqlrt: flatsql_query_template: %w", err)
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
		return nil, fmt.Errorf("flatsqlrt: flatsql_query_many: %w", err)
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
}

// QueryRawFlatBufferStream executes SQL whose every result cell is a BLOB
// (e.g. `SELECT _data FROM OMM WHERE ...`) and returns the aligned
// size-prefixed frame stream — the wire format served to the network.
func (d *Database) QueryRawFlatBufferStream(sql string, params ...interface{}) (*RawStream, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	sqlPtr, err := d.rt.allocCString(sql)
	if err != nil {
		return nil, err
	}
	defer d.rt.mod.Deallocate(sqlPtr)

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

	res, err := d.rt.mod.Execute("flatsql_query_raw_flatbuffer_stream",
		int32(d.handle), int32(sqlPtr), int32(blobPtr), int32(len(blob)), int32(len(params)))
	if err != nil {
		return nil, fmt.Errorf("flatsqlrt: flatsql_query_raw_flatbuffer_stream: %w", err)
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
	return &RawStream{
		Bytes:   out,
		Rows:    int(toFloat64(rowsRes[0])),
		Columns: int(toFloat64(colsRes[0])),
	}, nil
}

// ExportData snapshots the raw arena stream (the durability primitive).
func (d *Database) ExportData() ([]byte, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	ptrRes, err := d.rt.mod.Execute("flatsql_export_data", int32(d.handle))
	if err != nil {
		return nil, fmt.Errorf("flatsqlrt: flatsql_export_data: %w", err)
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
		return fmt.Errorf("flatsqlrt: flatsql_load_and_rebuild: %w", err)
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
