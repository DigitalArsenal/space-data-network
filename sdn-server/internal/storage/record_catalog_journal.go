package storage

import (
	"bytes"
	"container/heap"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

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
	return j.f.Sync()
}

func (j *recordCatalogJournal) Replay(store *FlatSQLStore) (int, error) {
	if j == nil || j.f == nil {
		return 0, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	info, err := j.f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	if j.readOnly {
		size = j.replayLimit
	}
	var off int64
	var hdr [8]byte
	count := 0
	var tx *sql.Tx
	knownProducerTables := map[string]bool{}
	beginTx := func() error {
		if tx != nil {
			return nil
		}
		var err error
		tx, err = store.db.Begin()
		return err
	}
	commitTx := func() error {
		if tx == nil {
			return nil
		}
		err := tx.Commit()
		tx = nil
		return err
	}
	rollbackTx := func() {
		if tx != nil {
			_ = tx.Rollback()
			tx = nil
		}
	}
	defer rollbackTx()

	for off < size {
		if _, err := j.f.ReadAt(hdr[:], off); err != nil {
			return count, err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		payload := make([]byte, n)
		if _, err := j.f.ReadAt(payload, off+8); err != nil {
			return count, err
		}
		event, err := decodeRecordCatalogEvent(payload)
		if err != nil {
			return count, fmt.Errorf("record catalog frame at %d: %w", off, err)
		}
		switch event.Kind {
		case recordCatalogEventRecordUpsert:
			tableName, err := ProducerStandardTableName(routedProducerID(event.PeerID), event.SchemaName)
			if err != nil {
				return count, fmt.Errorf("record catalog frame at %d: %w", off, err)
			}
			if !knownProducerTables[tableName] {
				if err := commitTx(); err != nil {
					return count, fmt.Errorf("record catalog frame at %d: commit before table init: %w", off, err)
				}
				if _, err := store.ensureProducerStandardTable(routedProducerID(event.PeerID), event.SchemaName); err != nil {
					return count, fmt.Errorf("record catalog frame at %d: %w", off, err)
				}
				knownProducerTables[tableName] = true
			}
			if err := beginTx(); err != nil {
				return count, fmt.Errorf("record catalog frame at %d: begin replay batch: %w", off, err)
			}
			if err := store.applyRecordCatalogRecordUpsertTo(tx, event, tableName); err != nil {
				return count, fmt.Errorf("record catalog frame at %d: %w", off, err)
			}
		case recordCatalogEventTagUpsert:
			if err := beginTx(); err != nil {
				return count, fmt.Errorf("record catalog frame at %d: begin replay batch: %w", off, err)
			}
			if err := store.applyRecordCatalogTagUpsertTo(tx, event); err != nil {
				return count, fmt.Errorf("record catalog frame at %d: %w", off, err)
			}
		default:
			if err := commitTx(); err != nil {
				return count, fmt.Errorf("record catalog frame at %d: commit before scope event: %w", off, err)
			}
			if err := store.applyRecordCatalogEvent(event); err != nil {
				return count, fmt.Errorf("record catalog frame at %d: %w", off, err)
			}
		}
		count++
		if count%recordCatalogReplayBatchSize == 0 {
			if err := commitTx(); err != nil {
				return count, fmt.Errorf("record catalog frame at %d: commit replay batch: %w", off, err)
			}
		}
		off += 8 + n
	}
	if err := commitTx(); err != nil {
		return count, fmt.Errorf("record catalog final replay batch: %w", err)
	}
	return count, nil
}

func (j *recordCatalogJournal) ReplayEngineHotWindow(store *FlatSQLStore, schemaName string, limit int) (int, error) {
	if j == nil || j.f == nil || limit <= 0 {
		return 0, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	info, err := j.f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	if j.readOnly {
		size = j.replayLimit
	}

	byCID := make(map[string]*recordCatalogEngineCandidate)
	candidates := &recordCatalogEngineCandidateHeap{}
	heap.Init(candidates)

	removeCandidate := func(cid string) {
		c := byCID[cid]
		if c == nil {
			return
		}
		heap.Remove(candidates, c.heapIndex)
		delete(byCID, cid)
	}
	addCandidate := func(event recordCatalogEvent) {
		if strings.TrimSpace(event.CID) == "" {
			return
		}
		if existing := byCID[event.CID]; existing != nil {
			existing.event = event
			heap.Fix(candidates, existing.heapIndex)
			return
		}
		candidate := &recordCatalogEngineCandidate{event: event}
		if candidates.Len() < limit {
			heap.Push(candidates, candidate)
			byCID[event.CID] = candidate
			return
		}
		if candidates.Len() == 0 || recordCatalogEngineSortKey(event) <= recordCatalogEngineSortKey((*candidates)[0].event) {
			return
		}
		evicted := heap.Pop(candidates).(*recordCatalogEngineCandidate)
		delete(byCID, evicted.event.CID)
		heap.Push(candidates, candidate)
		byCID[event.CID] = candidate
	}
	updateCandidateTag := func(event recordCatalogEvent) {
		c := byCID[event.CID]
		if c == nil {
			return
		}
		if event.CreatedAt >= c.tagTime {
			c.tags = normalizeSourceTags(event.Tags)
			c.tagTime = event.CreatedAt
		}
	}
	applySourceKeep := func(event recordCatalogEvent) {
		keep := normalizeSourceTags(event.Tags)
		for cid, c := range byCID {
			if c.tags.ProviderID == keep.ProviderID &&
				c.tags.SourceName == keep.SourceName &&
				c.tags.BatchID != keep.BatchID {
				removeCandidate(cid)
			}
		}
	}
	applyGCOlderThan := func(cutoff int64) {
		for cid, c := range byCID {
			ts := c.event.Index.SourceTimestamp
			if ts == 0 {
				ts = c.event.Timestamp
			}
			if ts < cutoff {
				removeCandidate(cid)
			}
		}
	}

	var off int64
	var hdr [8]byte
	for off < size {
		if _, err := j.f.ReadAt(hdr[:], off); err != nil {
			return candidates.Len(), err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		payload := make([]byte, n)
		if _, err := j.f.ReadAt(payload, off+8); err != nil {
			return candidates.Len(), err
		}
		event, err := decodeRecordCatalogEvent(payload)
		if err != nil {
			return candidates.Len(), fmt.Errorf("record catalog frame at %d: %w", off, err)
		}
		if event.SchemaName == schemaName {
			switch event.Kind {
			case recordCatalogEventRecordUpsert:
				addCandidate(event)
			case recordCatalogEventTagUpsert:
				updateCandidateTag(event)
			case recordCatalogEventRecordDelete:
				removeCandidate(event.CID)
			case recordCatalogEventSourceKeep:
				applySourceKeep(event)
			case recordCatalogEventGCOlderThan:
				applyGCOlderThan(event.CutoffUnix)
			}
		}
		off += 8 + n
	}

	ordered := make([]*recordCatalogEngineCandidate, candidates.Len())
	copy(ordered, *candidates)
	sort.Slice(ordered, func(i, j int) bool {
		return recordCatalogEngineSortKey(ordered[i].event) < recordCatalogEngineSortKey(ordered[j].event)
	})

	files := map[string]*os.File{}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()

	loaded := 0
	for _, c := range ordered {
		event := c.event
		data, err := store.readStreamRecordCached(files, event.StreamPath, event.StreamOffset, event.RecordLength)
		if err != nil {
			log.Warnf("FlatSQL engine compact hot-window rebuild: read %s@%d: %v", event.StreamPath, event.StreamOffset, err)
			continue
		}
		source := engineSourceName(&c.tags)
		if err := store.ensureEngineSource(source); err != nil {
			log.Warnf("FlatSQL engine compact hot-window rebuild: register source %q: %v", source, err)
			if store.engine.Poisoned() {
				return loaded, fmt.Errorf("FlatSQL engine poisoned registering source %q: %w", source, err)
			}
			continue
		}
		if _, err := store.engineDB.IngestOneWithSource(engineRecordPayload(data), source); err != nil {
			log.Warnf("FlatSQL engine compact hot-window rebuild: ingest record: %v", err)
			if store.engine.Poisoned() {
				return loaded, fmt.Errorf("FlatSQL engine poisoned during compact hot-window rebuild: %w", err)
			}
			continue
		}
		loaded++
	}
	store.engineResident[schemaName] = int64(loaded)
	if loaded > 0 {
		log.Infof("FlatSQL engine compact hot-window rebuild: loaded %d %s records (window %d)", loaded, schemaName, limit)
	}
	return loaded, nil
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
