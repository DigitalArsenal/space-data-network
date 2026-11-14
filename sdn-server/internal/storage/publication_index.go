package storage

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"
)

const maxPublicationEntries = 100000
const maxPublicationSelectionRecords = 100000
const maxPublicationRecordBytes = 16 << 20
const maxPublicationReferenceBytes int64 = 128 << 20

// References contain immutable file offsets; exported payload memory is one record.
type publicationRecordRef struct {
	CID, FramedCID, Identity, Path string
	Offset                         int64
	Size                           uint32
	Tags                           SourceTags
}

func (r publicationRecordRef) read() ([]byte, error) {
	f, err := os.Open(r.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, r.Size)
	if _, err = f.ReadAt(data, r.Offset); err != nil {
		return nil, err
	}
	if computeCID(data) != r.CID {
		return nil, errors.New("publication record checksum mismatch")
	}
	return data, nil
}
func (s *FlatSQLStore) publicationReferences(e publicationEntry) ([]publicationRecordRef, error) {
	path := filepath.Join(s.publicationEntryDir(e.Sequence), "records.sds")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	reader := io.TeeReader(f, h)
	var refs []publicationRecordRef
	var referenceBytes int64
	offset := int64(0)
	ordinal := 0
	for {
		var size uint32
		err = binary.Read(reader, binary.LittleEndian, &size)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if size < 8 || size > maxPublicationRecordBytes {
			return nil, errors.New("publication frame outside 8 byte to 16 MiB bounds")
		}
		data := make([]byte, 4+int(size))
		binary.LittleEndian.PutUint32(data, size)
		if _, err = io.ReadFull(reader, data[4:]); err != nil {
			return nil, err
		}
		if _, refused := e.Refused[ordinal]; !refused {
			identity, err := publicationIdentity(e.Schema, data[4:])
			if err != nil {
				return nil, err
			}
			refs = append(refs, publicationRecordRef{CID: computeCID(data[4:]), FramedCID: computeCID(data), Identity: identity, Path: path, Offset: offset + 4, Size: size, Tags: e.Tags})
		}
		if _, refused := e.Refused[ordinal]; !refused {
			referenceBytes += publicationReferenceBytes(refs[len(refs)-1])
			if referenceBytes > maxPublicationReferenceBytes || len(refs) > 200000 {
				return nil, errors.New("publication reference index exceeds capacity")
			}
		}
		offset += 4 + int64(size)
		ordinal++
	}
	if hex.EncodeToString(h.Sum(nil)) != e.Digest {
		return nil, errors.New("publication journal checksum mismatch")
	}
	return refs, nil
}

// Copy index metadata under the lock. All file reads use this pinned view after
// unlocking; neither hashing nor snapshot validation holds the writer's lock.
func (s *FlatSQLStore) publicationView() *FlatSQLStore {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	v := &FlatSQLStore{basePath: s.basePath, publicationSources: append([]PublicationSource(nil), s.publicationSources...), publicationEntries: append([]publicationEntry(nil), s.publicationEntries...), publicationFailures: map[publicationSourceKey]error{}, publicationGlobalFailure: s.publicationGlobalFailure}
	for k, e := range s.publicationFailures {
		v.publicationFailures[k] = e
	}
	v.publicationRelease = s.pinPublicationEntriesLocked(v.publicationEntries)
	return v
}

func (s *FlatSQLStore) publicationTagAlreadyRetained(schema, cid string, tags SourceTags) bool {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	if s.publicationFailures[publicationSourceKey{schema, tags.ProviderID, tags.SourceName}] != nil {
		return false
	}
	return s.publicationTags[publicationTagKey{schema, cid, tags}] > 0
}

func publicationReferenceBytes(r publicationRecordRef) int64 {
	t := r.Tags
	return int64(unsafe.Sizeof(r)+unsafe.Sizeof(publicationTagKey{})) + 256 + int64(len(r.CID)+len(r.FramedCID)+len(r.Identity)+len(r.Path)+len(t.ProviderID)+len(t.SourceName)+len(t.SourceURL)+len(t.BatchID)+len(t.ContentKeyID)+len(t.ProducerPeerID)+len(t.ProducerPublicKey)+len(t.License)+len(t.LicenseURL)+len(t.Citation))
}
func publicationReferencesBytes(refs []publicationRecordRef) int64 {
	var n int64
	for _, r := range refs {
		n += publicationReferenceBytes(r)
	}
	return n
}

func publicationIdentity(schema string, data []byte) (identity string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("invalid publication identity fields: %v", recovered)
		}
	}()
	return recordSupersedeKey(schema, data), nil
}

type publicationTagKey struct {
	schema, cid string
	tags        SourceTags
}

func (s *FlatSQLStore) indexPublicationTags(e publicationEntry, delta int) {
	if !e.Applied {
		return
	}
	if s.publicationTags == nil {
		s.publicationTags = map[publicationTagKey]int{}
	}
	for _, r := range e.Refs {
		k := publicationTagKey{e.Schema, r.CID, e.Tags}
		s.publicationTags[k] += delta
		if s.publicationTags[k] <= 0 {
			delete(s.publicationTags, k)
		}
	}
}
