package flatsqlrt

// diskstate.go — the Go binding for FlatSQL's disk-backed open and its durable
// state layer (flatsql docs/STORAGE-DURABILITY.md §3.3, §6.4).
//
// The engine ABI is deliberately small and every failure is a VALUE:
//
//	void*  flatsql_open_db(schema, dbName, path, journalMode)
//	int    flatsql_is_disk_backed(handle)
//	int    flatsql_open_state(handle)      -> records visible, or -1..-5
//	int    flatsql_reindex_all(handle)     -> full re-derivation from the stream
//	int    flatsql_flush_index(handle)     -> append stream, fsync, commit mark
//	double flatsql_flushed_offset(handle)  -> the high-water mark
//
// NOTHING here traps on a bad outcome. The embedded engine is the
// -fignore-exceptions build, where a C++ throw lowers to `unreachable` and
// poisons the whole instance (commit b26ed45), so an error that arrives as a
// negative return is the difference between "re-derive and carry on" and "the
// daemon lost its storage engine".

import (
	"errors"
	"fmt"
)

// JournalMode selects the engine's SQLite journal mode for a disk-backed open.
type JournalMode int32

const (
	// JournalDelete is SQLite's default rollback journal.
	JournalDelete JournalMode = 0
	// JournalWAL is UNAVAILABLE on every wasm target: WAL needs xShmMap shared
	// memory and neither the browser shim nor this host provides it
	// (SQLITE_OMIT_WAL is set on all three wasm builds). Named for completeness
	// so a caller cannot pass 1 believing it works.
	JournalWAL JournalMode = 1
	// JournalTruncate is what a disk-backed wasm database uses. It is crash-safe
	// under the single writer the one-daemon-per-box law already guarantees, and
	// it is the mode the engine's own tests and both shims exercise.
	JournalTruncate JournalMode = 2
	// JournalMemory keeps the rollback journal in RAM — NOT crash safe.
	JournalMemory JournalMode = 3
)

// State codes returned by flatsql_open_state / flatsql_reindex_all. Every one
// is recoverable by a full re-derivation; there is no code that means data was
// lost, because no recovery path ever rewrites the stream.
const (
	stateAbsent          = -1 // no persisted state yet
	stateVersionMismatch = -2 // format or schema fingerprint changed
	stateCorrupt         = -3 // verification failed
	stateTorn            = -4 // index claims a mark the stream cannot back
	stateNoFilesystem    = -5 // not disk-backed / host refused the FS
)

// Typed errors for the state codes. Callers switch on these instead of on
// magic numbers, and every one of them EXCEPT ErrStateNoFilesystem is answered
// by re-deriving.
var (
	// ErrStateAbsent — the database has no persisted index state. Normal on a
	// first boot.
	ErrStateAbsent = errors.New("flatsqlrt: no persisted engine state")
	// ErrStateVersionMismatch — the on-disk state was written by a different
	// format version or against a different schema.
	ErrStateVersionMismatch = errors.New("flatsqlrt: persisted engine state has a mismatched format/schema")
	// ErrStateCorrupt — verification of the persisted state failed.
	ErrStateCorrupt = errors.New("flatsqlrt: persisted engine state is corrupt")
	// ErrStateTorn — the index claims a high-water mark the record stream
	// cannot back (a partial write).
	ErrStateTorn = errors.New("flatsqlrt: persisted engine state is torn against its stream")
	// ErrStateNoFilesystem — the handle is not disk-backed, or the host refused
	// the filesystem. This is the ONE code a caller must not answer by
	// re-deriving: there is nothing to re-derive from.
	ErrStateNoFilesystem = errors.New("flatsqlrt: engine has no filesystem (not disk-backed)")
)

// stateErr maps an engine state code to a typed error. Non-negative is success.
func stateErr(op string, code int32) error {
	var base error
	switch code {
	case stateAbsent:
		base = ErrStateAbsent
	case stateVersionMismatch:
		base = ErrStateVersionMismatch
	case stateCorrupt:
		base = ErrStateCorrupt
	case stateTorn:
		base = ErrStateTorn
	case stateNoFilesystem:
		base = ErrStateNoFilesystem
	default:
		base = fmt.Errorf("unknown engine state code %d", code)
	}
	return fmt.Errorf("flatsqlrt: %s: %w", op, base)
}

// StateRecoverable reports whether err is a state error that a full
// re-derivation fixes. ErrStateNoFilesystem is the only one that is not:
// re-deriving needs a filesystem.
func StateRecoverable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrStateNoFilesystem) {
		return false
	}
	return errors.Is(err, ErrStateAbsent) ||
		errors.Is(err, ErrStateVersionMismatch) ||
		errors.Is(err, ErrStateCorrupt) ||
		errors.Is(err, ErrStateTorn)
}

// OpenDatabase parses a FlatBuffers .fbs schema and opens the database against
// a REAL FILE at path.
//
// path is handed to the engine verbatim: the engine's VFS treats paths as
// opaque host-namespace strings (its xFullPathname is the identity function),
// and this host resolves them inside the runtime's file root. The engine will
// also touch `path + "-journal"` (rollback journal) and `path + ".fsdata"`
// (its append-only record stream), so all three must be inside that root.
//
// An empty path, "" or ":memory:" is EXACTLY CreateDatabase — the engine
// documents that equivalence, and it is what keeps every existing ephemeral
// consumer byte-for-byte unaffected.
func (r *Runtime) OpenDatabase(schema, name, path string, mode JournalMode) (*Database, error) {
	if err := r.checkUsable("open_db"); err != nil {
		return nil, err
	}
	if path == "" || path == ":memory:" {
		return r.CreateDatabase(schema, name)
	}
	// Refuse BEFORE entering the guest when this runtime has no filesystem: the
	// engine would fail the open anyway, but it would do so after allocating and
	// after writing an error latch, and the caller deserves the honest reason.
	if r.io == nil {
		return nil, fmt.Errorf("flatsqlrt: open_db %q: %w (create the runtime WithFileIORoot)", path, ErrStateNoFilesystem)
	}
	if _, status := r.io.Resolve(path); status != ioStatusSuccess {
		return nil, fmt.Errorf("flatsqlrt: open_db %q: path is outside the engine file root %s", path, r.io.Root())
	}

	r.mod.Lock()
	defer r.mod.Unlock()

	schemaPtr, err := r.allocCString(schema)
	if err != nil {
		return nil, err
	}
	defer r.free(schemaPtr)
	namePtr, err := r.allocCString(name)
	if err != nil {
		return nil, err
	}
	defer r.free(namePtr)
	pathPtr, err := r.allocCString(path)
	if err != nil {
		return nil, err
	}
	defer r.free(pathPtr)

	res, err := r.mod.Execute("flatsql_open_db",
		int32(schemaPtr), int32(namePtr), int32(pathPtr), int32(mode))
	if err != nil {
		return nil, r.execErr("flatsql_open_db", err)
	}
	handle := toUint32(res[0])
	if handle == 0 {
		return nil, r.engineErr("open_db")
	}
	return &Database{rt: r, handle: handle, name: name}, nil
}

// IsDiskBacked reports whether this database is backed by a real file. The
// caller uses it to decide whether a boot may trust persisted state — a
// database that silently fell back to RAM must never be treated as durable.
func (d *Database) IsDiskBacked() (bool, error) {
	if err := d.rt.checkUsable("is_disk_backed"); err != nil {
		return false, err
	}
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	res, err := d.rt.mod.Execute("flatsql_is_disk_backed", int32(d.handle))
	if err != nil {
		return false, d.rt.execErr("flatsql_is_disk_backed", err)
	}
	return stateCode(res[0]) == 1, nil
}

// stateCode narrows an engine i32 return to a signed code. The wasmrt bridge
// hands back int32, but a negative value must survive the conversion — reading
// these through toUint32 would turn -3 into 4294967293 and every error into a
// success.
func stateCode(v interface{}) int32 {
	switch val := v.(type) {
	case int32:
		return val
	case int64:
		return int32(val)
	case uint32:
		return int32(val)
	case float64:
		return int32(val)
	default:
		return stateCorrupt
	}
}

// OpenState verifies the persisted index and re-indexes only the stream tail
// past the recorded high-water mark, returning the record count now visible.
//
// A negative code comes back as a typed error; every one of them except
// ErrStateNoFilesystem is answered by ReindexAll.
func (d *Database) OpenState() (int, error) {
	if err := d.rt.checkUsable("open_state"); err != nil {
		return 0, err
	}
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	res, err := d.rt.mod.Execute("flatsql_open_state", int32(d.handle))
	if err != nil {
		return 0, d.rt.execErr("flatsql_open_state", err)
	}
	code := stateCode(res[0])
	if code < 0 {
		return 0, stateErr("open_state", code)
	}
	return int(code), nil
}

// ReindexAll re-derives the whole index from the record stream. It is always
// available and always correct: the stream is never rewritten by any recovery
// path, which is why no state code means data loss.
//
// A -1 (absent) from the ENGINE means only "there is no stream file", and the
// engine has already done the part that matters — clearDerivedState() and
// storage_->reset() run BEFORE that early return (flatsql
// cpp/src/flatsql_state.cpp reindexAll). So an absent stream is a completed
// re-derivation over zero records, and this binding reports it as (0, nil)
// rather than as a failure. Getting that backwards would make every first boot
// look like a corrupt store.
func (d *Database) ReindexAll() (int, error) {
	if err := d.rt.checkUsable("reindex_all"); err != nil {
		return 0, err
	}
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	res, err := d.rt.mod.Execute("flatsql_reindex_all", int32(d.handle))
	if err != nil {
		return 0, d.rt.execErr("flatsql_reindex_all", err)
	}
	code := stateCode(res[0])
	if code == stateAbsent {
		return 0, nil
	}
	if code < 0 {
		return 0, stateErr("reindex_all", code)
	}
	return int(code), nil
}

// FlushIndex appends the engine's new stream bytes, FSYNCS THEM, and only then
// commits the index pages and the high-water mark. That order is the invariant
// the whole design rests on — the index may only ever claim a mark the stream
// can already back.
func (d *Database) FlushIndex() error {
	if err := d.rt.checkUsable("flush_index"); err != nil {
		return err
	}
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	res, err := d.rt.mod.Execute("flatsql_flush_index", int32(d.handle))
	if err != nil {
		return d.rt.execErr("flatsql_flush_index", err)
	}
	if code := stateCode(res[0]); code < 0 {
		return stateErr("flush_index", code)
	}
	return nil
}

// FlushedOffset returns the engine's high-water mark over its own record
// stream. Crossed as a float64 for the same reason the I/O contract uses f64:
// an i64 is legalized differently in the two wasm lanes.
func (d *Database) FlushedOffset() (int64, error) {
	if err := d.rt.checkUsable("flushed_offset"); err != nil {
		return 0, err
	}
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	res, err := d.rt.mod.Execute("flatsql_flushed_offset", int32(d.handle))
	if err != nil {
		return 0, d.rt.execErr("flatsql_flushed_offset", err)
	}
	v, ok := res[0].(float64)
	if !ok {
		return 0, fmt.Errorf("flatsqlrt: flushed_offset returned %T, want float64", res[0])
	}
	if v < 0 {
		return 0, stateErr("flushed_offset", int32(v))
	}
	return int64(v), nil
}
