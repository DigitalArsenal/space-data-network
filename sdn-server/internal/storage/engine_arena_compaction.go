package storage

// engine_arena_compaction.go — the engine's record arena, compacted while the
// node runs instead of refusing to mirror.
//
// The arena appends and never gives bytes back on its own: an evicted,
// deleted or superseded row is tombstoned and keeps its bytes. On host-02 that
// filled the 512 MiB budget within hours (635 "not mirroring" refusals on
// 2026-09-28/29, and before the budget existed, five `unreachable` traps from
// the arena's doubling). The engine now rewrites its arena with only the rows
// a query can still see (flatsql compactArena, docs/STORAGE-DURABILITY.md
// §6.4.2). Every surviving row keeps its sequence, so the residency ledger's
// (source, seq) keys stay valid without a rewrite.
//
// Two triggers:
//
//   - past the compaction mark (engineArenaCompactBytes) with more than half
//     of the arena dead, a background pass compacts it in bounded steps, each
//     under its own store write-lock hold, so readers and writers interleave
//     between steps (reads-never-wait: a step is at most one I/O budget);
//   - a mirror that would pass the budget compacts first, synchronously, when
//     anything is dead, and is refused only if it still does not fit.
//
// The boot's discard of a mostly-dead arena (flatsql_boot_state.go) stays as
// the fallback for an arena that was never compacted.

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

const engineArenaCompactStepEnv = "SDN_FLATSQL_ENGINE_COMPACT_STEP_MIB"

// engineArenaCompactStepBytes is one compaction step's I/O budget: the bytes
// one step writes to the temp stream or copies over the record stream. A
// step's store write-lock hold is that I/O plus, once per compaction, the
// in-memory swap (the kept bytes copied once) and one durable commit.
var engineArenaCompactStepBytes = resolveEngineArenaMiB(engineArenaCompactStepEnv, 16)

// engineArenaRuntimeCompaction turns runtime compaction off. Tests use it to
// build an arena the way a binary without it did.
var engineArenaRuntimeCompaction = true

// engineArenaCompactStepHook runs after each background compaction step,
// outside the store lock (tests: prove readers answer between steps).
var engineArenaCompactStepHook func(step int)

// engineArenaCompactor is one store's compaction state. It lives beside the
// store rather than in it (flatsql.go is the store's shared core).
type engineArenaCompactor struct {
	running atomic.Bool
	// runs counts compactions that changed the arena (tests, logs).
	runs atomic.Int64
	// history is the most recent compactions' accounts, oldest first.
	mu      sync.Mutex
	history []engineArenaCompactionAccount
}

// engineArenaCompactionHistory bounds the accounts a store keeps.
const engineArenaCompactionHistory = 16

func (c *engineArenaCompactor) record(acct engineArenaCompactionAccount) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = append(c.history, acct)
	if len(c.history) > engineArenaCompactionHistory {
		c.history = c.history[len(c.history)-engineArenaCompactionHistory:]
	}
	c.runs.Add(1)
}

// engineArenaCompactionAccount is what one compaction measured.
type engineArenaCompactionAccount struct {
	Reason                    string
	Report                    flatsqlrt.ArenaCompaction
	Steps                     int
	Total                     time.Duration
	LongestHold               time.Duration
	MemoryBefore, MemoryAfter uint64 // engine linear memory, bytes
	MemoryDuring              uint64 // after the swap, before the old arena is reused
	LedgerRepaired            int
	Synchronous               bool
	// Deferred: swapped in memory inside a write transaction; the persist
	// ran (or runs) as a separate background compaction.
	Deferred                      bool
	ArenaBefore, ArenaAfter       int64
	CapacityBefore, CapacityAfter int64
}

var engineArenaCompactors sync.Map // *FlatSQLStore -> *engineArenaCompactor

func (s *FlatSQLStore) arenaCompactor() *engineArenaCompactor {
	if c, ok := engineArenaCompactors.Load(s); ok {
		return c.(*engineArenaCompactor)
	}
	c, _ := engineArenaCompactors.LoadOrStore(s, &engineArenaCompactor{})
	return c.(*engineArenaCompactor)
}

// LastEngineArenaCompaction reports the last compaction that changed the
// arena and how many have.
func (s *FlatSQLStore) LastEngineArenaCompaction() (engineArenaCompactionAccount, int64) {
	c := s.arenaCompactor()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.history) == 0 {
		return engineArenaCompactionAccount{}, 0
	}
	return c.history[len(c.history)-1], c.runs.Load()
}

// EngineArenaCompactions reports the recent compactions, oldest first.
func (s *FlatSQLStore) EngineArenaCompactions() []engineArenaCompactionAccount {
	c := s.arenaCompactor()
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]engineArenaCompactionAccount(nil), c.history...)
}

// engineArenaCompactionDue: past the compaction mark, more than half dead —
// the same "mostly dead" ratio the boot discards at, measured by the engine
// (the frame bytes of tombstoned rows) instead of estimated from counts.
func engineArenaCompactionDue(stats flatsqlrt.ArenaStats) bool {
	return stats.Size > engineArenaCompactBytes && stats.DeadBytes*2 > stats.Size
}

// maybeCompactEngineArenaLocked starts a background compaction when one is
// due. Cheap below the mark: the Go-side arena measure answers without a
// guest call. Caller holds s.mu (either mode).
func (s *FlatSQLStore) maybeCompactEngineArenaLocked(reason string) {
	if !engineArenaRuntimeCompaction || s.ps != nil || s.engineDB == nil || s.engine == nil || s.closedErr() != nil {
		return
	}
	if s.engineArenaBytes.Load() <= engineArenaCompactBytes {
		return
	}
	c := s.arenaCompactor()
	if c.running.Load() || !s.engine.HasArenaCompaction() || s.engine.Poisoned() {
		return
	}
	stats, err := s.engineDB.ArenaStats()
	if err != nil || !engineArenaCompactionDue(stats) {
		return
	}
	if !c.running.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer c.running.Store(false)
		if _, err := s.compactEngineArena(reason); err != nil {
			log.Warnf("FlatSQL engine arena compaction (%s) stopped: %v", reason, err)
		}
	}()
}

// persistPendingArenaCompaction finishes a compaction that is between steps —
// above all one swapped in memory inside a write transaction, which a flush
// would otherwise persist whole in one store-lock hold — in bounded steps, each
// under its own hold. A background pass already at it is waited for instead.
// Called WITHOUT s.mu.
func (s *FlatSQLStore) persistPendingArenaCompaction(reason string) {
	if !engineArenaRuntimeCompaction {
		return
	}
	var pending bool
	func() {
		defer s.lockRead("engine arena compaction: pending?")()
		if s.ps != nil || s.engineDB == nil || s.engine == nil || s.closedErr() != nil || !s.engine.HasArenaCompaction() {
			return
		}
		stats, err := s.engineDB.ArenaStats()
		pending = err == nil && stats.Compacting
	}()
	if !pending {
		return
	}
	c := s.arenaCompactor()
	if !c.running.CompareAndSwap(false, true) {
		for deadline := time.Now().Add(2 * time.Minute); c.running.Load() && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
		}
		return
	}
	defer c.running.Store(false)
	if _, err := s.compactEngineArena(reason); err != nil && !errors.Is(err, errArenaCompactionStopped) {
		log.Warnf("FlatSQL engine arena compaction (%s): %v", reason, err)
	}
}

// errArenaCompactionStopped: the store closed or the engine was replaced
// between steps. Not a failure: the new engine starts clean, and a swap
// already committed is finished by the next open.
var errArenaCompactionStopped = errors.New("arena compaction stopped: store closed or engine replaced")

// compactEngineArena runs one compaction in bounded steps, each under its own
// store write-lock hold, then verifies the residency ledger and re-reads the
// arena measure.
func (s *FlatSQLStore) compactEngineArena(reason string) (engineArenaCompactionAccount, error) {
	acct := engineArenaCompactionAccount{Reason: reason}
	started := time.Now()
	var epoch uint64
	var reportBefore flatsqlrt.ArenaCompaction
	deferrals := 0
	for {
		var status flatsqlrt.CompactStatus
		var stepErr error
		err := func() error {
			defer s.lockWrite("engine arena compaction: step")()
			if s.closedErr() != nil || s.engineDB == nil || s.engine == nil {
				return errArenaCompactionStopped
			}
			if acct.Steps == 0 {
				epoch = s.engineEpoch
				s.noteArenaBefore(&acct)
				if report, err := s.engineDB.LastArenaCompaction(); err == nil {
					reportBefore = report
				}
			} else if s.engineEpoch != epoch {
				return errArenaCompactionStopped
			} else if stats, err := s.engineDB.ArenaStats(); err == nil && !stats.Compacting {
				// Finished by someone else (a flush persisted a swap made in
				// a transaction): do not start a second one here.
				status = flatsqlrt.CompactDone
				return nil
			}
			t0 := time.Now()
			status, stepErr = s.engineDB.CompactArenaStep(engineArenaCompactStepBytes)
			held := time.Since(t0)
			acct.Steps++
			if held > acct.LongestHold {
				acct.LongestHold = held
			}
			if mem, err := s.engine.MemoryStats(); err == nil && mem.Bytes > acct.MemoryDuring {
				acct.MemoryDuring = mem.Bytes
			}
			return nil
		}()
		if err != nil {
			return acct, err
		}
		if hook := engineArenaCompactStepHook; hook != nil && status != flatsqlrt.CompactDone {
			hook(acct.Steps)
		}
		if stepErr != nil {
			if s.engine != nil && s.engine.Poisoned() {
				return acct, fmt.Errorf("engine poisoned during arena compaction: %w", stepErr)
			}
			return acct, stepErr
		}
		if status == flatsqlrt.CompactDone {
			break
		}
		if status == flatsqlrt.CompactDeferred {
			// A transaction was open on the engine while we held the lock
			// (one that spans holds): the swap is in memory, the persist
			// waits for the transaction to end.
			if deferrals++; deferrals > 600 {
				return acct, errors.New("arena compaction: a transaction stayed open for a minute; the persist waits for the next flush")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	acct.Total = time.Since(started)
	err := func() error {
		defer s.lockWrite("engine arena compaction: settle")()
		if s.closedErr() != nil || s.engineDB == nil || s.engineEpoch != epoch {
			return errArenaCompactionStopped
		}
		if report, err := s.engineDB.LastArenaCompaction(); err == nil && report == reportBefore {
			// Nothing was dead any more (a pass before a refusal, or a flush,
			// got there first): nothing to settle or record.
			s.syncEngineArenaBytesLocked()
			return nil
		}
		return s.settleArenaCompactionLocked(&acct)
	}()
	return acct, err
}

// compactEngineArenaBeforeRefusalLocked is the synchronous form, for a mirror
// that would pass the budget: every step under the lock the caller already
// holds. It runs only when the engine says something is dead, and reports
// whether `need` more bytes now fit. Caller holds s.mu for writing.
func (s *FlatSQLStore) compactEngineArenaBeforeRefusalLocked(need int64) bool {
	if !engineArenaRuntimeCompaction || s.ps != nil || s.engineDB == nil || s.engine == nil || !s.engine.HasArenaCompaction() || s.engine.Poisoned() {
		return false
	}
	stats, err := s.engineDB.ArenaStats()
	if err != nil || (stats.DeadBytes == 0 && !stats.Compacting) {
		return false
	}
	acct := engineArenaCompactionAccount{Reason: "a mirror would pass the budget", Synchronous: true}
	s.noteArenaBefore(&acct)
	started := time.Now()
	deferred := false
	for {
		t0 := time.Now()
		status, err := s.engineDB.CompactArenaStep(engineArenaCompactStepBytes)
		held := time.Since(t0)
		acct.Steps++
		if held > acct.LongestHold {
			acct.LongestHold = held
		}
		if err != nil {
			log.Warnf("FlatSQL engine arena compaction before a refusal failed: %v", err)
			return false
		}
		if status == flatsqlrt.CompactDone {
			break
		}
		if status == flatsqlrt.CompactDeferred {
			// The mirror runs inside the caller's transaction: the arena was
			// swapped in memory, and a background pass persists it once the
			// transaction has committed.
			deferred = true
			break
		}
	}
	acct.Total = time.Since(started)
	if !deferred {
		if err := s.settleArenaCompactionLocked(&acct); err != nil {
			log.Warnf("FlatSQL engine arena compaction before a refusal: %v", err)
		}
		return s.engineArenaBytes.Load()+need <= engineArenaBudgetBytes
	}
	s.syncEngineArenaBytesLocked()
	if stats, err := s.engineDB.ArenaStats(); err == nil {
		acct.ArenaAfter, acct.CapacityAfter = stats.Size, stats.Capacity
	}
	if report, err := s.engineDB.LastArenaCompaction(); err == nil {
		acct.Report = report
	}
	if mem, err := s.engine.MemoryStats(); err == nil {
		acct.MemoryAfter, acct.MemoryDuring = mem.Bytes, mem.Bytes
	}
	acct.Deferred = true
	s.arenaCompactor().record(acct)
	log.Infof("FlatSQL engine arena compacted in memory before a refusal: %d MiB -> %d MiB of frames, %d rows kept, %d dropped, in %s (inside the write transaction); persisting it in the background",
		acct.ArenaBefore>>20, acct.ArenaAfter>>20, acct.Report.KeptRecords, acct.Report.DroppedRecords, acct.Total.Round(time.Millisecond))
	c := s.arenaCompactor()
	if c.running.CompareAndSwap(false, true) {
		go func() {
			defer c.running.Store(false)
			if _, err := s.compactEngineArena("persist a swap made before a refusal"); err != nil {
				log.Warnf("FlatSQL engine arena compaction (persist) stopped: %v", err)
			}
		}()
	}
	return s.engineArenaBytes.Load()+need <= engineArenaBudgetBytes
}

func (s *FlatSQLStore) noteArenaBefore(acct *engineArenaCompactionAccount) {
	if stats, err := s.engineDB.ArenaStats(); err == nil {
		acct.ArenaBefore, acct.CapacityBefore = stats.Size, stats.Capacity
	}
	if mem, err := s.engine.MemoryStats(); err == nil {
		acct.MemoryBefore = mem.Bytes
	}
}

// settleArenaCompactionLocked re-reads the arena measure, checks the
// residency ledger against the compacted partitions, and records and logs the
// account. Caller holds s.mu for writing.
func (s *FlatSQLStore) settleArenaCompactionLocked(acct *engineArenaCompactionAccount) error {
	s.syncEngineArenaBytesLocked()
	if stats, err := s.engineDB.ArenaStats(); err == nil {
		acct.ArenaAfter, acct.CapacityAfter = stats.Size, stats.Capacity
	}
	if report, err := s.engineDB.LastArenaCompaction(); err == nil {
		acct.Report = report
	}
	if mem, err := s.engine.MemoryStats(); err == nil {
		acct.MemoryAfter = mem.Bytes
	}
	repaired, err := s.repairEngineResidencyAfterCompactionLocked()
	acct.LedgerRepaired = repaired
	s.arenaCompactor().record(*acct)
	log.Infof("FlatSQL engine arena compacted (%s): %d MiB -> %d MiB of frames, capacity %d MiB -> %d MiB; %d rows kept, %d dropped, %d sequence runs; %d steps in %s, longest store-lock hold %s%s; engine memory %d MiB -> %d MiB; ledger rows repaired %d",
		acct.Reason, acct.ArenaBefore>>20, acct.ArenaAfter>>20, acct.CapacityBefore>>20, acct.CapacityAfter>>20,
		acct.Report.KeptRecords, acct.Report.DroppedRecords, acct.Report.SequenceRuns,
		acct.Steps, acct.Total.Round(time.Millisecond), acct.LongestHold.Round(time.Millisecond),
		map[bool]string{true: " (synchronous, before a refusal)", false: ""}[acct.Synchronous],
		acct.MemoryBefore>>20, acct.MemoryAfter>>20, repaired)
	return err
}

// repairEngineResidencyAfterCompactionLocked keeps the residency ledger exact
// across a compaction. A compaction drops exactly the rows the engine had
// tombstoned, and a tombstone is normally set together with the ledger delete
// — but in the caller's transaction, which can roll back after the engine
// call: that row was hidden while the ledger still held it, and a warm boot
// used to resurrect it (tombstones are not persisted), which healed the pair.
// After a compaction the row is gone for good. So each partition's ledger
// count is compared with the engine's; where they differ, the ledger rows the
// engine does not hold are dropped and their records mirrored again from the
// control tables. Caller holds s.mu for writing.
func (s *FlatSQLStore) repairEngineResidencyAfterCompactionLocked() (int, error) {
	rows, err := s.db.Query(`SELECT schema_name, source, COUNT(*) FROM sdn_engine_rows GROUP BY schema_name, source`)
	if err != nil {
		return 0, fmt.Errorf("read the residency ledger: %w", err)
	}
	ledger := map[string]map[string]int64{}
	for rows.Next() {
		var schema, source string
		var n int64
		if err := rows.Scan(&schema, &source, &n); err != nil {
			rows.Close()
			return 0, err
		}
		if ledger[schema] == nil {
			ledger[schema] = map[string]int64{}
		}
		ledger[schema][source] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	repaired := 0
	for schemaName, bySource := range ledger {
		binding, routed := s.engineRoutedSchemaFor(schemaName)
		if !routed {
			continue
		}
		sources := make([]string, 0, len(bySource))
		for source := range bySource {
			if s.engineSources[source] {
				sources = append(sources, source)
			}
		}
		states, err := s.enginePartitionStates(binding.Table, sources)
		if err != nil {
			return repaired, fmt.Errorf("engine partition counts for %s: %w", schemaName, err)
		}
		var lost []engineResidencyRow
		for _, source := range sources {
			if states[source].count == bySource[source] {
				continue
			}
			missing, err := s.engineRowsMissingFromPartition(schemaName, binding.Table, source)
			if err != nil {
				return repaired, err
			}
			lost = append(lost, missing...)
		}
		if len(lost) == 0 {
			continue
		}
		for start := 0; start < len(lost); start += engineTombstoneChunk {
			end := start + engineTombstoneChunk
			if end > len(lost) {
				end = len(lost)
			}
			args := make([]any, 0, end-start+1)
			args = append(args, engineLedgerSchema(schemaName))
			for _, row := range lost[start:end] {
				args = append(args, row.cid)
			}
			if _, err := s.db.Exec(fmt.Sprintf(`DELETE FROM sdn_engine_rows WHERE schema_name = ? AND cid IN (%s)`,
				strings.TrimSuffix(strings.Repeat("?, ", end-start), ", ")), args...); err != nil {
				return repaired, err
			}
		}
		s.engineResidentAdd(schemaName, -int64(len(lost)))
		records, err := s.readEngineRecordsByCID(schemaName, lost)
		if err != nil {
			return repaired, fmt.Errorf("read %s records to mirror again: %w", schemaName, err)
		}
		n, err := s.ingestEngineRecordSourcesLocked(schemaName, records)
		repaired += len(lost)
		log.Warnf("FlatSQL engine residency: %s — %d ledger row(s) named rows the compacted arena does not hold (a tombstone whose ledger delete rolled back); mirrored %d record(s) again", schemaName, len(lost), n)
		if err != nil {
			return repaired, err
		}
	}
	return repaired, nil
}

// engineRowsMissingFromPartition lists the ledger rows of one partition whose
// sequence the engine partition does not hold, in one engine statement.
func (s *FlatSQLStore) engineRowsMissingFromPartition(schemaName, table, source string) ([]engineResidencyRow, error) {
	res, err := s.engineDB.Query(`SELECT e.cid, e.seq FROM sdn_engine_rows e
		WHERE e.schema_name = ?1 AND e.source = ?2 AND NOT EXISTS (
			SELECT 1 FROM `+quoteEngineRelation(enginePartition(table, source))+` p WHERE p._rowid = e.seq)`,
		engineLedgerSchema(schemaName), source)
	if err != nil {
		return nil, fmt.Errorf("ledger rows missing from %s: %w", enginePartition(table, source), err)
	}
	out := make([]engineResidencyRow, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 2 {
			continue
		}
		cid, _ := row[0].(string)
		seq, _ := row[1].(int64)
		out = append(out, engineResidencyRow{cid: cid, source: source, seq: seq})
	}
	return out, nil
}
