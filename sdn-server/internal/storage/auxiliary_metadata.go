package storage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
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
)

type auxiliaryMetadataStore struct {
	mu          sync.Mutex
	f           *os.File
	path        string
	readOnly    bool
	replayLimit int64
	assetFrames map[string][]byte
}

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
		return &auxiliaryMetadataStore{f: f, path: path, readOnly: true, replayLimit: valid, assetFrames: assetFrames}, nil
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
	return &auxiliaryMetadataStore{f: f, path: path, assetFrames: assetFrames}, nil
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
	if _, err := m.f.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := m.f.Write(payload); err != nil {
		return err
	}
	if err := m.f.Sync(); err != nil {
		return err
	}
	if identity != "" {
		m.assetFrames[identity] = bytes.Clone(payload)
	}
	return nil
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

func (m *auxiliaryMetadataStore) Replay(store *FlatSQLStore) (int, error) {
	if m == nil || m.f == nil {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	info, err := m.f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	if m.readOnly {
		size = m.replayLimit
	}
	var off int64
	var hdr [8]byte
	count := 0
	for off < size {
		if _, err := m.f.ReadAt(hdr[:], off); err != nil {
			return count, err
		}
		n := int64(binary.LittleEndian.Uint32(hdr[0:]))
		crc := binary.LittleEndian.Uint32(hdr[4:])
		if n == 0 || off+8+n > size {
			break
		}
		payload := make([]byte, n)
		if _, err := m.f.ReadAt(payload, off+8); err != nil {
			return count, err
		}
		if crc32.ChecksumIEEE(payload) != crc {
			break
		}
		event, err := decodeAuxiliaryMetadataEvent(payload)
		if err != nil {
			return count, err
		}
		if err := store.applyAuxiliaryMetadataEvent(event); err != nil {
			return count, err
		}
		count++
		off += 8 + n
	}
	return count, nil
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

func (s *FlatSQLStore) appendAuxiliaryMetadata(event auxiliaryMetadataEvent) error {
	if s == nil || s.auxiliaryMetadata == nil {
		return nil
	}
	return s.auxiliaryMetadata.Append(event)
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
