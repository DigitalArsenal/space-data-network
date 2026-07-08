package storage

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
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
)

type auxiliaryMetadataStore struct {
	mu          sync.Mutex
	f           *os.File
	path        string
	readOnly    bool
	replayLimit int64
}

type auxiliaryMetadataEvent struct {
	Kind string `json:"kind"`

	Directory                     *DirectoryRecord                        `json:"directory,omitempty"`
	LocalEPM                      *auxiliaryLocalEPMRecord                `json:"local_epm,omitempty"`
	DatasetShardPublication       *DatasetShardPublication                `json:"dataset_shard_publication,omitempty"`
	DatasetShardPublicationDelete *auxiliaryDatasetShardPublicationDelete `json:"dataset_shard_publication_delete,omitempty"`
	PinLedger                     *PinLedgerEntry                         `json:"pin_ledger,omitempty"`
	DatasetReplayState            *DatasetPublicationReplayState          `json:"dataset_replay_state,omitempty"`
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
				return &auxiliaryMetadataStore{path: path, readOnly: true}, nil
			}
			return nil, fmt.Errorf("auxiliary metadata: open read-only: %w", err)
		}
		valid, err := scanRecordCatalogValidLength(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		return &auxiliaryMetadataStore{f: f, path: path, readOnly: true, replayLimit: valid}, nil
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
	if err := f.Truncate(valid); err != nil {
		f.Close()
		return nil, fmt.Errorf("auxiliary metadata: truncate torn tail: %w", err)
	}
	if _, err := f.Seek(valid, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &auxiliaryMetadataStore{f: f, path: path}, nil
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
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(hdr[4:], crc32.ChecksumIEEE(payload))
	if _, err := m.f.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := m.f.Write(payload); err != nil {
		return err
	}
	return m.f.Sync()
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
	default:
		return fmt.Errorf("unknown auxiliary metadata event kind %q", event.Kind)
	}
}
