package flatsqlrt

// arena.go — the Go binding for the engine's record-arena compaction
// (flatsql FlatSQLDatabase::compactArena, docs/STORAGE-DURABILITY.md §6.4.2).
//
//	int    flatsql_compact_arena(handle, maxStepBytes) -> 1 more, 0 done, <0 state code
//	double flatsql_arena_stat(handle, which)
//
// The record arena is append-only: a tombstoned row keeps its bytes until the
// arena is rewritten. CompactArenaStep rewrites it with only the rows a query
// can still see; every surviving row keeps its sequence, so the residency
// ledger's (source, seq) keys stay valid. One step does at most about
// maxStepBytes of I/O, and the caller may release its locks between steps:
// reads, ingests, tombstones and flushes may all run in between. A crash at
// any point leaves a state OpenState accepts.

import (
	"errors"
	"fmt"
)

// ArenaStats is what the engine reports about its record arena.
type ArenaStats struct {
	Size       int64 // bytes of frames in the arena
	Capacity   int64 // bytes the arena has allocated
	Records    int64 // frames in the arena
	DeadBytes  int64 // frame bytes of tombstoned rows
	Compacting bool  // a compaction is between steps
}

// ArenaCompaction describes the last compaction that adopted a new arena.
type ArenaCompaction struct {
	BeforeBytes, AfterBytes       int64
	BeforeCapacity, AfterCapacity int64
	KeptRecords, DroppedRecords   int64
	SequenceRuns, Steps           int64
	Completed                     bool
}

// ErrArenaStatUnsupported: the embedded engine predates arena compaction.
var ErrArenaStatUnsupported = errors.New("flatsqlrt: engine has no arena compaction")

// CompactStatus is what one compaction step leaves.
type CompactStatus int

const (
	// CompactDone: nothing is left to do (including: nothing was dead).
	CompactDone CompactStatus = iota
	// CompactPending: call again (outside a transaction) to continue.
	CompactPending
	// CompactDeferred: inside an open transaction the arena was swapped in
	// memory only — the room is there now — and the next call outside a
	// transaction persists the new layout (a flush does it too).
	CompactDeferred
)

// CompactArenaStep runs one step of an arena compaction. maxStepBytes <= 0
// runs the whole compaction in this call.
func (d *Database) CompactArenaStep(maxStepBytes int64) (CompactStatus, error) {
	if err := d.rt.checkUsable("compact_arena"); err != nil {
		return CompactPending, err
	}
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	if maxStepBytes < 0 {
		maxStepBytes = 0
	}
	defer d.rt.beginInFlight("flatsql_compact_arena (one bounded compaction step)")()
	res, err := d.rt.mod.Execute("flatsql_compact_arena", int32(d.handle), float64(maxStepBytes))
	if err != nil {
		return CompactPending, d.rt.attributeExecErr(d.rt.execErr("flatsql_compact_arena", err))
	}
	code := stateCode(res[0])
	switch {
	case code == 0:
		return CompactDone, nil
	case code == 1:
		return CompactPending, nil
	case code == 2:
		return CompactDeferred, nil
	case code < 0:
		return CompactPending, fmt.Errorf("%w (%s)", stateErr("compact_arena", code), d.rt.lastError())
	default:
		return CompactPending, fmt.Errorf("flatsqlrt: compact_arena returned unexpected status %d", code)
	}
}

// arenaStat reads one figure of flatsql_arena_stat. Lock must be held.
func (d *Database) arenaStat(which int32) (float64, error) {
	res, err := d.rt.mod.Execute("flatsql_arena_stat", int32(d.handle), which)
	if err != nil {
		return 0, d.rt.execErr("flatsql_arena_stat", err)
	}
	v, ok := res[0].(float64)
	if !ok {
		return 0, fmt.Errorf("flatsqlrt: arena_stat returned %T, want float64", res[0])
	}
	return v, nil
}

// HasArenaCompaction reports whether the embedded engine exports compaction.
func (r *Runtime) HasArenaCompaction() bool {
	return r != nil && r.arenaCompaction
}

// ArenaStats reads the arena figures.
func (d *Database) ArenaStats() (ArenaStats, error) {
	if !d.rt.HasArenaCompaction() {
		return ArenaStats{}, ErrArenaStatUnsupported
	}
	if err := d.rt.checkUsable("arena_stat"); err != nil {
		return ArenaStats{}, err
	}
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	var v [5]float64
	for i := range v {
		f, err := d.arenaStat(int32(i))
		if err != nil {
			return ArenaStats{}, err
		}
		v[i] = f
	}
	return ArenaStats{
		Size: int64(v[0]), Capacity: int64(v[1]), Records: int64(v[2]),
		DeadBytes: int64(v[3]), Compacting: v[4] == 1,
	}, nil
}

// LastArenaCompaction reads the report of the last compaction that adopted a
// new arena.
func (d *Database) LastArenaCompaction() (ArenaCompaction, error) {
	if !d.rt.HasArenaCompaction() {
		return ArenaCompaction{}, ErrArenaStatUnsupported
	}
	if err := d.rt.checkUsable("arena_stat"); err != nil {
		return ArenaCompaction{}, err
	}
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	var v [9]float64
	for i := range v {
		f, err := d.arenaStat(int32(10 + i))
		if err != nil {
			return ArenaCompaction{}, err
		}
		v[i] = f
	}
	return ArenaCompaction{
		BeforeBytes: int64(v[0]), AfterBytes: int64(v[1]),
		BeforeCapacity: int64(v[2]), AfterCapacity: int64(v[3]),
		KeptRecords: int64(v[4]), DroppedRecords: int64(v[5]),
		SequenceRuns: int64(v[6]), Steps: int64(v[7]), Completed: v[8] == 1,
	}, nil
}

// SetArenaLimit caps the engine's record arena in bytes (flatsql
// set_arena_limit): its growth stops at the cap and an ingest past it is
// refused with "arena capacity exhausted" instead of trapping. A host that
// bounds the bytes it mirrors sets the cap just above that bound, so the
// arena's doubling never allocates past it either.
func (d *Database) SetArenaLimit(bytes int64) error {
	if !d.rt.HasArenaCompaction() {
		return ErrArenaStatUnsupported
	}
	if err := d.rt.checkUsable("set_arena_limit"); err != nil {
		return err
	}
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	if _, err := d.rt.mod.Execute("flatsql_set_arena_limit", int32(d.handle), float64(bytes)); err != nil {
		return d.rt.execErr("flatsql_set_arena_limit", err)
	}
	return nil
}
