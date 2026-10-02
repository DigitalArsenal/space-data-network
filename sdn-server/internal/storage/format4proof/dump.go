package format4proof

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Record-set digests: what a store holds for a type, reduced to numbers and
// order-independent multiset digests, so two formats' stores compare after
// the same writes (W01–W10) without holding either in memory.
//
//   - Data: over the unique CIDs, (cid, sha256 of the stored bytes).
//   - Copies: over every stored copy, (producer token, cid).
//   - Tags: over the tag instances of held records, the six-field identity
//     plus source_url (contract C-3). TagsAt adds each instance's time
//     (format 1's created_at, format 4's at): equal only where both stores
//     got the instance the same way (migrated rows), not for live writes,
//     whose clocks differ.

// Multiset is an order-independent digest: the lane-wise sum (mod 2^64) of
// the four 64-bit words of each element's SHA-256.
type Multiset struct {
	sum [4]uint64
	n   int64
}

// Add adds one element (its fields joined by 0x1f).
func (m *Multiset) Add(fields ...string) {
	h := sha256.Sum256([]byte(strings.Join(fields, "\x1f")))
	for i := 0; i < 4; i++ {
		m.sum[i] += binary.LittleEndian.Uint64(h[i*8:])
	}
	m.n++
}

// String renders the digest with its count.
func (m Multiset) String() string {
	var b [32]byte
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint64(b[i*8:], m.sum[i])
	}
	return fmt.Sprintf("%d:%s", m.n, hex.EncodeToString(b[:12]))
}

// PartitionCount is one producer's copies of a type.
type PartitionCount struct {
	Records int64 `json:"records"`
	Bytes   int64 `json:"bytes"`
}

// TypeDigest is one type's record set.
type TypeDigest struct {
	Schema     string                    `json:"schema"`
	Records    int64                     `json:"records"` // unique CIDs
	Copies     int64                     `json:"copies"`
	Bytes      int64                     `json:"bytes"` // Σ stored length over one copy per CID
	Data       string                    `json:"data"`
	CopySet    string                    `json:"copy_set"`
	Tags       string                    `json:"tags"`
	TagsAt     string                    `json:"tags_at"`
	Partitions map[string]PartitionCount `json:"partitions"`
	// Oversized (format 1, DigestFormat1Limit): copies left out as larger
	// than format 4 stores (C-6).
	Oversized int64 `json:"oversized,omitempty"`
}

// DigestDiff lists the fields where two digests of a type differ.
func DigestDiff(a, b TypeDigest) []string {
	var out []string
	cmp := func(name string, x, y any) {
		if fmt.Sprint(x) != fmt.Sprint(y) {
			out = append(out, fmt.Sprintf("%s: f1 %v, s %v", name, x, y))
		}
	}
	cmp("records", a.Records, b.Records)
	cmp("copies", a.Copies, b.Copies)
	cmp("bytes", a.Bytes, b.Bytes)
	cmp("data", a.Data, b.Data)
	cmp("copy set", a.CopySet, b.CopySet)
	cmp("tags", a.Tags, b.Tags)
	keys := map[string]bool{}
	for k := range a.Partitions {
		keys[k] = true
	}
	for k := range b.Partitions {
		keys[k] = true
	}
	var ks []string
	for k := range keys {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		cmp("partition "+k, a.Partitions[k], b.Partitions[k])
	}
	return out
}

func tagFields(cid string, t storage.SourceTags) []string {
	return []string{cid, t.ProviderID, t.SourceName, t.SourceURL, t.BatchID, t.ContentKeyID, t.ProducerPeerID, t.ProducerPublicKey}
}

// cidKey is a compact set key for a CID string.
func cidKey(cid string) [16]byte {
	h := sha256.Sum256([]byte(cid))
	var k [16]byte
	copy(k[:], h[:16])
	return k
}

// DigestFormat1 digests the given schemas of a closed format-1 store through
// its own engine (storage.OpenMigrationSource, never native SQLite).
func DigestFormat1(storeDir string, schemas []string) (map[string]TypeDigest, error) {
	return DigestFormat1Limit(storeDir, schemas, 0)
}

// DigestFormat1Limit is DigestFormat1 leaving out the copies whose stored
// bytes exceed maxBytes (> 0): the records format 4 cannot hold (C-6),
// counted in Oversized instead.
func DigestFormat1Limit(storeDir string, schemas []string, maxBytes int64) (map[string]TypeDigest, error) {
	src, err := storage.OpenMigrationSource(storeDir)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	tables, err := src.ProducerTables()
	if err != nil {
		return nil, err
	}
	out := map[string]TypeDigest{}
	for _, schema := range schemas {
		d := TypeDigest{Schema: schema, Partitions: map[string]PartitionCount{}}
		var data, copies, tags, tagsAt Multiset
		held := map[[16]byte]bool{}
		var cids []string
		for _, t := range tables {
			if t.Schema != schema {
				continue
			}
			pc := PartitionCount{}
			for after := int64(0); ; {
				recs, err := src.ScanRecords(t, after, 2000)
				if err != nil {
					return nil, fmt.Errorf("digest %s %s: %w", schema, t.Name, err)
				}
				if len(recs) == 0 {
					break
				}
				for _, r := range recs {
					after = r.RowID
					if maxBytes > 0 && int64(len(r.Stored)) > maxBytes {
						d.Oversized++
						continue
					}
					pc.Records++
					pc.Bytes += int64(len(r.Stored))
					copies.Add(t.Token, r.CID)
					k := cidKey(r.CID)
					if held[k] {
						continue
					}
					held[k] = true
					cids = append(cids, r.CID)
					data.Add(r.CID, digest(r.Stored))
					d.Bytes += int64(len(r.Stored))
				}
			}
			d.Partitions[t.Token] = pc
		}
		for i := 0; i < len(cids); i += 2000 {
			j := i + 2000
			if j > len(cids) {
				j = len(cids)
			}
			byCID, err := src.TagsFor(schema, cids[i:j])
			if err != nil {
				return nil, fmt.Errorf("digest %s tags: %w", schema, err)
			}
			for _, cid := range cids[i:j] {
				for _, t := range byCID[cid] {
					f := tagFields(cid, t.SourceTags)
					tags.Add(f...)
					tagsAt.Add(append(f, i64(t.CreatedAt))...)
				}
			}
		}
		d.Records, d.Copies = data.n, copies.n
		d.Data, d.CopySet, d.Tags, d.TagsAt = data.String(), copies.String(), tags.String(), tagsAt.String()
		out[schema] = d
	}
	return out, nil
}

// DigestStore digests the given schemas of a closed store of arm. Format 2
// is not digested (equivalence is against format 1): it returns nil, nil.
func DigestStore(arm, dir string, schemas []string) (map[string]TypeDigest, error) {
	switch arm {
	case ArmF1:
		return DigestFormat1(dir, schemas)
	case ArmS:
		return digestFormat4(dir, schemas)
	}
	return nil, nil
}
