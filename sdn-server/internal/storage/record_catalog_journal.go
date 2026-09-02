package storage

import (
	"bytes"
	"container/heap"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const (
	recordCatalogJournalFileName = "record-catalog.flatsqlmeta"
	recordCatalogFrameVersion    = byte(1)

	recordCatalogEventRecordUpsert byte = 1
	recordCatalogEventTagUpsert    byte = 2
	recordCatalogEventRecordDelete byte = 3
	recordCatalogEventSourceKeep   byte = 4
	recordCatalogEventGCOlderThan  byte = 5

	recordCatalogReplayBatchSize = 10000
)

type recordCatalogJournal struct {
	mu          sync.Mutex
	f           *os.File
	path        string
	readOnly    bool
	replayLimit int64

	// onAppend observes every LIVE catalog mutation. It is the single funnel
	// through which the hydration shield learns which records live traffic
	// (re)wrote while a background replay is in flight, so a historical
	// destructive frame can never clobber them. Nil outside a store.
	onAppend func([]recordCatalogEvent)

	// digest is the RUNNING frame-header fingerprint covering [0, digestOffset),
	// used by the warm-boot handshake (flatsql_boot_state.go). Keeping it
	// running is what makes a periodic checkpoint cost O(frames since the last
	// one) instead of O(catalog) — this lock is the one live appends take, so an
	// O(catalog) walk here would be a new writer stall on every interval. Both
	// fields are guarded by mu.
	digest       hash.Hash
	digestOffset int64

	// engineHotWindowPasses counts the FULL journal reads the engine
	// hot-window rebuild has made on this journal. A pass costs the whole file
	// (5.5 GB on host-02), so the number of them is the boot cost; the routed
	// standard count must never be a multiplier on it. Guarded by mu, read by
	// the test that pins that invariant.
	engineHotWindowPasses int
}

type recordCatalogEvent struct {
	Kind byte

	SchemaName   string
	CID          string
	PeerID       string
	StreamPath   string
	StreamOffset int64
	RecordLength int64
	SignatureHex string
	Timestamp    int64
	CreatedAt    int64

	Index recordCatalogIndex
	Tags  SourceTags

	CutoffUnix int64
}

type recordCatalogIndex struct {
	RowID           int64
	SourceTimestamp int64
	HasNoradCatID   bool
	NoradCatID      int64
	EntityID        string
	ObjectType      string
	OpsStatusCode   string
	HasEpochUnix    bool
	EpochUnix       int64
	EpochDay        string
}

type recordCatalogEngineCandidate struct {
	event     recordCatalogEvent
	tags      SourceTags
	tagTime   int64
	heapIndex int
}

type recordCatalogEngineCandidateHeap []*recordCatalogEngineCandidate

func (h recordCatalogEngineCandidateHeap) Len() int { return len(h) }

func (h recordCatalogEngineCandidateHeap) Less(i, j int) bool {
	return recordCatalogEngineSortKey(h[i].event) < recordCatalogEngineSortKey(h[j].event)
}

func (h recordCatalogEngineCandidateHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIndex = i
	h[j].heapIndex = j
}

func (h *recordCatalogEngineCandidateHeap) Push(x any) {
	c := x.(*recordCatalogEngineCandidate)
	c.heapIndex = len(*h)
	*h = append(*h, c)
}

func (h *recordCatalogEngineCandidateHeap) Pop() any {
	old := *h
	n := len(old)
	c := old[n-1]
	c.heapIndex = -1
	*h = old[:n-1]
	return c
}

func recordCatalogEngineSortKey(event recordCatalogEvent) int64 {
	if event.Index.RowID > 0 {
		return event.Index.RowID
	}
	if event.CreatedAt > 0 {
		return event.CreatedAt
	}
	return event.Timestamp
}

func openRecordCatalogJournal(path string, readOnly bool) (*recordCatalogJournal, error) {
	if readOnly {
		f, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			if os.IsNotExist(err) {
				return &recordCatalogJournal{path: path, readOnly: true}, nil
			}
			return nil, fmt.Errorf("record catalog: open read-only: %w", err)
		}
		valid, err := scanRecordCatalogValidLength(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		return &recordCatalogJournal{f: f, path: path, readOnly: true, replayLimit: valid}, nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("record catalog: open: %w", err)
	}
	valid, err := scanRecordCatalogValidLength(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Truncate(valid); err != nil {
		f.Close()
		return nil, fmt.Errorf("record catalog: truncate torn tail: %w", err)
	}
	if _, err := f.Seek(valid, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &recordCatalogJournal{f: f, path: path}, nil
}

func scanRecordCatalogValidLength(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	var off int64
	var hdr [8]byte
	for off < size {
		if size-off < 8 {
			break
		}
		if _, err := f.ReadAt(hdr[:], off); err != nil {
			return 0, err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		crc := binary.LittleEndian.Uint32(hdr[4:])
		if n == 0 || off+8+n > size {
			break
		}
		payload := make([]byte, n)
		if _, err := f.ReadAt(payload, off+8); err != nil {
			return 0, err
		}
		if crc32.ChecksumIEEE(payload) != crc {
			break
		}
		off += 8 + n
	}
	return off, nil
}

func (j *recordCatalogJournal) Append(event recordCatalogEvent) error {
	return j.AppendAll([]recordCatalogEvent{event})
}

func (j *recordCatalogJournal) AppendAll(events []recordCatalogEvent) error {
	if j == nil || len(events) == 0 {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.readOnly {
		return fmt.Errorf("record catalog journal %s is read-only", j.path)
	}
	var out []byte
	for _, event := range events {
		payload, err := encodeRecordCatalogEvent(event)
		if err != nil {
			return err
		}
		var hdr [8]byte
		binary.LittleEndian.PutUint32(hdr[0:], uint32(len(payload)))
		binary.LittleEndian.PutUint32(hdr[4:], crc32.ChecksumIEEE(payload))
		out = append(out, hdr[:]...)
		out = append(out, payload...)
	}
	if _, err := j.f.Write(out); err != nil {
		return err
	}
	if err := j.f.Sync(); err != nil {
		return err
	}
	// Tell the hydration shield which records this live write touched. The
	// caller holds the store write lock across this append, and a concurrent
	// background replay applies each window under that same lock, so the live
	// row and its shield entry become visible to the replay together — the
	// replay can never see the row without the shield entry that protects it.
	if j.onAppend != nil {
		j.onAppend(events)
	}
	return nil
}

func (j *recordCatalogJournal) Replay(store *FlatSQLStore) (int, error) {
	return j.replay(store, nil)
}

// ReplayFrom applies the journal starting at a byte offset instead of at zero.
//
// A non-zero start is ONLY sound because the control tables it resumes into now
// survive the restart (the database is a real file — flatsql_boot_state.go).
// The caller must have proved the offset names THIS journal; resumeOffset is
// the only thing that produces one, and it returns 0 for every doubt. Frames
// below the offset keep the rows already on disk — that is the entire win: a
// warm boot pays for the tail, not for the catalog.
func (j *recordCatalogJournal) ReplayFrom(store *FlatSQLStore, from int64) (int, error) {
	if from <= 0 {
		return j.replay(store, nil)
	}
	return j.replayFramesFrom(context.Background(), store, nil, false, from)
}

// replay applies the whole catalog INLINE, without touching the store write
// lock: it is used where the caller already owns the store (initial open) or
// already holds the write lock (engine-poison recovery in engine_link.go).
// progress may be nil.
func (j *recordCatalogJournal) replay(store *FlatSQLStore, progress func(done int)) (int, error) {
	return j.replayFrames(context.Background(), store, progress, false)
}

// replayChunked applies the catalog in bounded windows, ACQUIRING AND RELEASING
// the store write lock around each one so that readers (/api/v1/stats and every
// other s.mu.RLock path) interleave and are never starved for the length of the
// replay. It is the form the post-boot background hydration uses.
//
// See record_catalog_replay.go for why releasing the lock mid-replay is safe:
// upserts are content-addressed and therefore idempotent against live re-adds,
// and the hydration shield stops a historical destructive frame from landing on
// top of a live re-add of the same CID.
func (j *recordCatalogJournal) replayChunked(ctx context.Context, store *FlatSQLStore, progress func(done int)) (int, error) {
	return j.replayFrames(ctx, store, progress, true)
}

// replayFrames drives the journal. Frames are applied in journal order; runs of
// upserts are accumulated and written as multi-row INSERTs (see
// recordCatalogReplayBatch), which is what turns a per-record, ~4-engine-calls
// replay into a batched one.
//
// When chunked, each window takes the store write lock, applies at most
// recordCatalogReplayWindow frames in one transaction, and releases the lock.
// Lock order is always store.mu -> journal.mu, matching the live write path
// (which holds store.mu across its journal append), so the two can never
// deadlock against each other.
func (j *recordCatalogJournal) replayFrames(ctx context.Context, store *FlatSQLStore, progress func(done int), chunked bool) (int, error) {
	return j.replayFramesFrom(ctx, store, progress, chunked, 0)
}

// replayFramesFrom is replayFrames with an explicit start offset. `from` must be
// a frame boundary in THIS journal; see ReplayFrom.
func (j *recordCatalogJournal) replayFramesFrom(ctx context.Context, store *FlatSQLStore, progress func(done int), chunked bool, from int64) (int, error) {
	if j == nil || j.f == nil {
		return 0, nil
	}

	// Snapshot the journal length once: frames appended by LIVE writes after the
	// replay starts are, by construction, already applied to the control tables
	// by the writer itself and must not be replayed on top of newer state.
	j.mu.Lock()
	info, err := j.f.Stat()
	if err != nil {
		j.mu.Unlock()
		return 0, err
	}
	size := info.Size()
	if j.readOnly {
		size = j.replayLimit
	}
	j.mu.Unlock()

	// NOTHING TO REPLAY IS THE WHOLE POINT — TAKE IT BEFORE ANY O(CATALOG) WORK.
	//
	// A clean restart resumes with `from == size`. Everything below this line is
	// sized by the CATALOG, not by the tail: scanMaxRowID walks every frame
	// header in the journal, and newReplayRowIDState loads EVERY sdn_record_index
	// row into a Go map to seed the rowid-owner table. Measured on a 120k-frame
	// synthetic store, paying for those on a zero-frame resume dominated the
	// entire "warm" boot. Neither is needed when no frame will be applied: no
	// rowid is handed out, so there is nothing to keep out of the way of.
	if from > 0 && from >= size {
		return 0, nil
	}

	// The rowid resolver must know the highest rowid the journal will ask for
	// BEFORE it hands out any fresh rowid, or a fresh rowid could collide with an
	// explicit one in a later frame. One cheap scan of the frame headers gives it
	// (see replayRowIDState for why colliding rowids exist at all).
	//
	// A warm resume only needs the rowids the TAIL can ask for, so the scan
	// starts where the replay does, and it COLLECTS them so ownership can be
	// seeded for exactly those rows instead of for the whole index.
	warm := from > 0
	journalMaxRowID, tailRowIDs, err := j.scanRowIDsFrom(from, size, warm)
	if err != nil {
		return 0, err
	}
	// newReplayRowIDState OPENS the shared rowid band (store.recordIndexRowIDs):
	// for as long as this replay runs, live index inserts allocate an explicit
	// rowid above every rowid the journal can ask for instead of taking the
	// engine's MAX(rowid)+1 out of the half-hydrated table. Closing it is
	// mandatory — an unbalanced begin would leave the live path allocating
	// explicitly forever.
	rowIDs, err := newReplayRowIDStateFor(store, journalMaxRowID, tailRowIDs)
	if err != nil {
		return 0, err
	}
	defer store.recordIndexRowIDs.end()

	// A warm resume starts at `from`. It is clamped to the snapshot length so a
	// mark taken against a longer journal can never seek past the end, and an
	// out-of-range value degrades to a full replay rather than to silence.
	off := from
	if off < 0 || off > size {
		off = 0
	}
	count := 0
	knownProducerTables := map[string]bool{}

	for off < size {
		// Cancellation is checked BETWEEN windows: a shutdown mid-hydration
		// drains within one window instead of wedging the process.
		if err := ctx.Err(); err != nil {
			return count, err
		}
		applied, next, err := j.replayWindow(store, off, size, knownProducerTables, rowIDs, chunked)
		count += applied
		if err != nil {
			return count, err
		}
		if next == off {
			break // defensive: never spin on a zero-length window
		}
		off = next
		if progress != nil {
			progress(count)
		}
	}
	return count, nil
}

// replayWindow applies up to recordCatalogReplayWindow frames starting at off,
// under (when chunked) one store-write-lock hold and one transaction. It returns
// the number of frames applied and the offset to resume from.
// scanMaxRowID reads the journal's frame stream and returns the highest
// Index.RowID any record-upsert frame carries (0 if none). Header-only work plus
// a decode per frame — ~0.1s for the 171k-frame dev catalog.
func (j *recordCatalogJournal) scanMaxRowID(size int64) (int64, error) {
	return j.scanMaxRowIDFrom(0, size)
}

// scanMaxRowIDFrom is scanMaxRowID bounded to [from, size). A warm resume only
// ever asks for the rowids its TAIL carries, so scanning the whole journal for a
// 100-frame tail would make the resume O(catalog) again — which is the one thing
// it must not be. `from` must be a frame boundary.
func (j *recordCatalogJournal) scanMaxRowIDFrom(from, size int64) (int64, error) {
	max, _, err := j.scanRowIDsFrom(from, size, false)
	return max, err
}

// scanRowIDsFrom returns the highest explicit rowid in [from, size) and, when
// collect is set, every explicit rowid it saw. The set is what lets a warm
// resume seed rowid ownership for its TAIL only (newReplayRowIDStateFor) instead
// of loading the whole record index.
func (j *recordCatalogJournal) scanRowIDsFrom(from, size int64, collect bool) (int64, []int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	off := from
	if off < 0 || off > size {
		off = 0
	}
	var hdr [8]byte
	var max int64
	var ids []int64
	if collect {
		ids = []int64{}
	}
	for off < size {
		if _, err := j.f.ReadAt(hdr[:], off); err != nil {
			return 0, nil, err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		payload := make([]byte, n)
		if _, err := j.f.ReadAt(payload, off+8); err != nil {
			return 0, nil, err
		}
		event, err := decodeRecordCatalogEvent(payload)
		if err != nil {
			return 0, nil, fmt.Errorf("record catalog frame at %d: %w", off, err)
		}
		if event.Kind == recordCatalogEventRecordUpsert && event.Index.RowID > 0 {
			if event.Index.RowID > max {
				max = event.Index.RowID
			}
			if collect {
				ids = append(ids, event.Index.RowID)
			}
		}
		off += 8 + n
	}
	return max, ids, nil
}

func (j *recordCatalogJournal) replayWindow(
	store *FlatSQLStore,
	off, size int64,
	knownProducerTables map[string]bool,
	rowIDs *replayRowIDState,
	chunked bool,
) (int, int64, error) {
	if chunked {
		store.mu.Lock()
		defer store.mu.Unlock()
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	var batch recordCatalogReplayBatch
	tableOf := map[string]string{}
	tableFor := func(event recordCatalogEvent) (string, error) {
		name, ok := tableOf[event.CID+"\x00"+event.PeerID+"\x00"+event.SchemaName]
		if !ok {
			return ProducerStandardTableName(routedProducerID(event.PeerID), event.SchemaName)
		}
		return name, nil
	}

	// flush writes the accumulated upserts in ONE transaction. Producer tables
	// are created during accumulation (DDL, outside the transaction), so flush
	// only ever runs INSERTs.
	flush := func() error {
		if batch.len() == 0 {
			return nil
		}
		tx, err := store.db.Begin()
		if err != nil {
			return fmt.Errorf("begin replay batch: %w", err)
		}
		if err := batch.flush(store, tx, tableFor, rowIDs); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit replay batch: %w", err)
		}
		batch.reset()
		return nil
	}

	var hdr [8]byte
	applied := 0
	for off < size && applied < recordCatalogReplayWindow {
		if _, err := j.f.ReadAt(hdr[:], off); err != nil {
			return applied, off, err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		payload := make([]byte, n)
		if _, err := j.f.ReadAt(payload, off+8); err != nil {
			return applied, off, err
		}
		event, err := decodeRecordCatalogEvent(payload)
		if err != nil {
			return applied, off, fmt.Errorf("record catalog frame at %d: %w", off, err)
		}

		switch event.Kind {
		case recordCatalogEventRecordUpsert:
			tableName, err := ProducerStandardTableName(routedProducerID(event.PeerID), event.SchemaName)
			if err != nil {
				return applied, off, fmt.Errorf("record catalog frame at %d: %w", off, err)
			}
			if !knownProducerTables[tableName] {
				// DDL must not run inside the batch transaction.
				if _, err := store.ensureProducerStandardTable(routedProducerID(event.PeerID), event.SchemaName); err != nil {
					return applied, off, fmt.Errorf("record catalog frame at %d: %w", off, err)
				}
				knownProducerTables[tableName] = true
			}
			tableOf[event.CID+"\x00"+event.PeerID+"\x00"+event.SchemaName] = tableName
			batch.records = append(batch.records, event)
		case recordCatalogEventTagUpsert:
			batch.tags = append(batch.tags, event)
		default:
			// A destructive frame (RecordDelete / SourceKeep / GCOlderThan) must
			// observe everything the journal applied before it: flush first, then
			// apply it outside the batch transaction (these appliers write through
			// store.db directly, and consult the hydration shield so they can
			// never delete a record that live traffic re-added mid-replay).
			if err := flush(); err != nil {
				return applied, off, fmt.Errorf("record catalog frame at %d: %w", off, err)
			}
			if err := store.applyRecordCatalogEvent(event); err != nil {
				return applied, off, fmt.Errorf("record catalog frame at %d: %w", off, err)
			}
		}
		applied++
		off += 8 + n
	}

	if err := flush(); err != nil {
		return applied, off, err
	}
	return applied, off, nil
}

// engineHotWindowCandidateBudget bounds how many hot-window candidates ONE
// journal pass may hold across ALL schemas at once.
//
// The pass fans out into one heap per schema, so its peak memory is the SUM of
// the resident candidate sets, not one schema's. A box holding records in every
// routed standard would otherwise retain 400,000 ($OMM) + 225 x 10,000 generic
// events at once — a gigabyte of live heap on an 8 GB droplet.
//
// The budget is RESERVED at window creation, against the schema's own window
// limit, not measured after the fact: a heap that is small now can still grow to
// its limit later, so checking the live count would bound nothing. A schema
// whose reservation does not fit is deferred to a follow-up pass instead of
// allocating a heap. Deferral is safe precisely because a deferred schema has NO
// partial state — the follow-up pass reads it from offset 0, exactly as the
// first pass would have. The FIRST schema a pass meets is always admitted, so a
// single window larger than the whole budget (an operator-raised
// storage.engine_hot_window) still makes progress rather than starving.
//
// Passes are therefore ceil(live candidates / budget) — never one per schema.
// On every box in the fleet today (a handful of populated standards) that is
// exactly ONE pass.
//
// A var, not a const, so the test that proves deferral loses nothing can drive
// the multi-pass path with a small budget instead of a gigabyte fixture.
var engineHotWindowCandidateBudget = 600_000

// engineHotWindowCancelCheckFrames is how often a hot-window pass polls its
// context. The pass holds the store write lock, and Stop() waits on the
// goroutine that runs it: an uninterruptible multi-GB journal scan here is the
// shape that ran 15 of 22 host-01 stops into SIGKILL (see node.go
// StartBackgroundRecordCatalogHydration).
const engineHotWindowCancelCheckFrames = 4096

// recordCatalogEngineWindow is ONE schema's hot-window candidate set for the
// duration of a journal pass: the newest `limit` records of that schema that
// have survived every delete / source-keep / GC frame seen so far.
type recordCatalogEngineWindow struct {
	schemaName string
	limit      int
	byCID      map[string]*recordCatalogEngineCandidate
	candidates *recordCatalogEngineCandidateHeap
}

func newRecordCatalogEngineWindow(schemaName string, limit int) *recordCatalogEngineWindow {
	w := &recordCatalogEngineWindow{
		schemaName: schemaName,
		limit:      limit,
		byCID:      map[string]*recordCatalogEngineCandidate{},
		candidates: &recordCatalogEngineCandidateHeap{},
	}
	heap.Init(w.candidates)
	return w
}

func (w *recordCatalogEngineWindow) len() int { return w.candidates.Len() }

func (w *recordCatalogEngineWindow) remove(cid string) {
	c := w.byCID[cid]
	if c == nil {
		return
	}
	heap.Remove(w.candidates, c.heapIndex)
	delete(w.byCID, cid)
}

func (w *recordCatalogEngineWindow) add(event recordCatalogEvent) {
	if strings.TrimSpace(event.CID) == "" {
		return
	}
	if existing := w.byCID[event.CID]; existing != nil {
		existing.event = event
		heap.Fix(w.candidates, existing.heapIndex)
		return
	}
	candidate := &recordCatalogEngineCandidate{event: event}
	if w.candidates.Len() < w.limit {
		heap.Push(w.candidates, candidate)
		w.byCID[event.CID] = candidate
		return
	}
	if w.candidates.Len() == 0 || recordCatalogEngineSortKey(event) <= recordCatalogEngineSortKey((*w.candidates)[0].event) {
		return
	}
	evicted := heap.Pop(w.candidates).(*recordCatalogEngineCandidate)
	delete(w.byCID, evicted.event.CID)
	heap.Push(w.candidates, candidate)
	w.byCID[event.CID] = candidate
}

func (w *recordCatalogEngineWindow) updateTag(event recordCatalogEvent) {
	c := w.byCID[event.CID]
	if c == nil {
		return
	}
	if event.CreatedAt >= c.tagTime {
		c.tags = normalizeSourceTags(event.Tags)
		c.tagTime = event.CreatedAt
	}
}

func (w *recordCatalogEngineWindow) applySourceKeep(event recordCatalogEvent) {
	keep := normalizeSourceTags(event.Tags)
	for cid, c := range w.byCID {
		if c.tags.ProviderID == keep.ProviderID &&
			c.tags.SourceName == keep.SourceName &&
			c.tags.BatchID != keep.BatchID {
			w.remove(cid)
		}
	}
}

func (w *recordCatalogEngineWindow) applyGCOlderThan(cutoff int64) {
	for cid, c := range w.byCID {
		ts := c.event.Index.SourceTimestamp
		if ts == 0 {
			ts = c.event.Timestamp
		}
		if ts < cutoff {
			w.remove(cid)
		}
	}
}

func (w *recordCatalogEngineWindow) apply(event recordCatalogEvent) {
	switch event.Kind {
	case recordCatalogEventRecordUpsert:
		w.add(event)
	case recordCatalogEventTagUpsert:
		w.updateTag(event)
	case recordCatalogEventRecordDelete:
		w.remove(event.CID)
	case recordCatalogEventSourceKeep:
		w.applySourceKeep(event)
	case recordCatalogEventGCOlderThan:
		w.applyGCOlderThan(event.CutoffUnix)
	}
}

// ordered returns the surviving candidates oldest-first, which is the order the
// engine must ingest them in so the newest write of a cid wins.
func (w *recordCatalogEngineWindow) ordered() []*recordCatalogEngineCandidate {
	ordered := make([]*recordCatalogEngineCandidate, w.candidates.Len())
	copy(ordered, *w.candidates)
	sort.Slice(ordered, func(i, j int) bool {
		return recordCatalogEngineSortKey(ordered[i].event) < recordCatalogEngineSortKey(ordered[j].event)
	})
	return ordered
}

// ReplayEngineHotWindows rebuilds the engine hot window for MANY schemas in ONE
// PASS over the journal.
//
// WHY ONE PASS IS THE WHOLE POINT. There is exactly one record-catalog journal
// per store, and a pass over it is a full sequential read + decode of every
// frame — 5.5 GB and ~78 s on host-02 today. The per-schema filter is applied
// AFTER the frame is decoded, so scanning once per schema costs a whole file
// read per schema whether or not that schema owns a single record. With $OMM and
// $TBS routed that was 2 passes; with every embedded standard routed it would be
// 226, i.e. hours of boot under the store write lock (s.mu) with every API read,
// ingest and p2p operation queued behind it. Fanning out into per-schema heaps
// makes the cost of routing 226 standards the same ONE read this always paid for
// two — bounded in memory by engineHotWindowCandidateBudget and cancellable by
// ctx.
//
// limitFor gives each schema its own window budget (engineWindowFor: the full
// window for decorated standards, the smaller generic one for the rest). A
// schema whose limit is <= 0 is skipped entirely.
func (j *recordCatalogJournal) ReplayEngineHotWindows(ctx context.Context, store *FlatSQLStore, schemas []string, limitFor func(string) int) (int, error) {
	return j.ReplayEngineHotWindowsOpts(ctx, store, schemas, limitFor, engineReplayOptions{CallerHoldsStoreLock: true})
}

// engineReplayOptions shapes one engine hot-window replay.
type engineReplayOptions struct {
	// From is the journal offset to start at (0 = the whole journal); see
	// ReplayEngineHotWindowsFrom.
	From int64
	// CallerHoldsStoreLock says the caller already owns the store write lock
	// (open before the store is shared, RebuildDerivedState, poison recovery).
	// When false — the daemon's background hydration — the replay takes the
	// lock PER INGEST BATCH and releases it between batches, so every reader
	// lane (directory, sources, summary, identity) interleaves with the
	// rebuild instead of parking behind it for its whole duration (owner
	// 2026-09-02: reads are independent of data-layer maintenance).
	CallerHoldsStoreLock bool
}

// ReplayEngineHotWindowsFrom is ReplayEngineHotWindows over the journal TAIL
// starting at a frame boundary the caller has proved names this journal (the
// resume mark): the engine already holds every record below it from its
// persisted state, so the tail's records are ADDED to the resident counts and
// the hot window is re-enforced per schema afterwards. from <= 0 is the whole
// journal.
func (j *recordCatalogJournal) ReplayEngineHotWindowsFrom(ctx context.Context, store *FlatSQLStore, schemas []string, limitFor func(string) int, from int64) (int, error) {
	return j.ReplayEngineHotWindowsOpts(ctx, store, schemas, limitFor, engineReplayOptions{From: from, CallerHoldsStoreLock: true})
}

// ReplayEngineHotWindowsOpts is the general form of ReplayEngineHotWindows.
func (j *recordCatalogJournal) ReplayEngineHotWindowsOpts(ctx context.Context, store *FlatSQLStore, schemas []string, limitFor func(string) int, opts engineReplayOptions) (int, error) {
	if j == nil || j.f == nil || limitFor == nil {
		return 0, nil
	}
	pending := make([]string, 0, len(schemas))
	seen := map[string]bool{}
	for _, schemaName := range schemas {
		if seen[schemaName] || limitFor(schemaName) <= 0 {
			continue
		}
		seen[schemaName] = true
		pending = append(pending, schemaName)
	}
	if len(pending) == 0 {
		return 0, nil
	}

	total := 0
	for len(pending) > 0 {
		loaded, deferred, err := j.replayEngineHotWindowPass(ctx, store, pending, limitFor, opts)
		total += loaded
		if err != nil {
			return total, err
		}
		if len(deferred) >= len(pending) {
			// No progress is impossible by construction (the first schema a
			// pass meets always gets a heap), but a bug that made it possible
			// must not loop forever inside the store lock.
			log.Warnf("FlatSQL engine compact hot-window rebuild: %d schema(s) made no progress; stopping", len(deferred))
			return total, nil
		}
		pending = deferred
	}
	return total, nil
}

// replayEngineHotWindowPass is ONE journal read. It returns the records loaded
// into the engine and the schemas it deferred because the candidate budget was
// already spent when it first met them.
func (j *recordCatalogJournal) replayEngineHotWindowPass(ctx context.Context, store *FlatSQLStore, schemas []string, limitFor func(string) int, opts engineReplayOptions) (int, []string, error) {
	from := opts.From
	// THE JOURNAL LOCK COVERS THE SIZE SNAPSHOT ONLY. The file is append-only
	// and ReadAt on a prefix is safe against a concurrent append, so the scan
	// below runs WITHOUT j.mu: a live writer (store.mu -> journal.mu) must never
	// park on this pass while holding the store lock, or every reader parks
	// behind it too. Frames appended after the snapshot are ingested by the
	// writer itself and are simply not part of this pass.
	j.mu.Lock()
	info, err := j.f.Stat()
	if err != nil {
		j.mu.Unlock()
		return 0, nil, err
	}
	size := info.Size()
	if j.readOnly {
		size = j.replayLimit
	}
	// A TAIL replay starts at the resume mark. A mark at (or past) the end is
	// the clean-restart case: nothing to ingest, and no pass is counted.
	tail := from > 0
	if tail && from >= size {
		j.mu.Unlock()
		return 0, nil, nil
	}
	j.engineHotWindowPasses++
	j.mu.Unlock()

	// BOTH SPELLINGS PER SCHEMA. The journal frame carries the schema name the
	// WRITER passed — the bare code for every record the module SDK and the
	// wasm provider sources store ("OMM", "OEM", "IRM") — while `schemas` is
	// the routed set, spelled canonically. Keying `wanted` only canonically
	// made the pass skip every bare-spelled frame, so those records came back
	// resident-zero after a restart. canonical maps whatever the frame says
	// back to the routed name, which is what the per-schema heaps, the
	// deferral set and the residency bookkeeping are keyed by.
	wanted := make(map[string]int, len(schemas)*2)
	canonical := make(map[string]string, len(schemas)*2)
	for _, schemaName := range schemas {
		limit := limitFor(schemaName)
		for _, alias := range engineSchemaNameAliases(schemaName) {
			if _, taken := canonical[alias]; taken {
				continue
			}
			wanted[alias] = limit
			canonical[alias] = schemaName
		}
	}
	windows := make(map[string]*recordCatalogEngineWindow, len(schemas))
	deferredSet := map[string]bool{}
	reserved := 0

	var off int64
	if tail {
		off = from
	}
	var hdr [8]byte
	// ONE reusable frame buffer: a pass over host-01's journal decodes millions
	// of frames, and a per-frame allocation is millions of garbage buffers.
	var payload []byte
	frames := 0
	for off < size {
		if frames%engineHotWindowCancelCheckFrames == 0 {
			select {
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			default:
			}
		}
		frames++
		if _, err := j.f.ReadAt(hdr[:], off); err != nil {
			return 0, nil, err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		if int64(cap(payload)) < n {
			payload = make([]byte, n)
		}
		frame := payload[:n]
		if _, err := j.f.ReadAt(frame, off+8); err != nil {
			return 0, nil, err
		}
		off += 8 + n

		// The schema name is the first field of the frame, so an unwanted
		// schema costs a peek instead of a full decode + allocation.
		schemaName, ok := peekRecordCatalogEventSchema(frame)
		if !ok {
			return 0, nil, fmt.Errorf("record catalog frame at %d: malformed header", off-(8+n))
		}
		limit, isWanted := wanted[schemaName]
		if !isWanted || limit <= 0 {
			continue
		}
		schemaName = canonical[schemaName]
		if deferredSet[schemaName] {
			continue
		}
		window := windows[schemaName]
		if window == nil {
			if len(windows) > 0 && reserved+limit > engineHotWindowCandidateBudget {
				deferredSet[schemaName] = true
				continue
			}
			window = newRecordCatalogEngineWindow(schemaName, limit)
			windows[schemaName] = window
			reserved += limit
		}
		event, err := decodeRecordCatalogEvent(frame)
		if err != nil {
			return 0, nil, fmt.Errorf("record catalog frame at %d: %w", off-(8+n), err)
		}
		window.apply(event)
	}

	// Stream file handles are shared across schemas: the hot window reads the
	// same few append-only files millions of times otherwise.
	files := map[string]*os.File{}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()

	// EVERY SOURCE THE PASS WILL INGEST INTO IS REGISTERED UP FRONT, in one
	// locked section and one unified-view rebuild: registering inside a batch
	// rebuilds every view (~0.4 s on a 226-standard store) while readers wait.
	sourceSet := map[string]bool{}
	for _, schemaName := range schemas {
		window := windows[schemaName]
		if window == nil || deferredSet[schemaName] {
			continue
		}
		for _, c := range window.ordered() {
			sourceSet[engineSourceName(&c.tags)] = true
		}
	}
	if len(sourceSet) > 0 {
		names := make([]string, 0, len(sourceSet))
		for name := range sourceSet {
			names = append(names, name)
		}
		sort.Strings(names)
		unlock := func() {}
		if !opts.CallerHoldsStoreLock {
			unlock = store.lockWrite("engine hot-window: register sources")
		}
		store.registerEngineSourcesLocked(names)
		unlock()
	}

	loaded := 0
	// Schemas the journal never named are resident zero; set them in ONE
	// locked section rather than one lock per schema.
	var absent []string
	for _, schemaName := range schemas {
		if deferredSet[schemaName] {
			continue
		}
		select {
		case <-ctx.Done():
			return loaded, nil, ctx.Err()
		default:
		}
		window := windows[schemaName]
		if window == nil {
			// A tail says nothing about schemas it does not mention.
			if !tail {
				absent = append(absent, schemaName)
			}
			continue
		}
		n, err := j.loadEngineHotWindow(ctx, store, window, files, tail, opts.CallerHoldsStoreLock)
		loaded += n
		if err != nil {
			return loaded, nil, err
		}
	}
	if len(absent) > 0 {
		unlock := func() {}
		if !opts.CallerHoldsStoreLock {
			unlock = store.lockWrite("engine hot-window: resident-zero schemas")
		}
		for _, schemaName := range absent {
			store.engineResidentSet(schemaName, 0)
			store.engineSchemaLoadedSet(schemaName)
		}
		unlock()
	}

	deferred := make([]string, 0, len(deferredSet))
	for _, schemaName := range schemas {
		if deferredSet[schemaName] {
			deferred = append(deferred, schemaName)
		}
	}
	if len(deferred) > 0 {
		log.Infof("FlatSQL engine compact hot-window rebuild: candidate budget reached; %d schema(s) deferred to a follow-up pass", len(deferred))
	}
	return loaded, deferred, nil
}

// loadEngineHotWindow ingests one schema's surviving candidates into the engine,
// oldest first, and records the resulting residency.
func (j *recordCatalogJournal) loadEngineHotWindow(ctx context.Context, store *FlatSQLStore, window *recordCatalogEngineWindow, files map[string]*os.File, additive, callerHoldsLock bool) (int, error) {
	// lock takes the store write lock for ONE critical section (a batch
	// flush, or the residency bookkeeping) when this pass does not already
	// own it. Stream-file reads and payload checks happen outside it.
	lock := func(what string) func() {
		if callerHoldsLock {
			return func() {}
		}
		return store.lockWrite(what)
	}
	binding, routed := store.engineRoutedSchemaFor(window.schemaName)
	if !routed {
		if !additive {
			unlock := lock("engine hot-window: unrouted schema")
			store.engineResidentSet(window.schemaName, 0)
			unlock()
		}
		return 0, nil
	}
	var poisoned error
	flushed := 0
	loadStart := time.Now()
	batch := &engineIngestBatch{store: store}
	batch.onFlush = func(stream []byte, source string, n int) error {
		if store.engineHydrateBatchHook != nil {
			store.engineHydrateBatchHook() // outside the lock, once per batch
		}
		unlock := lock("engine hot-window: ingest batch " + window.schemaName)
		defer unlock()
		if err := store.ensureEngineSource(source); err != nil {
			log.Warnf("FlatSQL engine compact hot-window rebuild: register source %q: %v", source, err)
			if store.engine.Poisoned() {
				poisoned = fmt.Errorf("FlatSQL engine poisoned registering source %q: %w", source, err)
			}
			return err
		}
		if err := store.ingestEngineStreamLocked(stream, source); err != nil {
			log.Warnf("FlatSQL engine compact hot-window rebuild: ingest %d %s record(s): %v", n, window.schemaName, err)
			if store.engine.Poisoned() {
				poisoned = fmt.Errorf("FlatSQL engine poisoned during compact hot-window rebuild: %w", err)
			}
			return err
		}
		if additive {
			store.engineResidentAdd(window.schemaName, int64(n))
		}
		flushed++
		if flushed%64 == 0 {
			log.Infof("FlatSQL engine compact hot-window rebuild: %s — %d record(s) ingested so far (%d batches, %s)",
				window.schemaName, batch.total+int64(n), flushed, time.Since(loadStart).Round(time.Second))
		}
		return nil
	}
	for _, c := range window.ordered() {
		if poisoned != nil {
			return int(batch.total), poisoned
		}
		if batch.records == 0 {
			select {
			case <-ctx.Done():
				return int(batch.total), ctx.Err()
			default:
			}
		}
		event := c.event
		data, err := store.readStreamRecordCached(files, event.StreamPath, event.StreamOffset, event.RecordLength)
		if err != nil {
			log.Warnf("FlatSQL engine compact hot-window rebuild: read %s@%d: %v", event.StreamPath, event.StreamOffset, err)
			continue
		}
		// Same raw-read caveat as the per-schema boot rebuild: these bytes are
		// the stream frame verbatim, so anything the engine cannot route is
		// refused HERE rather than counted as resident after a silent drop
		// (engineIngestablePayload).
		payload, reason, ok := engineIngestablePayload(binding, data)
		if !ok {
			log.Warnf("FlatSQL engine compact hot-window rebuild: skip %s record at %s@%d: %s",
				window.schemaName, event.StreamPath, event.StreamOffset, reason)
			continue
		}
		// add flushes the previous batch on a source change or a full batch;
		// its error was already logged by onFlush and a poisoned engine is
		// caught at the top of the loop.
		_ = batch.add(payload, engineSourceName(&c.tags))
	}
	_ = batch.flush()
	if poisoned != nil {
		return int(batch.total), poisoned
	}
	loaded := int(batch.total)
	unlock := lock("engine hot-window: residency " + window.schemaName)
	if additive {
		if loaded > 0 {
			if err := store.enforceEngineHotWindowLocked(window.schemaName); err != nil {
				unlock()
				return loaded, err
			}
		}
	} else {
		store.engineResidentSet(window.schemaName, int64(loaded))
	}
	store.engineSchemaLoadedSet(window.schemaName)
	unlock()
	if loaded > 0 {
		log.Infof("FlatSQL engine compact hot-window rebuild: loaded %d %s records (window %d)", loaded, window.schemaName, window.limit)
	}
	return loaded, nil
}

// peekRecordCatalogEventSchema reads just the frame version, kind and schema
// name — the first three fields decodeRecordCatalogEvent reads — so a pass can
// reject a frame it does not want without decoding or allocating the rest.
// It must stay in lockstep with encodeRecordCatalogEvent's field order.
func peekRecordCatalogEventSchema(payload []byte) (string, bool) {
	if len(payload) < 2 || payload[0] != recordCatalogFrameVersion {
		return "", false
	}
	rest := payload[2:]
	n, read := binary.Uvarint(rest)
	if read <= 0 || uint64(len(rest)-read) < n {
		return "", false
	}
	return string(rest[read : read+int(n)]), true
}

// EngineHotWindowPasses reports how many full journal reads the engine
// hot-window rebuild has made on this journal.
func (j *recordCatalogJournal) EngineHotWindowPasses() int {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.engineHotWindowPasses
}

func (j *recordCatalogJournal) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return nil
	}
	return j.f.Close()
}

// reopen swaps the journal's file handle onto the on-disk inode currently at
// j.path (compaction.go, after a compaction commit has renamed a fresh
// compacted snapshot over the old journal path). The OLD *os.File descriptor
// keeps pointing at the pre-compaction inode across a rename -- os.Rename
// never invalidates an already-open fd, it just unlinks the old directory
// entry -- so without this, any subsequent Append would keep writing into
// that now-unlinked (and, once every referencing fd closes, garbage
// collected) inode and be silently lost forever. Re-scans the valid tail
// defensively (writeCompactedJournalSnapshot already fsynced the whole file
// before the rename, so this should simply validate the entire thing) and
// seeks to EOF so the next Append lands after the compacted content. Callers
// hold the store's s.mu (Lock).
func (j *recordCatalogJournal) reopen() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.readOnly {
		return fmt.Errorf("record catalog journal %s is read-only", j.path)
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("record catalog: reopen: %w", err)
	}
	valid, err := scanRecordCatalogValidLength(f)
	if err != nil {
		f.Close()
		return err
	}
	if err := f.Truncate(valid); err != nil {
		f.Close()
		return fmt.Errorf("record catalog: reopen truncate torn tail: %w", err)
	}
	if _, err := f.Seek(valid, io.SeekStart); err != nil {
		f.Close()
		return err
	}
	old := j.f
	j.f = f
	// A reopen is a NEW INODE (compaction renames a rewritten journal over this
	// path), so every byte the running digest folded in belongs to a file that
	// no longer exists. Reset it, or the next warm-boot mark would fingerprint
	// the old journal and name offsets in the new one.
	j.digest = nil
	j.digestOffset = 0
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// writeCompactedJournalSnapshot writes a brand-new compacted record-catalog
// journal to tmpPath (which must not already exist -- compaction.go always
// hands this a fresh, generation-suffixed temp path) containing exactly
// `events` framed identically to AppendAll's on-disk format, then fsyncs it.
// Called BEFORE the compaction commit manifest is written, so the temp is
// fully durable before it can ever be renamed over the live journal.
func writeCompactedJournalSnapshot(tmpPath string, events []recordCatalogEvent) error {
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("record catalog: create compaction snapshot %s: %w", tmpPath, err)
	}
	var out []byte
	for _, event := range events {
		payload, err := encodeRecordCatalogEvent(event)
		if err != nil {
			f.Close()
			return fmt.Errorf("record catalog: encode compaction snapshot event: %w", err)
		}
		var hdr [8]byte
		binary.LittleEndian.PutUint32(hdr[0:], uint32(len(payload)))
		binary.LittleEndian.PutUint32(hdr[4:], crc32.ChecksumIEEE(payload))
		out = append(out, hdr[:]...)
		out = append(out, payload...)
	}
	if len(out) > 0 {
		if _, err := f.Write(out); err != nil {
			f.Close()
			return fmt.Errorf("record catalog: write compaction snapshot %s: %w", tmpPath, err)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("record catalog: sync compaction snapshot %s: %w", tmpPath, err)
	}
	return f.Close()
}

// compactionOffsetRemap resolves a live frame's pre-compaction (streamPath,
// oldOffset) to its post-compaction offset. ok is false only for a
// programming-error case (a row referencing a frame CompactStreams did not
// itself just enumerate as live) -- callers treat that as a hard error
// rather than silently mis-writing an offset.
type compactionOffsetRemap func(streamPath string, oldOffset int64) (newOffset int64, ok bool)

// recordCatalogUpsertAttribution is the subset of a
// recordCatalogEventRecordUpsert event that identifies WHO durably owns a
// cid: the producer that published it and (optionally) their signature over
// it. See latestUpsertAttributionByCID.
type recordCatalogUpsertAttribution struct {
	PeerID       string
	SignatureHex string
}

// latestUpsertAttributionByCID scans the journal's CURRENT valid content
// (the pre-compaction journal -- called from buildCompactedRecordCatalogSnapshot
// BEFORE the compacted temp is written or anything is renamed) and returns,
// per schema, a map of cid -> the peer_id/signature_hex carried by the
// LATEST recordCatalogEventRecordUpsert event journaled for that cid (a map
// assignment per matching event naturally keeps only the last one seen,
// since events are read in on-disk/append order).
//
// This is the durable source of truth buildCompactedRecordCatalogSnapshot
// must draw producer attribution from, INSTEAD of recordReadSource's
// UNION-ALL-then-"GROUP BY cid" query: SQLite's own documentation says bare
// (non-aggregated) columns in a GROUP BY query with no MIN()/MAX() draw
// their value from an UNDEFINED row of the group. For a repeat-CID record
// co-published by producers A and B, that query can return producer B's
// peer_id/signature_hex even though the record's ONE durable journal event
// (the thing an actual reopen replays) was written for producer A --
// repeat-CID mirror writes (mirrorRoutedRecord / mirrorRoutedRecordFromExisting,
// producer_standard_tables.go) update producer B's live table immediately
// but are, BY DESIGN, never themselves journaled. So a plain reopen
// (replaying the untouched journal from an empty store) ALWAYS lands the
// cid under producer A -- never under a mirror -- and the compacted
// snapshot must match that exactly, not an arbitrary pick that can silently
// re-attribute the record to a different producer than an ordinary restart
// would have. (stream_path/stream_offset/record_length/timestamp/created_at
// are unaffected by this ambiguity: mirror writes always copy those columns
// verbatim from the record they are mirroring, so every producer-table row
// for a given cid already agrees on them byte-for-byte -- only peer_id and
// signature_hex, which a mirror legitimately writes as ITS OWN, can differ.)
func (j *recordCatalogJournal) latestUpsertAttributionByCID() (map[string]map[string]recordCatalogUpsertAttribution, error) {
	if j == nil || j.f == nil {
		return nil, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	info, err := j.f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if j.readOnly {
		size = j.replayLimit
	}

	out := make(map[string]map[string]recordCatalogUpsertAttribution)
	var off int64
	var hdr [8]byte
	for off < size {
		if _, err := j.f.ReadAt(hdr[:], off); err != nil {
			return nil, err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		payload := make([]byte, n)
		if _, err := j.f.ReadAt(payload, off+8); err != nil {
			return nil, err
		}
		event, err := decodeRecordCatalogEvent(payload)
		if err != nil {
			return nil, fmt.Errorf("record catalog frame at %d: %w", off, err)
		}
		if event.Kind == recordCatalogEventRecordUpsert {
			bySchema := out[event.SchemaName]
			if bySchema == nil {
				bySchema = make(map[string]recordCatalogUpsertAttribution)
				out[event.SchemaName] = bySchema
			}
			// Overwrite on every match: the LAST (highest-offset, i.e. most
			// recently journaled) upsert event for this cid wins, matching
			// what replay's own last-event-standing semantics would leave
			// live (e.g. a delete-then-re-store cycle re-journals a fresh
			// upsert under a possibly different producer).
			bySchema[event.CID] = recordCatalogUpsertAttribution{
				PeerID:       event.PeerID,
				SignatureHex: event.SignatureHex,
			}
		}
		off += 8 + n
	}
	return out, nil
}

// buildCompactedRecordCatalogSnapshot regenerates the MINIMAL set of record
// catalog events that reproduce the store's CURRENT live state with
// REMAPPED stream offsets -- a log-compaction collapsing the journal's
// entire append history into exactly the rows that still exist:
//
//   - one record-upsert event per sdn_record_index row, in ascending rowid
//     order (sdn_record_index.rowid is both the durable global sync cursor
//     replay must reproduce exactly -- carried via Index.RowID and replayed
//     with an explicit-rowid INSERT, see applyRecordCatalogIndexUpsertTo --
//     and the engine hot-window replay sort key, recordCatalogEngineSortKey),
//     joined against that schema's recordReadSource for the payload columns
//     (timestamp, record_length, created_at, stream_path) with stream_offset
//     resolved through remap. Repeat-CID mirror rows in OTHER producer
//     tables for the same cid are already never journaled on the live write
//     path (mirrorRoutedRecord never calls recordCatalog.Append) -- one
//     event per cid here is exactly what an ordinary reopen already
//     reconstructs, so the snapshot loses nothing durable. peer_id and
//     signature_hex -- the two columns a repeat-CID mirror legitimately
//     writes as its OWN -- are NOT trusted from that same ambiguous
//     GROUP BY join; they are instead resolved through
//     latestUpsertAttributionByCID, which reads them from the cid's actual
//     durable journal event so the compacted snapshot's producer
//     attribution always matches what a plain reopen would produce (see its
//     doc comment for why the join alone cannot guarantee that).
//   - one tag-upsert event per sdn_record_source_tags row.
//
// Callers hold s.mu (Lock).
func (s *FlatSQLStore) buildCompactedRecordCatalogSnapshot(remap compactionOffsetRemap) ([]recordCatalogEvent, error) {
	type indexRow struct {
		rowID           int64
		schemaName      string
		cid             string
		noradCatID      sql.NullInt64
		entityID        sql.NullString
		objectType      sql.NullString
		opsStatusCode   sql.NullString
		epochUnix       sql.NullInt64
		epochDay        sql.NullString
		sourceTimestamp int64
	}

	rows, err := s.db.Query(`
		SELECT rowid, schema_name, cid, norad_cat_id, entity_id, object_type, ops_status_code, epoch_unix, epoch_day, source_timestamp
		FROM sdn_record_index
		ORDER BY rowid ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("compact snapshot: scan sdn_record_index: %w", err)
	}
	var indexRows []indexRow
	for rows.Next() {
		var r indexRow
		if err := rows.Scan(&r.rowID, &r.schemaName, &r.cid, &r.noradCatID, &r.entityID, &r.objectType, &r.opsStatusCode, &r.epochUnix, &r.epochDay, &r.sourceTimestamp); err != nil {
			rows.Close()
			return nil, fmt.Errorf("compact snapshot: scan sdn_record_index row: %w", err)
		}
		indexRows = append(indexRows, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("compact snapshot: iterate sdn_record_index: %w", err)
	}
	rows.Close()

	// Ground truth for producer attribution -- see latestUpsertAttributionByCID's
	// doc comment for why recordReadSource's GROUP BY join below cannot be
	// trusted for peer_id/signature_hex on a repeat-CID cid.
	attribution, err := s.recordCatalog.latestUpsertAttributionByCID()
	if err != nil {
		return nil, fmt.Errorf("compact snapshot: scan journal for producer attribution: %w", err)
	}

	type payloadRow struct {
		peerID       string
		timestamp    int64
		streamPath   string
		streamOffset int64
		recordLength int64
		signatureHex sql.NullString
		createdAt    int64
	}
	payloadsBySchema := map[string]map[string]payloadRow{}
	schemaPayloads := func(schemaName string) (map[string]payloadRow, error) {
		if m, ok := payloadsBySchema[schemaName]; ok {
			return m, nil
		}
		readSource, err := s.recordReadSource(schemaName)
		if err != nil {
			return nil, fmt.Errorf("compact snapshot: read source for %s: %w", schemaName, err)
		}
		prows, err := s.db.Query(fmt.Sprintf(`
			SELECT cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex, created_at
			FROM %s
		`, readSource))
		if err != nil {
			return nil, fmt.Errorf("compact snapshot: read %s records: %w", schemaName, err)
		}
		m := map[string]payloadRow{}
		for prows.Next() {
			var cid string
			var p payloadRow
			if err := prows.Scan(&cid, &p.peerID, &p.timestamp, &p.streamPath, &p.streamOffset, &p.recordLength, &p.signatureHex, &p.createdAt); err != nil {
				prows.Close()
				return nil, fmt.Errorf("compact snapshot: scan %s record row: %w", schemaName, err)
			}
			m[cid] = p
		}
		if err := prows.Err(); err != nil {
			prows.Close()
			return nil, fmt.Errorf("compact snapshot: iterate %s records: %w", schemaName, err)
		}
		prows.Close()
		payloadsBySchema[schemaName] = m
		return m, nil
	}

	events := make([]recordCatalogEvent, 0, len(indexRows))
	for _, r := range indexRows {
		m, err := schemaPayloads(r.schemaName)
		if err != nil {
			return nil, err
		}
		p, ok := m[r.cid]
		if !ok {
			return nil, fmt.Errorf("compact snapshot: sdn_record_index row (schema=%s cid=%s) has no backing record row", r.schemaName, r.cid)
		}
		newOffset, ok := remap(p.streamPath, p.streamOffset)
		if !ok {
			return nil, fmt.Errorf("compact snapshot: no compacted offset for %s@%d (schema=%s cid=%s)", p.streamPath, p.streamOffset, r.schemaName, r.cid)
		}
		peerID := p.peerID
		signatureHex := ""
		if p.signatureHex.Valid {
			signatureHex = p.signatureHex.String
		}
		// Prefer the journal's own recorded attribution (the peer_id and
		// signature_hex that a plain reopen -- replaying this same,
		// untouched journal -- would actually land this cid under) over
		// recordReadSource's ambiguous GROUP BY pick. Every sdn_record_index
		// row is expected to have a matching journal upsert event (that is
		// the durable write path); fall back to the SQL join's pick only in
		// the (should not happen, but tolerated rather than hard-failing
		// compaction over it) case where one is missing.
		if bySchema, ok := attribution[r.schemaName]; ok {
			if a, ok := bySchema[r.cid]; ok {
				peerID = a.PeerID
				signatureHex = a.SignatureHex
			}
		}
		idx := recordCatalogIndex{
			RowID:           r.rowID,
			SourceTimestamp: r.sourceTimestamp,
		}
		if r.noradCatID.Valid {
			idx.HasNoradCatID = true
			idx.NoradCatID = r.noradCatID.Int64
		}
		if r.entityID.Valid {
			idx.EntityID = r.entityID.String
		}
		if r.objectType.Valid {
			idx.ObjectType = r.objectType.String
		}
		if r.opsStatusCode.Valid {
			idx.OpsStatusCode = r.opsStatusCode.String
		}
		if r.epochUnix.Valid {
			idx.HasEpochUnix = true
			idx.EpochUnix = r.epochUnix.Int64
		}
		if r.epochDay.Valid {
			idx.EpochDay = r.epochDay.String
		}
		events = append(events, recordCatalogEvent{
			Kind:         recordCatalogEventRecordUpsert,
			SchemaName:   r.schemaName,
			CID:          r.cid,
			PeerID:       peerID,
			StreamPath:   p.streamPath,
			StreamOffset: newOffset,
			RecordLength: p.recordLength,
			SignatureHex: signatureHex,
			Timestamp:    p.timestamp,
			CreatedAt:    p.createdAt,
			Index:        idx,
		})
	}

	tagRows, err := s.db.Query(`
		SELECT schema_name, cid, provider_id, source_name, source_url, batch_id, content_key_id, producer_peer_id, producer_public_key, created_at
		FROM sdn_record_source_tags
		ORDER BY rowid ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("compact snapshot: scan sdn_record_source_tags: %w", err)
	}
	for tagRows.Next() {
		var schemaName, cid string
		var tags SourceTags
		var sourceURL sql.NullString
		var createdAt int64
		if err := tagRows.Scan(&schemaName, &cid, &tags.ProviderID, &tags.SourceName, &sourceURL, &tags.BatchID, &tags.ContentKeyID, &tags.ProducerPeerID, &tags.ProducerPublicKey, &createdAt); err != nil {
			tagRows.Close()
			return nil, fmt.Errorf("compact snapshot: scan sdn_record_source_tags row: %w", err)
		}
		if sourceURL.Valid {
			tags.SourceURL = sourceURL.String
		}
		events = append(events, recordCatalogEvent{
			Kind:       recordCatalogEventTagUpsert,
			SchemaName: schemaName,
			CID:        cid,
			Tags:       tags,
			CreatedAt:  createdAt,
		})
	}
	if err := tagRows.Err(); err != nil {
		tagRows.Close()
		return nil, fmt.Errorf("compact snapshot: iterate sdn_record_source_tags: %w", err)
	}
	tagRows.Close()

	return events, nil
}

func encodeRecordCatalogEvent(event recordCatalogEvent) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte(recordCatalogFrameVersion)
	buf.WriteByte(event.Kind)
	writeRCString(&buf, event.SchemaName)
	writeRCString(&buf, event.CID)
	writeRCString(&buf, event.PeerID)
	writeRCString(&buf, event.StreamPath)
	writeRCI64(&buf, event.StreamOffset)
	writeRCI64(&buf, event.RecordLength)
	writeRCString(&buf, event.SignatureHex)
	writeRCI64(&buf, event.Timestamp)
	writeRCI64(&buf, event.CreatedAt)
	writeRCIndex(&buf, event.Index)
	writeRCTags(&buf, event.Tags)
	writeRCI64(&buf, event.CutoffUnix)
	return buf.Bytes(), nil
}

func decodeRecordCatalogEvent(payload []byte) (recordCatalogEvent, error) {
	reader := bytes.NewReader(payload)
	version, err := reader.ReadByte()
	if err != nil {
		return recordCatalogEvent{}, err
	}
	if version != recordCatalogFrameVersion {
		return recordCatalogEvent{}, fmt.Errorf("unsupported record catalog frame version %d", version)
	}
	kind, err := reader.ReadByte()
	if err != nil {
		return recordCatalogEvent{}, err
	}
	event := recordCatalogEvent{Kind: kind}
	if event.SchemaName, err = readRCString(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if event.CID, err = readRCString(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if event.PeerID, err = readRCString(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if event.StreamPath, err = readRCString(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if event.StreamOffset, err = readRCI64(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if event.RecordLength, err = readRCI64(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if event.SignatureHex, err = readRCString(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if event.Timestamp, err = readRCI64(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if event.CreatedAt, err = readRCI64(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if event.Index, err = readRCIndex(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if event.Tags, err = readRCTags(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if event.CutoffUnix, err = readRCI64(reader); err != nil {
		return recordCatalogEvent{}, err
	}
	if reader.Len() != 0 {
		return recordCatalogEvent{}, fmt.Errorf("record catalog frame has %d trailing bytes", reader.Len())
	}
	return event, nil
}

func writeRCIndex(buf *bytes.Buffer, index recordCatalogIndex) {
	writeRCI64(buf, index.RowID)
	writeRCI64(buf, index.SourceTimestamp)
	writeRCBool(buf, index.HasNoradCatID)
	writeRCI64(buf, index.NoradCatID)
	writeRCString(buf, index.EntityID)
	writeRCString(buf, index.ObjectType)
	writeRCString(buf, index.OpsStatusCode)
	writeRCBool(buf, index.HasEpochUnix)
	writeRCI64(buf, index.EpochUnix)
	writeRCString(buf, index.EpochDay)
}

func readRCIndex(reader *bytes.Reader) (recordCatalogIndex, error) {
	var index recordCatalogIndex
	var err error
	if index.RowID, err = readRCI64(reader); err != nil {
		return index, err
	}
	if index.SourceTimestamp, err = readRCI64(reader); err != nil {
		return index, err
	}
	if index.HasNoradCatID, err = readRCBool(reader); err != nil {
		return index, err
	}
	if index.NoradCatID, err = readRCI64(reader); err != nil {
		return index, err
	}
	if index.EntityID, err = readRCString(reader); err != nil {
		return index, err
	}
	if index.ObjectType, err = readRCString(reader); err != nil {
		return index, err
	}
	if index.OpsStatusCode, err = readRCString(reader); err != nil {
		return index, err
	}
	if index.HasEpochUnix, err = readRCBool(reader); err != nil {
		return index, err
	}
	if index.EpochUnix, err = readRCI64(reader); err != nil {
		return index, err
	}
	if index.EpochDay, err = readRCString(reader); err != nil {
		return index, err
	}
	return index, nil
}

func writeRCTags(buf *bytes.Buffer, tags SourceTags) {
	writeRCString(buf, tags.ProviderID)
	writeRCString(buf, tags.SourceName)
	writeRCString(buf, tags.SourceURL)
	writeRCString(buf, tags.BatchID)
	writeRCString(buf, tags.ContentKeyID)
	writeRCString(buf, tags.ProducerPeerID)
	writeRCString(buf, tags.ProducerPublicKey)
}

func readRCTags(reader *bytes.Reader) (SourceTags, error) {
	var tags SourceTags
	var err error
	if tags.ProviderID, err = readRCString(reader); err != nil {
		return tags, err
	}
	if tags.SourceName, err = readRCString(reader); err != nil {
		return tags, err
	}
	if tags.SourceURL, err = readRCString(reader); err != nil {
		return tags, err
	}
	if tags.BatchID, err = readRCString(reader); err != nil {
		return tags, err
	}
	if tags.ContentKeyID, err = readRCString(reader); err != nil {
		return tags, err
	}
	if tags.ProducerPeerID, err = readRCString(reader); err != nil {
		return tags, err
	}
	if tags.ProducerPublicKey, err = readRCString(reader); err != nil {
		return tags, err
	}
	return tags, nil
}

func writeRCString(buf *bytes.Buffer, value string) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(len(value)))
	buf.Write(tmp[:n])
	buf.WriteString(value)
}

func readRCString(reader *bytes.Reader) (string, error) {
	n, err := binary.ReadUvarint(reader)
	if err != nil {
		return "", err
	}
	if n > uint64(reader.Len()) {
		return "", fmt.Errorf("record catalog string length %d exceeds remaining frame %d", n, reader.Len())
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func writeRCI64(buf *bytes.Buffer, value int64) {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], uint64(value))
	buf.Write(tmp[:])
}

func readRCI64(reader *bytes.Reader) (int64, error) {
	var tmp [8]byte
	if _, err := io.ReadFull(reader, tmp[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(tmp[:])), nil
}

func writeRCBool(buf *bytes.Buffer, value bool) {
	if value {
		buf.WriteByte(1)
		return
	}
	buf.WriteByte(0)
}

func readRCBool(reader *bytes.Reader) (bool, error) {
	b, err := reader.ReadByte()
	if err != nil {
		return false, err
	}
	switch b {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("invalid record catalog bool %d", b)
	}
}

func (s *FlatSQLStore) recordCatalogUpsertEvent(exec sqlQueryExecer, schemaName, cid, peerID string, timestamp int64, streamPath string, streamOffset, recordLength int64, signature []byte, createdAt int64, data []byte) (recordCatalogEvent, error) {
	signatureHex := ""
	if len(signature) > 0 {
		signatureHex = fmt.Sprintf("%x", signature)
	}
	index, err := recordCatalogIndexFromStoredRow(exec, schemaName, cid, timestamp, data)
	if err != nil {
		return recordCatalogEvent{}, err
	}
	return recordCatalogEvent{
		Kind:         recordCatalogEventRecordUpsert,
		SchemaName:   schemaName,
		CID:          cid,
		PeerID:       peerID,
		StreamPath:   streamPath,
		StreamOffset: streamOffset,
		RecordLength: recordLength,
		SignatureHex: signatureHex,
		Timestamp:    timestamp,
		CreatedAt:    createdAt,
		Index:        index,
	}, nil
}

func recordCatalogIndexFromStoredRow(exec sqlQueryExecer, schemaName, cid string, sourceTimestamp int64, data []byte) (recordCatalogIndex, error) {
	index := recordCatalogIndex{SourceTimestamp: sourceTimestamp}
	if err := exec.QueryRow(`SELECT rowid FROM sdn_record_index WHERE schema_name = ? AND cid = ?`, schemaName, cid).Scan(&index.RowID); err != nil {
		return index, fmt.Errorf("query record index rowid: %w", err)
	}
	fields, err := extractIndexedFields(schemaName, data)
	if err != nil {
		return index, nil
	}
	if fields.noradCatID != nil {
		index.HasNoradCatID = true
		index.NoradCatID = int64(*fields.noradCatID)
	}
	index.EntityID = fields.entityID
	index.ObjectType = fields.objectType
	index.OpsStatusCode = fields.opsStatusCode
	if fields.epochUnix != nil {
		index.HasEpochUnix = true
		index.EpochUnix = *fields.epochUnix
	}
	index.EpochDay = fields.epochDay
	return index, nil
}

func recordCatalogTagUpsertEvent(exec sqlQueryExecer, schemaName, cid string, tags SourceTags) (recordCatalogEvent, error) {
	tags = normalizeSourceTags(tags)
	var createdAt int64
	if err := exec.QueryRow(`
		SELECT created_at
		FROM sdn_record_source_tags
		WHERE schema_name = ?
		  AND cid = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id = ?
		  AND content_key_id = ?
		  AND producer_peer_id = ?
		  AND producer_public_key = ?
	`, schemaName, cid, tags.ProviderID, tags.SourceName, tags.BatchID, tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey).Scan(&createdAt); err != nil {
		return recordCatalogEvent{}, fmt.Errorf("query source tag created_at: %w", err)
	}
	return recordCatalogEvent{
		Kind:       recordCatalogEventTagUpsert,
		SchemaName: schemaName,
		CID:        cid,
		Tags:       tags,
		CreatedAt:  createdAt,
	}, nil
}

func (s *FlatSQLStore) applyRecordCatalogEvent(event recordCatalogEvent) error {
	switch event.Kind {
	case recordCatalogEventRecordUpsert:
		return s.applyRecordCatalogRecordUpsert(event)
	case recordCatalogEventTagUpsert:
		return s.applyRecordCatalogTagUpsert(event)
	case recordCatalogEventRecordDelete:
		return s.applyRecordCatalogRecordDelete(event.SchemaName, event.CID)
	case recordCatalogEventSourceKeep:
		return s.applyRecordCatalogSourceKeep(event.SchemaName, event.Tags.ProviderID, event.Tags.SourceName, event.Tags.BatchID)
	case recordCatalogEventGCOlderThan:
		return s.applyRecordCatalogGCOlderThan(event.SchemaName, event.CutoffUnix)
	default:
		return fmt.Errorf("unknown record catalog event kind %d", event.Kind)
	}
}

func (s *FlatSQLStore) applyRecordCatalogRecordUpsert(event recordCatalogEvent) error {
	if strings.TrimSpace(event.SchemaName) == "" || strings.TrimSpace(event.CID) == "" {
		return fmt.Errorf("record upsert requires schema and cid")
	}
	tableName, err := s.ensureProducerStandardTable(routedProducerID(event.PeerID), event.SchemaName)
	if err != nil {
		return fmt.Errorf("ensure producer table: %w", err)
	}
	return s.applyRecordCatalogRecordUpsertTo(s.db, event, tableName)
}

func (s *FlatSQLStore) applyRecordCatalogRecordUpsertTo(exec sqlExecer, event recordCatalogEvent, tableName string) error {
	var signatureHex any
	if strings.TrimSpace(event.SignatureHex) != "" {
		signatureHex = strings.TrimSpace(event.SignatureHex)
	}
	if _, err := exec.Exec(flatsqldrv.WithoutJournal(fmt.Sprintf(`
		INSERT OR IGNORE INTO %s (
			cid, peer_id, timestamp, stream_path, stream_offset, record_length, signature_hex, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, tableName)), event.CID, event.PeerID, event.Timestamp, event.StreamPath, event.StreamOffset, event.RecordLength, signatureHex, event.CreatedAt); err != nil {
		return fmt.Errorf("replay record metadata: %w", err)
	}
	return s.applyRecordCatalogIndexUpsertTo(exec, event)
}

func (s *FlatSQLStore) applyRecordCatalogIndexUpsert(event recordCatalogEvent) error {
	return s.applyRecordCatalogIndexUpsertTo(s.db, event)
}

func (s *FlatSQLStore) applyRecordCatalogIndexUpsertTo(exec sqlExecer, event recordCatalogEvent) error {
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
	if index.RowID > 0 {
		_, err := exec.Exec(flatsqldrv.WithoutJournal(`
			INSERT INTO sdn_record_index (
				rowid, schema_name, cid, norad_cat_id, entity_id, object_type, ops_status_code, epoch_unix, epoch_day, source_timestamp
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(schema_name, cid) DO UPDATE SET
				norad_cat_id = excluded.norad_cat_id,
				entity_id = excluded.entity_id,
				object_type = excluded.object_type,
				ops_status_code = excluded.ops_status_code,
				epoch_unix = excluded.epoch_unix,
				epoch_day = excluded.epoch_day,
				source_timestamp = excluded.source_timestamp
		`), index.RowID, event.SchemaName, event.CID, norad, entity, objectType, opsStatusCode, epoch, day, sourceTimestamp)
		if err != nil {
			return fmt.Errorf("replay record index with rowid: %w", err)
		}
		return nil
	}
	_, err := exec.Exec(flatsqldrv.WithoutJournal(`
		INSERT INTO sdn_record_index (
			schema_name, cid, norad_cat_id, entity_id, object_type, ops_status_code, epoch_unix, epoch_day, source_timestamp
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(schema_name, cid) DO UPDATE SET
			norad_cat_id = excluded.norad_cat_id,
			entity_id = excluded.entity_id,
			object_type = excluded.object_type,
			ops_status_code = excluded.ops_status_code,
			epoch_unix = excluded.epoch_unix,
			epoch_day = excluded.epoch_day,
			source_timestamp = excluded.source_timestamp
	`), event.SchemaName, event.CID, norad, entity, objectType, opsStatusCode, epoch, day, sourceTimestamp)
	if err != nil {
		return fmt.Errorf("replay record index: %w", err)
	}
	return nil
}

func (s *FlatSQLStore) applyRecordCatalogTagUpsert(event recordCatalogEvent) error {
	return s.applyRecordCatalogTagUpsertTo(s.db, event)
}

func (s *FlatSQLStore) applyRecordCatalogTagUpsertTo(exec sqlExecer, event recordCatalogEvent) error {
	tags := normalizeSourceTags(event.Tags)
	if strings.TrimSpace(event.SchemaName) == "" || strings.TrimSpace(event.CID) == "" {
		return fmt.Errorf("source tag upsert requires schema and cid")
	}
	if err := ValidateSourceTags(tags); err != nil {
		return err
	}
	_, err := exec.Exec(flatsqldrv.WithoutJournal(`
		INSERT INTO sdn_record_source_tags (
			schema_name, cid, provider_id, source_name, source_url, batch_id,
			content_key_id, producer_peer_id, producer_public_key, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(schema_name, cid, provider_id, source_name, batch_id, content_key_id, producer_peer_id, producer_public_key)
		DO UPDATE SET source_url = excluded.source_url
	`), event.SchemaName, event.CID, tags.ProviderID, tags.SourceName, tags.SourceURL, tags.BatchID, tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("replay source tag: %w", err)
	}
	return nil
}

func (s *FlatSQLStore) applyRecordCatalogRecordDelete(schemaName, cid string) error {
	if strings.TrimSpace(schemaName) == "" || strings.TrimSpace(cid) == "" {
		return fmt.Errorf("record delete requires schema and cid")
	}
	// A historical delete must never take out a record that LIVE traffic re-added
	// while this replay was running: the live re-add is strictly newer than any
	// frame in the pre-boot journal prefix. Inert outside a replay.
	if s.hydrationShield.has(schemaName, cid) {
		return nil
	}
	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return err
	}
	if legacyExists, exErr := s.tableExists(tableName); exErr == nil && legacyExists {
		if _, err := s.db.Exec(flatsqldrv.WithoutJournal(fmt.Sprintf(`DELETE FROM %s WHERE cid = ?`, tableName)), cid); err != nil {
			return err
		}
	}
	s.deleteRoutedMirrorsWhere(s.db, tableName, `cid = ?`, cid)
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`DELETE FROM sdn_record_index WHERE schema_name = ? AND cid = ?`), schemaName, cid); err != nil {
		return err
	}
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`DELETE FROM sdn_record_source_tags WHERE schema_name = ? AND cid = ?`), schemaName, cid); err != nil {
		return err
	}
	return nil
}

func (s *FlatSQLStore) applyRecordCatalogSourceKeep(schemaName, providerID, sourceName, keepBatch string) error {
	if strings.TrimSpace(schemaName) == "" || strings.TrimSpace(providerID) == "" || strings.TrimSpace(sourceName) == "" || strings.TrimSpace(keepBatch) == "" {
		return fmt.Errorf("source keep event requires schema, provider, source, and keep batch")
	}
	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_record_catalog_source_keep_cids (cid TEXT PRIMARY KEY)`)); err != nil {
		return err
	}
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`DELETE FROM temp_sdn_record_catalog_source_keep_cids`)); err != nil {
		return err
	}
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`
		INSERT OR IGNORE INTO temp_sdn_record_catalog_source_keep_cids (cid)
		SELECT cid FROM sdn_record_source_tags
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?
	`), schemaName, providerID, sourceName, keepBatch); err != nil {
		return err
	}
	// Never let a historical batch-clear delete records that live traffic
	// re-added mid-replay. Inert outside a replay.
	if err := s.unshieldTempCIDs("temp_sdn_record_catalog_source_keep_cids", schemaName); err != nil {
		return err
	}
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`
		DELETE FROM sdn_record_source_tags
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?
	`), schemaName, providerID, sourceName, keepBatch); err != nil {
		return err
	}
	return s.deleteOrphanedRecordCatalogRows(schemaName, tableName, `cid IN (SELECT cid FROM temp_sdn_record_catalog_source_keep_cids)`)
}

func (s *FlatSQLStore) applyRecordCatalogGCOlderThan(schemaName string, cutoffUnix int64) error {
	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_record_catalog_gc_cids (cid TEXT PRIMARY KEY)`)); err != nil {
		return err
	}
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`DELETE FROM temp_sdn_record_catalog_gc_cids`)); err != nil {
		return err
	}
	readSource, err := s.recordReadSource(schemaName)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(fmt.Sprintf(`
		INSERT OR IGNORE INTO temp_sdn_record_catalog_gc_cids (cid)
		SELECT cid FROM %s WHERE timestamp < ?
	`, readSource)), cutoffUnix); err != nil {
		return err
	}
	// Never let a historical GC sweep delete records that live traffic re-added
	// mid-replay. Inert outside a replay.
	if err := s.unshieldTempCIDs("temp_sdn_record_catalog_gc_cids", schemaName); err != nil {
		return err
	}
	if err := s.applyRecordCatalogRecordSetDelete(schemaName, tableName, `cid IN (SELECT cid FROM temp_sdn_record_catalog_gc_cids)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`
		DELETE FROM sdn_record_source_tags
		WHERE schema_name = ?
		  AND cid IN (SELECT cid FROM temp_sdn_record_catalog_gc_cids)
	`), schemaName); err != nil {
		return err
	}
	return nil
}

func (s *FlatSQLStore) deleteOrphanedRecordCatalogRows(schemaName, tableName, cidWhere string, args ...any) error {
	where := cidWhere + ` AND cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`
	args = append(args, schemaName)
	return s.applyRecordCatalogRecordSetDelete(schemaName, tableName, where, args...)
}

func (s *FlatSQLStore) applyRecordCatalogRecordSetDelete(schemaName, tableName, where string, args ...any) error {
	if legacyExists, exErr := s.tableExists(tableName); exErr == nil && legacyExists {
		if _, err := s.db.Exec(flatsqldrv.WithoutJournal(fmt.Sprintf(`DELETE FROM %s WHERE %s`, tableName, where)), args...); err != nil {
			return err
		}
	}
	s.deleteRoutedMirrorsWhere(s.db, tableName, where, args...)
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(fmt.Sprintf(`DELETE FROM sdn_record_index WHERE schema_name = ? AND %s`, where)), append([]any{schemaName}, args...)...); err != nil {
		return err
	}
	return nil
}
