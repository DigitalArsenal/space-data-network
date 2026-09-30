package storage

// format2_daemon_writes.go — the node's record writes on store format 2
// (design §6, §7, A2, A3, A20; T6 scope 3). Every write goes through the
// partition store's router: a ring entry per record in the producer's
// (producer, type) partition, returning once the commit holding it is
// durable. Dedupe, CAT supersede (scoped to the write's source), source tags
// (a RETAG row per further tag tuple), lane counters, reconcile and tombs are
// the engine's; nothing here takes s.mu or writes a control row for a record.
//
// Writes wait for the type owner to label them (A20: the publish API and
// module flows read their own writes at type level). Licences are LICENCE
// frames in the partition, ahead of the records (§6.1); the control table of
// batch licences is still written, because it is what the export and the
// connectors read (source_batch_license.go).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/metrics"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

// format2PutChunk bounds the records one PutBatch call holds in memory; the
// ring's credits bound what is in flight.
const format2PutChunk = 4096

func format2Tags(tags *SourceTags) *format2.Tags {
	if tags == nil {
		return nil
	}
	t := normalizeSourceTags(*tags)
	return &format2.Tags{ProviderID: t.ProviderID, SourceName: t.SourceName, SourceURL: t.SourceURL, BatchID: t.BatchID,
		ContentKeyID: t.ContentKeyID, ProducerPeerID: t.ProducerPeerID, ProducerPublicKey: t.ProducerPublicKey,
		License: t.License, LicenseURL: t.LicenseURL, Citation: t.Citation, ShareAlike: t.ShareAlike}
}

// f2StoreBatch is storeBatch/storeOne on format 2. It returns how many
// records were new to the standard (the legacy count: a CID some producer
// already held, and an ingest-identity repeat, are not inserted) and each
// record's CID. Records the engine refused fail the batch, after the others
// are stored, with a *RefusedRecordsError naming each.
func (s *FlatSQLStore) f2StoreBatch(schemaName string, records [][]byte, peerID string, signature []byte, tags *SourceTags) (int, []string, error) {
	return s.f2StoreBatchIdentity(schemaName, records, peerID, signature, tags, true)
}

// RefusedRecordsError is a batch write the store refused some records of,
// after storing the others: each refused record's CID and why. A caller
// that reports per record (the publish API) reports these as failed and the
// rest as stored; any other caller fails, and a feed's checkpoint does not
// move past records it did not store.
type RefusedRecordsError struct {
	Schema  string
	Records int
	// Refused maps each refused record's CID to its reason, in the order
	// Order lists them.
	Refused map[string]error
	Order   []string
}

func (e *RefusedRecordsError) Error() string {
	if len(e.Order) == 0 {
		return fmt.Sprintf("store %s batch: records refused", e.Schema)
	}
	first := e.Order[0]
	if len(e.Order) == e.Records {
		return fmt.Sprintf("store %s batch: every record was refused (first %s: %v)", e.Schema, first, e.Refused[first])
	}
	return fmt.Sprintf("store %s batch: the engine refused %d of %d record(s) (first %s: %v)", e.Schema, len(e.Order), e.Records, first, e.Refused[first])
}

func (e *RefusedRecordsError) refuse(cid string, err error) {
	if e.Refused == nil {
		e.Refused = map[string]error{}
	}
	if _, ok := e.Refused[cid]; !ok {
		e.Order = append(e.Order, cid)
	}
	e.Refused[cid] = err
}

// StoreIngestBatch stores an ingest's batch of records of one standard:
// StoreBatch, or StoreBatchWithSourceTags with tags. A record the store
// refused fails the batch (after the rest are stored), so a feed that
// fetched the records fails and its checkpoint does not move past them, as
// when it stored record by record (terabyte audit M9 review).
func (s *FlatSQLStore) StoreIngestBatch(schemaName string, records [][]byte, peerID string, tags *SourceTags) (int, error) {
	if tags == nil {
		return s.StoreBatch(schemaName, records, peerID, nil)
	}
	return s.StoreBatchWithSourceTags(schemaName, records, peerID, nil, *tags)
}

// f2StoreBatchIdentity is f2StoreBatch; identity applies the ingest identity
// (off for a dataset shard import, which stores what the peer holds, as
// format 1's import did).
func (s *FlatSQLStore) f2StoreBatchIdentity(schemaName string, records [][]byte, peerID string, signature []byte, tags *SourceTags, identity bool) (int, []string, error) {
	if err := s.requireWritable("store batch"); err != nil {
		return 0, nil, err
	}
	if err := s.f2Closed(); err != nil {
		return 0, nil, err
	}
	if len(records) == 0 {
		return 0, nil, nil
	}
	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid schema name: %w", err)
	}
	sealed := encfield.HasEncryptedFields(tableName)
	if tags != nil {
		if err := ValidateSourceTags(normalizeSourceTags(*tags)); err != nil {
			return 0, nil, err
		}
	}
	ctx := s.f2ctx()
	cids := make([]string, len(records))
	for i, data := range records {
		cids[i] = computeCID(data)
	}
	present, err := s.ps.Present(ctx, schemaName, cids)
	if present == nil {
		present = map[string]bool{}
	}
	if err != nil {
		return 0, nil, fmt.Errorf("check %s record batch: %w", schemaName, err)
	}

	// INGEST IDENTITY (record_ingest_identity.go): a new CID whose identity
	// the lane holds is the same record fetched again; it lands nowhere and
	// the CID holding it takes this write's tag.
	identityOn := identity && ingestIdentityApplies(schemaName, tags)
	var (
		lane       ingestIdentityLane
		identities []string
		held       map[string]string
		repeats    [][2]string // (held CID, the repeat's CID)
		newRows    []ingestIdentityRow
	)
	if identityOn {
		lane = ingestIdentityLaneOf(tags)
		identities = make([]string, len(records))
		probe := make([]string, 0, len(records))
		for i, data := range records {
			if present[cids[i]] {
				continue
			}
			if identities[i] = recordIngestIdentity(schemaName, data); identities[i] != "" {
				probe = append(probe, identities[i])
			}
		}
		if held, err = s.f2HeldIngestIdentities(schemaName, lane, probe); err != nil {
			return 0, nil, err
		}
	}

	inserted := 0
	seen := make(map[string]bool, len(records))
	puts := make([]format2.Put, 0, min(len(records), format2PutChunk))
	var putCIDs []string
	refused := &RefusedRecordsError{Schema: schemaName, Records: len(records)}
	flush := func() error {
		if len(puts) == 0 {
			return nil
		}
		res, err := s.ps.PutBatch(ctx, schemaName, puts, peerID, signature, format2Tags(tags))
		if err != nil {
			return err
		}
		for i, r := range res {
			if r.Err != nil {
				refused.refuse(putCIDs[i], r.Err)
				continue
			}
			if !present[putCIDs[i]] && !seen[putCIDs[i]] {
				inserted++
			}
			seen[putCIDs[i]] = true
		}
		puts, putCIDs = puts[:0], putCIDs[:0]
		return nil
	}
	for i, data := range records {
		if identityOn && identities[i] != "" && !present[cids[i]] {
			if h, ok := held[identities[i]]; ok && h != cids[i] {
				repeats = append(repeats, [2]string{h, cids[i]})
				continue
			}
			held[identities[i]] = cids[i]
			newRows = append(newRows, ingestIdentityRow{identity: identities[i], cid: cids[i]})
		}
		put := format2.Put{Data: data}
		if sealed {
			stored, err := s.storableRecordBytes(schemaName, data)
			if err != nil {
				return inserted, cids, err
			}
			put.Sealed = stored
		}
		puts = append(puts, put)
		putCIDs = append(putCIDs, cids[i])
		if len(puts) >= format2PutChunk {
			if err := flush(); err != nil {
				return inserted, cids, err
			}
		}
	}
	if err := flush(); err != nil {
		return inserted, cids, err
	}
	s.f2.lanes.invalidate()
	// One label budget for the whole write: the batch's partition, then the
	// partitions its identity repeats were retagged into.
	labelsBy := time.Now().Add(format2.LabelWaitMax)
	if err := s.f2WaitLabeled(ctx, schemaName, []string{peerID}, labelsBy); err != nil {
		return inserted, cids, err
	}
	if identityOn {
		// An identity is held by a stored CID only.
		if len(refused.Order) > 0 {
			kept := newRows[:0]
			for _, r := range newRows {
				if _, no := refused.Refused[r.cid]; !no {
					kept = append(kept, r)
				}
			}
			newRows = kept
		}
		if len(newRows) > 0 {
			unlock := s.lockWrite("format2 ingest identities")
			err := upsertIngestIdentities(s.db, schemaName, lane, newRows)
			unlock()
			if err != nil {
				return inserted, cids, err
			}
		}
		var retag []string
		for _, r := range repeats {
			if why, no := refused.Refused[r[0]]; no {
				// Its identity's record was refused in this batch: nothing
				// holds the repeat.
				refused.refuse(r[1], fmt.Errorf("the record holding its ingest identity (%s) was refused: %w", r[0], why))
				continue
			}
			retag = append(retag, r[0])
		}
		var retagged []string
		for _, cid := range dedupeStrings(retag) {
			peer, err := s.f2RetagCommit(schemaName, cid, *tags)
			if err != nil {
				return inserted, cids, err
			}
			retagged = append(retagged, peer)
		}
		if err := s.f2WaitLabeled(ctx, schemaName, retagged, labelsBy); err != nil {
			return inserted, cids, err
		}
	}
	if len(refused.Order) > 0 {
		return inserted, cids, refused
	}
	return inserted, cids, nil
}

// f2StoreOne is the single-record write.
func (s *FlatSQLStore) f2StoreOne(schemaName string, data []byte, peerID string, signature []byte, tags *SourceTags) (string, error) {
	if identityOn := ingestIdentityApplies(schemaName, tags); identityOn {
		// The held CID is what the caller tags and reports (storeOne).
		identity := recordIngestIdentity(schemaName, data)
		cid := computeCID(data)
		if identity != "" {
			present, err := s.ps.Present(s.f2ctx(), schemaName, []string{cid})
			if err != nil {
				return "", err
			}
			if !present[cid] {
				held, err := s.f2HeldIngestIdentities(schemaName, ingestIdentityLaneOf(tags), []string{identity})
				if err != nil {
					return "", err
				}
				if h, ok := held[identity]; ok && h != cid {
					if err := s.f2Retag(schemaName, h, *tags); err != nil {
						return "", err
					}
					return h, nil
				}
			}
		}
	}
	_, cids, err := s.f2StoreBatch(schemaName, [][]byte{data}, peerID, signature, tags)
	if err != nil {
		return "", err
	}
	return cids[0], nil
}

// f2HeldIngestIdentities is heldIngestIdentities with the liveness join on
// the partition store: an identity counts only while its CID has a live copy.
func (s *FlatSQLStore) f2HeldIngestIdentities(schemaName string, lane ingestIdentityLane, identities []string) (map[string]string, error) {
	out := map[string]string{}
	unique := dedupeStrings(identities)
	if len(unique) == 0 {
		return out, nil
	}
	candidates := map[string]string{}
	for start := 0; start < len(unique); start += batchProbeCIDs {
		end := min(start+batchProbeCIDs, len(unique))
		batch := unique[start:end]
		args := make([]any, 0, len(batch)+3)
		args = append(args, schemaName, lane.providerID, lane.sourceName)
		for _, id := range batch {
			args = append(args, id)
		}
		rows, err := s.db.Query(`SELECT identity, cid FROM sdn_record_ingest_identity
			WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND identity IN (`+placeholderList(len(batch))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("probe %s ingest identities: %w", schemaName, err)
		}
		for rows.Next() {
			var id, cid string
			if err := rows.Scan(&id, &cid); err != nil {
				rows.Close()
				return nil, err
			}
			candidates[id] = cid
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	cids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		cids = append(cids, c)
	}
	live, err := s.ps.Present(s.f2ctx(), schemaName, cids)
	if err != nil {
		return nil, err
	}
	for id, c := range candidates {
		if live[c] {
			out[id] = c
		}
	}
	return out, nil
}

// f2Retag attaches a source tag to a stored record: the record again, with
// that tag, into the partition of its FIRST copy (the engine appends a RETAG
// row, or nothing when the copy already carries the tuple).
func (s *FlatSQLStore) f2Retag(schemaName, cid string, tags SourceTags) error {
	peer, err := s.f2RetagCommit(schemaName, cid, tags)
	if err != nil {
		return err
	}
	return s.f2WaitLabeled(s.f2ctx(), schemaName, []string{peer}, time.Now().Add(format2.LabelWaitMax))
}

// f2RetagCommit is f2Retag without the label wait: it returns once the
// retag is durable, with the producer whose partition holds it.
func (s *FlatSQLStore) f2RetagCommit(schemaName, cid string, tags SourceTags) (string, error) {
	tags = normalizeSourceTags(tags)
	if err := ValidateSourceTags(tags); err != nil {
		return "", err
	}
	ctx := s.f2ctx()
	rec, err := s.ps.GetRecord(ctx, schemaName, cid)
	if err != nil {
		if errors.Is(err, format2.ErrNotFound) {
			return "", fmt.Errorf("source-tagged record not found: %s/%s", schemaName, cid)
		}
		return "", err
	}
	plain, err := s.openStoredRecordBytes(schemaName, rec.Data)
	if err != nil {
		return "", err
	}
	put := format2.Put{Data: plain}
	if encfield.IsSealed(rec.Data) {
		put.Sealed = rec.Data
	}
	peer := rec.PeerID
	if strings.TrimSpace(peer) == "" {
		peer = rec.Producer
	}
	res, err := s.ps.PutBatch(ctx, schemaName, []format2.Put{put}, peer, rec.Signature, format2Tags(&tags))
	if err != nil {
		return "", err
	}
	s.f2.lanes.invalidate()
	if len(res) == 1 && res[0].Err != nil {
		return "", fmt.Errorf("retag %s: %w", cid, res[0].Err)
	}
	return peer, nil
}

// f2Delete kills every live copy of a CID (a TOMB_CID in each partition
// holding it).
func (s *FlatSQLStore) f2Delete(schemaName, cid string) error {
	if err := s.requireWritable("delete record"); err != nil {
		return err
	}
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return fmt.Errorf("invalid schema name: %w", err)
	}
	ctx := s.f2ctx()
	copies, err := s.ps.CopiesOf(ctx, schemaName, cid)
	if err != nil {
		return err
	}
	if len(copies) == 0 {
		return fmt.Errorf("not found: %s", cid)
	}
	// Every tomb first, then one label wait over their partitions under one
	// budget: never LabelWaitMax per copy.
	done := map[string]bool{}
	var producers []string
	for _, c := range copies {
		if done[c.Producer] {
			continue
		}
		done[c.Producer] = true
		err := s.ps.Delete(ctx, schemaName, c.Producer, cid)
		if err != nil {
			if len(producers) > 0 {
				s.f2.lanes.invalidate()
			}
			return err
		}
		producers = append(producers, c.Producer)
	}
	s.f2.lanes.invalidate()
	return s.f2WaitLabeled(ctx, schemaName, producers, time.Now().Add(format2.LabelWaitMax))
}

// f2ReconcileSourceBatch is ReconcileSourceBatch on format 2: RECONCILE(keep)
// in every partition holding a tag instance of the (provider, source) lane
// outside the kept batch (A2). Matched counts those tag instances (the legacy
// count of tag rows whose record is held); Deleted counts the records that
// left the type, from its head.
func (s *FlatSQLStore) f2ReconcileSourceBatch(result SourceBatchReconcileResult) (SourceBatchReconcileResult, error) {
	ctx := s.f2ctx()
	// The lane's own lanes only (terabyte audit B7), never every lane of the
	// store.
	typ := format2.TypeName(result.SchemaName)
	lanes, err := s.ps.LanesWhere(ctx, format2.LaneFilter{Type: typ, Provider: result.ProviderID, Source: result.SourceName})
	if err != nil {
		return result, err
	}
	tokens := map[string]bool{}
	for _, l := range lanes {
		if l.Type != typ || !l.Tuple || l.Provider != result.ProviderID || l.Source != result.SourceName || l.Batch == result.KeepBatch || l.Count <= 0 {
			continue
		}
		result.Matched += l.Count
		tokens[l.Producer] = true
	}
	if !result.Apply || result.Matched == 0 {
		return result, nil
	}
	before, err := s.ps.TypeCounterOf(result.SchemaName)
	if err != nil {
		return result, err
	}
	// Every reconcile first, then one label wait over their partitions under
	// one budget: never LabelWaitMax per partition.
	reconciled := make([]string, 0, len(tokens))
	for token := range tokens {
		err := s.ps.Reconcile(ctx, result.SchemaName, token, result.ProviderID, result.SourceName, result.KeepBatch)
		if err != nil {
			if len(reconciled) > 0 {
				s.f2.lanes.invalidate()
			}
			return result, err
		}
		reconciled = append(reconciled, token)
	}
	s.f2.lanes.invalidate()
	if err := s.f2WaitLabeled(ctx, result.SchemaName, reconciled, time.Now().Add(format2.LabelWaitMax)); err != nil {
		return result, err
	}
	// Deleted counts logical records gone from the type (the legacy count):
	// a copy another producer still holds stays live, promoted (A14).
	after, err := s.ps.TypeCounterOf(result.SchemaName)
	if err != nil {
		return result, err
	}
	if d := before.FirstLive - after.FirstLive; d > 0 {
		result.Deleted = d
	}
	return result, nil
}

// f2WaitLabeled waits, until deadline, for the type owner to label the
// partitions of peers (A20). A wait that reaches its deadline is not a
// failed write: the records are durable, so it is logged and counted
// (sdn_storage_label_wait_timeouts_total) and the write succeeds; type-level
// reads see the records once the owner catches up.
func (s *FlatSQLStore) f2WaitLabeled(ctx context.Context, schemaName string, peers []string, deadline time.Time) error {
	if len(peers) == 0 {
		return nil
	}
	return labelWaitOutcome(s.ps.WaitLabeledUntil(ctx, schemaName, peers, deadline), s.ps.LabelWaitTimeouts)
}

// f2LabelWarnAt is when a label-wait deadline was last logged (unix ns).
var f2LabelWarnAt atomic.Int64

// labelWaitOutcome maps a label wait's error to the write's: a
// *format2.LabelWaitError is counted, logged at most once a minute with the
// running count, and dropped; anything else is the write's error.
func labelWaitOutcome(err error, total func() uint64) error {
	var lw *format2.LabelWaitError
	if !errors.As(err, &lw) {
		return err
	}
	metrics.StorageLabelWaitTimeouts(lw.Schema, lw.Partitions)
	now := time.Now().UnixNano()
	if last := f2LabelWarnAt.Load(); now-last >= int64(time.Minute) && f2LabelWarnAt.CompareAndSwap(last, now) {
		log.Warnf("format 2: %v (%d partition label waits have reached their deadline since start)", lw, total())
	}
	return nil
}

// f2GarbageCollectToQuota hands the cap to the engine's quota planner (§13):
// it evicts in arrival order down to the 0.85 low-water mark on its own
// writer threads. Nothing is evicted by this call; it returns 0.
func (s *FlatSQLStore) f2GarbageCollectToQuota(maxBytes int64) (int64, error) {
	if err := s.f2Closed(); err != nil {
		return 0, err
	}
	if err := s.ps.SetQuota(uint64(maxBytes)); err != nil {
		return 0, fmt.Errorf("garbage collect to quota: set the partition store's quota: %w", err)
	}
	return 0, nil
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// f2ImportDatasetShardChunk is importDatasetShardChunk on format 2: the
// records go to the serving peer's partition through the router, grouped by
// the source tag each carries in the (signed) index, with the same tag
// normalization. A record lands under the CIDv1 of its bytes: the engine
// verifies that CID, so an old bundle's bare-hex identity is not kept.
func (s *FlatSQLStore) f2ImportDatasetShardChunk(index *DatasetExportIndex, providerPeerID string, records []DatasetExportIndexRecord, readRecord datasetShardRecordReader) (int, error) {
	if err := s.f2Closed(); err != nil {
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
		n, _, err := s.f2StoreBatchIdentity(index.SchemaName, g.data, provider, nil, g.tags, false)
		imported += n
		if err != nil {
			return imported, fmt.Errorf("store imported %s records: %w", index.SchemaName, err)
		}
	}
	return imported, nil
}
