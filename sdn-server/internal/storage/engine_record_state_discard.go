package storage

import (
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// THE ENGINE'S RECORD STATE IS ONE UNIT: the arena (<db>.fsdata), the index
// rows the engine keeps inside the control database, and the PARTITION MAP —
// the `_flatsql_source_ranges` table that says which source each byte range of
// the arena belongs to. The engine routes every frame it replays at open by
// that map (flatsql cpp/src/flatsql_state.cpp loadStreamFromDisk →
// sourceForOffset). Throw the arena away and keep the map, and the NEXT arena
// is read through the OLD map: frames land in whatever partition used to own
// those offsets.
//
// That is exactly what the dev node did on 2026-09-14. A boot discarded a
// mostly-dead arena by removing the file, the hot-window rebuild appended a
// fresh arena at offset 0, the next flush persisted the fresh ranges NEXT TO
// the stale ones (INSERT OR REPLACE keyed by start offset never deletes), and
// the following boot attributed 6,412 live IQC rows to CAT's source — where the
// residency reconcile, finding a partition the ledger had no rows for,
// tombstoned every one of them. The engine cannot notice: reindex on an absent
// stream returns before it clears anything, and a torn recovery keeps in memory
// every range that reaches past the surviving stream.
//
// So the store owns the invariant instead: BEFORE the engine opens its record
// state, the map and the mark are checked against the file, and a state that
// cannot describe its stream is discarded WHOLE — map emptied, arena truncated
// to zero bytes (not removed: an EMPTY stream below the mark takes the engine's
// torn path, which clears the index rows and resets the arena in one rebuild
// transaction; an ABSENT one returns early and clears nothing).

// engineStreamSize is the size of the engine's record arena, 0 when absent.
func engineStreamSize(dbPath string) uint64 {
	info, err := os.Stat(dbPath + ".fsdata")
	if err != nil || info.Size() < 0 {
		return 0
	}
	return uint64(info.Size())
}

// engineOffsetCell reads a stream offset the engine stores as decimal TEXT
// (or as an integer, depending on how SQLite typed the column).
func engineOffsetCell(v any) (uint64, bool) {
	switch x := v.(type) {
	case string:
		n, err := strconv.ParseUint(x, 10, 64)
		return n, err == nil
	case int64:
		return uint64(x), x >= 0
	case float64:
		return uint64(x), x >= 0
	case []byte:
		n, err := strconv.ParseUint(string(x), 10, 64)
		return n, err == nil
	}
	return 0, false
}

// engineStateTables reports which of the engine's persisted state tables exist
// in the control database. A fresh file has none.
func engineStateTables(db *flatsqlrt.Database) map[string]bool {
	out := map[string]bool{}
	res, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('_flatsql_state', '_flatsql_source_ranges')`)
	if err != nil || res == nil {
		return out
	}
	for _, row := range res.Rows {
		if len(row) == 1 {
			out[fmt.Sprint(row[0])] = true
		}
	}
	return out
}

// engineRecordStateInconsistent reports whether the engine's persisted record
// state cannot describe the arena on disk, with the reason. Read-only, and run
// BEFORE OpenState so the engine never restores a map it cannot trust.
//
// Inconsistent means any of:
//   - the index's high-water mark lies past the end of the stream (the arena
//     was removed or cut — an older binary's discard, an operator's, or a
//     crash mid-flush);
//   - a partition range reaches past the end of the stream;
//   - two partition ranges overlap.
//
// A torn tail after a crash is deliberately in this set. The engine's own
// torn recovery keeps the complete prefix of the stream, but it also keeps
// every range that reached past it, and the next flush persists those over
// the frames that get appended there. The arena is a cache of the control
// tables; rebuilding the bounded window costs seconds, a partition that
// routes to the wrong source costs live rows.
func engineRecordStateInconsistent(db *flatsqlrt.Database, dbPath string) (string, bool) {
	tables := engineStateTables(db)
	if !tables["_flatsql_state"] {
		return "", false
	}
	streamBytes := engineStreamSize(dbPath)
	res, err := db.Query(`SELECT v FROM _flatsql_state WHERE k = 'flushed_offset'`)
	if err == nil && res != nil && len(res.Rows) == 1 && len(res.Rows[0]) == 1 {
		if mark, ok := engineOffsetCell(res.Rows[0][0]); ok && mark > streamBytes {
			return fmt.Sprintf("the index claims a mark at byte %d of a %d-byte record stream", mark, streamBytes), true
		}
	}
	if !tables["_flatsql_source_ranges"] {
		return "", false
	}
	res, err = db.Query(`SELECT "start", "stop", source FROM _flatsql_source_ranges`)
	if err != nil || res == nil {
		return "", false
	}
	type span struct {
		start, stop uint64
		source      string
	}
	spans := make([]span, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 3 {
			continue
		}
		start, okStart := engineOffsetCell(row[0])
		stop, okStop := engineOffsetCell(row[1])
		if !okStart || !okStop {
			return fmt.Sprintf("partition range row %v is unreadable", row), true
		}
		spans = append(spans, span{start: start, stop: stop, source: fmt.Sprint(row[2])})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i, s := range spans {
		if s.stop > streamBytes {
			return fmt.Sprintf("partition range [%d,%d) of %q reaches past the %d-byte record stream", s.start, s.stop, s.source, streamBytes), true
		}
		if i > 0 && s.start < spans[i-1].stop {
			p := spans[i-1]
			return fmt.Sprintf("partition range [%d,%d) of %q overlaps [%d,%d) of %q", s.start, s.stop, s.source, p.start, p.stop, p.source), true
		}
	}
	return "", false
}

// discardEngineRecordState throws the engine's record state away as one unit,
// on a freshly opened handle BEFORE its OpenState: the partition map is
// emptied and the arena is truncated to zero bytes (created if absent).
// OpenState then finds the index torn against an empty stream and ReindexAll
// clears every derived index row, resets the arena and flushes a zero mark —
// an empty record state under the existing tables, sources and views, which
// the hot-window rebuild refills from the control tables.
//
// The map has to go through SQL on the handle that will open the state, and
// before it does: the engine reads the map into memory at the start of
// OpenState and keeps it across a torn/absent stream, and its flush only ever
// adds to the table. There is no engine call that forgets a range.
func discardEngineRecordState(db *flatsqlrt.Database, dbPath string) error {
	if engineStateTables(db)["_flatsql_source_ranges"] {
		if _, err := db.Query(`DELETE FROM _flatsql_source_ranges`); err != nil {
			return fmt.Errorf("discard engine partition map: %w", err)
		}
	}
	if err := os.WriteFile(dbPath+".fsdata", nil, 0o644); err != nil {
		return fmt.Errorf("discard engine record stream: %w", err)
	}
	return nil
}
