package storage

// format4_daemon_writes.go — the node's record writes on store format 4
// (design §4, §10; contract §3.7, §5.4, C-37). Every write is one engine PUT
// per chunk; the engine writes each record into the file of its tag's source
// feed (provider, source) of the type, or local when it carries no tag, and
// acks after a durable commit and the watermark publish (C-4: the writer
// reads its own writes). Dedupe, copies, retags, the IQC ingest identity,
// CAT supersede-on-ingest, the per-file batch counters and supersede are the
// engine's; nothing here takes s.mu or writes a control row for a record. The
// batch licence is a control row, written by the store before the PUT (W-e).

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// format4PutChunk bounds the records one PUT holds in memory.
const format4PutChunk = 4096

// ErrFormat4Unsupported: a record API store format 4 does not serve (the
// reason is in the wrapped message). Never a silent empty answer.
var ErrFormat4Unsupported = errors.New("not available on store format 4")

func f4Unsupported(what string) error { return fmt.Errorf("%w: %s", ErrFormat4Unsupported, what) }

// format4Backend is store format 4 as the record backend.
type format4Backend struct {
	s *FlatSQLStore
	d *format4Daemon
}

var _ recordBackend = format4Backend{}

// closed answers ErrStoreClosed once Close ran.
func (b format4Backend) closed() error {
	if b.s == nil || b.s.closed.Load() {
		return ErrStoreClosed
	}
	return nil
}

// f4Type is the engine type of a schema ("OMM.fbs" -> "OMM").
func f4Type(schemaName string) (string, error) {
	typ, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return "", fmt.Errorf("invalid schema name: %w", err)
	}
	return typ, nil
}

// f4Tag is a normalized source tag as an engine lane identity (C-3).
func f4Tag(t SourceTags) format4.Tag {
	return format4.Tag{Provider: t.ProviderID, Source: t.SourceName, SourceURL: t.SourceURL, Batch: t.BatchID,
		ContentKeyID: t.ContentKeyID, ProducerPeer: t.ProducerPeerID, ProducerPubkey: t.ProducerPublicKey}
}

// f4Ident is a record's IQC ingest identity as the engine's 32 bytes (the
// sha256 recordIngestIdentity hexes), nil when the identity rule does not
// apply.
func f4Ident(schemaName string, data []byte, tags *SourceTags) *[32]byte {
	if !ingestIdentityApplies(schemaName, tags) {
		return nil
	}
	id := recordIngestIdentity(schemaName, data)
	raw, err := hex.DecodeString(strings.TrimPrefix(id, ingestIdentityPrefix))
	if id == "" || err != nil || len(raw) != 32 {
		return nil
	}
	var out [32]byte
	copy(out[:], raw)
	return &out
}

// f4Put is one write's records into the engine, chunked. It returns each
// record's outcome in input order. Records the engine refused are returned
// as a *RefusedRecordsError after the others are stored. An ingest-identity
// repeat lands nowhere: the engine gives the record holding the identity
// the write's tag in the same transaction (C-26; format 1's
// tagIdentityRepeats).
func (b format4Backend) f4Put(op, schemaName string, records [][]byte, peerID string, signature []byte, tags *SourceTags, identity bool) ([]string, []format4.Outcome, error) {
	if err := b.s.requireWritable(op); err != nil {
		return nil, nil, err
	}
	if err := b.closed(); err != nil {
		return nil, nil, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, nil, err
	}
	var batchTags []format4.Tag
	if tags != nil {
		t := normalizeSourceTags(*tags)
		if err := ValidateSourceTags(t); err != nil {
			return nil, nil, err
		}
		batchTags = []format4.Tag{f4Tag(t)}
	}
	if err := b.d.ensureType(schemaName); err != nil {
		return nil, nil, err
	}
	sealed := encfield.HasEncryptedFields(typ)
	now := time.Now().Unix()
	cids := make([]string, len(records))
	outcomes := make([]format4.Outcome, 0, len(records))
	refused := &RefusedRecordsError{Schema: schemaName, Records: len(records)}
	for start := 0; start < len(records); start += format4PutChunk {
		chunk := records[start:min(start+format4PutChunk, len(records))]
		batch := format4.Batch{Type: typ, Peer: peerID, Tags: batchTags, Mode: format4.ModeIngest, Records: make([]format4.In, len(chunk))}
		for i, data := range chunk {
			cid := computeCID(data)
			cids[start+i] = cid
			in := format4.In{CID: cid, Plain: data, TS: now, Sig: signature}
			if sealed {
				stored, err := b.s.storableRecordBytes(schemaName, data)
				if err != nil {
					return cids, outcomes, err
				}
				in.Sealed = stored
			}
			if identity {
				in.Ident = f4Ident(schemaName, data, tags)
			}
			batch.Records[i] = in
		}
		got, err := b.d.api().Put(b.d.ctx, batch)
		if err != nil {
			return cids, outcomes, fmt.Errorf("store %s records: %w", schemaName, err)
		}
		if len(got) != len(chunk) {
			return cids, outcomes, fmt.Errorf("store %s records: the engine answered %d outcomes for %d records", schemaName, len(got), len(chunk))
		}
		for i, o := range got {
			if o.Action == format4.ActRejected {
				refused.refuse(cids[start+i], fmt.Errorf("%s (%d)", format4.RejectReason(o.Reject), o.Reject))
			}
		}
		outcomes = append(outcomes, got...)
	}
	if len(refused.Order) > 0 {
		return cids, outcomes, refused
	}
	return cids, outcomes, nil
}

// f4CIDAt is the CID of a type's record at seq.
func (b format4Backend) f4CIDAt(typ string, seq int64) (string, error) {
	recs, err := b.d.api().Scan(b.d.ctx, format4.Query{Type: typ, SeqAfter: seq - 1, SeqThrough: seq, Limit: 1})
	if err != nil {
		return "", err
	}
	if len(recs) != 1 || recs[0].Seq != seq {
		return "", fmt.Errorf("no %s record at seq %d", typ, seq)
	}
	return recs[0].CID, nil
}

// f4Retag attaches a tag to a held record: its stored bytes again, by the
// peer of the copy holding it, with the tag. The engine retags that copy, or
// updates the instance's source URL when it holds the tag (DUP).
func (b format4Backend) f4Retag(schemaName, typ, cid string, tag format4.Tag) error {
	if !f4CIDStored(cid) {
		return fmt.Errorf("source-tagged record not found: %s/%s", schemaName, cid)
	}
	recs, err := b.d.api().Get(b.d.ctx, typ, []string{cid}, false, true)
	if err != nil && !errors.Is(err, format4.ErrNoType) {
		return err
	}
	if len(recs) == 0 {
		return fmt.Errorf("source-tagged record not found: %s/%s", schemaName, cid)
	}
	rec := recs[0]
	plain, err := b.s.openStoredRecordBytes(schemaName, rec.Data)
	if err != nil {
		return err
	}
	in := format4.In{CID: cid, Plain: plain, TS: rec.TS, Sig: rec.Sig}
	if encfield.IsSealed(rec.Data) {
		in.Sealed = rec.Data
	}
	got, err := b.d.api().Put(b.d.ctx, format4.Batch{Type: typ, Peer: rec.Peer, Tags: []format4.Tag{tag}, Mode: format4.ModeIngest,
		Records: []format4.In{in}})
	if err != nil {
		return fmt.Errorf("retag %s: %w", cid, err)
	}
	if len(got) == 1 && got[0].Action == format4.ActRejected {
		return fmt.Errorf("retag %s: %s (%d)", cid, format4.RejectReason(got[0].Reject), got[0].Reject)
	}
	return nil
}

// f4Inserted counts the records new to the type (format 1's count: a CID
// some producer already held, and an ingest-identity repeat, are not).
func f4Inserted(outcomes []format4.Outcome) int {
	n := 0
	for _, o := range outcomes {
		if o.Action == format4.ActNew {
			n++
		}
	}
	return n
}

// f4HeldCID is the CID a record's outcome leaves the caller to report: an
// ingest-identity repeat lands nowhere and the held record (its seq) took
// the write's tag, so the held CID is the one the write stored.
func (b format4Backend) f4HeldCID(typ, cid string, o format4.Outcome) (string, error) {
	if o.Action != format4.ActIdentDup {
		return cid, nil
	}
	return b.f4CIDAt(typ, o.Seq)
}

func (b format4Backend) storeBatch(schemaName string, records [][]byte, peerID string, signature []byte, tags *SourceTags) (int, error) {
	if len(records) == 0 {
		return 0, b.s.requireWritable("store batch")
	}
	_, outcomes, err := b.f4Put("store batch", schemaName, records, peerID, signature, tags, true)
	return f4Inserted(outcomes), err
}

// storeOne is the single-record write (Store: no tag).
func (b format4Backend) storeOne(schemaName string, data []byte, peerID string, signature []byte, tags *SourceTags) (string, error) {
	cids, outcomes, err := b.f4Put("store record", schemaName, [][]byte{data}, peerID, signature, tags, true)
	if err != nil {
		return "", err
	}
	typ, _ := f4Type(schemaName)
	return b.f4HeldCID(typ, cids[0], outcomes[0])
}

// StoreWithSourceTags writes the record with its tag in one PUT; the licence
// is already recorded.
func (b format4Backend) StoreWithSourceTags(schemaName string, data []byte, peerID string, signature []byte, tags SourceTags) (string, error) {
	return b.storeOne(schemaName, data, peerID, signature, &tags)
}

// StoreRoutedByProducer is keyed by the raw peer id: the engine derives the
// copy's producer token from it (C-13), as format 1 named the table.
func (b format4Backend) StoreRoutedByProducer(schemaName string, data []byte, peerID string, signature []byte) (string, error) {
	return b.storeOne(schemaName, data, peerID, signature, nil)
}

// importDatasetShardChunk stores a shard chunk as the serving peer's copies,
// grouped by the source tag each record carries in the (signed)
// index, with format 1's tag normalization. A record lands under the CIDv1 of
// its bytes (the engine verifies it); no ingest identity applies (format 1's
// import stored what the peer holds).
func (b format4Backend) importDatasetShardChunk(index *DatasetExportIndex, providerPeerID string, records []DatasetExportIndexRecord, readRecord datasetShardRecordReader) (int, error) {
	if err := b.closed(); err != nil {
		return 0, err
	}
	provider := strings.TrimSpace(providerPeerID)
	type group struct {
		tags *SourceTags
		data [][]byte
	}
	var order []string
	groups := map[string]*group{}
	for _, record := range records {
		data, err := readRecord(record)
		if err != nil {
			return 0, err
		}
		if computeCID(data) != record.CID && sha256Hex(data) != record.CID {
			return 0, fmt.Errorf("record CID mismatch for indexed record %s", record.CID)
		}
		tags := record.SourceTags
		if strings.TrimSpace(tags.ProviderID) == "" {
			tags.ProviderID = provider
		}
		if producer := strings.TrimSpace(tags.ProducerPeerID); producer == "" || producer == strings.TrimSpace(tags.ProviderID) {
			tags.ProducerPeerID = provider
		}
		var key string
		var tp *SourceTags
		if strings.TrimSpace(tags.ProviderID) != "" && strings.TrimSpace(tags.SourceName) != "" {
			t := tags
			tp = &t
			key = fmt.Sprintf("%q", []string{t.ProviderID, t.SourceName, t.SourceURL, t.BatchID, t.ContentKeyID, t.ProducerPeerID, t.ProducerPublicKey})
		}
		g := groups[key]
		if g == nil {
			g = &group{tags: tp}
			groups[key] = g
			order = append(order, key)
		}
		g.data = append(g.data, data)
	}
	imported := 0
	for _, key := range order {
		g := groups[key]
		_, outcomes, err := b.f4Put("import dataset shard", index.SchemaName, g.data, provider, nil, g.tags, false)
		imported += f4Inserted(outcomes)
		if err != nil {
			return imported, fmt.Errorf("store imported %s records: %w", index.SchemaName, err)
		}
	}
	return imported, nil
}

// UpsertSourceTags attaches a tag to a held record (f4Retag).
func (b format4Backend) UpsertSourceTags(schemaName, cid string, tags SourceTags) error {
	if err := b.closed(); err != nil {
		return err
	}
	tags = normalizeSourceTags(tags)
	if err := ValidateSourceTags(tags); err != nil {
		return err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return err
	}
	return b.f4Retag(schemaName, typ, cid, f4Tag(tags))
}

// Delete removes every copy of a CID.
func (b format4Backend) Delete(schemaName, cid string) error {
	if err := b.s.requireWritable("delete record"); err != nil {
		return err
	}
	if err := b.closed(); err != nil {
		return err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return err
	}
	if !f4CIDStored(cid) {
		return fmt.Errorf("not found: %s", cid)
	}
	n, err := b.d.api().Delete(b.d.ctx, typ, []string{cid})
	if errors.Is(err, format4.ErrNoType) {
		n, err = 0, nil
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("not found: %s", cid)
	}
	return nil
}

// f4Supersede is SUPERSEDE(keep) on a (provider, source) feed: the rows of
// every other batch in that feed's file go, then every record left with no
// row in any feed file. Matched counts those tag instances; Deleted counts the records
// that left the type (format 1's count: a record another producer's copy
// keeps stays).
func (b format4Backend) f4Supersede(result SourceBatchReconcileResult) (SourceBatchReconcileResult, error) {
	if err := b.closed(); err != nil {
		return result, err
	}
	typ, err := f4Type(result.SchemaName)
	if err != nil {
		return result, err
	}
	api, ctx := b.d.api(), b.d.ctx
	var before int64
	if result.Apply {
		if before, err = b.f4TypeRecords(typ); err != nil {
			return result, err
		}
	}
	r, err := api.Supersede(ctx, typ, result.ProviderID, result.SourceName, result.KeepBatch, result.Apply)
	if errors.Is(err, format4.ErrNoType) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Matched = r.TagsDeleted
	if !result.Apply || r.TagsDeleted == 0 {
		return result, nil
	}
	after, err := b.f4TypeRecords(typ)
	if err != nil {
		return result, err
	}
	if d := before - after; d > 0 {
		result.Deleted = d
	}
	return result, nil
}

func (b format4Backend) reconcileSourceBatch(result SourceBatchReconcileResult) (SourceBatchReconcileResult, error) {
	return b.f4Supersede(result)
}

func (b format4Backend) supersedeSourceBatches(result DatasetSupersedeResult, started time.Time) (DatasetSupersedeResult, error) {
	r, err := b.f4Supersede(SourceBatchReconcileResult{SchemaName: result.SchemaName, ProviderID: result.ProviderID,
		SourceName: result.SourceName, KeepBatch: result.KeepBatch, Apply: true})
	if err != nil {
		return result, err
	}
	result.TagsDeleted, result.RecordsDeleted = r.Matched, r.Deleted
	if r.Matched > 0 {
		result.Chunks = 1
		log.Infof("Dataset supersede %s %s/%s keep %s: retired %d tag instances and %d records in %s (format 4)",
			result.SchemaName, result.ProviderID, result.SourceName, result.KeepBatch, r.Matched, r.Deleted,
			time.Since(started).Round(time.Millisecond))
	}
	return result, nil
}

// GarbageCollect: the engine evicts by quota (C-11).
func (b format4Backend) GarbageCollect(time.Duration) (int64, error) {
	return 0, f4Unsupported("age-based garbage collection (the engine evicts by quota, oldest arrivals first)")
}

// GarbageCollectToQuota deletes the oldest records by arrival until the
// store is under maxBytes (QUOTA_GC, §3.8.11); it returns the records
// dropped.
func (b format4Backend) GarbageCollectToQuota(maxBytes int64) (int64, error) {
	if err := b.s.requireWritable("garbage collect to quota"); err != nil {
		return 0, err
	}
	if err := b.closed(); err != nil {
		return 0, err
	}
	r, err := b.d.api().QuotaGC(b.d.ctx, maxBytes)
	if err != nil {
		return 0, fmt.Errorf("garbage collect to quota: %w", err)
	}
	if r.RecordsDropped > 0 {
		log.Infof("GarbageCollectToQuota (format 4) dropped the %d oldest record(s) by arrival, %d bytes, %d file(s) (cap %d)",
			r.RecordsDropped, r.BytesFreed, r.FilesDropped, maxBytes)
	}
	return r.RecordsDropped, nil
}

// RefreshSourceBatchSummary: the lane counters are the writer's,
// transactional with the rows, so nothing is recomputed. The request is
// checked as format 1 checks it (every field required, a valid schema name).
func (b format4Backend) RefreshSourceBatchSummary(schemaName, providerID, sourceName, batchID string) error {
	for _, f := range []struct{ v, name string }{{schemaName, "schema name"}, {providerID, "provider id"}, {sourceName, "source name"},
		{batchID, "batch id"}} {
		if strings.TrimSpace(f.v) == "" {
			return errors.New(f.name + " is required")
		}
	}
	if _, err := f4Type(strings.TrimSpace(schemaName)); err != nil {
		return err
	}
	return nil
}

// RebuildSourceSummaries: the lane counters are the writer's.
func (b format4Backend) RebuildSourceSummaries() error { return nil }

// RebuildDerivedState: no hot window to hydrate.
func (b format4Backend) RebuildDerivedState() error { return nil }

// RebuildIndex rebuilds the derived type indexes and full-text indexes
// from the feed files; it returns the entries per schema.
func (b format4Backend) RebuildIndex() (map[string]int64, error) {
	if err := b.s.requireWritable("reindex"); err != nil {
		return nil, err
	}
	if err := b.closed(); err != nil {
		return nil, err
	}
	rows, err := b.d.api().Rebuild(b.d.ctx, "", format4.RebuildTypeIndex|format4.RebuildFTS)
	if err != nil {
		return nil, fmt.Errorf("reindex: %w", err)
	}
	entries := make(map[string]int64, len(rows))
	for _, r := range rows {
		entries[r.Type+".fbs"] = r.Entries
	}
	// Format 1 answers every schema the validator embeds, 0 for one with no
	// records (a standard the engine could not register holds none).
	v := b.s.validator
	if v == nil {
		return entries, nil
	}
	out := make(map[string]int64, len(entries))
	for _, schema := range v.Schemas() {
		out[schema] = entries[schema]
	}
	return out, nil
}

// recoverControlLocked replaces only the control instance; the engine is
// its own poison domain (onFailure reopens it).
func (b format4Backend) recoverControlLocked() (uint64, error) {
	return b.s.recoverControlInstanceLocked()
}

func (b format4Backend) close() error { return b.d.close() }
