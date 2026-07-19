package flowrt

// firehistory.go — a ServiceFlow's OBSERVED timer-fire history: the
// data-surface board's real "run log" source, replacing the disconnected,
// inert sdnruns.Store (the pre-existing Go-orchestration run engine, made
// fully inert per the STOP block — see plugin/plugins/sdnruntime/sdnruns.go).
//
// This is bookkeeping, never orchestration: the scheduler's OWN ticker
// already decides when to fire (sdncron.Scheduler); FireTrigger already runs
// that ONE fire to completion. All firehistory.go adds is OBSERVATION of that
// already-happening fire — start/finish timestamps, ok/error outcome, and,
// for an engine-linked flow, the store's exact per-table ROW-ID RANGE the
// fire's own ingests landed in (a real, read-derived fact from MAX(rowid)
// snapshots taken immediately before/after the drain — proven queryable via
// LinkedStore.Query's read-only SELECT surface). Nothing here fetches, fits,
// batches, invokes a provider, or fires an out-of-band trigger.
//
// Bounded in-memory ring (maxFireHistory): a process restart starts fresh —
// exactly like timerRunCount/lastInvokeAt already do. The stored $OMM/$OCM/
// $OBD rows themselves persist independently via the arena snapshot, so a
// restart never loses data, only the historical FIRE LOG entries older than
// the cap or predating this process's boot. See BackfillRange for what a
// fresh process does about rows that were ALREADY in the store when it
// started observing (e.g. a prior process's completed full-catalog drain).

import (
	"fmt"
	"time"
)

// maxFireHistory bounds the in-memory fire log (oldest entries drop first).
const maxFireHistory = 500

// TableRange is one arena table's rowid WINDOW a single fire's ingests landed
// in: (After, Through] — After is the DATABASE's MAX(rowid) immediately
// BEFORE the fire, Through is MAX(rowid) immediately after. rowid is a SINGLE
// sequence shared across every table in the linked store's database (proven
// empirically: interleaved sds_omm/sds_obd ingests get interleaved rowids),
// so Through-After counts ingests to EVERY table in the window, not just this
// one — callers that need "how many rows did THIS table gain" must run
// `SELECT COUNT(*) FROM <table> WHERE rowid > After AND rowid <= Through`
// (see CountInRange), never simple subtraction. The window itself is still
// exactly right for filtering: it selects precisely this table's rows that
// arrived during this one observed fire.
type TableRange struct {
	After   int64
	Through int64
}

// CountInRange runs the real `SELECT COUNT(*) ... WHERE rowid > ? AND rowid
// <= ?` this table's row count within r requires (see TableRange's doc for
// why simple subtraction is wrong). Returns 0 for a nil store, an empty/
// backwards range, or a query error.
func CountInRange(store *LinkedStore, table string, r TableRange) int {
	if store == nil || r.Through <= r.After {
		return 0
	}
	res, err := store.Query(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE rowid > ? AND rowid <= ?", table), r.After, r.Through)
	if err != nil || len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
		return 0
	}
	return asInt(res.Rows[0][0])
}

// asInt coerces a flatsqlrt scalar result cell (int64 or int, depending on
// the engine's marshaling path) into a plain int.
func asInt(v interface{}) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

// FireRecord is one OBSERVED timer-trigger firing (see the package doc). The
// data-surface board's run log renders these directly.
type FireRecord struct {
	ID         string
	TriggerID  string
	StartedAt  time.Time
	FinishedAt time.Time // zero while the fire is still in flight (OngoingFire)
	Status     string    // "ok" | "error" ("" while in flight)
	Error      string

	// OMM/OCM/OBD are this fire's exact rowid ranges in the flow's linked
	// store (zero-value TableRange{} when the flow has no engine link, i.e. a
	// bridge-mode flow, or the store was unreadable at snapshot time).
	OMM TableRange
	OCM TableRange
	OBD TableRange
}

// newFireRecordID derives a stable, sortable id from the fire's start time.
// FireTrigger serializes all firings for one ServiceFlow (sf.mu), so two
// records from the SAME flow never collide.
func newFireRecordID(startedAt time.Time) string {
	return "fire-" + startedAt.Format("20060102T150405.000000000Z")
}

// MaxRowid returns table's current MAX(rowid) in store (0 for an empty or
// unreadable table, or a nil store) — the data-surface board's "progress so
// far" read for an ONGOING fire (building a live (After, now] TableRange
// alongside OngoingFire, since Through is unset until the fire completes).
func MaxRowid(store *LinkedStore, table string) int64 {
	return countTableMaxRowid(store, table)
}

// countTableMaxRowid returns MAX(rowid) for table in store (0 for an empty or
// unreadable table, or a nil store). Read-only.
func countTableMaxRowid(store *LinkedStore, table string) int64 {
	if store == nil {
		return 0
	}
	res, err := store.Query(fmt.Sprintf("SELECT COALESCE(MAX(rowid),0) FROM %s", table))
	if err != nil || len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
		return 0
	}
	return int64(asInt(res.Rows[0][0]))
}

// snapshotTableMaxRowids reads every OD-write-lane table's current
// MAX(rowid) in one pass (nil store yields all zeros).
func snapshotTableMaxRowids(store *LinkedStore) (omm, ocm, obd int64) {
	return countTableMaxRowid(store, "sds_omm"),
		countTableMaxRowid(store, "sds_ocm"),
		countTableMaxRowid(store, "sds_obd")
}

// beginFire records a fire's start (before-snapshot) and marks it ongoing.
// Returns the in-progress record; the caller finishes it via endFire.
func (sf *ServiceFlow) beginFire(triggerID string, store *LinkedStore) FireRecord {
	started := time.Now().UTC()
	ommBefore, ocmBefore, obdBefore := snapshotTableMaxRowids(store)
	rec := FireRecord{
		ID:        newFireRecordID(started),
		TriggerID: triggerID,
		StartedAt: started,
		OMM:       TableRange{After: ommBefore},
		OCM:       TableRange{After: ocmBefore},
		OBD:       TableRange{After: obdBefore},
	}
	sf.fireHistMu.Lock()
	sf.ongoing = &rec
	sf.fireHistMu.Unlock()
	return rec
}

// endFire completes a fire (after-snapshot + outcome), clears the ongoing
// marker, and appends it to the bounded history.
func (sf *ServiceFlow) endFire(rec FireRecord, store *LinkedStore, fireErr error) {
	rec.FinishedAt = time.Now().UTC()
	rec.OMM.Through, rec.OCM.Through, rec.OBD.Through = snapshotTableMaxRowids(store)
	if fireErr != nil {
		rec.Status = "error"
		rec.Error = fireErr.Error()
	} else {
		rec.Status = "ok"
	}

	sf.fireHistMu.Lock()
	sf.ongoing = nil
	sf.fireHist = append(sf.fireHist, rec)
	if len(sf.fireHist) > maxFireHistory {
		sf.fireHist = sf.fireHist[len(sf.fireHist)-maxFireHistory:]
	}
	sf.fireHistMu.Unlock()
}

// FireHistory returns a copy of the recorded fire history, oldest first.
// Bounded (maxFireHistory) and in-memory only — see the package doc.
func (sf *ServiceFlow) FireHistory() []FireRecord {
	sf.fireHistMu.Lock()
	defer sf.fireHistMu.Unlock()
	out := make([]FireRecord, len(sf.fireHist))
	copy(out, sf.fireHist)
	return out
}

// OngoingFire returns the currently in-flight fire's live record (FinishedAt/
// Status not yet set) and true, or (FireRecord{}, false) when idle.
func (sf *ServiceFlow) OngoingFire() (FireRecord, bool) {
	sf.fireHistMu.Lock()
	defer sf.fireHistMu.Unlock()
	if sf.ongoing == nil {
		return FireRecord{}, false
	}
	return *sf.ongoing, true
}

// BackfillRange returns a synthesized TableRange spanning EVERY row currently
// in table — (0, MAX(rowid)] — for the one-time "backfill" pseudo-run a fresh
// process (empty FireHistory) synthesizes to surface rows a PRIOR process
// already stored (e.g. a completed full-catalog drain before this process's
// current boot). Honest, not fabricated: it reports exactly what is in the
// store right now, labeled as pre-existing rather than attributed to a
// specific past firing this process never observed.
func BackfillRange(store *LinkedStore, table string) TableRange {
	return TableRange{After: 0, Through: countTableMaxRowid(store, table)}
}
