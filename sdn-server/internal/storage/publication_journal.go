package storage

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// PublicationSource opts an exact source into durable publication retention.
// ProductSource on CAT selects its OCM closure for source snapshots.
type PublicationSource struct{ Schema, Provider, Source, ProductSource string }
type publicationEntry struct {
	Digest          string
	Sequence        uint64
	Schema          string
	Tags            SourceTags
	Peer            string
	Signature       []byte
	Identity, OwnTS bool
	OutcomeKnown    bool `json:"-"`
	Applied         bool
	Refused         map[int]int32 `json:",omitempty"`
}

func (s *FlatSQLStore) publicationDir() string {
	return filepath.Join(s.basePath, "publication-journal")
}
func (s *FlatSQLStore) publicationEntryDir(seq uint64) string {
	return filepath.Join(s.publicationDir(), fmt.Sprintf("%020d", seq))
}
func (s *FlatSQLStore) retainsPublication(schema, provider, source string) bool {
	for _, x := range s.publicationSources {
		if x.Schema == schema && x.Provider == provider && x.Source == source {
			return true
		}
	}
	return false
}

// ConfigurePublicationSources is called before ingestion starts. Settings are
// durable and additive: removing a publishing rule cannot discard pending data.
// Existing records require reingestion before their first retained snapshot.
func (s *FlatSQLStore) ConfigurePublicationSources(sources []PublicationSource) error {
	if len(sources) == 0 {
		return nil
	}
	if _, ok := s.rb.(format4Backend); !ok {
		return errors.New("publication retention requires store format 4")
	}
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	if err := s.requireWritable("configure publication retention"); err != nil {
		return err
	}
	merged := append([]PublicationSource(nil), s.publicationSources...)
	for _, x := range sources {
		x.Provider = strings.TrimSpace(x.Provider)
		x.Source = strings.TrimSpace(x.Source)
		x.ProductSource = strings.TrimSpace(x.ProductSource)
		if (x.Schema != "CAT.fbs" && x.Schema != "OCM.fbs") || x.Provider == "" || x.Source == "" {
			return errors.New("publication retention requires CAT/OCM and exact provider/source")
		}
		if x.ProductSource != "" && x.Schema != "CAT.fbs" {
			return errors.New("catalog_product_source requires CAT")
		}
		found := false
		for i, old := range merged {
			if old.Schema == x.Schema && old.Provider == x.Provider && old.Source == x.Source {
				if old.ProductSource != "" && x.ProductSource != "" && old.ProductSource != x.ProductSource {
					return errors.New("catalog product source cannot change")
				}
				if x.ProductSource != "" {
					merged[i].ProductSource = x.ProductSource
				}
				found = true
			}
		}
		if !found {
			count, err := s.CountRawRecords(RawRecordQuery{SchemaName: x.Schema, ProviderID: x.Provider, SourceName: x.Source})
			if err != nil {
				return err
			}
			if count > 0 {
				return errors.New("enable publication retention on an empty source before initial ingestion")
			}
			merged = append(merged, x)
		}
	}
	for _, x := range merged {
		if x.ProductSource != "" {
			found := false
			for _, p := range merged {
				if p.Schema == "OCM.fbs" && p.Provider == x.Provider && p.Source == x.ProductSource {
					found = true
				}
			}
			if !found {
				return errors.New("catalog product source requires its own retention option")
			}
		}
	}
	if err := os.MkdirAll(s.publicationDir(), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	if err = publicationAtomicFile(filepath.Join(s.publicationDir(), "sources.json"), raw); err != nil {
		return err
	}
	if err = publicationSyncDir(s.basePath); err != nil {
		return err
	}
	s.publicationSources = merged
	s.publishSourceKeys()
	s.publicationEnabled.Store(true)
	return nil
}

// A prepared SDS stream and metadata are synced and renamed before engine PUT.
// No later retained write passes an interrupted PUT. Startup replays that intent
// before exposing the store, then durably marks it applied. Thus a crash after
// PUT and before the marker loses neither its bytes nor its batch identity.
func (b format4Backend) putRetained(op, schema string, records [][]byte, peer string, sig []byte, tags SourceTags, opt f4PutOpt) ([]string, []format4.Outcome, error) {
	s := b.s
	if err := s.requireWritable(op); err != nil {
		return nil, nil, err
	}
	tags = normalizeSourceTags(tags)
	if err := ValidateSourceTags(tags); err != nil {
		return nil, nil, err
	}
	if tags.BatchID == "" {
		return nil, nil, errors.New("retained publication requires batch id")
	}
	for _, data := range records {
		if err := s.validator.Validate(context.Background(), schema, data); err != nil {
			return nil, nil, err
		}
	}
	seq := uint64(1)
	if n := len(s.publicationEntries); n > 0 {
		seq = s.publicationEntries[n-1].Sequence + 1
	}
	e := publicationEntry{Sequence: seq, Schema: schema, Tags: tags, Peer: peer, Signature: sig, Identity: opt.identity, OwnTS: opt.ownTS}
	temp, err := os.MkdirTemp(s.publicationDir(), ".prepare-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(temp)
	f, err := os.OpenFile(filepath.Join(temp, "records.sds"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.New()
	writer := io.MultiWriter(f, digest)
	for _, data := range records {
		if uint64(len(data)) > uint64(^uint32(0)) {
			err = errors.New("publication record exceeds frame limit")
			break
		}
		if err = binary.Write(writer, binary.LittleEndian, uint32(len(data))); err != nil {
			break
		}
		if _, err = writer.Write(data); err != nil {
			break
		}
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, nil, err
	}
	e.Digest = hex.EncodeToString(digest.Sum(nil))
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, nil, err
	}
	if err = publicationAtomicFile(filepath.Join(temp, "entry.json"), raw); err != nil {
		return nil, nil, err
	}
	if err = os.Rename(temp, s.publicationEntryDir(seq)); err != nil {
		return nil, nil, err
	}
	s.publicationEntries = append(s.publicationEntries, e)
	if err = publicationSyncDir(s.publicationDir()); err != nil {
		s.pausePublication(e, err)
		return nil, nil, err
	}
	if s.publicationHook != nil {
		s.publicationHook("prepared")
	}
	cids, out, writeErr := b.f4PutDirect(op, schema, records, peer, sig, &tags, opt)
	if writeErr == nil && s.publicationHook != nil {
		s.publicationHook("stored")
	}
	var refused *RefusedRecordsError
	if writeErr != nil && !errors.As(writeErr, &refused) {
		s.pausePublication(e, writeErr)
		return cids, out, writeErr
	}
	e.Refused = publicationRefusals(out)
	// The engine's per-record outcome is final even when a record was refused.
	// Retrying a failed completion write does not replay a successful PUT.
	var markerErr error
	for attempt := 0; attempt < 3; attempt++ {
		markerErr = s.markPublicationApplied(e)
		if markerErr == nil {
			break
		}
	}
	if markerErr != nil {
		s.pausePublication(e, markerErr)
		return cids, out, fmt.Errorf("engine write completed; publication acknowledgement pending: %w", markerErr)
	}
	return cids, out, writeErr
}
func publicationRefusals(out []format4.Outcome) map[int]int32 {
	refused := map[int]int32{}
	for i, o := range out {
		if o.Action == format4.ActRejected {
			refused[i] = o.Reject
		}
	}
	return refused
}
func (s *FlatSQLStore) markPublicationApplied(e publicationEntry) error {
	for i := range s.publicationEntries {
		if s.publicationEntries[i].Sequence == e.Sequence {
			s.publicationEntries[i].Refused = e.Refused
			s.publicationEntries[i].OutcomeKnown = true
		}
	}
	raw, err := json.Marshal(e.Refused)
	if err != nil {
		return err
	}
	if s.publicationFault != nil {
		if err = s.publicationFault("applied"); err != nil {
			return err
		}
	}
	if err = publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "applied"), raw); err != nil {
		return err
	}
	for i := range s.publicationEntries {
		if s.publicationEntries[i].Sequence == e.Sequence {
			e.Applied = true
			s.publicationEntries[i] = e
			break
		}
	}
	return nil
}
func (s *FlatSQLStore) pausePublication(e publicationEntry, err error) {
	if s.publicationFailures == nil {
		s.publicationFailures = map[publicationSourceKey]error{}
	}
	s.publicationFailures[publicationSourceKey{e.Schema, e.Tags.ProviderID, e.Tags.SourceName}] = err
	log.Errorf("publication source requires operator recovery (%s): %v", e.Schema, err)
}

// PublicationRecoveryStatus reports isolated sources requiring operator recovery.
func (s *FlatSQLStore) PublicationRecoveryStatus() map[string]string {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	out := map[string]string{}
	for k, err := range s.publicationFailures {
		out[k.schema+"/"+k.provider+"/"+k.source] = err.Error()
	}
	return out
}
func (s *FlatSQLStore) recoverPublicationJournal() error {
	raw, err := os.ReadFile(filepath.Join(s.publicationDir(), "sources.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &s.publicationSources); err != nil {
		return err
	}
	dirs, err := os.ReadDir(s.publicationDir())
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		if strings.HasPrefix(dir.Name(), ".prepare-") {
			if err = os.RemoveAll(filepath.Join(s.publicationDir(), dir.Name())); err != nil {
				log.Warnf("publication temporary cleanup: %v", err)
			}
			continue
		}
		if strings.HasPrefix(dir.Name(), ".") {
			continue
		}
		var e publicationEntry
		raw, err = os.ReadFile(filepath.Join(s.publicationDir(), dir.Name(), "entry.json"))
		if err == nil {
			err = json.Unmarshal(raw, &e)
		}
		if err == nil && filepath.Base(s.publicationEntryDir(e.Sequence)) != dir.Name() {
			err = errors.New("invalid publication journal sequence")
		}
		if err != nil {
			// Scope cannot be trusted; isolate configured publication sources while
			// leaving ordinary storage and the node available for repair.
			for _, x := range s.publicationSources {
				s.pausePublication(publicationEntry{Schema: x.Schema, Tags: SourceTags{ProviderID: x.Provider, SourceName: x.Source}}, err)
			}
			continue
		}
		s.publicationEntries = append(s.publicationEntries, e)
		raw, err = os.ReadFile(filepath.Join(s.publicationEntryDir(e.Sequence), "applied"))
		if err == nil {
			if string(raw) != "1\n" {
				err = json.Unmarshal(raw, &e.Refused)
			}
			e.Applied = err == nil
		} else if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		if err == nil {
			_, err = s.publicationRecords(e)
		} // Applied streams are checked too.
		if err != nil {
			s.pausePublication(e, err)
			continue
		}
		s.publicationEntries[len(s.publicationEntries)-1] = e
		if e.Applied {
			continue
		}
		if s.publicationFailures[publicationSourceKey{e.Schema, e.Tags.ProviderID, e.Tags.SourceName}] != nil {
			continue
		}
		records, readErr := s.publicationRecords(e)
		if readErr != nil {
			s.pausePublication(e, readErr)
			continue
		}
		data := make([][]byte, len(records))
		for i, r := range records {
			data[i] = r.Data
		}
		for attempt := 0; attempt < 3; attempt++ {
			var out []format4.Outcome
			_, out, err = s.rb.(format4Backend).f4PutDirect("recover publication", e.Schema, data, e.Peer, e.Signature, &e.Tags, f4PutOpt{identity: e.Identity, ownTS: e.OwnTS})
			var refused *RefusedRecordsError
			if err != nil && !errors.As(err, &refused) {
				continue
			}
			e.Refused = publicationRefusals(out)
			err = s.markPublicationApplied(e)
			if err == nil {
				break
			}
		}
		if err != nil {
			s.pausePublication(e, err)
		}
	}
	s.publishSourceKeys()
	s.publicationEnabled.Store(len(s.publicationSources) > 0)
	return nil
}
func (s *FlatSQLStore) publicationRecords(e publicationEntry) ([]DatasetExportRecord, error) {
	f, err := os.Open(filepath.Join(s.publicationEntryDir(e.Sequence), "records.sds"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	remaining := info.Size()
	digest := sha256.New()
	reader := io.TeeReader(f, digest)
	var out []DatasetExportRecord
	ordinal := 0
	for remaining > 0 {
		var n uint32
		if err = binary.Read(reader, binary.LittleEndian, &n); err != nil {
			return nil, err
		}
		remaining -= 4
		if n < 8 || int64(n) > remaining {
			return nil, errors.New("invalid publication SDS frame")
		}
		data := make([]byte, int(n))
		if _, err = io.ReadFull(reader, data); err != nil {
			return nil, err
		}
		remaining -= int64(n)
		if _, refused := e.Refused[ordinal]; !refused {
			out = append(out, DatasetExportRecord{CID: computeCID(data), Data: data, SourceTags: e.Tags})
		}
		ordinal++
	}
	if e.Digest != hex.EncodeToString(digest.Sum(nil)) {
		return nil, errors.New("publication journal checksum mismatch")
	}
	return out, nil
}
func publicationSyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func publicationAtomicFile(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return publicationSyncDir(filepath.Dir(path))
}

// RetainedPublicationBatches includes superseded and expired batches. Completion
// is decided by the existing publication series ledger, not by input retention.
func (s *FlatSQLStore) RetainedPublicationBatches() []PublicationBatch {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	byKey := map[string]PublicationBatch{}
	for _, e := range s.publicationEntries {
		if !e.Applied || s.publicationFailures[publicationSourceKey{e.Schema, e.Tags.ProviderID, e.Tags.SourceName}] != nil {
			continue
		}
		key := e.Schema + "\x00" + e.Tags.ProviderID + "\x00" + e.Tags.SourceName + "\x00" + e.Tags.BatchID
		byKey[key] = PublicationBatch{e.Schema, e.Tags.ProviderID, e.Tags.SourceName, e.Tags.BatchID, e.Sequence}
	}
	var out []PublicationBatch
	for _, v := range byKey {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out
}

type PublicationBatch struct {
	Schema, Provider, Source, Batch string
	Sequence                        uint64
}

// PublicationSelection owns immutable bytes at a durable cutoff. The journal
// retains those bytes independently of live-record expiry and replacement.
// Reclamation requires batch completion AND no retained selection or snapshot
// reference; journal reclamation is deliberately absent.
type PublicationSelection struct {
	Cutoff                          uint64
	Schema, Provider, Source, Batch string
	records                         []DatasetExportRecord
}

func (p *PublicationSelection) Fingerprint() (string, int) {
	h := newPublicationSetHash(p.Schema, p.Provider, p.Source, p.Batch)
	for _, r := range p.records {
		h.Write([]byte(r.CID))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), len(p.records)
}
func (p *PublicationSelection) Records() []DatasetExportRecord {
	out := make([]DatasetExportRecord, len(p.records))
	for i, r := range p.records {
		out[i] = r
		out[i].Data = append([]byte(nil), r.Data...)
	}
	return out
}
func (s *FlatSQLStore) retainedSelectionLocked(schema, provider, source, batch string, cutoff uint64) (*PublicationSelection, error) {
	if err := s.publicationFailures[publicationSourceKey{schema, provider, source}]; err != nil {
		return nil, fmt.Errorf("publication source paused: %w", err)
	}
	p := &PublicationSelection{Cutoff: cutoff, Schema: schema, Provider: provider, Source: source, Batch: batch}
	if batch != "" {
		p.Cutoff = 0
	}
	byKey := map[string]DatasetExportRecord{}
	for _, e := range s.publicationEntries {
		if !e.Applied || e.Sequence > cutoff || e.Schema != schema || e.Tags.ProviderID != provider || e.Tags.SourceName != source || (batch != "" && e.Tags.BatchID != batch) {
			continue
		}
		if batch != "" {
			p.Cutoff = e.Sequence
		}
		records, err := s.publicationRecords(e)
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			key := r.CID
			if schema == "CAT.fbs" && batch == "" {
				if id := recordSupersedeKey(schema, r.Data); id != "" {
					key = id
				}
			}
			byKey[key] = r
		}
	}
	for _, r := range byKey {
		p.records = append(p.records, r)
	}
	sort.Slice(p.records, func(i, j int) bool { return p.records[i].CID < p.records[j].CID })
	return p, nil
}

type publicationSourceKey struct{ schema, provider, source string }

func (s *FlatSQLStore) publishSourceKeys() {
	keys := map[publicationSourceKey]bool{}
	for _, x := range s.publicationSources {
		keys[publicationSourceKey{x.Schema, x.Provider, x.Source}] = true
	}
	s.publicationSourceKeys.Store(&keys)
}
func (s *FlatSQLStore) publicationSourceEnabled(schema, provider, source string) bool {
	keys := s.publicationSourceKeys.Load()
	return keys != nil && (*keys)[publicationSourceKey{schema, provider, source}]
}

// RetryPublicationRecovery retries interrupted storage and acknowledgement writes
// once per call. Reconciliation calls it between publication attempts.
func (s *FlatSQLStore) RetryPublicationRecovery() {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	for _, e := range s.publicationEntries {
		if e.Applied {
			continue
		}
		key := publicationSourceKey{e.Schema, e.Tags.ProviderID, e.Tags.SourceName}
		records, err := s.publicationRecords(e)
		if err == nil && !e.OutcomeKnown {
			data := make([][]byte, len(records))
			for i, r := range records {
				data[i] = r.Data
			}
			var out []format4.Outcome
			_, out, err = s.rb.(format4Backend).f4PutDirect("retry publication", e.Schema, data, e.Peer, e.Signature, &e.Tags, f4PutOpt{identity: e.Identity, ownTS: e.OwnTS})
			var refused *RefusedRecordsError
			if err == nil || errors.As(err, &refused) {
				e.Refused = publicationRefusals(out)
				err = nil
			}
		}
		if err == nil {
			err = s.markPublicationApplied(e)
		}
		if err != nil {
			s.pausePublication(e, err)
		} else {
			delete(s.publicationFailures, key)
		}
	}
}
