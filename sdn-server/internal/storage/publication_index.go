package storage

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ipfs/go-cid"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

const maxPublicationEntries = 100000
const maxPublicationSelectionRecords = 100000
const maxPublicationRecordBytes = 16 << 20
const maxPublicationReferenceBytes int64 = 128 << 20

// References contain immutable file offsets; exported payload memory is one record.
type publicationRefFile struct {
	Path          string
	Tags          SourceTags
	Identities    []string
	Products      [][32]byte
	ProductsError error
}
type publicationRecordRef struct {
	Digest, FramedDigest [32]byte
	Offset               int64
	Size, identity       uint32
	file                 *publicationRefFile
}

func publicationDigestCID(d [32]byte) string {
	var raw [36]byte
	copy(raw[:4], []byte{1, 0x55, 0x12, 0x20})
	copy(raw[4:], d[:])
	return "b" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))
}
func (r publicationRecordRef) CID() string       { return publicationDigestCID(r.Digest) }
func (r publicationRecordRef) FramedCID() string { return publicationDigestCID(r.FramedDigest) }
func (r publicationRecordRef) Path() string      { return r.file.Path }
func (r publicationRecordRef) Tags() SourceTags  { return r.file.Tags }
func (r publicationRecordRef) Identity() string {
	if r.identity == 0 {
		return ""
	}
	return r.file.Identities[r.identity-1]
}
func newPublicationReference(file *publicationRefFile, data []byte, offset int64, identity string) publicationRecordRef {
	var framed [4]byte
	binary.LittleEndian.PutUint32(framed[:], uint32(len(data)))
	h := sha256.New()
	h.Write(framed[:])
	h.Write(data)
	r := publicationRecordRef{Digest: sha256.Sum256(data), Offset: offset, Size: uint32(len(data)), file: file}
	copy(r.FramedDigest[:], h.Sum(nil))
	if identity != "" {
		file.Identities = append(file.Identities, identity)
		r.identity = uint32(len(file.Identities))
	}
	return r
}

func (r publicationRecordRef) read() ([]byte, error) {
	f, err := os.Open(r.Path())
	if err != nil {
		return nil, publicationReadError(err)
	}
	defer f.Close()
	data := make([]byte, r.Size)
	if _, err = f.ReadAt(data, r.Offset); err != nil {
		return nil, publicationReadError(err)
	}
	if sha256.Sum256(data) != r.Digest {
		return nil, publicationContentError(errors.New("publication record checksum mismatch"))
	}
	return data, nil
}
func (s *FlatSQLStore) publicationReferences(e publicationEntry) ([]publicationRecordRef, error) {
	path := filepath.Join(s.publicationEntryDir(e.Sequence), "records.sds")
	f, err := os.Open(path)
	if err != nil {
		return nil, publicationReadError(err)
	}
	defer f.Close()
	h := sha256.New()
	reader := io.TeeReader(f, h)
	var refs []publicationRecordRef
	metadata := &publicationRefFile{Path: path, Tags: e.Tags}
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
			return nil, publicationReadError(err)
		}
		if size < 8 || size > maxPublicationRecordBytes {
			return nil, publicationContentError(errors.New("publication frame outside 8 byte to 16 MiB bounds"))
		}
		data := make([]byte, 4+int(size))
		binary.LittleEndian.PutUint32(data, size)
		if _, err = io.ReadFull(reader, data[4:]); err != nil {
			return nil, publicationReadError(err)
		}
		if _, refused := e.Refused[ordinal]; !refused {
			identity, err := publicationIdentity(e.Schema, data[4:])
			if err != nil {
				return nil, publicationContentError(err)
			}
			refs = append(refs, newPublicationReference(metadata, data[4:], offset+4, identity))
			if e.Schema == "CAT.fbs" {
				cat, parseErr := parseCAT(data[4:])
				if parseErr != nil {
					metadata.ProductsError = parseErr
				} else {
					for i := 0; i < cat.PRODUCTSLength(); i++ {
						id, err := catalogProductCID(cat, i)
						if err != nil {
							metadata.ProductsError = err
							continue
						}
						c, err := cid.Decode(id)
						if err != nil || len(c.Hash()) != 34 {
							metadata.ProductsError = errors.New("invalid catalog product identifier")
							continue
						}
						var digest [32]byte
						copy(digest[:], c.Hash()[2:])
						metadata.Products = append(metadata.Products, digest)
					}
				}
			}

		}
		if _, refused := e.Refused[ordinal]; !refused {
			referenceBytes += publicationReferenceBytes(refs[len(refs)-1])
			if referenceBytes > s.publicationByteLimit() || len(refs) > s.publicationRecordLimit() {
				return nil, errors.New("publication reference index exceeds capacity")
			}
		}
		offset += 4 + int64(size)
		ordinal++
	}
	if hex.EncodeToString(h.Sum(nil)) != e.Digest {
		return nil, publicationContentError(errors.New("publication journal checksum mismatch"))
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
	return s.publicationTags[publicationTagIdentity(schema, cid, tags)] > 0
}

// 88 bytes for the reference plus conservative tag-index and identity accounting.
func publicationReferenceBytes(r publicationRecordRef) int64 {
	return int64(unsafe.Sizeof(r)) + 80 + int64(len(r.Identity()))
}
func publicationSourceTagsBytes(t SourceTags) int64 {
	return int64(unsafe.Sizeof(t)) + int64(len(t.ProviderID)+len(t.SourceName)+len(t.SourceURL)+len(t.BatchID)+len(t.ContentKeyID)+len(t.ProducerPeerID)+len(t.ProducerPublicKey)+len(t.License)+len(t.LicenseURL)+len(t.Citation))
}
func publicationReferencesBytes(refs []publicationRecordRef) int64 {
	var n int64
	for _, r := range refs {
		n += publicationReferenceBytes(r)
	}
	if len(refs) > 0 {
		f := refs[0].file
		n += int64(unsafe.Sizeof(*f)+uintptr(cap(f.Identities))*unsafe.Sizeof("")) + int64(len(f.Path)) + publicationSourceTagsBytes(f.Tags) + int64(cap(f.Products))*32
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

type publicationTagKey [32]byte

func publicationTagIdentity(schema, cid string, tags SourceTags) publicationTagKey {
	raw, _ := json.Marshal(tags)
	h := sha256.New()
	h.Write([]byte(schema))
	h.Write([]byte{0})
	h.Write([]byte(cid))
	h.Write([]byte{0})
	h.Write(raw)
	var key publicationTagKey
	copy(key[:], h.Sum(nil))
	return key
}

func (s *FlatSQLStore) indexPublicationTags(e publicationEntry, delta int) {
	s.accountPublicationEntry(e, delta)
	if !e.Applied {
		return
	}
	if s.publicationTags == nil {
		s.publicationTags = map[publicationTagKey]int{}
	}
	for _, r := range e.Refs {
		k := publicationTagIdentity(e.Schema, r.CID(), e.Tags)
		s.publicationTags[k] += delta
		if s.publicationTags[k] <= 0 {
			delete(s.publicationTags, k)
		}
	}
}
