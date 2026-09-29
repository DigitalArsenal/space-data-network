package format2

// The router core (design §6.1, §7, A2, A3, A20; T6 scope 3). Go keeps the
// trust gate, the plaintext CID, the producer's raw peer id and the
// RecordAttr bytes; everything else (dedupe, supersede, index extraction,
// counters, lanes, reconcile, quota) is engine code. A write returns only
// after the commit holding its records is durable (§6.4): the ack IS the
// durability contract, so nothing here can report a record stored that a
// crash would lose.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// Tags is a write's source provenance (storage.SourceTags) and its batch
// licence.
type Tags struct {
	ProviderID, SourceName, SourceURL, BatchID, ContentKeyID, ProducerPeerID, ProducerPublicKey string
	License, LicenseURL, Citation                                                              string
	ShareAlike                                                                                 bool
}

func (t *Tags) sourceTag() SourceTag {
	if t == nil {
		return SourceTag{}
	}
	return SourceTag{ProviderID: t.ProviderID, SourceName: t.SourceName, BatchID: t.BatchID, ContentKeyID: t.ContentKeyID,
		ProducerPeerID: t.ProducerPeerID, ProducerPublicKey: t.ProducerPublicKey}
}

func (t *Tags) hasLicence() bool {
	return t != nil && (t.License != "" || t.LicenseURL != "" || t.Citation != "" || t.ShareAlike)
}

// LicenceKey is the RecordAttr licence key of a (provider, source, batch).
func LicenceKey(provider, source, batch string) string {
	return provider + "\x1f" + source + "\x1f" + batch
}

// PutResult is one record of a batch write.
type PutResult struct {
	CID string
	Err error // a *RejectError when the engine refused the record
}

// Put is one record of a batch: its plaintext and, for an (encrypted)
// standard, the sealed bytes that are stored (design A19).
type Put struct {
	Data   []byte
	Sealed []byte
}

// CIDBytes is the binary CIDv1 raw sha2-256 of data (0x01 0x55 0x12 0x20 +
// digest), the form the engine stores.
func CIDBytes(data []byte) []byte {
	d := sha256.Sum256(data)
	return append([]byte{0x01, 0x55, 0x12, 0x20}, d[:]...)
}

// CIDText is the text CID of a binary CIDv1 (the legacy store's cid).
func CIDText(bin []byte) string {
	c, err := cid.Cast(bin)
	if err != nil {
		return ""
	}
	return c.String()
}

// CIDFromText decodes a text CID to the binary form.
func CIDFromText(text string) ([]byte, error) {
	c, err := cid.Decode(text)
	if err != nil {
		return nil, err
	}
	if c.Prefix().MhType != mh.SHA2_256 || len(c.Bytes()) != cidBytes {
		return nil, fmt.Errorf("format2: %s is not a CIDv1 raw sha2-256", text)
	}
	return c.Bytes(), nil
}

// PutBatch stores records of one schema from one producer with one set of
// tags, and returns once every record is durable (or rejected). A batch
// licence is a LICENCE entry in the same ring before the records, so
// "licence before records" holds by pseq order (§6.1).
func (s *Store) PutBatch(ctx context.Context, schema string, puts []Put, peerID string, signature []byte, tags *Tags) ([]PutResult, error) {
	spec, err := s.spec(schema)
	if err != nil {
		return nil, err
	}
	p, err := s.w.Partition([]byte(peerID), spec.FID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	lk := ""
	if tags.hasLicence() {
		lk = LicenceKey(tags.ProviderID, tags.SourceName, tags.BatchID)
		body, _ := json.Marshal(struct {
			SchemaName string `json:"schema_name"`
			ProviderID string `json:"provider_id"`
			SourceName string `json:"source_name"`
			BatchID    string `json:"batch_id"`
			License    string `json:"license,omitempty"`
			LicenseURL string `json:"license_url,omitempty"`
			Citation   string `json:"citation,omitempty"`
			ShareAlike bool   `json:"share_alike,omitempty"`
			UpdatedAt  int64  `json:"updated_at,omitempty"`
		}{schema, tags.ProviderID, tags.SourceName, tags.BatchID, tags.License, tags.LicenseURL, tags.Citation, tags.ShareAlike, now.Unix()})
		if _, err := p.Enqueue(ctx, &Entry{Kind: EntLicence, ArrivalMs: now.UnixMilli(),
			Attr: BuildRecordAttr(RecordAttr{PeerID: []byte(peerID), LicenceKey: lk}), Frame: frame(body)}); err != nil {
			return nil, err
		}
	}
	attr := BuildRecordAttr(RecordAttr{PeerID: []byte(peerID), Signature: signature, SourceTimestamp: now.Unix(),
		LicenceKey: lk, Tag: tags.sourceTag()})
	out := make([]PutResult, len(puts))
	rseqs := make([]uint64, len(puts))
	for i, put := range puts {
		c := CIDBytes(put.Data)
		out[i].CID = CIDText(c)
		e := &Entry{Kind: EntRecord, Flags: FlagCidPresent | FlagAckWanted, ArrivalMs: now.UnixMilli(), CID: c,
			Attr: attr, Frame: frame(put.Data)}
		if put.Sealed != nil {
			e.Flags |= FlagSealed
			e.Sealed = put.Sealed
		}
		if rseqs[i], err = p.Enqueue(ctx, e); err != nil {
			return out[:i], err
		}
	}
	if len(puts) == 0 {
		return out, nil
	}
	err = p.WaitAck(ctx, rseqs[len(rseqs)-1])
	rejects := p.Rejects()
	var last *RejectError
	if errors.As(err, &last) {
		rejects[last.Rseq] = last.Code
		err = nil
	}
	if err != nil {
		return out, err
	}
	for i, r := range rseqs {
		if code, ok := rejects[r]; ok {
			out[i].Err = &RejectError{Rseq: r, Code: code}
		}
	}
	return out, nil
}

// Reconcile keeps only batch `keep` of a (provider, source) lane in the
// producer's partition: every other tag instance of the lane is retired and
// records left with no live tag are tombstoned (A2). It returns once the
// reconcile is durable.
func (s *Store) Reconcile(ctx context.Context, schema, peerID, provider, source, keep string) error {
	spec, err := s.spec(schema)
	if err != nil {
		return err
	}
	p, err := s.w.Partition([]byte(peerID), spec.FID)
	if err != nil {
		return err
	}
	// Payload: (u16 len, bytes) for provider, source and keep, in turn.
	payload := make([]byte, 0, 6+len(provider)+len(source)+len(keep))
	for _, f := range []string{provider, source, keep} {
		if len(f) > 0xffff {
			return fmt.Errorf("format2: reconcile field of %d bytes", len(f))
		}
		payload = binary.LittleEndian.AppendUint16(payload, uint16(len(f)))
		payload = append(payload, f...)
	}
	rseq, err := p.Enqueue(ctx, &Entry{Kind: EntReconcile, ArrivalMs: time.Now().UnixMilli(), Frame: payload})
	if err != nil {
		return err
	}
	return p.WaitAck(ctx, rseq)
}

// Delete kills a record by CID in the producer's partition (TOMB_CID).
func (s *Store) Delete(ctx context.Context, schema, peerID, cidText string) error {
	spec, err := s.spec(schema)
	if err != nil {
		return err
	}
	c, err := CIDFromText(cidText)
	if err != nil {
		return err
	}
	p, err := s.w.Partition([]byte(peerID), spec.FID)
	if err != nil {
		return err
	}
	rseq, err := p.Enqueue(ctx, &Entry{Kind: EntTombCid, Flags: FlagCidPresent, CID: c, ArrivalMs: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	return p.WaitAck(ctx, rseq)
}

func frame(data []byte) []byte {
	f := binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(data)), uint32(len(data)))
	return append(f, data...)
}
