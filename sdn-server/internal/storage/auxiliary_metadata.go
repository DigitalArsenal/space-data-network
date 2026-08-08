package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"strings"
	"sync"
)

const (
	auxiliaryMetadataFileName     = "auxiliary.flatsqlmeta"
	auxiliaryMetadataFrameVersion = 1

	auxiliaryEventDirectoryUpsert               = "directory_upsert"
	auxiliaryEventLocalEPMUpsert                = "local_epm_upsert"
	auxiliaryEventDatasetShardPublicationUpsert = "dataset_shard_publication_upsert"
	auxiliaryEventDatasetShardPublicationDelete = "dataset_shard_publication_delete"
	auxiliaryEventPinLedgerUpsert               = "pin_ledger_upsert"
	auxiliaryEventDatasetReplayStateUpsert      = "dataset_replay_state_upsert"
	auxiliaryEventAssetOIDCReceiptConsume       = "asset_oidc_receipt_consume"
	auxiliaryEventAssetPinReferenceUpsert       = "asset_pin_reference_upsert"
	auxiliaryEventAssetPinReferenceTransition   = "asset_pin_reference_transition"
	auxiliaryEventAssetPinReferenceDelete       = "asset_pin_reference_delete"
	auxiliaryEventAssetPinAuditAppend           = "asset_pin_audit_append"
	auxiliaryEventSourceBatchLicenseUpsert      = "source_batch_license_upsert"
)

type auxiliaryMetadataStore struct {
	mu          sync.Mutex
	f           *os.File
	appendFile  auxiliaryMetadataAppendFile
	path        string
	readOnly    bool
	replayLimit int64
	assetFrames map[string][]byte
	// digest / digestOffset are the running prefix fingerprint behind the
	// resume mark (flatsql_boot_state.go). Incremental for the same reason the
	// record catalog's is: a checkpoint must never re-walk the whole journal.
	digest       hash.Hash
	digestOffset int64
	// chunkBytes overrides the per-chunk replay byte budget
	// (WithAuxiliaryReplayChunkBytes). Zero = auxiliaryReplayChunkBytes.
	chunkBytes int64
}

type auxiliaryMetadataAppendFile interface {
	Write([]byte) (int, error)
	Sync() error
}

var errAuxiliaryMetadataAppendRecoveryRequired = errors.New("auxiliary metadata append requires recovery")

type auxiliaryMetadataEvent struct {
	Kind string `json:"kind"`

	Directory                     *DirectoryRecord                        `json:"directory,omitempty"`
	LocalEPM                      *auxiliaryLocalEPMRecord                `json:"local_epm,omitempty"`
	DatasetShardPublication       *DatasetShardPublication                `json:"dataset_shard_publication,omitempty"`
	DatasetShardPublicationDelete *auxiliaryDatasetShardPublicationDelete `json:"dataset_shard_publication_delete,omitempty"`
	PinLedger                     *PinLedgerEntry                         `json:"pin_ledger,omitempty"`
	DatasetReplayState            *DatasetPublicationReplayState          `json:"dataset_replay_state,omitempty"`
	AssetOIDCReceipt              *AssetOIDCReceipt                       `json:"asset_oidc_receipt,omitempty"`
	AssetPinReferenceUpsert       *auxiliaryAssetPinReferenceUpsert       `json:"asset_pin_reference_upsert,omitempty"`
	AssetPinReferenceTransition   *auxiliaryAssetPinReferenceTransition   `json:"asset_pin_reference_transition,omitempty"`
	AssetPinReferenceDelete       *auxiliaryAssetPinReferenceDelete       `json:"asset_pin_reference_delete,omitempty"`
	AssetPinAuditEvent            *AssetPinAuditEvent                     `json:"asset_pin_audit_event,omitempty"`
	SourceBatchLicense            *SourceBatchLicense                     `json:"source_batch_license,omitempty"`
}

type auxiliaryMetadataFrame struct {
	Version int                    `json:"version"`
	Event   auxiliaryMetadataEvent `json:"event"`
}

type auxiliaryLocalEPMRecord struct {
	PeerID            string `json:"peer_id"`
	EncryptedEPMBytes string `json:"encrypted_epm_bytes"`
	UpdatedAt         int64  `json:"updated_at"`
}

type auxiliaryDatasetShardPublicationDelete struct {
	Query  DatasetShardPublicationQuery `json:"query"`
	Offset int                          `json:"offset"`
}

func openAuxiliaryMetadataStore(path string, readOnly bool) (*auxiliaryMetadataStore, error) {
	if readOnly {
		f, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			if os.IsNotExist(err) {
				return &auxiliaryMetadataStore{path: path, readOnly: true, assetFrames: map[string][]byte{}}, nil
			}
			return nil, fmt.Errorf("auxiliary metadata: open read-only: %w", err)
		}
		valid, err := scanRecordCatalogValidLength(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		assetFrames, err := scanAuxiliaryAssetFrames(f, valid)
		if err != nil {
			f.Close()
			return nil, err
		}
		return &auxiliaryMetadataStore{f: f, appendFile: f, path: path, readOnly: true, replayLimit: valid, assetFrames: assetFrames}, nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("auxiliary metadata: open: %w", err)
	}
	valid, err := scanRecordCatalogValidLength(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	assetFrames, err := scanAuxiliaryAssetFrames(f, valid)
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Truncate(valid); err != nil {
		f.Close()
		return nil, fmt.Errorf("auxiliary metadata: truncate torn tail: %w", err)
	}
	if _, err := f.Seek(valid, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &auxiliaryMetadataStore{f: f, appendFile: f, path: path, assetFrames: assetFrames}, nil
}

func scanAuxiliaryAssetFrames(f *os.File, size int64) (map[string][]byte, error) {
	frames := map[string][]byte{}
	var off int64
	var hdr [8]byte
	for off < size {
		if _, err := f.ReadAt(hdr[:], off); err != nil {
			return nil, err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		payload := make([]byte, n)
		if _, err := f.ReadAt(payload, off+8); err != nil {
			return nil, err
		}
		event, err := decodeAuxiliaryMetadataEvent(payload)
		if err != nil {
			return nil, err
		}
		identity := auxiliaryAssetFrameIdentity(event)
		if existing, ok := frames[identity]; identity != "" && ok {
			equal, err := equalAuxiliaryAssetFramePayloads(identity, existing, payload)
			if err != nil {
				return nil, err
			}
			if !equal {
				return nil, auxiliaryAssetFrameConflict(identity)
			}
		} else if identity != "" {
			frames[identity] = bytes.Clone(payload)
		}
		off += 8 + n
	}
	return frames, nil
}

func (m *auxiliaryMetadataStore) Append(event auxiliaryMetadataEvent) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly {
		return fmt.Errorf("auxiliary metadata %s is read-only", m.path)
	}
	payload, err := encodeAuxiliaryMetadataEvent(event)
	if err != nil {
		return err
	}
	identity := auxiliaryAssetFrameIdentity(event)
	if err := m.checkAssetFrameLocked(identity, payload); err != nil {
		return err
	}
	if _, ok := m.assetFrames[identity]; identity != "" && ok {
		return nil
	}
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(hdr[4:], crc32.ChecksumIEEE(payload))
	if err := writeAuxiliaryMetadataFramePart(m.appendFile, hdr[:], false); err != nil {
		return err
	}
	if err := writeAuxiliaryMetadataFramePart(m.appendFile, payload, true); err != nil {
		return err
	}
	if err := m.appendFile.Sync(); err != nil {
		return errors.Join(err, errAuxiliaryMetadataAppendRecoveryRequired)
	}
	if identity != "" {
		m.assetFrames[identity] = bytes.Clone(payload)
	}
	return nil
}

func writeAuxiliaryMetadataFramePart(file auxiliaryMetadataAppendFile, part []byte, priorWrite bool) error {
	n, err := file.Write(part)
	invalidCount := n < 0 || n > len(part)
	if invalidCount {
		countErr := fmt.Errorf("auxiliary metadata: invalid write count %d for %d bytes", n, len(part))
		if err != nil {
			err = errors.Join(err, countErr)
		} else {
			err = countErr
		}
	} else if n != len(part) && err == nil {
		err = io.ErrShortWrite
	}
	if err == nil {
		return nil
	}
	if priorWrite || n > 0 || invalidCount {
		return errors.Join(err, errAuxiliaryMetadataAppendRecoveryRequired)
	}
	return err
}

func (m *auxiliaryMetadataStore) CheckAssetFrame(event auxiliaryMetadataEvent) error {
	if m == nil {
		return nil
	}
	payload, err := encodeAuxiliaryMetadataEvent(event)
	if err != nil {
		return err
	}
	identity := auxiliaryAssetFrameIdentity(event)
	if identity == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checkAssetFrameLocked(identity, payload)
}

func (m *auxiliaryMetadataStore) ResolveAssetOIDCReceipt(receipt AssetOIDCReceipt) (AssetOIDCReceipt, error) {
	if m == nil {
		return receipt, nil
	}
	event := auxiliaryMetadataEvent{Kind: auxiliaryEventAssetOIDCReceiptConsume, AssetOIDCReceipt: &receipt}
	payload, err := encodeAuxiliaryMetadataEvent(event)
	if err != nil {
		return AssetOIDCReceipt{}, err
	}
	identity := auxiliaryAssetFrameIdentity(event)
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.assetFrames[identity]
	if !ok {
		return receipt, nil
	}
	equal, err := equalAuxiliaryAssetFramePayloads(identity, existing, payload)
	if err != nil {
		return AssetOIDCReceipt{}, err
	}
	if !equal {
		return AssetOIDCReceipt{}, auxiliaryAssetFrameConflict(identity)
	}
	durable, err := decodeAuxiliaryMetadataEvent(existing)
	if err != nil {
		return AssetOIDCReceipt{}, err
	}
	if durable.AssetOIDCReceipt == nil {
		return AssetOIDCReceipt{}, fmt.Errorf("auxiliary asset frame %q is missing its receipt", identity)
	}
	return *durable.AssetOIDCReceipt, nil
}

func (m *auxiliaryMetadataStore) checkAssetFrameLocked(identity string, payload []byte) error {
	existing, ok := m.assetFrames[identity]
	if !ok {
		return nil
	}
	equal, err := equalAuxiliaryAssetFramePayloads(identity, existing, payload)
	if err != nil {
		return err
	}
	if equal {
		return nil
	}
	return auxiliaryAssetFrameConflict(identity)
}

func equalAuxiliaryAssetFramePayloads(identity string, left, right []byte) (bool, error) {
	if !strings.HasPrefix(identity, "receipt:") {
		return bytes.Equal(left, right), nil
	}
	leftEvent, err := decodeAuxiliaryMetadataEvent(left)
	if err != nil {
		return false, err
	}
	rightEvent, err := decodeAuxiliaryMetadataEvent(right)
	if err != nil {
		return false, err
	}
	if leftEvent.AssetOIDCReceipt == nil || rightEvent.AssetOIDCReceipt == nil {
		return false, fmt.Errorf("auxiliary asset frame %q is missing its receipt", identity)
	}
	return equalAssetOIDCReceiptIdentity(*leftEvent.AssetOIDCReceipt, *rightEvent.AssetOIDCReceipt), nil
}

func auxiliaryAssetFrameIdentity(event auxiliaryMetadataEvent) string {
	switch event.Kind {
	case auxiliaryEventAssetOIDCReceiptConsume:
		if event.AssetOIDCReceipt != nil {
			return "receipt:" + strings.ToLower(strings.TrimSpace(event.AssetOIDCReceipt.Digest))
		}
	case auxiliaryEventAssetPinReferenceUpsert:
		if event.AssetPinReferenceUpsert != nil {
			return "event:" + strings.TrimSpace(event.AssetPinReferenceUpsert.Event.EventID)
		}
	case auxiliaryEventAssetPinReferenceTransition:
		if event.AssetPinReferenceTransition != nil {
			return "event:" + strings.TrimSpace(event.AssetPinReferenceTransition.Event.EventID)
		}
	case auxiliaryEventAssetPinReferenceDelete:
		if event.AssetPinReferenceDelete != nil {
			return "event:" + strings.TrimSpace(event.AssetPinReferenceDelete.Event.EventID)
		}
	case auxiliaryEventAssetPinAuditAppend:
		if event.AssetPinAuditEvent != nil {
			return "event:" + strings.TrimSpace(event.AssetPinAuditEvent.EventID)
		}
	}
	return ""
}

func auxiliaryAssetFrameConflict(identity string) error {
	if strings.HasPrefix(identity, "receipt:") {
		return fmt.Errorf("auxiliary asset frame %q: %w", identity, ErrAssetOIDCReceiptConflict)
	}
	return fmt.Errorf("auxiliary asset frame %q: %w", identity, ErrAssetPinAuditConflict)
}

// auxiliaryReplayChunkFrames is how many frames share ONE transaction during a
// replay.
//
// THE SIZE OF THIS NUMBER IS THE WHOLE DEFECT. Before it existed every frame
// was its own autocommit transaction, which against the disk-backed,
// TRUNCATE-journalled control database is a journal write + a database write +
// a truncate, each fsynced: ~250 transactions/second, rewriting the same 2.2 MB
// file, 211 s of store-open on a 20 MB auxiliary journal (task
// flatsql-aux-replay-resume-mark). An in-order replay of an append-only journal
// needs NO per-event durability — the journal IS the source of truth, and a
// crash mid-replay simply replays again from the last mark. So the only thing
// per-event commits bought was the fsync bill.
//
// 512 keeps a chunk's rollback footprint small and its transaction short enough
// that the engine's page cache is not asked to hold an unbounded write set.
const auxiliaryReplayChunkFrames = 512

// auxiliaryReplayChunkBytes bounds the SAME chunk in bytes.
//
// A frame count alone does not bound a transaction: 512 frames is 512 tiny
// directory rows or 512 multi-megabyte payloads, and only one of those two is
// "a short transaction". This is the replay-side half of the length-awareness
// the owner asked for (graph/tasks/sdn-sharding-not-length-aware.md) — the
// shard side is DefaultDatasetPublicationMaxShardBytes.
//
// 8 MiB is the write set a chunk may hold, not a frame limit: a frame LARGER
// than the budget is still applied whole (one frame per chunk), because a
// replay that refuses its own journal is worse than a big transaction, and
// because a frame is never split. Overridable per store with
// WithAuxiliaryReplayChunkBytes.
const auxiliaryReplayChunkBytes int64 = 8 << 20

// replayChunkBytes is the effective per-chunk byte budget for this store.
func (m *auxiliaryMetadataStore) replayChunkBytes() int64 {
	if m != nil && m.chunkBytes > 0 {
		return m.chunkBytes
	}
	return auxiliaryReplayChunkBytes
}

// Replay applies the whole auxiliary journal. Retained for the callers that
// genuinely mean "from the beginning" — the poisoned-engine recovery
// (engine_link.go) and tests.
func (m *auxiliaryMetadataStore) Replay(store *FlatSQLStore) (int, error) {
	count, _, err := m.ReplayFrom(store, 0)
	return count, err
}

// ReplayFrom applies the auxiliary journal from `from` and reports how many
// frames it applied and the offset it applied THROUGH — which is what the
// resume mark is allowed to name.
//
// `from` is trusted only as far as it is plausible: a negative offset, or one
// past the journal's valid length, replays everything. The caller
// (auxiliaryResumeOffset) has already verified the prefix digest; this is the
// second belt.
func (m *auxiliaryMetadataStore) ReplayFrom(store *FlatSQLStore, from int64) (int, int64, error) {
	if m == nil || m.f == nil {
		return 0, 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	info, err := m.f.Stat()
	if err != nil {
		return 0, 0, err
	}
	size := info.Size()
	if m.readOnly {
		size = m.replayLimit
	}
	off := from
	if off < 0 || off > size {
		off = 0
	}
	count := 0
	for off < size {
		applied, next, stop, err := m.replayChunkLocked(store, off, size)
		count += applied
		off = next
		if err != nil {
			return count, off, err
		}
		if stop {
			break
		}
	}
	return count, off, nil
}

// replayChunkLocked applies up to auxiliaryReplayChunkFrames frames — or
// replayChunkBytes() bytes, whichever comes first — inside ONE transaction and
// reports how far it got. stop=true means the journal ended early — a
// zero-length frame, a frame running past the valid length, or a CRC mismatch —
// which is the torn tail the replay has always simply stopped at.
//
// A budget stop is NOT a torn tail. That distinction is load-bearing: the
// caller treats stop=true as "the journal ends here", so a byte budget that
// reported itself the same way would silently truncate the replay. The two
// reasons are tracked separately below.
//
// A failure anywhere in the chunk rolls the WHOLE chunk back and reports the
// offset unchanged, so nothing partial is ever counted as applied. That is
// stricter than the per-event path it replaces (which left earlier events
// committed), and it is the safe direction: the boot fails, and the next one
// replays from a mark that covers only committed work.
func (m *auxiliaryMetadataStore) replayChunkLocked(store *FlatSQLStore, off, size int64) (int, int64, bool, error) {
	tx, err := store.beginAuxiliaryReplayBatch()
	if err != nil {
		return 0, off, false, err
	}
	defer store.endAuxiliaryReplayBatch()
	defer tx.Rollback() // no-op once the commit below has succeeded

	var hdr [8]byte
	applied := 0
	cur := off
	torn := false
	budget := m.replayChunkBytes()
	var chunkBytes int64
	for applied < auxiliaryReplayChunkFrames && cur < size {
		if _, err := m.f.ReadAt(hdr[:], cur); err != nil {
			return 0, off, false, err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		crc := binary.LittleEndian.Uint32(hdr[4:])
		if n == 0 || cur+8+n > size {
			torn = true
			break
		}
		// Byte budget: stop BETWEEN frames, never inside one, and never
		// before the chunk has applied anything.
		if applied > 0 && budget > 0 && chunkBytes+8+n > budget {
			break
		}
		payload := make([]byte, n)
		if _, err := m.f.ReadAt(payload, cur+8); err != nil {
			return 0, off, false, err
		}
		if crc32.ChecksumIEEE(payload) != crc {
			torn = true
			break
		}
		event, err := decodeAuxiliaryMetadataEvent(payload)
		if err != nil {
			return 0, off, false, err
		}
		if err := store.applyAuxiliaryMetadataEvent(event); err != nil {
			return 0, off, false, err
		}
		applied++
		cur += 8 + n
		chunkBytes += 8 + n
	}
	if err := tx.Commit(); err != nil {
		return 0, off, false, fmt.Errorf("commit auxiliary metadata replay chunk: %w", err)
	}
	// Only a TORN frame ends the replay. Stopping on either budget (frames or
	// bytes) with journal left simply means the next chunk continues.
	return applied, cur, torn && cur < size, nil
}

// validLength reports the auxiliary journal's CRC-valid length as established
// when it was opened: writers truncate a torn tail away, readers are bounded by
// replayLimit.
func (m *auxiliaryMetadataStore) validLength() int64 {
	if m == nil || m.f == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly {
		return m.replayLimit
	}
	info, err := m.f.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}

// digestPrefix fingerprints the auxiliary journal's frame headers over
// [0, limit) — headers only, incrementally, for the reasons spelled out on
// recordCatalogJournal.digestPrefix.
func (m *auxiliaryMetadataStore) digestPrefix(limit int64) (string, error) {
	if m == nil || m.f == nil {
		return "", errors.New("auxiliary metadata journal is not open")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.digest == nil || limit < m.digestOffset {
		m.digest = newAuxiliaryMetadataDigest()
		m.digestOffset = 0
	}
	if err := extendRecordCatalogDigest(m.digest, m.f, &m.digestOffset, limit); err != nil {
		return "", err
	}
	return sealRecordCatalogDigest(m.digest, limit)
}

func (m *auxiliaryMetadataStore) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.f == nil {
		return nil
	}
	return m.f.Close()
}

func encodeAuxiliaryMetadataEvent(event auxiliaryMetadataEvent) ([]byte, error) {
	if event.Kind == "" {
		return nil, fmt.Errorf("auxiliary metadata event kind is required")
	}
	return json.Marshal(auxiliaryMetadataFrame{
		Version: auxiliaryMetadataFrameVersion,
		Event:   event,
	})
}

func decodeAuxiliaryMetadataEvent(payload []byte) (auxiliaryMetadataEvent, error) {
	var frame auxiliaryMetadataFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		return auxiliaryMetadataEvent{}, err
	}
	if frame.Version != auxiliaryMetadataFrameVersion {
		return auxiliaryMetadataEvent{}, fmt.Errorf("unsupported auxiliary metadata frame version %d", frame.Version)
	}
	if frame.Event.Kind == "" {
		return auxiliaryMetadataEvent{}, fmt.Errorf("auxiliary metadata event kind is required")
	}
	return frame.Event, nil
}

// appendAuxiliaryMetadata is the funnel for the EIGHT auxiliary writers that
// apply THEN append, and it advances the applied high-water mark on the way
// out — the same structural trick appendCatalogEvents uses: the mark moves at
// the one place that is definitionally reached only after an apply.
//
// See noteAuxiliaryApplied for why the ninth writer needs the other funnel.
func (s *FlatSQLStore) appendAuxiliaryMetadata(event auxiliaryMetadataEvent) error {
	if s == nil || s.auxiliaryMetadata == nil {
		return nil
	}
	if err := s.auxiliaryMetadata.Append(event); err != nil {
		return err
	}
	s.noteAuxiliaryApplied()
	return nil
}

// appendAuxiliaryMetadataBeforeApply is the funnel for the ONE writer that
// journals BEFORE it commits (the asset-pin lane). It must not advance
// anything: between this call and that commit there is a frame on disk whose
// rows do not exist yet, and a mark covering it would lose them for good. The
// caller notes the offset itself, after its commit succeeds.
func (s *FlatSQLStore) appendAuxiliaryMetadataBeforeApply(event auxiliaryMetadataEvent) error {
	if s == nil || s.auxiliaryMetadata == nil {
		return nil
	}
	return s.auxiliaryMetadata.Append(event)
}

// auxWriter is the SQL surface an auxiliary-metadata applier writes through.
// Both *sql.DB (live traffic) and *sql.Tx (a replay chunk) satisfy it.
type auxWriter interface {
	Exec(query string, args ...any) (sql.Result, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// auxWrite hands an applier the right writer: the CHUNK TRANSACTION while a
// batched replay is in flight, the store database otherwise.
//
// LOCKING. auxReplayWriter is written only where nothing else can read it: in
// newFlatSQLStore before the store is published, and in the poisoned-engine
// recovery, which holds s.mu. Every reader of it is an apply* handler, and
// every live apply* caller holds s.mu. The field therefore never races, and it
// is nil on every path that is not literally inside a replay.
func (s *FlatSQLStore) auxWrite() auxWriter {
	if s.auxReplayWriter != nil {
		return s.auxReplayWriter
	}
	return s.db
}

// beginAuxiliaryReplayBatch opens one chunk transaction and routes the
// auxiliary appliers into it.
//
// The reentrancy refusal is not decoration: the FlatSQL driver's transactions
// are ENGINE-GLOBAL (flatsqldrv.Open), so a second BEGIN while one is open is
// "cannot start a transaction within a transaction" from the engine — a boot
// failure, not a silent one, but a confusing one.
func (s *FlatSQLStore) beginAuxiliaryReplayBatch() (*sql.Tx, error) {
	if s.auxReplayWriter != nil {
		return nil, errors.New("auxiliary metadata replay is already batching")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin auxiliary metadata replay chunk: %w", err)
	}
	s.auxReplayWriter = tx
	return tx, nil
}

func (s *FlatSQLStore) endAuxiliaryReplayBatch() {
	s.auxReplayWriter = nil
}

// withAuxiliaryTx runs one applier's multi-statement work transactionally.
// Inside a batched replay it JOINS the chunk transaction and lets the batch own
// the commit; outside one it opens and commits its own, exactly as the
// per-event path did.
func (s *FlatSQLStore) withAuxiliaryTx(operation string, fn func(assetPinQueryExecer) error) error {
	if w := s.auxReplayWriter; w != nil {
		return fn(w)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin %s: %w", operation, err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *FlatSQLStore) checkAuxiliaryAssetFrame(event auxiliaryMetadataEvent) error {
	if s == nil || s.auxiliaryMetadata == nil {
		return nil
	}
	return s.auxiliaryMetadata.CheckAssetFrame(event)
}

func (s *FlatSQLStore) applyAuxiliaryMetadataEvent(event auxiliaryMetadataEvent) error {
	switch event.Kind {
	case auxiliaryEventDirectoryUpsert:
		if event.Directory == nil {
			return fmt.Errorf("directory metadata event missing payload")
		}
		return s.applyDirectoryRecordUpsert(*event.Directory)
	case auxiliaryEventLocalEPMUpsert:
		if event.LocalEPM == nil {
			return fmt.Errorf("local EPM metadata event missing payload")
		}
		return s.applyLocalEPMEncryptedUpsert(*event.LocalEPM)
	case auxiliaryEventDatasetShardPublicationUpsert:
		if event.DatasetShardPublication == nil {
			return fmt.Errorf("dataset shard publication metadata event missing payload")
		}
		return s.applyDatasetShardPublicationUpsert(*event.DatasetShardPublication)
	case auxiliaryEventDatasetShardPublicationDelete:
		if event.DatasetShardPublicationDelete == nil {
			return fmt.Errorf("dataset shard publication delete metadata event missing payload")
		}
		_, err := s.applyDatasetShardPublicationDelete(event.DatasetShardPublicationDelete.Query, event.DatasetShardPublicationDelete.Offset)
		return err
	case auxiliaryEventPinLedgerUpsert:
		if event.PinLedger == nil {
			return fmt.Errorf("pin ledger metadata event missing payload")
		}
		return s.applyPinLedgerEntryUpsert(*event.PinLedger)
	case auxiliaryEventDatasetReplayStateUpsert:
		if event.DatasetReplayState == nil {
			return fmt.Errorf("dataset replay state metadata event missing payload")
		}
		return s.applyDatasetPublicationReplayStateUpsert(*event.DatasetReplayState)
	case auxiliaryEventSourceBatchLicenseUpsert:
		if event.SourceBatchLicense == nil {
			return fmt.Errorf("source batch license metadata event missing payload")
		}
		return s.applySourceBatchLicenseUpsert(*event.SourceBatchLicense)
	case auxiliaryEventAssetOIDCReceiptConsume:
		if event.AssetOIDCReceipt == nil {
			return fmt.Errorf("asset OIDC receipt metadata event missing payload")
		}
		return s.applyAssetOIDCReceiptConsume(*event.AssetOIDCReceipt)
	case auxiliaryEventAssetPinReferenceUpsert:
		if event.AssetPinReferenceUpsert == nil {
			return fmt.Errorf("asset pin reference upsert metadata event missing payload")
		}
		return s.applyAssetPinReferenceUpsert(*event.AssetPinReferenceUpsert)
	case auxiliaryEventAssetPinReferenceTransition:
		if event.AssetPinReferenceTransition == nil {
			return fmt.Errorf("asset pin reference transition metadata event missing payload")
		}
		return s.applyAssetPinReferenceTransition(*event.AssetPinReferenceTransition)
	case auxiliaryEventAssetPinReferenceDelete:
		if event.AssetPinReferenceDelete == nil {
			return fmt.Errorf("asset pin reference delete metadata event missing payload")
		}
		return s.applyAssetPinReferenceDelete(*event.AssetPinReferenceDelete)
	case auxiliaryEventAssetPinAuditAppend:
		if event.AssetPinAuditEvent == nil {
			return fmt.Errorf("asset pin audit metadata event missing payload")
		}
		return s.applyAssetPinAuditAppend(*event.AssetPinAuditEvent)
	default:
		return fmt.Errorf("unknown auxiliary metadata event kind %q", event.Kind)
	}
}
