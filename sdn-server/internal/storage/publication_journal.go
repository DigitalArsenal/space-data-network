package storage

import (
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
	"strconv"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// PublicationSource opts an exact source into durable publication retention.
// ProductSource on CAT selects its OCM closure for source snapshots.
type PublicationSource struct{ Schema, Provider, Source, ProductSource string }
type publicationEntry struct {
	ReferenceBytes  int64                  `json:"-"`
	Generation      bool                   `json:"-"`
	Refs            []publicationRecordRef `json:"-"`
	Published       bool                   `json:"-"`
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
	s.publicationWriteMu.Lock()
	defer s.publicationWriteMu.Unlock()
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	if err := s.requireWritable("configure publication retention"); err != nil {
		return err
	}
	merged := append([]PublicationSource(nil), s.publicationSources...)
	for _, x := range sources {
		if encfield.HasEncryptedFields(strings.TrimSuffix(x.Schema, ".fbs")) {
			return errors.New("publication retention refuses standards with sealed fields")
		}
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

	if len(s.publicationEntries) >= maxPublicationEntries {
		return nil, nil, errors.New("publication journal capacity reached; complete and retire retained history before ingestion")
	}
	pending := map[string]bool{}
	indexed := 0
	var referenceBytes int64
	for _, old := range s.publicationEntries {
		indexed += len(old.Refs)
		referenceBytes += old.ReferenceBytes
		if !old.Published && old.Schema == schema && old.Tags.ProviderID == tags.ProviderID && old.Tags.SourceName == tags.SourceName {
			pending[old.Tags.BatchID] = true
		}
	}
	if !pending[tags.BatchID] && len(pending) >= 4096 {
		return nil, nil, errors.New("retained publication backlog exceeds 4096 batches; stop ingestion and complete publication")
	}
	if indexed+len(records) > 200000 {
		return nil, nil, errors.New("publication index exceeds 200000 records; drain and retire retained history")
	}
	for _, data := range records {
		referenceBytes += publicationReferenceBytes(publicationRecordRef{CID: strings.Repeat("x", 59), FramedCID: strings.Repeat("x", 59), Identity: recordSupersedeKey(schema, data), Path: filepath.Join(s.publicationDir(), "00000000000000000000", "records.sds"), Tags: tags})
		if referenceBytes > maxPublicationReferenceBytes {
			return nil, nil, errors.New("publication reference index exceeds 128 MiB; drain and retire retained history")
		}
		if len(data) > maxPublicationRecordBytes {
			return nil, nil, errors.New("publication record exceeds 16 MiB")
		}
	}
	seq := uint64(1)
	if n := len(s.publicationEntries); n > 0 {
		seq = s.publicationEntries[n-1].Sequence + 1
	}
	e := publicationEntry{Sequence: seq, Schema: schema, Tags: tags, Peer: peer, Signature: sig, Identity: opt.identity, OwnTS: opt.ownTS}
	if err := s.publicationStep("prepare"); err != nil {
		return nil, nil, err
	}
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
		if err = s.publicationStep("write"); err != nil {
			break
		}
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
		err = s.publicationStep("data-sync")
		if err == nil {
			err = f.Sync()
		}
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
	if err = s.publicationStep("metadata"); err != nil {
		return nil, nil, err
	}
	if err = publicationAtomicFile(filepath.Join(temp, "source.json"), raw); err != nil {
		return nil, nil, err
	}
	if err = publicationAtomicFile(filepath.Join(temp, "entry.json"), raw); err != nil {
		return nil, nil, err
	}
	if err = s.publicationStep("rename"); err != nil {
		return nil, nil, err
	}
	if err = os.Rename(temp, s.publicationEntryDir(seq)); err != nil {
		return nil, nil, err
	}
	s.publicationMu.Lock()
	s.publicationEntries = append(s.publicationEntries, e)
	s.publicationMu.Unlock()
	err = s.publicationStep("directory-sync")
	if err == nil {
		err = publicationSyncDir(s.publicationDir())
	}
	if err != nil {
		s.pausePublication(e, err)
		return nil, nil, err
	}
	if s.publicationHook != nil {
		s.publicationHook("prepared")
	}
	var cids []string
	var out []format4.Outcome
	writeErr := s.publicationStep("engine")
	if writeErr == nil {
		cids, out, writeErr = b.f4PutDirect(op, schema, records, peer, sig, &tags, opt)
	}
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
	if writeErr == nil && schema == "CAT.fbs" && strings.HasSuffix(tags.BatchID, ":complete") {
		_, writeErr = s.completeCatalogGenerationLocked(tags.ProviderID, tags.SourceName)
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
	refs, err := s.publicationReferences(e)
	if err != nil {
		return err
	}
	e.Refs = refs
	e.ReferenceBytes = publicationReferencesBytes(refs)
	total, count := e.ReferenceBytes, len(refs)
	for _, old := range s.publicationEntries {
		if old.Sequence != e.Sequence {
			total += old.ReferenceBytes
			count += len(old.Refs)
		}
	}
	if total > maxPublicationReferenceBytes || count > 200000 {
		return errors.New("publication recovery exceeds reference index capacity")
	}
	if len(refs) == 0 {
		if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "published"), []byte("1\n")); err != nil {
			return err
		}
		e.Published = true
	}
	s.publicationMu.Lock()
	for i := range s.publicationEntries {
		if s.publicationEntries[i].Sequence == e.Sequence {
			s.publicationEntries[i].Refused = e.Refused
			s.publicationEntries[i].OutcomeKnown = true
		}
	}
	s.publicationMu.Unlock()
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
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
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
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
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
			damaged := err
			scopeRaw, scopeErr := os.ReadFile(filepath.Join(s.publicationDir(), dir.Name(), "source.json"))
			seq, seqErr := strconv.ParseUint(dir.Name(), 10, 64)
			if scopeErr == nil && json.Unmarshal(scopeRaw, &e) == nil && seqErr == nil {
				e.Sequence = seq
				e.Applied = false
				s.publicationEntries = append(s.publicationEntries, e)
				s.pausePublication(e, damaged)
				continue
			}
			if seqErr == nil {
				s.publicationEntries = append(s.publicationEntries, publicationEntry{Sequence: seq})
			}
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
			e.Refs, err = s.publicationReferences(e)
			if err == nil {
				e.ReferenceBytes = publicationReferencesBytes(e.Refs)
				total := e.ReferenceBytes
				count := len(e.Refs)
				for _, old := range s.publicationEntries {
					total += old.ReferenceBytes
					count += len(old.Refs)
				}
				if total > maxPublicationReferenceBytes || count > 200000 {
					err = errors.New("publication recovery exceeds reference index capacity")
					e.Refs = nil
				}
			}
		} // Applied streams are checked too.
		if err != nil {
			s.pausePublication(e, err)
			continue
		}
		_, completedErr := os.Stat(filepath.Join(s.publicationEntryDir(e.Sequence), "published"))
		e.Published = completedErr == nil
		_, generationErr := os.Stat(filepath.Join(s.publicationEntryDir(e.Sequence), "generation"))
		e.Generation = generationErr == nil
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
	s.retryCatalogGenerationsLocked()
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
		if !e.Applied || e.Published || s.publicationFailures[publicationSourceKey{e.Schema, e.Tags.ProviderID, e.Tags.SourceName}] != nil {
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
	store                           *FlatSQLStore
	Cutoff                          uint64
	Schema, Provider, Source, Batch string
	refs                            []publicationRecordRef
}

func (p *PublicationSelection) Fingerprint() (string, int) {
	h := newPublicationSetHash(p.Schema, p.Provider, p.Source, p.Batch)
	for _, r := range p.refs {
		h.Write([]byte(r.CID))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), len(p.refs)
}

// Record hydrates one selected record, bounded by the journal payload limit.
func (p *PublicationSelection) Record(index int) (DatasetExportRecord, error) {
	if index < 0 || index >= len(p.refs) {
		return DatasetExportRecord{}, errors.New("publication record index out of bounds")
	}
	r := p.refs[index]
	data, err := p.readReference(r)
	return DatasetExportRecord{CID: r.CID, Data: data, SourceTags: r.Tags}, err
}
func (p *PublicationSelection) readReference(r publicationRecordRef) ([]byte, error) {
	data, err := r.read()
	if err != nil && p.store != nil {
		p.store.pausePublication(publicationEntry{Schema: p.Schema, Tags: r.Tags}, err)
	}
	return data, err
}

func (s *FlatSQLStore) retainedSelectionLocked(schema, provider, source, batch string, cutoff uint64) (*PublicationSelection, error) {
	if err := s.publicationFailures[publicationSourceKey{schema, provider, source}]; err != nil {
		return nil, fmt.Errorf("publication source paused: %w", err)
	}
	p := &PublicationSelection{Schema: schema, Provider: provider, Source: source, Batch: batch}
	byKey := map[string]publicationRecordRef{}
	terminalComplete := false
	for _, e := range s.publicationEntries {
		if !e.Applied || e.Sequence > cutoff || e.Schema != schema || e.Tags.ProviderID != provider || e.Tags.SourceName != source || (batch != "" && e.Tags.BatchID != batch) {
			continue
		}
		p.Cutoff = e.Sequence
		terminalComplete = e.Generation
		for _, r := range e.Refs {
			key := r.CID
			if schema == "CAT.fbs" && batch == "" && r.Identity != "" {
				key = r.Identity
			}
			byKey[key] = r
		}
		if len(byKey) > maxPublicationSelectionRecords {
			return nil, errors.New("publication selection exceeds 100000 records; partition the source")
		}
	}
	if schema == "CAT.fbs" && strings.HasSuffix(batch, ":complete") && !terminalComplete {
		return nil, errors.New("terminal catalog batch has no completed generation")
	}
	excluded, err := s.catalogExclusions(schema, provider, source)
	if err != nil {
		return nil, err
	}
	for _, r := range byKey {
		if batch == "" && excluded[r.CID] {
			continue
		}
		p.refs = append(p.refs, r)
	}
	sort.Slice(p.refs, func(i, j int) bool { return p.refs[i].CID < p.refs[j].CID })
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
	s.publicationWriteMu.Lock()
	defer s.publicationWriteMu.Unlock()
	resolved := map[publicationSourceKey]bool{}
	failed := map[publicationSourceKey]bool{}
	for _, e := range s.publicationEntries {
		if e.Applied {
			continue
		}
		key := publicationSourceKey{e.Schema, e.Tags.ProviderID, e.Tags.SourceName}
		raw, err := os.ReadFile(filepath.Join(s.publicationEntryDir(e.Sequence), "entry.json"))
		var saved publicationEntry
		if err == nil {
			err = json.Unmarshal(raw, &saved)
		}
		if err == nil && (saved.Sequence != e.Sequence || saved.Schema != e.Schema || saved.Tags != e.Tags) {
			err = errors.New("publication entry scope mismatch")
		}
		var records []DatasetExportRecord
		if err == nil {
			records, err = s.publicationRecords(e)
		}
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
			failed[key] = true
			s.pausePublication(e, err)
		} else {
			resolved[key] = true
		}
	}
	s.publicationMu.Lock()
	for key := range resolved {
		if !failed[key] {
			delete(s.publicationFailures, key)
		}
	}
	s.publicationMu.Unlock()
	s.retryCatalogGenerationsLocked()
}

// CompletePublication records successful batch publication independently of
// series retention. A continued batch only completes through its captured cutoff.
func (s *FlatSQLStore) CompletePublication(p *PublicationSelection) error {
	if p == nil || p.Batch == "" {
		return nil
	}
	s.publicationWriteMu.Lock()
	defer s.publicationWriteMu.Unlock()
	for i, e := range s.publicationEntries {
		if e.Applied && !e.Published && e.Sequence <= p.Cutoff && e.Schema == p.Schema && e.Tags.ProviderID == p.Provider && e.Tags.SourceName == p.Source && e.Tags.BatchID == p.Batch {
			if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "published"), []byte("1\n")); err != nil {
				return err
			}
			s.publicationMu.Lock()
			s.publicationEntries[i].Published = true
			s.publicationMu.Unlock()
		}
	}
	return nil
}

// RetainsPublicationSource distinguishes retained recovery from live progress.
func (s *FlatSQLStore) RetainsPublicationSource(schema, provider, source string) bool {
	return s.publicationSourceEnabled(schema, provider, source)
}
func (s *FlatSQLStore) PublicationBatchComplete(schema, provider, source, batch string) bool {
	if batch == "" {
		return false
	}
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	found := false
	for _, e := range s.publicationEntries {
		if e.Schema == schema && e.Tags.ProviderID == provider && e.Tags.SourceName == source && e.Tags.BatchID == batch {
			found = true
			if !e.Applied || !e.Published {
				return false
			}
		}
	}
	return found
}

func (s *FlatSQLStore) publicationStep(name string) error {
	if s.publicationFault != nil {
		return s.publicationFault(name)
	}
	return nil
}
