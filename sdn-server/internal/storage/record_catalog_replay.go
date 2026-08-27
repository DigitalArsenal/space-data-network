package storage

// record_catalog_replay.go: the batched, reader-friendly record-catalog replay.
//
// The catalog journal is the DURABLE record state; the SQL control tables that
// feed /api/v1/stats, /api/v1/data/index and batch clear live in the in-memory
// FlatSQL engine and must be rebuilt from the journal on every daemon start.
//
// Two properties matter and the naive replay got both wrong:
//
//  1. THROUGHPUT. The engine is a WASM module: every Exec is a host->guest call
//     that parses and plans the statement, and flatsqldrv follows each Exec with
//     a second `SELECT last_insert_rowid(), changes()` query to populate
//     driver.Result. A per-frame replay therefore costs ~4 WASM SQL round-trips
//     per record (record row + index row, each doubled by the metadata query).
//     On the dev catalog (171,379 frames) that is ~562,000 engine calls and the
//     replay never finished. Applying the SAME rows as multi-row INSERTs
//     collapses that to a few hundred calls: measured 2,101 rec/s -> 200,565
//     rec/s (95x) on an AOT engine, with FLAT per-batch cost (no O(n^2)).
//
//  2. AVAILABILITY. The replay must not starve readers. Holding the store write
//     lock for the whole replay blocks every s.mu.RLock reader — the same
//     failure as the 2026-07-06 production blackout that produced
//     storeWriteChunkSize (see flatsql.go). The replay therefore runs in bounded
//     WINDOWS, taking and RELEASING the store write lock around each one, so
//     reads interleave.
//
// Releasing the lock between windows lets live writes interleave with a replay
// of historical frames, which is safe for upserts (records are content
// addressed, so a replayed historical upsert of a CID carries byte-identical
// derived index/tag values as the live re-add of that same CID) but NOT for
// destructive frames: a historical RecordDelete / SourceKeep / GCOlderThan must
// never land on top of a LIVE re-add of the same CID. The hydration shield below
// closes exactly that hole.
//
// THE CROSS-BOOT REPLAY CURSOR (sdn-replay-checkpoint-resume) — RULED UNSOUND,
// NOW RESOLVED. Read the whole note before touching the resume path.
//
// This file used to say a persisted cursor "is UNSOUND here and must not be
// reintroduced", and the reasoning was correct FOR THE STORE IT DESCRIBED:
// engine.CreateDatabase opened a brand-new IN-MEMORY "sdn-control" database on
// every process start, so the control tables a saved cursor would "resume into"
// were always EMPTY, and seeking past frame N without applying frames [0,N)
// would silently drop every record those frames describe. The note's own
// condition for lifting it was explicit — "a correct cursor requires the
// control-table STATE to survive the restart too, not just an offset int64".
//
// THAT CONDITION IS NOW MET. The control database is a real file
// (flatsql_boot_state.go), so an offset means something. The resume is
// nevertheless hedged at every step, because the failure mode of getting it
// wrong is silent data loss rather than a crash:
//
//   - the mark carries a DIGEST of the journal prefix it was taken against, so a
//     compacted or replaced journal can never be resumed into;
//   - it is only ever written under the store write lock, after the rows it
//     covers are committed;
//   - and any doubt at all returns offset 0 AND discards the control database,
//     so "from zero" is also "from empty" — which matters because producer rows
//     replay with INSERT OR IGNORE and cannot be corrected in place.
//
// The safeguards this file added while the cursor was impossible all REMAIN, and
// none of them were load-bearing only for the old shape:
//  1. FAIL CLOSED while incomplete (export.go ErrRecordCatalogHydrating) — a
//     query-selected export (archive pin, publication shard, manifest replay
//     verification) refuses rather than silently landing a partial answer. A
//     warm boot still reports un-hydrated until its tail is applied.
//  2. Honest logging: a cancelled replay says it restarts from where the mark
//     stands, never that it "resumes" further than it did.

import (
	"fmt"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
)

const (
	// recordCatalogReplayWindow bounds how many journal frames one replay
	// window applies: one store-write-lock hold + one control transaction.
	// Readers interleave BETWEEN windows, so this sets the worst-case reader
	// stall. Batched, a 5,000-frame window is tens of milliseconds — far inside
	// the two-second reader budget that storeWriteChunkSize was tuned against.
	recordCatalogReplayWindow = 5000

	// recordCatalogReplayParamBudget bounds how many bind parameters may appear
	// in ONE multi-row statement, which is what turns ~4 engine calls per record
	// into ~4 engine calls per couple-hundred records.
	//
	// It is a PARAMETER budget, not a row count, because the three replay
	// statements have different widths (8, 9/10 and 10 columns) and the real
	// limit lives in the engine, not in the row count: FlatSQL binds a statement's
	// parameters recursively inside the WASM guest, so an over-wide VALUES list
	// overflows the guest stack — a 500-row upsert (~4,500-5,000 params) aborts
	// with "calling stack:3351 ... flatsql_query_params" and burns a core until it
	// does. Measured safe and linear up to 200 rows x 9 cols (1,800 params, 7.5
	// µs/row); 1,600 keeps a ~3x margin under the observed failure point while
	// capturing essentially all of the batching win (8.4 µs/row at 100 rows vs
	// 7.5 µs/row at 200).
	recordCatalogReplayParamBudget = 1600
)

// replayStatementRows returns how many rows of a cols-wide statement fit inside
// the engine's per-statement parameter budget (always at least one).
func replayStatementRows(cols int) int {
	if cols <= 0 {
		return 1
	}
	if rows := recordCatalogReplayParamBudget / cols; rows > 0 {
		return rows
	}
	return 1
}

// hydrationShield records the (schema, cid) pairs written by LIVE traffic while
// a full record-catalog replay is in flight.
//
// The replay reconstructs the control tables from journal frames that were
// written BEFORE this process started, and it runs concurrently with live
// publishes (that is the whole point: boot must not block). So the replay can
// reach a historical "delete CID X" frame at a moment when a live publish has
// ALREADY re-added X. Applying that stale delete would silently drop a record
// that genuinely exists — the exact class of "records disappear" bug this loop
// is trying to eliminate.
//
// The shield makes the rule explicit: a historical destructive frame is IGNORED
// for any CID that live traffic (re)wrote during the replay. Live state is by
// definition newer than anything in the pre-boot journal prefix, so "live wins"
// is the correct ordering, and it is the ordering that a single-threaded replay
// followed by live traffic would have produced.
//
// It is populated from the journal append path (recordCatalogJournal.onAppend),
// which every live catalog mutation funnels through, under the writer's store
// write lock — the same lock the replay takes to apply a window. A live row and
// its shield entry are therefore published atomically with respect to the
// replay: the replay can never see the row without also seeing the shield entry.
//
// Outside a replay the shield is inactive and every operation is a no-op.
type hydrationShield struct {
	mu     sync.Mutex
	active bool
	cids   map[string]struct{}
}

func shieldKey(schemaName, cid string) string { return schemaName + "\x00" + cid }

func (h *hydrationShield) activate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active = true
	h.cids = make(map[string]struct{})
}

func (h *hydrationShield) deactivate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active = false
	h.cids = nil
}

// note records a live write. Only upserts are shielded: a live DELETE of X does
// not need protection from a historical delete of X (they agree).
func (h *hydrationShield) note(events []recordCatalogEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active {
		return
	}
	for _, event := range events {
		switch event.Kind {
		case recordCatalogEventRecordUpsert, recordCatalogEventTagUpsert:
			if event.SchemaName != "" && event.CID != "" {
				h.cids[shieldKey(event.SchemaName, event.CID)] = struct{}{}
			}
		}
	}
}

// has reports whether live traffic (re)wrote this record during the replay, in
// which case a historical destructive frame for it must be skipped.
func (h *hydrationShield) has(schemaName, cid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active {
		return false
	}
	_, ok := h.cids[shieldKey(schemaName, cid)]
	return ok
}

// shieldedCIDs returns the live-written CIDs for one schema, for the range
// destructive frames (SourceKeep / GCOlderThan) to exclude from their delete set.
func (h *hydrationShield) shieldedCIDs(schemaName string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active || len(h.cids) == 0 {
		return nil
	}
	prefix := schemaName + "\x00"
	out := make([]string, 0, len(h.cids))
	for key := range h.cids {
		if strings.HasPrefix(key, prefix) {
			out = append(out, strings.TrimPrefix(key, prefix))
		}
	}
	return out
}

// unshieldTempCIDs removes live-written CIDs from a staged delete set, so a
// historical range delete (SourceKeep / GCOlderThan) cannot take out a record
// that live traffic re-added while the replay was running. No-op when no replay
// is in flight, which is the steady state.
func (s *FlatSQLStore) unshieldTempCIDs(tempTable, schemaName string) error {
	shielded := s.hydrationShield.shieldedCIDs(schemaName)
	if len(shielded) == 0 {
		return nil
	}
	perStatement := replayStatementRows(1)
	for start := 0; start < len(shielded); start += perStatement {
		end := start + perStatement
		if end > len(shielded) {
			end = len(shielded)
		}
		chunk := shielded[start:end]
		args := make([]any, 0, len(chunk))
		for _, cid := range chunk {
			args = append(args, cid)
		}
		sqlText := fmt.Sprintf(`DELETE FROM %s WHERE cid IN (%s)`, tempTable, placeholders(len(chunk), 1))
		if _, err := s.db.Exec(flatsqldrv.WithoutJournal(sqlText), args...); err != nil {
			return fmt.Errorf("exclude live-written records from %s: %w", tempTable, err)
		}
	}
	return nil
}

// placeholders builds "(?,?),(?,?)" style VALUES lists: rows groups of cols.
func placeholders(rows, cols int) string {
	var b strings.Builder
	for r := 0; r < rows; r++ {
		if r > 0 {
			b.WriteByte(',')
		}
		if cols > 1 {
			b.WriteByte('(')
		}
		for c := 0; c < cols; c++ {
			if c > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('?')
		}
		if cols > 1 {
			b.WriteByte(')')
		}
	}
	return b.String()
}

// replayRowIDState resolves the sdn_record_index rowid for every replayed frame
// so that a replay can NEVER produce a rowid collision.
//
// This is the bug that actually killed the daemon. sdn_record_index is ONE
// global table: its rowid is the durable datasync cursor and the hot-window sort
// key, and the journal carries it per frame (Index.RowID) so a replay can
// reproduce it. But the journal's rowids are NOT globally unique in practice:
// while the catalog was never being replayed at boot (the very bug 4bbeb380 set
// out to fix), sdn_record_index started EMPTY on every restart, so each boot's
// live writes handed out rowids from 1 again. A journal that spans several boots
// therefore contains, e.g., OMM.fbs rowid=1 AND PRR.fbs rowid=1 for different
// CIDs.
//
// Replaying those back into one table is a rowid PRIMARY KEY violation, and
// ON CONFLICT(schema_name, cid) does not cover it. The FlatSQL engine is
// compiled WITHOUT exception support, so the constraint failure does not surface
// as a Go error — it aborts the guest ("unreachable" inside
// flatsql_query_params), POISONS the WASM runtime, and every later engine call
// spins forever. That is the 100%-CPU wedge: the replay dies at frame 6, and
// afterwards /api/v1/stats and anything else that touches the engine hangs.
//
// Resolution rule (deterministic, order-preserving):
//   - keep the journal's rowid when nothing else owns it — the common case, so
//     the datasync cursor is reproduced exactly for effectively every record;
//   - a repeat frame for a row that already owns that rowid keeps it;
//   - otherwise hand out a fresh rowid above every rowid the journal or the
//     table can contain, so it can collide with nothing, now or later.
//
// ONE ALLOCATOR, TWO LANES (2026-07-30). The rule above is only sound if the
// replay is the ONLY writer handing out rowids, and a CHUNKED replay is not: it
// releases store.mu between windows precisely so readers and live publishes can
// interleave. A live publish inserts its index row WITHOUT an explicit rowid, so
// the engine gives it MAX(rowid)+1 of the PARTIALLY hydrated table — a rowid
// squarely inside the range the replay has not reached yet and will later ask
// for by name. The ownership map is snapshotted once at replay start and never
// learns about that row, so the replay hands the same rowid out a second time:
//
//	replay record index batch: flatsqlrt: query_params:
//	  SQL execution error: UNIQUE constraint failed: sdn_record_index.rowid
//
// Reproduced off-host on host-01's real 450 MB journal at ~285,000 frames under
// live writes; on host-01 itself it is why the catalog had NEVER finished
// hydrating (before flatsql b26ed45 the same collision surfaced as an
// `unreachable` guest trap, which hid the cause for months).
//
// The fix is a PARTITION with a single allocator (recordIndexRowIDs), not a
// bigger lock — holding store.mu for all 1.34M frames is the reader blackout
// this file exists to prevent. While a replay is in flight:
//
//   - [1 .. journalMaxRowID] is the REPLAY's exclusive band: only frames whose
//     rowid the journal recorded may land there, so the datasync cursor is still
//     reproduced verbatim for effectively every historical record;
//   - everything above it is a single monotonic counter that BOTH lanes draw
//     from — the replay for a fresh rowid, and the live path for an EXPLICIT one
//     instead of the engine's MAX(rowid)+1.
//
// Live rows therefore land above the whole historical range (which is also the
// truthful cursor ordering: they are newer than every pre-boot frame), and no
// journal rowid can ever be occupied by a live row.
type replayRowIDState struct {
	owner map[int64]string   // rowid -> shieldKey(schema, cid)
	alloc *recordIndexRowIDs // shared fresh-rowid counter (never nil)
}

// recordIndexRowIDs is the store-wide sdn_record_index rowid allocator.
//
// Idle in the steady state: allocateLive reports "no" and the live upsert keeps
// its ordinary 9-column statement, so nothing about normal writes changes. A
// replay calls begin() to open the shared band and end() when it is done.
type recordIndexRowIDs struct {
	mu     sync.Mutex
	active int   // replays in flight (hydrateMu serializes, but be honest)
	next   int64 // next rowid no other writer can be holding
}

// begin opens the shared band strictly above `above` — which the replay sets to
// max(table MAX(rowid), highest rowid the journal will ask for). It never lowers
// the counter, so a second replay cannot reissue rowids the first one gave out.
func (a *recordIndexRowIDs) begin(above int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if above+1 > a.next {
		a.next = above + 1
	}
	a.active++
}

func (a *recordIndexRowIDs) end() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active > 0 {
		a.active--
	}
}

// reserve hands the REPLAY a rowid from the shared band. Always allocates: the
// replay owns its own begin/end pairing.
func (a *recordIndexRowIDs) reserve() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	rid := a.next
	a.next++
	return rid
}

// allocateLive hands a LIVE writer an explicit rowid, but only while a replay is
// in flight. Outside a replay it returns false and the caller inserts without a
// rowid, exactly as before — the engine's MAX(rowid)+1 is correct when nothing
// else is handing rowids out.
func (a *recordIndexRowIDs) allocateLive() (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active == 0 {
		return 0, false
	}
	rid := a.next
	a.next++
	return rid, true
}

// newReplayRowIDState seeds ownership from any rows already in sdn_record_index
// (the admin force re-sync replays into a populated table) and opens the shared
// fresh-rowid band above both the table's max rowid and the highest rowid the
// journal will ask for. The caller MUST pair this with store.recordIndexRowIDs.end().
func newReplayRowIDState(store *FlatSQLStore, journalMaxRowID int64) (*replayRowIDState, error) {
	return newReplayRowIDStateFor(store, journalMaxRowID, nil)
}

// newReplayRowIDStateFor is newReplayRowIDState with the ownership seed narrowed
// to a known set of rowids.
//
// wanted == nil means "seed from the whole table" — the cold path, unchanged.
// A WARM RESUME passes the rowids its tail actually carries, and that is what
// keeps crash recovery O(tail) instead of O(catalog): seeding from the whole
// table would load every sdn_record_index row into a Go map to replay a hundred
// frames, which on a catalog-scale store is most of the boot. Ownership only
// ever has to answer "is THIS rowid already someone else's", and the only rowids
// asked about are the ones in the frames being applied.
func newReplayRowIDStateFor(store *FlatSQLStore, journalMaxRowID int64, wanted []int64) (*replayRowIDState, error) {
	state := &replayRowIDState{owner: map[int64]string{}, alloc: &store.recordIndexRowIDs}

	var maxExisting int64
	if err := store.db.QueryRow(`SELECT COALESCE(MAX(rowid), 0) FROM sdn_record_index`).Scan(&maxExisting); err != nil {
		return nil, fmt.Errorf("record index max rowid: %w", err)
	}
	if maxExisting > 0 && (wanted == nil || len(wanted) > 0) {
		if err := seedRowIDOwners(store, state, wanted); err != nil {
			return nil, err
		}
	}

	above := maxExisting
	if journalMaxRowID > above {
		above = journalMaxRowID
	}
	state.alloc.begin(above)
	return state, nil
}

// seedRowIDOwners fills state.owner. wanted == nil reads the whole table; a
// non-empty slice is queried in bounded IN() chunks so one huge tail cannot
// build a statement with a hundred thousand bind parameters.
func seedRowIDOwners(store *FlatSQLStore, state *replayRowIDState, wanted []int64) error {
	scan := func(query string, args ...any) error {
		rows, err := store.db.Query(query, args...)
		if err != nil {
			return fmt.Errorf("seed record index rowid owners: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var rid int64
			var schemaName, cid string
			if err := rows.Scan(&rid, &schemaName, &cid); err != nil {
				return fmt.Errorf("scan record index rowid owner: %w", err)
			}
			state.owner[rid] = shieldKey(schemaName, cid)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate record index rowid owners: %w", err)
		}
		return nil
	}

	if wanted == nil {
		return scan(`SELECT rowid, schema_name, cid FROM sdn_record_index`)
	}

	const chunk = 500
	for start := 0; start < len(wanted); start += chunk {
		end := start + chunk
		if end > len(wanted) {
			end = len(wanted)
		}
		batch := wanted[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 0, len(batch))
		for _, rid := range batch {
			args = append(args, rid)
		}
		if err := scan(
			`SELECT rowid, schema_name, cid FROM sdn_record_index WHERE rowid IN (`+placeholders+`)`,
			args...); err != nil {
			return err
		}
	}
	return nil
}

// resolve returns the rowid to insert for this frame, per the rule above.
func (r *replayRowIDState) resolve(event recordCatalogEvent) int64 {
	key := shieldKey(event.SchemaName, event.CID)
	if rid := event.Index.RowID; rid > 0 {
		switch owner, taken := r.owner[rid]; {
		case !taken:
			r.owner[rid] = key
			return rid
		case owner == key:
			return rid // same row, repeat frame: it already owns this rowid
		}
	}
	rid := r.alloc.reserve()
	r.owner[rid] = key
	return rid
}

// recordCatalogReplayBatch accumulates consecutive upsert frames so they can be
// applied as multi-row INSERTs instead of one engine round-trip per record.
// Frames are kept in journal order; the batch is flushed before any frame that
// must observe the batch's effect (a destructive frame), so replay order is
// preserved exactly.
type recordCatalogReplayBatch struct {
	records []recordCatalogEvent
	tags    []recordCatalogEvent
}

func (b *recordCatalogReplayBatch) len() int { return len(b.records) + len(b.tags) }

func (b *recordCatalogReplayBatch) reset() {
	b.records = b.records[:0]
	b.tags = b.tags[:0]
}

// flush applies the accumulated frames to the control tables through exec.
//
// Dedup rules mirror the per-frame SQL exactly, so batching is semantically
// identical to applying the frames one at a time:
//
//   - producer record table: INSERT OR IGNORE -> the FIRST frame for a CID wins.
//   - sdn_record_index:      ON CONFLICT DO UPDATE -> the LAST frame wins.
//   - sdn_record_source_tags: ON CONFLICT DO UPDATE -> the LAST frame wins.
//
// Deduplicating in Go is also what makes the multi-row form legal: SQLite
// rejects an ON CONFLICT DO UPDATE that would touch the same row twice in one
// statement, and the dev catalog carries ~110k record frames for ~60k distinct
// CIDs (records are re-published across batches).
func (b *recordCatalogReplayBatch) flush(store *FlatSQLStore, exec sqlExecer, tableFor func(recordCatalogEvent) (string, error), rowIDs *replayRowIDState) error {
	if err := b.flushRecords(store, exec, tableFor, rowIDs); err != nil {
		return err
	}
	return b.flushTags(store, exec)
}

func (b *recordCatalogReplayBatch) flushRecords(store *FlatSQLStore, exec sqlExecer, tableFor func(recordCatalogEvent) (string, error), rowIDs *replayRowIDState) error {
	if len(b.records) == 0 {
		return nil
	}

	// Group by destination producer table, keeping FIRST-wins per CID to match
	// INSERT OR IGNORE.
	tableOrder := make([]string, 0, 4)
	byTable := make(map[string][]recordCatalogEvent)
	seenRow := make(map[string]struct{}, len(b.records))

	// sdn_record_index is LAST-wins per (schema, cid): overwrite in place so the
	// final slice preserves first-appearance order while carrying last-seen
	// values.
	indexAt := make(map[string]int, len(b.records))
	indexRows := make([]recordCatalogEvent, 0, len(b.records))

	for _, event := range b.records {
		if strings.TrimSpace(event.SchemaName) == "" || strings.TrimSpace(event.CID) == "" {
			return fmt.Errorf("record upsert requires schema and cid")
		}
		tableName, err := tableFor(event)
		if err != nil {
			return err
		}
		rowKey := tableName + "\x00" + event.CID
		if _, dup := seenRow[rowKey]; !dup {
			seenRow[rowKey] = struct{}{}
			if _, known := byTable[tableName]; !known {
				tableOrder = append(tableOrder, tableName)
			}
			byTable[tableName] = append(byTable[tableName], event)
		}

		idxKey := shieldKey(event.SchemaName, event.CID)
		if at, ok := indexAt[idxKey]; ok {
			indexRows[at] = event
		} else {
			indexAt[idxKey] = len(indexRows)
			indexRows = append(indexRows, event)
		}
	}

	for _, tableName := range tableOrder {
		if err := store.insertRecordRowsBatch(exec, tableName, byTable[tableName]); err != nil {
			return err
		}
	}

	// Resolve every index row to a rowid that cannot collide. Done AFTER the
	// (schema, cid) dedupe so a repeat frame never burns a fresh rowid, and in
	// journal order so the assignment is deterministic across runs.
	resolved := make([]int64, len(indexRows))
	for i, event := range indexRows {
		resolved[i] = rowIDs.resolve(event)
	}
	return store.insertRecordIndexBatch(exec, indexRows, resolved)
}

func (b *recordCatalogReplayBatch) flushTags(store *FlatSQLStore, exec sqlExecer) error {
	if len(b.tags) == 0 {
		return nil
	}
	// LAST-wins on the 8-column ON CONFLICT target.
	at := make(map[string]int, len(b.tags))
	rows := make([]recordCatalogEvent, 0, len(b.tags))
	for _, event := range b.tags {
		if strings.TrimSpace(event.SchemaName) == "" || strings.TrimSpace(event.CID) == "" {
			return fmt.Errorf("source tag upsert requires schema and cid")
		}
		tags := normalizeSourceTags(event.Tags)
		if err := ValidateSourceTags(tags); err != nil {
			return err
		}
		event.Tags = tags
		key := strings.Join([]string{
			event.SchemaName, event.CID, tags.ProviderID, tags.SourceName,
			tags.BatchID, tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey,
		}, "\x00")
		if idx, ok := at[key]; ok {
			rows[idx] = event
			continue
		}
		at[key] = len(rows)
		rows = append(rows, event)
	}
	return store.insertSourceTagsBatch(exec, rows)
}

// insertRecordRowsBatch writes producer (producer, standard) metadata rows as
// multi-row INSERT OR IGNORE statements.
func (s *FlatSQLStore) insertRecordRowsBatch(exec sqlExecer, tableName string, events []recordCatalogEvent) error {
	const cols = 8
	perStatement := replayStatementRows(cols)
	for start := 0; start < len(events); start += perStatement {
		end := start + perStatement
		if end > len(events) {
			end = len(events)
		}
		chunk := events[start:end]
		args := make([]any, 0, len(chunk)*cols)
		for _, event := range chunk {
			var signatureHex any
			if strings.TrimSpace(event.SignatureHex) != "" {
				signatureHex = strings.TrimSpace(event.SignatureHex)
			}
			args = append(args, event.CID, event.PeerID, event.Timestamp, event.StreamPath,
				event.StreamOffset, event.RecordLength, signatureHex, event.CreatedAt)
		}
		sqlText := fmt.Sprintf(`
			INSERT OR IGNORE INTO %s (
				cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex, created_at
			)
			VALUES %s
		`, tableName, placeholders(len(chunk), cols))
		if _, err := exec.Exec(flatsqldrv.WithoutJournal(sqlText), args...); err != nil {
			return fmt.Errorf("replay record metadata batch into %s: %w", tableName, err)
		}
	}
	return nil
}

// insertRecordIndexBatch writes sdn_record_index rows as multi-row upserts, each
// with the rowid the replay resolved for it (rowIDs[i] pairs with events[i]).
// Every row carries an explicit, collision-free rowid, so the statement can
// never trip the rowid PRIMARY KEY — which in this engine is not a Go error but
// a guest abort that poisons the runtime (see replayRowIDState).
func (s *FlatSQLStore) insertRecordIndexBatch(exec sqlExecer, events []recordCatalogEvent, rowIDs []int64) error {
	if len(events) == 0 {
		return nil
	}
	if len(rowIDs) != len(events) {
		return fmt.Errorf("record index batch: %d rowids for %d events", len(rowIDs), len(events))
	}

	const cols = 10
	perStatement := replayStatementRows(cols)
	for start := 0; start < len(events); start += perStatement {
		end := start + perStatement
		if end > len(events) {
			end = len(events)
		}
		chunk := events[start:end]
		args := make([]any, 0, len(chunk)*cols)
		for i, event := range chunk {
			args = append(args, rowIDs[start+i])
			args = append(args, recordCatalogIndexArgs(event)...)
		}
		sqlText := fmt.Sprintf(`
			INSERT INTO sdn_record_index (
				rowid, schema_name, cid, norad_cat_id, entity_id, object_type, ops_status_code, epoch_unix, epoch_day, source_timestamp
			)
			VALUES %s
			ON CONFLICT(schema_name, cid) DO UPDATE SET
				norad_cat_id = excluded.norad_cat_id,
				entity_id = excluded.entity_id,
				object_type = excluded.object_type,
				ops_status_code = excluded.ops_status_code,
				epoch_unix = excluded.epoch_unix,
				epoch_day = excluded.epoch_day,
				source_timestamp = excluded.source_timestamp
		`, placeholders(len(chunk), cols))
		if _, err := exec.Exec(flatsqldrv.WithoutJournal(sqlText), args...); err != nil {
			return fmt.Errorf("replay record index batch: %w", err)
		}
	}
	return nil
}

// recordCatalogIndexArgs returns the nine index bind values for one frame, with
// the SAME nil-vs-value normalization the per-frame path uses.
func recordCatalogIndexArgs(event recordCatalogEvent) []any {
	index := event.Index
	var norad any
	if index.HasNoradCatID {
		norad = index.NoradCatID
	}
	var entity any
	if index.EntityID != "" {
		entity = index.EntityID
	}
	var objectType any
	if index.ObjectType != "" {
		objectType = index.ObjectType
	}
	var opsStatusCode any
	if index.OpsStatusCode != "" {
		opsStatusCode = index.OpsStatusCode
	}
	var epoch any
	if index.HasEpochUnix {
		epoch = index.EpochUnix
	}
	var day any
	if index.EpochDay != "" {
		day = index.EpochDay
	}
	sourceTimestamp := index.SourceTimestamp
	if sourceTimestamp == 0 {
		sourceTimestamp = event.Timestamp
	}
	return []any{
		event.SchemaName, event.CID, norad, entity, objectType,
		opsStatusCode, epoch, day, sourceTimestamp,
	}
}

// insertSourceTagsBatch writes sdn_record_source_tags rows as multi-row upserts.
func (s *FlatSQLStore) insertSourceTagsBatch(exec sqlExecer, events []recordCatalogEvent) error {
	// FOUR columns now, not ten: the seven provenance strings are interned
	// once per distinct tuple (source_provenance.go) and the row carries the
	// dictionary id. A replay batch of a million cellular records therefore
	// binds one integer per row where it used to bind seven strings.
	const cols = 4
	perStatement := replayStatementRows(cols)
	for start := 0; start < len(events); start += perStatement {
		end := start + perStatement
		if end > len(events) {
			end = len(events)
		}
		chunk := events[start:end]
		args := make([]any, 0, len(chunk)*cols)
		for _, event := range chunk {
			tags := normalizeSourceTags(event.Tags)
			provenanceID, err := s.provenanceIDTx(exec, tags)
			if err != nil {
				return err
			}
			args = append(args, event.SchemaName, event.CID, provenanceID, event.CreatedAt)
			// THIS is the one tag write in the tree that does not maintain
			// sdn_record_source_summary — the per-record writers both call
			// incrementSourceSummary. Recording the lane is what lets the
			// rebuild find what changed without scanning the 1.8 M-row tag
			// table; without it the rebuild is back to a full DISTINCT and the
			// 2 m 41 s boot this task removed. See sourceSummaryLanes.
			s.replayedSourceLanes.note(sourceSummaryLane{
				SchemaName: event.SchemaName,
				ProviderID: tags.ProviderID,
				SourceName: tags.SourceName,
				BatchID:    tags.BatchID,
			})
		}
		sqlText := fmt.Sprintf(`
			INSERT INTO `+sourceTagRowsTable+` (schema_name, cid, provenance_id, created_at)
			VALUES %s
			ON CONFLICT(schema_name, cid, provenance_id)
			DO UPDATE SET created_at = excluded.created_at
		`, placeholders(len(chunk), cols))
		if _, err := exec.Exec(flatsqldrv.WithoutJournal(sqlText), args...); err != nil {
			return fmt.Errorf("replay source tag batch: %w", err)
		}
	}
	return nil
}
