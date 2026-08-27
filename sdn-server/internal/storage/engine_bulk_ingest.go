package storage

// Batched engine-mirror ingest.
//
// WHY THIS FILE EXISTS — the measurement, not the folklore.
//
// The cellular lane landed $TBS at ~47-87 records/s and two hypotheses were on
// the table: (a) the engine lock, and (b) the guest/host call count, because
// the daemon's dispatch account showed `flatsql_ingest_one_with_source` called
// once per record (740,400 calls in a 30-minute window). BOTH ARE REFUTED BY
// A/B ON THE SHIPPED ENGINE (4,000 real $TBS records, one box, one pin):
//
//	single (one flatsql_ingest_one_with_source per record) ....    47 rows/s
//	single, all of them inside ONE explicit transaction ....... 27,810 rows/s
//	single, journal_mode=WAL + synchronous=NORMAL .............    84 rows/s
//	bulk stream (ONE flatsql_ingest_with_source) inside one tx  65,414 rows/s
//
// The lock was held 0% of the window and the bulk export — which already
// existed — was EXACTLY as slow as the per-record one outside a transaction.
// The cost was never the calls: it was ONE IMPLICIT TRANSACTION PER INGEST on
// a `journal_mode=truncate` database, i.e. a journal write + fsync per record.
// (The engine's VFS refuses WAL: `PRAGMA journal_mode=WAL` answers `truncate`.)
//
// So the fix is the transaction, and the stream export is the free 2.4x on top
// of it. This file is that fix, and nothing else: it is application-blind, it
// knows no standard, and it changes no stored bytes — the mirror lands the
// same rows in the same shadow tables, in the same order, under the same
// source names. Every failure falls back to the exact per-record path this
// replaced, because the engine mirror is a CACHE over the stream files and the
// record catalog, and a cache is never worth a lost write.

import (
	"encoding/binary"
	"fmt"
)

// engineIngestGroup is a run of consecutive payloads that share one source.
// Order within a group is the caller's order; groups keep the caller's
// order too, so the engine sees the same sequence it always did.
type engineIngestGroup struct {
	source   string
	payloads [][]byte
}

// groupEngineIngests collapses consecutive same-source payloads into runs.
// Consecutive, not global, so a caller that interleaves sources still gets
// its original ingest ORDER — the engine assigns record sequences on ingest
// and the hot-window eviction is oldest-first over them.
func groupEngineIngests(sources []string, payloads [][]byte) []engineIngestGroup {
	groups := make([]engineIngestGroup, 0, 4)
	for i := range payloads {
		if n := len(groups); n > 0 && groups[n-1].source == sources[i] {
			groups[n-1].payloads = append(groups[n-1].payloads, payloads[i])
			continue
		}
		groups = append(groups, engineIngestGroup{source: sources[i], payloads: [][]byte{payloads[i]}})
	}
	return groups
}

// sizePrefixedStream frames payloads for flatsql_ingest_with_source: a
// little-endian uint32 length in front of each buffer, which is the same
// framing the FlatSQL stream files use.
func sizePrefixedStream(payloads [][]byte) []byte {
	total := 0
	for _, p := range payloads {
		total += 4 + len(p)
	}
	stream := make([]byte, 0, total)
	var prefix [4]byte
	for _, p := range payloads {
		binary.LittleEndian.PutUint32(prefix[:], uint32(len(p)))
		stream = append(stream, prefix[:]...)
		stream = append(stream, p...)
	}
	return stream
}

// engineBulkIngestThreshold is the batch size below which the per-record path
// is used unchanged. One BEGIN/COMMIT costs a journal round trip of its own,
// so a two-record mirror should not pay for one.
const engineBulkIngestThreshold = 4

// bulkIngestEngineGroups ingests groups under ONE engine transaction.
//
// It returns the number of payloads ingested and whether the batched path
// completed. `false` means the caller must fall through to the per-record
// path: the transaction has already been rolled back, so the engine holds
// none of these rows and re-ingesting them one by one is correct, not a
// double-write.
//
// Caller holds the store write lock (engine transactions are engine-global —
// see flatsqldrv.Open's contract), and has already registered every source.
func (s *FlatSQLStore) bulkIngestEngineGroups(groups []engineIngestGroup) (int64, bool) {
	if s.engineDB == nil {
		return 0, false
	}
	total := 0
	for _, g := range groups {
		total += len(g.payloads)
	}
	if total < engineBulkIngestThreshold {
		return 0, false
	}

	if _, err := s.engineDB.Query("BEGIN"); err != nil {
		log.Warnf("FlatSQL engine: begin batched mirror transaction: %v", err)
		return 0, false
	}
	ingested := int64(0)
	for _, g := range groups {
		if _, err := s.engineDB.IngestWithSource(sizePrefixedStream(g.payloads), g.source); err != nil {
			log.Warnf("FlatSQL engine: batched mirror of %d record(s) into %q failed, falling back to per-record ingest: %v",
				len(g.payloads), g.source, err)
			if _, rbErr := s.engineDB.Query("ROLLBACK"); rbErr != nil {
				// A rollback that does not take leaves the engine in a state
				// only a rebuild can trust. Say so loudly; the caller's
				// per-record fallback still runs and the boot rebuild is the
				// backstop.
				log.Warnf("FlatSQL engine: rollback after a failed batched mirror: %v", rbErr)
			}
			return 0, false
		}
		ingested += int64(len(g.payloads))
	}
	if _, err := s.engineDB.Query("COMMIT"); err != nil {
		log.Warnf("FlatSQL engine: commit batched mirror transaction: %v", err)
		if _, rbErr := s.engineDB.Query("ROLLBACK"); rbErr != nil {
			log.Warnf("FlatSQL engine: rollback after a failed batched-mirror commit: %v", rbErr)
		}
		return 0, false
	}
	return ingested, true
}

// ensureEngineSourcesFor registers every source a batch will use, BEFORE the
// batch's transaction opens. RegisterSource creates the source's shadow table
// and rebuilds the unified views; doing that inside the ingest transaction
// would put DDL and a view rebuild on the write path's critical section.
//
// It returns the sources it could not register, so the caller drops exactly
// those payloads instead of failing the batch.
func (s *FlatSQLStore) ensureEngineSourcesFor(schemaName string, sources []string) (map[string]bool, error) {
	unusable := map[string]bool{}
	seen := map[string]bool{}
	for _, source := range sources {
		if seen[source] {
			continue
		}
		seen[source] = true
		if err := s.ensureEngineSource(source); err != nil {
			log.Warnf("FlatSQL engine: register source %q for %s: %v", source, schemaName, err)
			if s.engine.Poisoned() {
				return nil, fmt.Errorf("FlatSQL engine poisoned registering source %q: %w", source, err)
			}
			unusable[source] = true
		}
	}
	return unusable, nil
}

// engineRebuildFlushSize is how many hot-window records the boot rebuild
// accumulates before flushing them under one transaction. It bounds the
// transient memory the rebuild holds (records are a few hundred bytes each)
// while keeping the fsync count at one per batch instead of one per record.
const engineRebuildFlushSize = 4096

// flushEngineRebuildBatch mirrors one accumulated rebuild batch and reports
// (ingested, skipped). Sources are already registered by the caller.
func (s *FlatSQLStore) flushEngineRebuildBatch(payloads [][]byte, sources []string) (int, int) {
	if n, ok := s.bulkIngestEngineGroups(groupEngineIngests(sources, payloads)); ok {
		return int(n), len(payloads) - int(n)
	}
	rebuilt, skipped := 0, 0
	for i, payload := range payloads {
		if _, err := s.engineDB.IngestOneWithSource(payload, sources[i]); err != nil {
			log.Warnf("FlatSQL engine rebuild: ingest record: %v", err)
			skipped++
			continue
		}
		rebuilt++
	}
	return rebuilt, skipped
}
