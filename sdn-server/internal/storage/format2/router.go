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
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// Tags is a write's source provenance (storage.SourceTags) and its batch
// licence.
type Tags struct {
	ProviderID, SourceName, SourceURL, BatchID, ContentKeyID, ProducerPeerID, ProducerPublicKey string
	License, LicenseURL, Citation                                                               string
	ShareAlike                                                                                  bool
}

func (t *Tags) sourceTag() SourceTag {
	if t == nil {
		return SourceTag{}
	}
	return SourceTag{ProviderID: t.ProviderID, SourceName: t.SourceName, SourceURL: t.SourceURL, BatchID: t.BatchID,
		ContentKeyID: t.ContentKeyID, ProducerPeerID: t.ProducerPeerID, ProducerPublicKey: t.ProducerPublicKey}
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
// tags, and returns once every record is durable (or rejected), each
// refused record with its own *RejectError. A batch licence is a LICENCE
// entry in the same ring before the records, so "licence before records"
// holds by pseq order (§6.1); a licence the engine refused fails the batch
// with a *LicenceRejectError (its records are stored, under a licence key
// no licence holds).
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
	// rseqs are the batch's entries: its licence, then its records.
	rseqs := make([]uint64, 0, len(puts)+1)
	if tags.hasLicence() {
		lk = LicenceKey(tags.ProviderID, tags.SourceName, tags.BatchID)
		rseq, err := p.Enqueue(ctx, licenceEntry(schema, peerID, tags, lk, now))
		if err != nil {
			return nil, err
		}
		rseqs = append(rseqs, rseq)
	}
	first := len(rseqs) // the first record's entry
	attr := BuildRecordAttr(RecordAttr{PeerID: []byte(peerID), Signature: signature, SourceTimestamp: now.Unix(),
		LicenceKey: lk, Tag: tags.sourceTag()})
	out := make([]PutResult, len(puts))
	for i, put := range puts {
		c := CIDBytes(put.Data)
		out[i].CID = CIDText(c)
		e := &Entry{Kind: EntRecord, Flags: FlagCidPresent | FlagAckWanted, ArrivalMs: now.UnixMilli(), CID: c,
			Attr: attr, Frame: frame(put.Data)}
		if put.Sealed != nil {
			e.Flags |= FlagSealed
			e.Sealed = put.Sealed
		}
		rseq, err := p.Enqueue(ctx, e)
		if err != nil {
			if len(rseqs) > 0 {
				// What was queued is committed without this call waiting
				// for it: its head moves unseen by an ack, and its rejects
				// have no waiter.
				p.Forget(rseqs)
				s.heads.Touch(p.PID)
			}
			return out[:i], err
		}
		rseqs = append(rseqs, rseq)
	}
	if len(rseqs) == 0 {
		return out, nil
	}
	err = s.waitAck(ctx, p, rseqs[len(rseqs)-1])
	var last *RejectError
	if errors.As(err, &last) {
		err = nil
	}
	if err != nil {
		p.Forget(rseqs)
		return out, err
	}
	// This batch's rejects only: another batch on the partition takes its own.
	rejects := p.TakeRejects(rseqs)
	if last != nil {
		rejects[last.Rseq] = last.Code
	}
	for i, r := range rseqs[first:] {
		if code, ok := rejects[r]; ok {
			out[i].Err = &RejectError{Rseq: r, Code: code}
		}
	}
	if first > 0 {
		if code, ok := rejects[rseqs[0]]; ok {
			return out, &LicenceRejectError{Key: lk, Reject: &RejectError{Rseq: rseqs[0], Code: code}}
		}
	}
	return out, nil
}

// licenceEntry is a batch's LICENCE entry.
func licenceEntry(schema, peerID string, tags *Tags, lk string, now time.Time) *Entry {
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
	return &Entry{Kind: EntLicence, ArrivalMs: now.UnixMilli(),
		Attr: BuildRecordAttr(RecordAttr{PeerID: []byte(peerID), LicenceKey: lk}), Frame: frame(body)}
}

// LicenceRejectError: the engine refused a batch's licence. The batch's
// PutResults still say which records were stored.
type LicenceRejectError struct {
	Key    string
	Reject *RejectError
}

func (e *LicenceRejectError) Error() string {
	return fmt.Sprintf("format2: the batch licence %q was refused: %v", strings.ReplaceAll(e.Key, "\x1f", "/"), e.Reject)
}

func (e *LicenceRejectError) Unwrap() error { return e.Reject }

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
	return s.waitOne(ctx, p, rseq)
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
	return s.waitOne(ctx, p, rseq)
}

// waitAck waits for rseq's ack and marks the partition however the wait
// ends (acked, rejected, quarantined, cancelled): its head may have moved,
// and the next counter read reads it again, and no other head (B8).
func (s *Store) waitAck(ctx context.Context, p *Partition, rseq uint64) error {
	err := p.WaitAck(ctx, rseq)
	s.heads.Touch(p.PID)
	return err
}

// waitOne is waitAck for a single entry: a wait that ends other than acked
// or rejected gives the entry's reject up (Forget).
func (s *Store) waitOne(ctx context.Context, p *Partition, rseq uint64) error {
	err := s.waitAck(ctx, p, rseq)
	var rej *RejectError
	if err != nil && !errors.As(err, &rej) {
		p.Forget([]uint64{rseq})
	}
	return err
}

func frame(data []byte) []byte {
	f := binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(data)), uint32(len(data)))
	return append(f, data...)
}

// LabelWaitMax is the budget of a label wait: one per operation, however
// many partitions it waits on. The type owner labels a commit within
// milliseconds; a wait that outlives this ends with a *LabelWaitError
// (ErrNotLabeled) and is counted in LabelWaitTimeouts.
const LabelWaitMax = 5 * time.Second

// ErrNotLabeled: a label wait reached its deadline. The records it waited on
// are durable (acked) and readable by CID; type-level reads (windows,
// datasync pages, <TYPE> SQL) see them once the type owner labels them.
var ErrNotLabeled = errors.New("format2: records durable, not yet labeled for type-level reads")

// LabelWaitError is how a label wait ends at its deadline; errors.Is(err,
// ErrNotLabeled) holds.
type LabelWaitError struct {
	Schema string
	// PID is the first partition not labeled at the deadline; Partitions
	// counts them.
	PID        uint32
	Partitions int
	// PseqHi is that partition's pseq_hi, LabeledThrough its labels as last
	// read (Known false: they could not be read); PseqHi 0 with Err set: its
	// head could not be read.
	PseqHi, LabeledThrough uint64
	Known                  bool
	// Err is the last read error of the wait, if any.
	Err    error
	Waited time.Duration
}

func (e *LabelWaitError) Error() string {
	msg := fmt.Sprintf("format2: %s partition %d not labeled through pseq %d after %v", e.Schema, e.PID, e.PseqHi,
		e.Waited.Round(time.Millisecond))
	if e.Known {
		msg += fmt.Sprintf(" (labeled through %d)", e.LabeledThrough)
	} else {
		msg += " (labels unreadable)"
	}
	if e.Partitions > 1 {
		msg += fmt.Sprintf(", %d partitions behind", e.Partitions)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg + "; the records are durable"
}

func (e *LabelWaitError) Is(target error) bool { return target == ErrNotLabeled }
func (e *LabelWaitError) Unwrap() error        { return e.Err }

// WaitLabeled waits until the producer's partition of schema is labeled by
// its type owner through every record acked so far, so type-level reads see
// them (A20: the publish API and module flows wait for labeling; stream push
// and datasync do not). It returns within LabelWaitMax of being called,
// whatever form the labels take (inline in the type head up to 128
// partitions, in the type log past that): nil once labeled, a
// *LabelWaitError (ErrNotLabeled) at the deadline, the context's error or
// ErrStopped.
func (s *Store) WaitLabeled(ctx context.Context, schema, peerID string) error {
	return s.WaitLabeledUntil(ctx, schema, []string{peerID}, time.Now().Add(LabelWaitMax))
}

// WaitLabeledUntil is WaitLabeled for the partitions of several producers of
// one schema under one deadline: an operation that committed to many
// partitions (delete, reconcile, retags) waits once, never LabelWaitMax per
// partition. A partition this store never registered has nothing acked
// through it and is skipped; the lookup takes no Writer lock, so a wait
// never queues behind another producer's registration.
func (s *Store) WaitLabeledUntil(ctx context.Context, schema string, peers []string, deadline time.Time) error {
	start := time.Now()
	spec, err := s.spec(schema)
	if err != nil {
		return err
	}
	var behind *LabelWaitError
	seen := make(map[uint32]bool, len(peers))
	for _, peer := range peers {
		p := s.w.Registered([]byte(peer), spec.FID)
		if p == nil || seen[p.PID] {
			continue
		}
		seen[p.PID] = true
		st, err := waitLabels(ctx, s.stop, deadline, func() (uint64, error) {
			return s.heads.partitionPseqHi(p.PID)
		}, func() (uint64, bool, error) {
			return s.heads.labeledThrough(spec.FID, p.PID)
		})
		if err != nil {
			return err
		}
		if !st.timedOut {
			continue
		}
		s.heads.waitTimeouts.Add(1)
		if behind == nil {
			behind = &LabelWaitError{Schema: schema, PID: p.PID, PseqHi: st.hi, LabeledThrough: st.lt, Known: st.known,
				Err: st.readErr}
		}
		behind.Partitions++
	}
	if behind != nil {
		behind.Waited = time.Since(start)
		return behind
	}
	return nil
}

// LabelWaitTimeouts counts the partitions whose label wait ended on the
// deadline.
func (s *Store) LabelWaitTimeouts() uint64 { return s.heads.waitTimeouts.Load() }

// labelWait is where a wait on one partition ended.
type labelWait struct {
	hi, lt   uint64
	known    bool  // lt was read
	readErr  error // the last read error (pseq_hi or labels)
	timedOut bool
}

// waitLabels reads the partition's pseq_hi (readHi), then polls its labels
// (readLabels) until they reach it or deadline passes (timedOut). A read
// that fails counts as unknown and is polled again: the records are durable
// already, so a transient error (EMFILE, a torn segment) must not fail the
// write. err is set only when ctx or stop ends the wait.
func waitLabels(ctx context.Context, stop <-chan struct{}, deadline time.Time, readHi func() (uint64, error),
	readLabels func() (uint64, bool, error)) (st labelWait, err error) {
	hiRead := false
	var wait backoff
	for {
		select {
		case <-stop:
			return st, ErrStopped
		default:
		}
		if !hiRead {
			if hi, err := readHi(); err != nil {
				st.readErr = err
			} else {
				st.hi, hiRead = hi, true
				if hi == 0 {
					return st, nil // nothing committed, nothing to label
				}
			}
		}
		if hiRead {
			if lt, known, err := readLabels(); err != nil {
				if errors.Is(err, ErrStopped) {
					return st, err
				}
				st.readErr, st.known = err, false
			} else {
				st.lt, st.known = lt, known
				if known && lt >= st.hi {
					return st, nil
				}
			}
		}
		if !time.Now().Before(deadline) {
			st.timedOut = true
			return st, nil
		}
		if err := wait.sleep(ctx, stop); err != nil {
			return st, err
		}
	}
}
