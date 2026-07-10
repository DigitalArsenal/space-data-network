package storage

// compaction.go implements physical stream-file compaction (D5 design,
// scratchpad D5_compaction_design.md): reclaiming the disk bytes that
// GarbageCollectToQuota / GarbageCollect can never touch, since eviction
// there only deletes metadata rows -- the payload bytes those rows pointed
// at stay in the append-only .flatsql stream files forever (see
// LiveRecordBytes' and GarbageCollectToQuota's doc comments in flatsql.go).
//
// Scope: stream files are per-STANDARD and SHARED across every producer
// (newFlatSQLStreamAppender, flatsql.go) -- the (producer, standard) tables
// are pointer/metadata tables, and many rows across many producer tables
// (plus the legacy per-standard table on pre-flip databases) can reference
// one physical frame. Compaction is therefore a WHOLE-STORE operation: every
// standard's stream file plus the single durable record-catalog journal are
// rewritten to temps and committed as ONE atomic unit via a CRC-checked
// commit manifest, so a crash at any point leaves the store fully pre- or
// fully post-compaction from recoverPendingCompaction's perspective -- never
// torn.
//
// This is the MINIMAL SAFE INCREMENT (design §8): CompactStreams is an
// explicit, operator/maintenance-invoked method. It is deliberately NOT
// wired into enforceStorageQuota or the periodic GC loop (internal/node,
// out of this package's scope) -- that automatic trigger is follow-up work
// once this mechanism has soaked behind the manual entrypoint.

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// compactionCrashPoint is a package-level crash-injection seam: production
// code leaves it a no-op. Tests replace it with a function that panics with
// a recognizable sentinel at a named stage, simulating the process dying at
// exactly that point -- everything already fsynced before the call stays on
// disk exactly as it is; everything CompactStreams would have done AFTER
// the call (including any of its own error-path cleanup) never runs. Tests
// then reopen the store (which drives recoverPendingCompaction) and assert
// the store is fully pre- or fully post-compaction, never torn.
var compactionCrashPoint = func(stage string) {}

const (
	compactionStageAfterStreamTemps = "after-stream-temps"
	compactionStageAfterJournalTemp = "after-journal-temp"
	compactionStageAfterManifest    = "after-manifest"
	compactionStageMidRename        = "mid-rename"
)

// compactionManifestPrefix names the write-ahead commit manifest
// (flatsql-streams/compaction.commit-<gen>): a fully-written, CRC-valid
// manifest is compaction's linearization point (design §3) -- before it
// exists (or it is torn/CRC-invalid), NOTHING durable has changed and
// recovery rolls back by deleting orphan temps; once it exists and
// validates, every rename it lists WILL be applied (recovery rolls
// forward, idempotently -- a temp already renamed away is simply skipped).
const compactionManifestPrefix = "compaction.commit-"

// compactionTempInfix marks every stream/journal compaction temp file
// (<STANDARD>.flatsql.compact-<gen>, record-catalog.flatsqlmeta.compact-<gen>)
// so recovery can glob for and sweep them independently of the manifest.
const compactionTempInfix = ".compact-"

// compactionGenCounter seeds compaction generation numbers from the
// process's start time and increments monotonically thereafter, so every
// CompactStreams call in this process's lifetime gets a distinct generation
// (temp/manifest filenames never collide) without relying on clock
// resolution. Compaction is single-writer (holds s.mu.Lock() end-to-end;
// only one process ever holds the store's writer lock), so process-lifetime
// uniqueness is sufficient.
var compactionGenCounter = int64(0)

func init() {
	// Avoid a literal 0 generation (indistinguishable from an unset/zero
	// value in ad hoc debugging) and give successive process runs
	// non-overlapping ranges in the common case without depending on wall
	// clock monotonicity for correctness (the counter itself is what
	// guarantees uniqueness within THIS process's lifetime).
	atomic.StoreInt64(&compactionGenCounter, 1)
}

func nextCompactionGeneration() int64 {
	return atomic.AddInt64(&compactionGenCounter, 1)
}

// compactionRenamePair is one (temp -> final) rename the commit manifest
// promises to apply. Paths are stored relative to basePath so the manifest
// stays portable and never leaks an absolute tmp/test directory.
type compactionRenamePair struct {
	TempPath  string
	FinalPath string
}

type compactionManifest struct {
	Generation int64
	Pairs      []compactionRenamePair
}

// writeCompactionManifest writes m to path (which must not already exist)
// framed identically to a record-catalog journal frame
// ([4-byte length][4-byte CRC32][payload]) and fsyncs it. The manifest is
// written directly at its final name -- unlike the stream/journal temps, it
// has no separate temp+rename step, because its own atomicity comes from
// the framing + CRC: a crash mid-write leaves a short/torn file that
// readCompactionManifest's CRC check rejects exactly like
// scanRecordCatalogValidLength rejects a torn journal tail.
func writeCompactionManifest(path string, m compactionManifest) error {
	var body bytes.Buffer
	writeRCI64(&body, m.Generation)
	writeRCI64(&body, int64(len(m.Pairs)))
	for _, p := range m.Pairs {
		writeRCString(&body, p.TempPath)
		writeRCString(&body, p.FinalPath)
	}
	payload := body.Bytes()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("compaction manifest: create %s: %w", path, err)
	}
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(hdr[4:], crc32.ChecksumIEEE(payload))
	if _, err := f.Write(hdr[:]); err != nil {
		f.Close()
		return fmt.Errorf("compaction manifest: write header %s: %w", path, err)
	}
	if _, err := f.Write(payload); err != nil {
		f.Close()
		return fmt.Errorf("compaction manifest: write body %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("compaction manifest: sync %s: %w", path, err)
	}
	return f.Close()
}

// readCompactionManifest reads and validates the manifest at path. ok is
// false whenever the file is absent, short, torn, has a bad CRC, or has
// trailing garbage past its declared length -- ANY such case means the
// manifest never durably committed, so the caller must treat it as if it
// never existed (rollback), matching scanRecordCatalogValidLength's
// torn-tail handling for the record-catalog journal.
func readCompactionManifest(path string) (compactionManifest, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return compactionManifest{}, false, nil
		}
		return compactionManifest{}, false, fmt.Errorf("compaction manifest: read %s: %w", path, err)
	}
	if len(data) < 8 {
		return compactionManifest{}, false, nil
	}
	n := binary.LittleEndian.Uint32(data[0:4])
	crc := binary.LittleEndian.Uint32(data[4:8])
	if uint64(len(data)) != uint64(8)+uint64(n) {
		return compactionManifest{}, false, nil
	}
	payload := data[8:]
	if crc32.ChecksumIEEE(payload) != crc {
		return compactionManifest{}, false, nil
	}
	reader := bytes.NewReader(payload)
	gen, err := readRCI64(reader)
	if err != nil {
		return compactionManifest{}, false, nil
	}
	count, err := readRCI64(reader)
	if err != nil || count < 0 {
		return compactionManifest{}, false, nil
	}
	m := compactionManifest{Generation: gen}
	for i := int64(0); i < count; i++ {
		tempPath, err := readRCString(reader)
		if err != nil {
			return compactionManifest{}, false, nil
		}
		finalPath, err := readRCString(reader)
		if err != nil {
			return compactionManifest{}, false, nil
		}
		m.Pairs = append(m.Pairs, compactionRenamePair{TempPath: tempPath, FinalPath: finalPath})
	}
	if reader.Len() != 0 {
		return compactionManifest{}, false, nil
	}
	return m, true, nil
}

// fsyncDir opens path (which must be a directory) and fsyncs it -- needed
// after a rename or unlink so the directory-entry change itself is durable,
// not just the file content. No code elsewhere in this store fsyncs parent
// directories (export.go, datastore_identity.go, field_encryption.go,
// ipfs_publish.go, and the record-catalog journal's own Sync all stop at
// the file); compaction adds it as intentional hardening because the
// commit-manifest protocol's correctness depends on the rename itself
// being durable, not just the bytes under it.
func fsyncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("fsync dir %s: %w", path, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync dir %s: %w", path, err)
	}
	return nil
}

// standardCompactionPlan is one standard's (schema's) compaction work: the
// live frames found in its shared stream file, the temp file they were
// copied into, and the resulting old->new offset map every referencing
// metadata row (across every producer table + the legacy table) must be
// updated with.
type standardCompactionPlan struct {
	schemaName string
	standard   string
	streamRel  string // e.g. "flatsql-streams/OMM.flatsql"
	tempRel    string // e.g. "flatsql-streams/OMM.flatsql.compact-<gen>"
	offsetMap  map[int64]int64
	oldSize    int64
	newSize    int64
}

type liveStreamFrame struct {
	offset int64
	length int64
}

// CompactStreams physically reclaims stream-file and record-catalog-journal
// bytes left behind by logical eviction (GarbageCollect / GarbageCollectToQuota
// delete metadata rows only -- see LiveRecordBytes' doc comment). It rewrites
// every standard's shared stream file to a temp containing ONLY the frames
// still referenced by at least one live metadata row (byte-for-byte verbatim
// copies, tightly packed with no padding -- reproducing Append's on-disk
// layout exactly), rewrites the durable record-catalog journal as a minimal
// snapshot with remapped offsets (the journal's INSERT-OR-IGNORE replay pin
// means offsets can only be fixed by rewriting the journal itself, never by
// appending remap events -- design §1), and commits every rename as one
// atomic unit via a CRC-checked write-ahead manifest (design §2-3).
//
// Holds s.mu.Lock() for the entire operation: single-writer (storelock.go)
// plus every other mutator (StoreRoutedByProducer, GarbageCollectToQuota,
// ...) already serializing on s.mu means no concurrent operation can ever
// observe a half-swapped store, at the cost of stalling writers for the
// duration -- acceptable for this explicit, operator-invoked maintenance
// entrypoint (design §8's minimal increment is deliberately NOT wired into
// the live GC loop).
func (s *FlatSQLStore) CompactStreams() (reclaimedBytes int64, err error) {
	if err := s.requireWritable("compact streams"); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	gen := nextCompactionGeneration()
	genSuffix := compactionTempInfix + strconv.FormatInt(gen, 10)

	var createdTemps []string
	cleanupCreatedTemps := func() {
		for _, p := range createdTemps {
			if rmErr := os.Remove(p); rmErr != nil && !os.IsNotExist(rmErr) {
				log.Warnf("CompactStreams: cleanup temp %s: %v", p, rmErr)
			}
		}
	}

	// --- Step 1+2: per standard, enumerate live frames and write a
	// verbatim-copied, tightly-packed compacted temp. ---
	var plans []*standardCompactionPlan
	for _, schemaName := range s.validator.Schemas() {
		standard, tErr := sds.SchemaNameToTable(schemaName)
		if tErr != nil {
			// validator.Schemas() only returns names it already validated at
			// load time; a failure here would mean the validator itself is
			// inconsistent. Surface it rather than silently skip a standard.
			cleanupCreatedTemps()
			return 0, fmt.Errorf("compact streams: schema %q table name: %w", schemaName, tErr)
		}
		streamRel := filepath.Join(flatSQLStreamDirName, standard+".flatsql")
		streamAbs := filepath.Join(s.basePath, streamRel)

		info, statErr := os.Stat(streamAbs)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue // nothing was ever written for this standard
			}
			cleanupCreatedTemps()
			return 0, fmt.Errorf("compact streams: stat %s: %w", streamRel, statErr)
		}
		oldSize := info.Size()

		readSource, rsErr := s.recordReadSource(schemaName)
		if rsErr != nil {
			cleanupCreatedTemps()
			return 0, fmt.Errorf("compact streams: read source for %s: %w", schemaName, rsErr)
		}
		rows, qErr := s.db.Query(fmt.Sprintf(`
			SELECT DISTINCT stream_offset, record_length
			FROM %s
			WHERE stream_path = ?
			ORDER BY stream_offset ASC
		`, readSource), streamRel)
		if qErr != nil {
			cleanupCreatedTemps()
			return 0, fmt.Errorf("compact streams: enumerate live frames for %s: %w", schemaName, qErr)
		}
		var frames []liveStreamFrame
		for rows.Next() {
			var f liveStreamFrame
			if scanErr := rows.Scan(&f.offset, &f.length); scanErr != nil {
				rows.Close()
				cleanupCreatedTemps()
				return 0, fmt.Errorf("compact streams: scan live frame for %s: %w", schemaName, scanErr)
			}
			frames = append(frames, f)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			cleanupCreatedTemps()
			return 0, fmt.Errorf("compact streams: iterate live frames for %s: %w", schemaName, err)
		}
		rows.Close()

		tempRel := streamRel + genSuffix
		tempAbs := filepath.Join(s.basePath, tempRel)
		newSize, offsetMap, writeErr := writeCompactedStreamTemp(streamAbs, tempAbs, frames)
		if writeErr != nil {
			cleanupCreatedTemps()
			return 0, fmt.Errorf("compact streams: write compacted stream for %s: %w", schemaName, writeErr)
		}
		createdTemps = append(createdTemps, tempAbs)

		plans = append(plans, &standardCompactionPlan{
			schemaName: schemaName,
			standard:   standard,
			streamRel:  streamRel,
			tempRel:    tempRel,
			offsetMap:  offsetMap,
			oldSize:    oldSize,
			newSize:    newSize,
		})
	}

	compactionCrashPoint(compactionStageAfterStreamTemps)

	// --- Step 3: rewrite the record-catalog journal as a compacted
	// snapshot with every offset remapped through the plans built above. ---
	offsetByStreamPath := make(map[string]map[int64]int64, len(plans))
	for _, plan := range plans {
		offsetByStreamPath[plan.streamRel] = plan.offsetMap
	}
	remap := func(streamPath string, oldOffset int64) (int64, bool) {
		m, ok := offsetByStreamPath[streamPath]
		if !ok {
			return 0, false
		}
		newOffset, ok := m[oldOffset]
		return newOffset, ok
	}

	events, snapErr := s.buildCompactedRecordCatalogSnapshot(remap)
	if snapErr != nil {
		cleanupCreatedTemps()
		return 0, fmt.Errorf("compact streams: build record catalog snapshot: %w", snapErr)
	}

	// Guard against a live frame (its bytes just copied into a stream temp
	// above because some producer/legacy table row still references it)
	// whose cid has no sdn_record_index row -- buildCompactedRecordCatalogSnapshot
	// enumerates strictly from sdn_record_index, so such a frame would
	// otherwise be silently OMITTED from the compacted journal even though
	// its bytes are still physically present in the new stream file: a
	// permanently unreachable frame, never surfaced until (or unless) anyone
	// happens to notice DiskUsageBytes/LiveRecordBytes disagree. Never
	// silent: logs loudly and recovers a minimal record-upsert so the frame
	// is not lost either.
	events, guardErr := s.verifyAndRecoverOrphanLiveFrames(plans, events)
	if guardErr != nil {
		cleanupCreatedTemps()
		return 0, fmt.Errorf("compact streams: %w", guardErr)
	}

	journalPath := filepath.Join(s.basePath, recordCatalogJournalFileName)
	journalOldInfo, statErr := os.Stat(journalPath)
	var journalOldSize int64
	if statErr == nil {
		journalOldSize = journalOldInfo.Size()
	} else if !os.IsNotExist(statErr) {
		cleanupCreatedTemps()
		return 0, fmt.Errorf("compact streams: stat record catalog journal: %w", statErr)
	}

	journalTempRel := recordCatalogJournalFileName + genSuffix
	journalTempAbs := filepath.Join(s.basePath, journalTempRel)
	if err := writeCompactedJournalSnapshot(journalTempAbs, events); err != nil {
		cleanupCreatedTemps()
		return 0, fmt.Errorf("compact streams: %w", err)
	}
	createdTemps = append(createdTemps, journalTempAbs)

	journalNewInfo, statErr := os.Stat(journalTempAbs)
	if statErr != nil {
		cleanupCreatedTemps()
		return 0, fmt.Errorf("compact streams: stat compacted journal temp: %w", statErr)
	}
	journalNewSize := journalNewInfo.Size()

	compactionCrashPoint(compactionStageAfterJournalTemp)

	// --- Step 4: commit manifest -- the linearization point. ---
	manifest := compactionManifest{Generation: gen}
	for _, plan := range plans {
		manifest.Pairs = append(manifest.Pairs, compactionRenamePair{TempPath: plan.tempRel, FinalPath: plan.streamRel})
	}
	manifest.Pairs = append(manifest.Pairs, compactionRenamePair{TempPath: journalTempRel, FinalPath: recordCatalogJournalFileName})

	manifestRel := filepath.Join(flatSQLStreamDirName, compactionManifestPrefix+strconv.FormatInt(gen, 10))
	manifestAbs := filepath.Join(s.basePath, manifestRel)
	if err := writeCompactionManifest(manifestAbs, manifest); err != nil {
		cleanupCreatedTemps()
		return 0, fmt.Errorf("compact streams: %w", err)
	}
	if err := fsyncDir(s.streamDir); err != nil {
		_ = os.Remove(manifestAbs)
		cleanupCreatedTemps()
		return 0, fmt.Errorf("compact streams: %w", err)
	}
	if err := fsyncDir(s.basePath); err != nil {
		_ = os.Remove(manifestAbs)
		cleanupCreatedTemps()
		return 0, fmt.Errorf("compact streams: %w", err)
	}

	// COMMIT POINT: the manifest is now durable and CRC-valid. From here on,
	// every rename it lists WILL be applied -- either by the loop below, or
	// (if this process dies first) by recoverPendingCompaction on the next
	// writable open, which re-applies the full set idempotently (a temp
	// that is already gone means its rename already happened). A real I/O
	// error from os.Rename past this point is intentionally NOT cleaned up
	// here: the on-disk state is still a well-defined, recoverable
	// post-commit state, and the documented recovery path (next writable
	// open) resolves it -- see the design's recovery model (§4), which is
	// restart-triggered rather than live-self-healing by design for this
	// minimal increment.
	compactionCrashPoint(compactionStageAfterManifest)

	// --- Step 5: apply renames. ---
	for i, pair := range manifest.Pairs {
		tempAbs := filepath.Join(s.basePath, pair.TempPath)
		finalAbs := filepath.Join(s.basePath, pair.FinalPath)
		if err := os.Rename(tempAbs, finalAbs); err != nil {
			return 0, fmt.Errorf("compact streams: apply rename %s -> %s: %w", pair.TempPath, pair.FinalPath, err)
		}
		if i == 0 && len(manifest.Pairs) > 1 {
			compactionCrashPoint(compactionStageMidRename)
		}
	}
	if err := fsyncDir(s.streamDir); err != nil {
		return 0, fmt.Errorf("compact streams: %w", err)
	}
	if err := fsyncDir(s.basePath); err != nil {
		return 0, fmt.Errorf("compact streams: %w", err)
	}
	if err := os.Remove(manifestAbs); err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("compact streams: remove commit manifest: %w", err)
	}
	if err := fsyncDir(s.streamDir); err != nil {
		return 0, fmt.Errorf("compact streams: %w", err)
	}

	// --- Step 6: refresh live in-memory state (no restart needed). ---
	if err := s.recordCatalog.reopen(); err != nil {
		return 0, fmt.Errorf("compact streams: reopen record catalog journal onto new inode: %w", err)
	}
	for _, plan := range plans {
		if err := s.applyCompactionOffsetRemap(plan.standard, plan.streamRel, plan.offsetMap); err != nil {
			return 0, fmt.Errorf("compact streams: refresh in-memory offsets for %s: %w", plan.schemaName, err)
		}
	}

	for _, plan := range plans {
		reclaimedBytes += plan.oldSize - plan.newSize
	}
	reclaimedBytes += journalOldSize - journalNewSize

	log.Infof("CompactStreams: reclaimed %d bytes across %d standard(s) (journal %d -> %d bytes)", reclaimedBytes, len(plans), journalOldSize, journalNewSize)
	return reclaimedBytes, nil
}

// writeCompactedStreamTemp copies every live frame in frames (already
// ordered oldest-offset-first) from srcAbs into a brand-new file at
// tempAbs, verbatim and back-to-back with NO padding -- reproducing
// flatSQLStreamAppender.Append's on-disk layout exactly (flatsql.go
// Append: `[4-byte LE length][length bytes payload]`, `stream_offset`
// pointing at the length prefix). Each frame's on-disk length prefix is
// validated against the metadata's record_length before it is trusted and
// copied -- a mismatch means on-disk corruption, and this returns a hard
// error rather than silently compacting over it. Returns the resulting
// file size and the old->new offset map. Deliberately does NOT touch
// srcAbs.
func writeCompactedStreamTemp(srcAbs, tempAbs string, frames []liveStreamFrame) (newSize int64, offsetMap map[int64]int64, err error) {
	src, err := os.Open(srcAbs)
	if err != nil {
		return 0, nil, fmt.Errorf("open source stream %s: %w", srcAbs, err)
	}
	defer src.Close()

	tmp, err := os.OpenFile(tempAbs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, 0o600)
	if err != nil {
		return 0, nil, fmt.Errorf("create compacted stream temp %s: %w", tempAbs, err)
	}
	w := bufio.NewWriterSize(tmp, 4*1024*1024)

	offsetMap = make(map[int64]int64, len(frames))
	var cursor int64
	var prefix [4]byte
	for _, frame := range frames {
		if _, err := src.ReadAt(prefix[:], frame.offset); err != nil {
			tmp.Close()
			return 0, nil, fmt.Errorf("read frame prefix at %d: %w", frame.offset, err)
		}
		prefixLen := int64(binary.LittleEndian.Uint32(prefix[:]))
		if prefixLen != frame.length {
			tmp.Close()
			return 0, nil, fmt.Errorf("frame at %d: on-disk prefix length %d != record_length %d (corruption)", frame.offset, prefixLen, frame.length)
		}
		raw := make([]byte, 4+frame.length)
		if _, err := src.ReadAt(raw, frame.offset); err != nil {
			tmp.Close()
			return 0, nil, fmt.Errorf("read frame at %d (%d bytes): %w", frame.offset, len(raw), err)
		}
		if _, err := w.Write(raw); err != nil {
			tmp.Close()
			return 0, nil, fmt.Errorf("write frame at new offset %d: %w", cursor, err)
		}
		offsetMap[frame.offset] = cursor
		cursor += int64(len(raw))
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return 0, nil, fmt.Errorf("flush compacted stream temp %s: %w", tempAbs, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return 0, nil, fmt.Errorf("sync compacted stream temp %s: %w", tempAbs, err)
	}
	if err := tmp.Close(); err != nil {
		return 0, nil, fmt.Errorf("close compacted stream temp %s: %w", tempAbs, err)
	}
	return cursor, offsetMap, nil
}

// verifyAndRecoverOrphanLiveFrames cross-checks every live frame this
// compaction just copied into a stream temp (plans[*].offsetMap: every
// (oldOffset -> newOffset) pair CompactStreams enumerated as still
// referenced by at least one producer/legacy table row) against the record-
// upsert events buildCompactedRecordCatalogSnapshot produced (which
// enumerates strictly from sdn_record_index). Those two sets are expected
// to agree exactly; they can only disagree in the rare case where a
// producer-table row's cid has NO sdn_record_index row -- which the write
// paths deliberately allow: storeOne, storeBatchChunk, and
// StoreRoutedByProducer all treat an upsertRecordIndex/upsertRecordIndexExec
// failure as non-fatal ("Do not fail writes if index extraction fails for a
// record") and simply log and continue, leaving the stream frame and
// producer-table row in place with no index row at all.
//
// Left unguarded, such a frame's bytes would still be copied into the
// compacted stream temp (writeCompactedStreamTemp copies every frame the
// enumeration step found live, which reads straight from the producer/
// legacy tables, not sdn_record_index) but its metadata row would be
// silently DROPPED from the compacted journal -- no event, anywhere, would
// ever again reference that offset, permanently orphaning the frame with no
// error or log at the time it happens. This function makes that loud
// (a named Warnf identifying the exact standard/offset) instead of silent,
// and -- cheaply, since the still-live producer-table row already carries
// everything a minimal upsert event needs -- also RECOVERS it by
// synthesizing that event, so compaction never makes the loss worse than it
// already (silently) was. Callers hold s.mu (Lock).
func (s *FlatSQLStore) verifyAndRecoverOrphanLiveFrames(plans []*standardCompactionPlan, events []recordCatalogEvent) ([]recordCatalogEvent, error) {
	covered := make(map[string]map[int64]bool, len(plans))
	for _, event := range events {
		if event.Kind != recordCatalogEventRecordUpsert {
			continue
		}
		m := covered[event.StreamPath]
		if m == nil {
			m = make(map[int64]bool)
			covered[event.StreamPath] = m
		}
		m[event.StreamOffset] = true
	}

	var recovered []recordCatalogEvent
	for _, plan := range plans {
		m := covered[plan.streamRel]
		for oldOffset, newOffset := range plan.offsetMap {
			if m != nil && m[newOffset] {
				continue
			}
			log.Warnf("CompactStreams: live frame %s@%d (standard %s, new offset %d) has no sdn_record_index row -- recovering a minimal record-catalog entry so it is not silently dropped from the compacted journal", plan.streamRel, oldOffset, plan.standard, newOffset)
			event, ok, err := s.synthesizeOrphanRecordUpsertEvent(plan, oldOffset, newOffset)
			if err != nil {
				return nil, fmt.Errorf("recover orphan live frame %s@%d: %w", plan.streamRel, oldOffset, err)
			}
			if ok {
				recovered = append(recovered, event)
			}
		}
	}
	if len(recovered) == 0 {
		return events, nil
	}
	return append(events, recovered...), nil
}

// synthesizeOrphanRecordUpsertEvent rebuilds a minimal recordCatalogEvent
// for a live frame verifyAndRecoverOrphanLiveFrames found with no
// sdn_record_index row, reading the still-live producer/legacy table row
// that referenced (plan.streamRel, oldOffset) directly (the exact source
// writeCompactedStreamTemp's frame enumeration used). ok is false only if
// the row has already vanished by the time this runs -- unreachable under
// s.mu.Lock() held for CompactStreams' entire duration, but checked rather
// than assumed.
func (s *FlatSQLStore) synthesizeOrphanRecordUpsertEvent(plan *standardCompactionPlan, oldOffset, newOffset int64) (recordCatalogEvent, bool, error) {
	readSource, err := s.recordReadSource(plan.schemaName)
	if err != nil {
		return recordCatalogEvent{}, false, fmt.Errorf("read source for %s: %w", plan.schemaName, err)
	}
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT cid, peer_id, timestamp, record_length, signature_hex, created_at
		FROM %s
		WHERE stream_path = ? AND stream_offset = ?
	`, readSource), plan.streamRel, oldOffset)
	if err != nil {
		return recordCatalogEvent{}, false, fmt.Errorf("query orphan frame %s@%d: %w", plan.streamRel, oldOffset, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return recordCatalogEvent{}, false, fmt.Errorf("orphan frame %s@%d vanished from every read source", plan.streamRel, oldOffset)
	}
	var cid, peerID string
	var timestamp, recordLength, createdAt int64
	var signatureHex sql.NullString
	if err := rows.Scan(&cid, &peerID, &timestamp, &recordLength, &signatureHex, &createdAt); err != nil {
		return recordCatalogEvent{}, false, fmt.Errorf("scan orphan frame %s@%d: %w", plan.streamRel, oldOffset, err)
	}
	sigHex := ""
	if signatureHex.Valid {
		sigHex = signatureHex.String
	}
	return recordCatalogEvent{
		Kind:         recordCatalogEventRecordUpsert,
		SchemaName:   plan.schemaName,
		CID:          cid,
		PeerID:       peerID,
		StreamPath:   plan.streamRel,
		StreamOffset: newOffset,
		RecordLength: recordLength,
		SignatureHex: sigHex,
		Timestamp:    timestamp,
		CreatedAt:    createdAt,
		// Index left zero-valued (no RowID): replay's
		// applyRecordCatalogIndexUpsertTo then takes its "index.RowID <= 0"
		// branch -- a plain (non-explicit-rowid) INSERT ... ON CONFLICT DO
		// UPDATE into sdn_record_index -- recreating exactly the bare row
		// upsertRecordIndex/upsertRecordIndexExec would have inserted had
		// their own index-upsert not hit the non-fatal error that orphaned
		// this frame in the first place.
	}, true, nil
}

// buildCompactionOffsetRemapCase compiles offsetMap into a single SQLite
// `CASE stream_offset WHEN ? THEN ? WHEN ? THEN ? ... ELSE stream_offset
// END` expression plus a matching `stream_offset IN (?, ?, ...)` predicate,
// so every row a remap touches is rewritten by ONE UPDATE statement instead
// of one UPDATE per (old, new) pair.
//
// This matters because compaction always moves frames LEFT: it is the
// common case that some frame's newOffset equals another frame's
// oldOffset (e.g. old->new = {204->0, 408->204}). A loop issuing one
// `UPDATE ... SET stream_offset = :new WHERE stream_offset = :old` per pair
// is UNSAFE against that overlap: whichever pair happens to run first
// mutates rows in place, and a LATER pair's `WHERE stream_offset = :old`
// can then re-match rows the EARLIER pair just wrote there (a row that was
// just moved to offset 204 gets swept up again by the {408->204} pair's own
// WHERE clause and re-examined... concretely: run {204->0} first, then
// {408->204} second -- the second UPDATE's `WHERE stream_offset = 408`
// still only matches genuine 408-rows, so THAT specific order is safe: the
// unsafe order is {408->204} BEFORE {204->0} -- {408->204} moves the
// 408-rows to offset 204 first, and the SUBSEQUENT {204->0} pair's
// `WHERE stream_offset = 204` then re-captures BOTH the original 204-rows
// AND the just-arrived former-408-rows, collapsing two distinct physical
// frames' worth of metadata onto the SAME new offset 0 and leaving nothing
// pointing at the frame that should have landed at 204. Since the loop
// iterates a Go map, this ordering is UNORDERED and effectively random
// across runs -- silent, nondeterministic corruption. A single UPDATE
// statement has no such hazard: SQLite (like standard SQL) evaluates every
// row's WHERE predicate and SET expression against that row's value AS OF
// THE START of the statement -- one row's newly written value is never
// visible to another row's (or its own) expression evaluation within the
// SAME statement, so there is no "order" for the CASE/WHERE to depend on,
// and every matched row is moved exactly once based on its own original
// stream_offset alone, deterministically and correctly regardless of how
// much the old/new offset ranges overlap.
//
// Returns caseSQL == "" when every remaining pair is a true no-op
// (oldOffset == newOffset, filtered out here as an optimization only -- it
// changes nothing about correctness since a self-mapping row's WHERE match
// and CASE result are identical either way, it just avoids writing a row
// back to the same value it already holds).
func buildCompactionOffsetRemapCase(offsetMap map[int64]int64) (caseSQL, inSQL string, caseArgs, inArgs []any) {
	oldOffsets := make([]int64, 0, len(offsetMap))
	for oldOffset, newOffset := range offsetMap {
		if oldOffset == newOffset {
			continue
		}
		oldOffsets = append(oldOffsets, oldOffset)
	}
	if len(oldOffsets) == 0 {
		return "", "", nil, nil
	}
	// Sort purely so the generated SQL text (and any logged/debugged query)
	// is stable and reviewable across runs -- correctness does not depend on
	// this ordering; see the exhaustive explanation above.
	sort.Slice(oldOffsets, func(i, j int) bool { return oldOffsets[i] < oldOffsets[j] })

	var buf strings.Builder
	buf.WriteString("CASE stream_offset")
	caseArgs = make([]any, 0, len(oldOffsets)*2)
	inArgs = make([]any, 0, len(oldOffsets))
	inPlaceholders := make([]string, 0, len(oldOffsets))
	for _, oldOffset := range oldOffsets {
		buf.WriteString(" WHEN ? THEN ?")
		caseArgs = append(caseArgs, oldOffset, offsetMap[oldOffset])
		inArgs = append(inArgs, oldOffset)
		inPlaceholders = append(inPlaceholders, "?")
	}
	buf.WriteString(" ELSE stream_offset END")
	return buf.String(), "stream_offset IN (" + strings.Join(inPlaceholders, ",") + ")", caseArgs, inArgs
}

// applyCompactionOffsetRemap refreshes every (producer, standard) table's
// and the legacy table's stream_offset column for the given standard,
// in-memory, immediately after the on-disk rename has already taken effect
// -- so reads issued against the SAME live store object see the new,
// smaller file straight away, with no restart required. Every row that
// referenced a remapped physical frame is moved by ONE atomic
// CASE-expression UPDATE per table (buildCompactionOffsetRemapCase), never
// a per-pair loop -- see that function's doc comment for why a per-pair
// loop is unsafe here. This also naturally covers repeat-CID mirror rows in
// multiple producer tables sharing one physical frame's old offset: the
// single UPDATE's WHERE/CASE match every row whose CURRENT stream_offset is
// one of the old offsets, regardless of how many rows across however many
// tables share it, moving them all to the same new offset together in the
// same statement. Finally clears each touched table's engine tombstone set
// (flatsqlrt.Database.ClearTombstones, documented as "used after a
// compaction rebuild"): safe even if a table name was never a registered
// tombstone-tracked source (clearTombstones is a no-op for an unknown name,
// sqlite_engine.cpp), so this call is harmless on the (expected) common
// case where these plain DDL-created control tables were never part of
// that mechanism, and correct on the case where they were. Callers hold
// s.mu (Lock).
func (s *FlatSQLStore) applyCompactionOffsetRemap(standard, streamPath string, offsetMap map[int64]int64) error {
	if len(offsetMap) == 0 {
		return nil
	}
	var tables []string
	if exists, err := s.tableExists(standard); err != nil {
		return fmt.Errorf("check legacy table %s: %w", standard, err)
	} else if exists {
		tables = append(tables, standard)
	}
	producerTables, err := s.listProducerStandardTables()
	if err != nil {
		return fmt.Errorf("list (producer, standard) tables: %w", err)
	}
	for _, t := range producerTables {
		if t.Standard == standard {
			tables = append(tables, t.TableName)
		}
	}

	caseSQL, inSQL, caseArgs, inArgs := buildCompactionOffsetRemapCase(offsetMap)
	if caseSQL == "" {
		// Every pair in offsetMap was a true no-op (oldOffset == newOffset).
		// Nothing to move, but still clear tombstones below for consistency
		// with a real remap.
		for _, tableName := range tables {
			if err := s.engineDB.ClearTombstones(tableName); err != nil {
				log.Warnf("CompactStreams: clear tombstones for %s: %v", tableName, err)
			}
		}
		return nil
	}

	for _, tableName := range tables {
		updateSQL := flatsqldrv.WithoutJournal(fmt.Sprintf(
			`UPDATE %s SET stream_offset = %s WHERE stream_path = ? AND %s`,
			tableName, caseSQL, inSQL,
		))
		args := make([]any, 0, len(caseArgs)+1+len(inArgs))
		args = append(args, caseArgs...)
		args = append(args, streamPath)
		args = append(args, inArgs...)
		if _, err := s.db.Exec(updateSQL, args...); err != nil {
			return fmt.Errorf("atomic remap update %s (%d offset pairs): %w", tableName, len(inArgs), err)
		}
		if err := s.engineDB.ClearTombstones(tableName); err != nil {
			log.Warnf("CompactStreams: clear tombstones for %s: %v", tableName, err)
		}
	}
	return nil
}

// recoverPendingCompaction inspects basePath/streamDir for an interrupted
// CompactStreams call and resolves it deterministically:
//
//   - a CRC-valid commit manifest exists -> ROLL FORWARD: apply every
//     (temp, final) rename it lists that has not already happened (temp
//     present => apply; temp absent => a prior partial run already applied
//     it -- idempotent), then remove the manifest.
//   - no manifest, or a torn/CRC-invalid one -> ROLL BACK: the compaction
//     never durably committed, so every temp for that generation (a torn
//     manifest) or every orphaned temp at all (no manifest) is deleted,
//     leaving the pre-compaction stream files and journal as the sole
//     truth.
//
// Called from newFlatSQLStore for WRITABLE opens only, after the writer
// lock is acquired and the stream directory is ensured, but BEFORE the
// record-catalog journal is opened -- so journal replay always sees a
// journal file that is unambiguously the pre- or post-compaction one, never
// a leftover temp or a half-renamed one. Read-only opens never call this:
// they take no writer lock and must never mutate the store (mirrors every
// other writer-only invariant in this store).
func recoverPendingCompaction(basePath, streamDir string) error {
	manifests, err := filepath.Glob(filepath.Join(streamDir, compactionManifestPrefix+"*"))
	if err != nil {
		return fmt.Errorf("compaction recovery: glob manifests: %w", err)
	}
	for _, manifestAbs := range manifests {
		gen := compactionGenerationFromManifestPath(manifestAbs)
		m, ok, readErr := readCompactionManifest(manifestAbs)
		if readErr != nil {
			return fmt.Errorf("compaction recovery: read manifest %s: %w", manifestAbs, readErr)
		}
		if !ok {
			log.Warnf("compaction recovery: discarding torn/invalid compaction manifest %s -- rolling back", manifestAbs)
			if rmErr := os.Remove(manifestAbs); rmErr != nil && !os.IsNotExist(rmErr) {
				return fmt.Errorf("compaction recovery: remove invalid manifest %s: %w", manifestAbs, rmErr)
			}
			if err := sweepCompactionTemps(basePath, streamDir, &gen); err != nil {
				return err
			}
			continue
		}
		for _, pair := range m.Pairs {
			tempAbs := filepath.Join(basePath, pair.TempPath)
			finalAbs := filepath.Join(basePath, pair.FinalPath)
			if _, statErr := os.Stat(tempAbs); statErr == nil {
				if renErr := os.Rename(tempAbs, finalAbs); renErr != nil {
					return fmt.Errorf("compaction recovery: rollforward rename %s -> %s: %w", pair.TempPath, pair.FinalPath, renErr)
				}
			} else if !os.IsNotExist(statErr) {
				return fmt.Errorf("compaction recovery: stat %s: %w", pair.TempPath, statErr)
			}
		}
		if err := fsyncDir(streamDir); err != nil {
			return fmt.Errorf("compaction recovery: %w", err)
		}
		if err := fsyncDir(basePath); err != nil {
			return fmt.Errorf("compaction recovery: %w", err)
		}
		if rmErr := os.Remove(manifestAbs); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("compaction recovery: remove committed manifest %s: %w", manifestAbs, rmErr)
		}
		if err := fsyncDir(streamDir); err != nil {
			return fmt.Errorf("compaction recovery: %w", err)
		}
		log.Infof("compaction recovery: rolled forward pending compaction (generation %d)", m.Generation)
	}
	// Final defensive sweep, independent of generation: catches temps left
	// behind by a crash BEFORE the manifest was ever written (no manifest
	// existed above to key a generation-scoped sweep off of).
	return sweepCompactionTemps(basePath, streamDir, nil)
}

// compactionGenerationFromManifestPath extracts the generation suffix from
// a "compaction.commit-<gen>" manifest file name. Returns 0 (matching no
// real generation, since generations start at 1) if the name is malformed
// -- callers fall back to sweeping ALL generations in that case via the
// unconditional final sweep in recoverPendingCompaction.
func compactionGenerationFromManifestPath(manifestAbs string) int64 {
	base := filepath.Base(manifestAbs)
	genStr := strings.TrimPrefix(base, compactionManifestPrefix)
	gen, err := strconv.ParseInt(genStr, 10, 64)
	if err != nil {
		return 0
	}
	return gen
}

// sweepCompactionTemps removes orphaned compaction temp files: stream temps
// (<STANDARD>.flatsql.compact-<gen>) and the journal temp
// (record-catalog.flatsqlmeta.compact-<gen>). gen == nil sweeps every
// generation (used when no manifest exists at all, or as the final
// defensive pass); a non-nil gen scopes the sweep to one generation (used
// when a specific manifest was found torn/invalid, so only ITS temps are
// known-orphaned).
func sweepCompactionTemps(basePath, streamDir string, gen *int64) error {
	var streamPattern, journalPattern string
	if gen != nil {
		suffix := compactionTempInfix + strconv.FormatInt(*gen, 10)
		streamPattern = filepath.Join(streamDir, "*.flatsql"+suffix)
		journalPattern = filepath.Join(basePath, recordCatalogJournalFileName+suffix)
	} else {
		streamPattern = filepath.Join(streamDir, "*.flatsql"+compactionTempInfix+"*")
		journalPattern = filepath.Join(basePath, recordCatalogJournalFileName+compactionTempInfix+"*")
	}

	removed := 0
	for _, pattern := range []string{streamPattern, journalPattern} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("compaction recovery: glob %s: %w", pattern, err)
		}
		for _, p := range matches {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("compaction recovery: remove orphan temp %s: %w", p, err)
			}
			removed++
		}
	}
	if removed > 0 {
		if err := fsyncDir(streamDir); err != nil {
			return fmt.Errorf("compaction recovery: %w", err)
		}
		if err := fsyncDir(basePath); err != nil {
			return fmt.Errorf("compaction recovery: %w", err)
		}
		log.Infof("compaction recovery: swept %d orphan compaction temp file(s)", removed)
	}
	return nil
}
