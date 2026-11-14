package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func publicationGenerationLimit(x PublicationSource) int {
	n := x.CatalogObjectLimit
	if n == 0 {
		n = maxPublicationSelectionRecords
	}
	return min(maxPublicationSelectionRecords, n+(n+3)/4)
}

// Four generations per source include 25% object headroom (capped by the
// selection bound). Pending batches retain their bytes; completed history
// coalesces to the latest generation through reclamation.
func (s *FlatSQLStore) publicationRecordLimit() int {
	n := 0
	for _, x := range s.publicationSources {
		n += 4 * publicationGenerationLimit(x)
	}
	if n == 0 {
		n = 2 * maxPublicationSelectionRecords
	}
	return n
}

func (s *FlatSQLStore) generationUsage(x PublicationSource) (cutoff uint64, products, indexes int) {
	cutoff = s.latestCatalogGeneration(x)
	for _, e := range s.publicationEntries {
		if e.Aborted || e.Sequence <= cutoff || e.Tags.ProviderID != x.Provider {
			continue
		}
		if e.Schema == "CAT.fbs" && e.Tags.SourceName == x.Source {
			indexes += len(e.Refs)
		}
		if e.Schema == "OCM.fbs" && e.Tags.SourceName == x.ProductSource {
			products += len(e.Refs)
		}
	}
	return
}

// Admission reserves both halves before the first product. The reservation is
// reconstructed from durable entries, so restart cannot spend its index space.
func (s *FlatSQLStore) admitPublicationGeneration(schema string, tags SourceTags, count int) error {
	x, paired := s.catalogSource(schema, tags.ProviderID, tags.SourceName)
	if !paired {
		return nil
	}
	_, products, indexes := s.generationUsage(x)
	limit := publicationGenerationLimit(x)
	used := products
	if schema == "CAT.fbs" {
		used = indexes
	}
	refusal := func(reason string) error {
		if err := s.abortPublicationGeneration(x); err != nil {
			return err
		}
		return fmt.Errorf("catalog_object_limit=%d (generation allowance=%d): %s; retained use=%d references, %d bytes; generation admission canceled", x.CatalogObjectLimit, limit, reason, s.publicationRecordCount, s.publicationReferenceCountBytes)
	}
	if used+count > limit {
		return refusal(fmt.Sprintf("generation use=%d plus request=%d exceeds allowance", used, count))
	}
	reserved := 0
	for _, pair := range s.publicationSources {
		if pair.ProductSource == "" {
			continue
		}
		_, p, c := s.generationUsage(pair)
		if p+c > 0 || pair.Provider == x.Provider && pair.Source == x.Source {
			reserved += max(0, 2*publicationGenerationLimit(pair)-p-c)
		}
	}
	if s.publicationRecordCount+reserved > s.publicationRecordLimit() {
		return refusal(fmt.Sprintf("generation reservation=%d exceeds reference capacity=%d", reserved, s.publicationRecordLimit()))
	}
	if s.publicationReferenceCountBytes+int64(reserved)*256 > s.publicationByteLimit() {
		return refusal(fmt.Sprintf("generation reservation exceeds byte capacity=%d", s.publicationByteLimit()))
	}
	return nil
}

// The abort plan precedes every discard marker. Recovery finishes an interrupted
// cancellation before reading entries. Live records remain available; snapshots
// and announcements expose only admitted, complete generations.
func (s *FlatSQLStore) abortPublicationGeneration(x PublicationSource) error {
	cutoff := s.latestCatalogGeneration(x)
	var sequences []uint64
	for _, e := range s.publicationEntries {
		if e.Sequence > cutoff && !e.Aborted && e.Tags.ProviderID == x.Provider && ((e.Schema == "CAT.fbs" && e.Tags.SourceName == x.Source) || (e.Schema == "OCM.fbs" && e.Tags.SourceName == x.ProductSource)) {
			sequences = append(sequences, e.Sequence)
		}
	}
	if len(sequences) == 0 {
		return nil
	}
	raw, _ := json.Marshal(sequences)
	if err := publicationAtomicFile(filepath.Join(s.publicationDir(), "generation-abort.json"), raw); err != nil {
		return err
	}
	if err := s.recoverPublicationGenerationAbort(); err != nil {
		return err
	}
	selected := map[uint64]bool{}
	for _, seq := range sequences {
		selected[seq] = true
	}
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	for i, e := range s.publicationEntries {
		if !selected[e.Sequence] {
			continue
		}
		s.indexPublicationTags(e, -1)
		e.Aborted, e.Discarded, e.Published = true, true, true
		path := filepath.Join(s.publicationEntryDir(e.Sequence), "records.sds")
		if s.publicationPins[path] == 0 {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			e.Refs, e.ReferenceBytes = nil, 0
		}
		s.publicationEntries[i] = e
		s.indexPublicationTags(e, 1)
	}
	return nil
}

func (s *FlatSQLStore) recoverPublicationGenerationAbort() error {
	path := filepath.Join(s.publicationDir(), "generation-abort.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var sequences []uint64
	if err := json.Unmarshal(raw, &sequences); err != nil {
		return err
	}
	for _, seq := range sequences {
		if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(seq), "aborted"), []byte("1\n")); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return publicationSyncDir(s.publicationDir())
}
func (s *FlatSQLStore) publicationByteLimit() int64 {
	return int64(s.publicationRecordLimit())*256 + int64(max(1, len(s.publicationSources)))*(1<<20)
}
