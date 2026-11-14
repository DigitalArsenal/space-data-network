package storage

import (
	"errors"
	"fmt"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"path/filepath"
	"sort"
	"strings"
)

// CatalogSnapshot binds a complete CAT view and its exact OCM closure to the
// same durable journal cutoff. Product-only commits leave the CAT view intact.
type CatalogSnapshot struct {
	Cutoff          uint64
	Index, Products *PublicationSelection
}

func (s *CatalogSnapshot) Close() {
	if s != nil {
		s.Index.Close()
		s.Products.Close()
	}
}

func (s *FlatSQLStore) catalogSource(schema, provider, source string) (PublicationSource, bool) {
	for _, x := range s.publicationSources {
		if x.Schema == "CAT.fbs" && x.Provider == provider && x.ProductSource != "" && ((schema == "CAT.fbs" && x.Source == source) || (schema == "OCM.fbs" && x.ProductSource == source)) {
			return x, true
		}
	}
	return PublicationSource{}, false
}
func (s *FlatSQLStore) publicationCutoffLocked() uint64 {
	var seq uint64
	for _, e := range s.publicationEntries {
		if !e.Applied {
			continue
		}
		seq = e.Sequence
	}
	return seq
}
func (s *FlatSQLStore) captureCatalogLocked(x PublicationSource, cutoff uint64) (*CatalogSnapshot, error) {
	index, err := s.retainedSelectionLocked("CAT.fbs", x.Provider, x.Source, "", cutoff)
	if err != nil {
		return nil, err
	}
	products := &PublicationSelection{Schema: "OCM.fbs", Provider: x.Provider, Source: x.ProductSource}
	if err := s.publicationFailures[publicationSourceKey{"OCM.fbs", x.Provider, x.ProductSource}]; err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, r := range index.refs {
		data, err := r.read()
		if err != nil {
			return nil, err
		}
		cat, err := parseCAT(data)
		if err != nil {
			return nil, &PublicationContentError{Cause: err}
		}
		for i := 0; i < cat.PRODUCTSLength(); i++ {
			var product CAT.CATProduct
			if !cat.PRODUCTS(&product, i) {
				return nil, &PublicationContentError{Cause: errors.New("invalid catalog product")}
			}
			wanted[string(product.RECORD_CID())] = true
		}
		if len(wanted) > maxPublicationSelectionRecords {
			return nil, publicationContentError(errors.New("catalog closure exceeds 100000 records; partition the source"))
		}
	}
	selected := map[string]publicationRecordRef{}
	for _, e := range s.publicationEntries {
		if !e.Applied || e.Sequence > cutoff || e.Schema != "OCM.fbs" || e.Tags.ProviderID != x.Provider || e.Tags.SourceName != x.ProductSource {
			continue
		}
		products.SourceCutoff = max(products.SourceCutoff, e.SourceSequence)
		for _, r := range e.Refs {
			if wanted[r.CID()] || wanted[r.FramedCID()] {
				selected[r.CID()] = r
				delete(wanted, r.CID())
				delete(wanted, r.FramedCID())
			}
		}
	}
	if len(wanted) != 0 {
		return nil, &PublicationContentError{Cause: errors.New("catalog snapshot has an unresolved product reference")}
	}
	for _, r := range selected {
		products.refs = append(products.refs, r)
	}
	sort.Slice(products.refs, func(i, j int) bool { return products.refs[i].CID() < products.refs[j].CID() })
	// Pair identities follow this index source's cutoff, never unrelated writes.
	cutoff = index.Cutoff
	products.Cutoff = cutoff
	return &CatalogSnapshot{Cutoff: cutoff, Index: index, Products: products}, nil
}

// CaptureCatalogSnapshot validates all references before returning either set.
// A cutoff of zero selects the latest completed journal transaction. Nonzero
// cutoffs make a retry reproduce the same selection after concurrent ingestion.
func (s *FlatSQLStore) CaptureCatalogSnapshot(provider, indexSource string, cutoff uint64) (*CatalogSnapshot, error) {
	original := s
	s = s.publicationView()
	defer s.publicationRelease()
	x, ok := s.catalogSource("CAT.fbs", provider, indexSource)
	if !ok {
		return nil, errors.New("catalog snapshot source is not configured")
	}
	head := s.publicationCutoffLocked()
	if cutoff == 0 {
		cutoff = s.latestCatalogGeneration(x)
		if cutoff == 0 {
			return nil, errors.New("no completed catalog generation; commit the final index batch with :complete")
		}
	}
	complete := false
	for _, e := range s.publicationEntries {
		if e.Sequence == cutoff && e.Generation && e.Schema == "CAT.fbs" && e.Tags.ProviderID == provider && e.Tags.SourceName == indexSource {
			complete = true
			break
		}
	}
	if !complete {
		return nil, errors.New("catalog cutoff is not a completed generation")
	}
	if cutoff > head {
		return nil, errors.New("catalog cutoff exceeds committed head")
	}
	snap, err := s.captureCatalogLocked(x, cutoff)
	if err == nil {
		snap.Index.pin(original)
		snap.Products.pin(original)
	}
	return snap, err
}

// SelectPublication returns nil for an ordinary source, preserving its existing
// export behavior. Retained batches and catalog snapshots use journal bytes.
func (s *FlatSQLStore) SelectPublication(schema, provider, source, batch string) (*PublicationSelection, error) {
	schema = strings.TrimSpace(schema)
	provider = strings.TrimSpace(provider)
	source = strings.TrimSpace(source)
	batch = strings.TrimSpace(batch)
	if !s.publicationEnabled.Load() || !s.publicationSourceEnabled(schema, provider, source) {
		return nil, nil
	}
	original := s
	s = s.publicationView()
	defer s.publicationRelease()
	if !s.retainsPublication(schema, provider, source) {
		return nil, nil
	}
	cutoff := s.publicationCutoffLocked()
	if batch == "" {
		if x, ok := s.catalogSource(schema, provider, source); ok {
			cutoff = s.latestCatalogGeneration(x)
			if cutoff == 0 {
				return nil, errors.New("no completed catalog generation")
			}
			snap, err := s.captureCatalogLocked(x, cutoff)
			if err != nil {
				return nil, err
			}
			snap.Index.pin(original)
			snap.Products.pin(original)
			if schema == "CAT.fbs" {
				snap.Products.Close()
				return snap.Index, nil
			}
			snap.Index.Close()
			return snap.Products, nil
		}
	}
	p, err := s.retainedSelectionLocked(schema, provider, source, batch, cutoff)
	if err == nil {
		p.pin(original)
	}
	return p, err
}

// ExportCatalogSnapshot writes both deterministic SDS shards only after closure
// validation. The caller exposes the returned pair together; an error exposes
// neither. The immutable selection survives concurrent deletion and replacement.
func (s *FlatSQLStore) ExportCatalogSnapshot(dir, provider, source string, cutoff uint64) (*CatalogSnapshot, []*DatasetExport, error) {
	snap, err := s.CaptureCatalogSnapshot(provider, source, cutoff)
	if err != nil {
		return nil, nil, err
	}
	var exports []*DatasetExport
	for _, p := range []*PublicationSelection{snap.Products, snap.Index} {
		if len(p.refs) == 0 {
			continue
		}
		ex, err := s.ExportDatasetWindow(filepath.Join(dir, p.Schema), IndexedRecordQuery{SchemaName: p.Schema, ProviderID: p.Provider, SourceName: p.Source, Selection: p})
		if err != nil {
			return nil, nil, err
		}
		exports = append(exports, ex)
	}
	return snap, exports, nil
}
func (p *PublicationSelection) window(q IndexedRecordQuery) ([]DatasetExportRecord, error) {
	if q.SchemaName != p.Schema || q.ProviderID != p.Provider || q.SourceName != p.Source || q.BatchID != p.Batch {
		return nil, errors.New("publication selection scope mismatch")
	}
	if q.Day != "" || q.NoradCatID != nil || q.EntityID != "" || q.ObjectType != "" || q.OpsStatusCode != "" || q.ActivePayloads || q.CAReadyResidentSet || q.From != nil || q.To != nil {
		return nil, errors.New("retained publication accepts source/batch filters only")
	}
	if q.Offset < 0 {
		return nil, fmt.Errorf("negative publication offset")
	}
	start := min(q.Offset, len(p.refs))
	end := len(p.refs)
	if q.Limit > 0 {
		end = start + min(q.Limit, end-start)
	}
	out := make([]DatasetExportRecord, 0, end-start)
	for _, r := range p.refs[start:end] {
		out = append(out, DatasetExportRecord{CID: r.CID(), SourceTags: r.Tags(), load: func() ([]byte, error) { return p.readReference(r) }})
	}
	return out, nil
}
func (p *PublicationSelection) ByteWindow(q IndexedRecordQuery, budget int64) (int, bool, error) {
	if _, err := p.window(q); err != nil {
		return 0, false, err
	}
	start := min(q.Offset, len(p.refs))
	end := len(p.refs)
	if q.Limit > 0 {
		end = min(end, start+q.Limit)
	}
	rows := p.refs[start:end]
	var bytes int64
	for i, r := range rows {
		bytes += int64(4 + r.Size)
		if bytes > budget && i > 0 {
			return i, true, nil
		}
	}

	return len(rows), false, nil
}

// CatalogProductSource identifies the configured product closure of a CAT source.
func (s *FlatSQLStore) CatalogProductSource(provider, source string) (string, bool) {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	x, ok := s.catalogSource("CAT.fbs", provider, source)
	return x.ProductSource, ok
}

func (s *FlatSQLStore) latestCatalogGeneration(x PublicationSource) uint64 {
	var cutoff uint64
	for _, e := range s.publicationEntries {
		if e.Applied && e.Generation && e.Schema == "CAT.fbs" && e.Tags.ProviderID == x.Provider && e.Tags.SourceName == x.Source {
			cutoff = e.Sequence
		}
	}
	return cutoff
}

// CompleteCatalogGeneration is the producer's explicit commit after every
// product and index chunk is durable. A terminal index batch ending :complete
// invokes the same operation. Uncommitted writes leave the last generation visible.
func (s *FlatSQLStore) CompleteCatalogGeneration(provider, source string) (uint64, error) {
	return s.completeCatalogGenerationLocked(provider, source)
}
func (s *FlatSQLStore) completeCatalogGenerationLocked(provider, source string) (uint64, error) {
	return s.completeCatalogGenerationAtLocked(provider, source, 0)
}
func (s *FlatSQLStore) completeCatalogGenerationAtLocked(provider, source string, cutoff uint64) (uint64, error) {
	v := s.publicationView()
	defer v.publicationRelease()
	x, ok := v.catalogSource("CAT.fbs", provider, source)
	if !ok {
		return 0, errors.New("catalog_product_source is not configured")
	}
	if cutoff == 0 {
		cutoff = v.publicationCutoffLocked()
	}
	var terminal publicationEntry
	for _, e := range v.publicationEntries {
		if e.Applied && e.Sequence <= cutoff && e.Schema == "CAT.fbs" && e.Tags.ProviderID == provider && e.Tags.SourceName == source {
			terminal = e
		}
	}
	if terminal.Sequence == 0 {
		return 0, errors.New("catalog generation has no index records")
	}
	if terminal.Generation {
		return terminal.Sequence, nil
	}
	if terminal.GenerationFailure.Attempts >= 3 {
		return 0, fmt.Errorf("catalog generation quarantined: %s", terminal.GenerationFailure.Reason)
	}
	snap, err := v.captureCatalogLocked(x, terminal.Sequence)
	if err != nil {
		s.publicationWriteMu.Lock()
		defer s.publicationWriteMu.Unlock()
		if saveErr := s.recordGenerationFailure(terminal, err); saveErr != nil {
			return 0, saveErr
		}
		return 0, err
	}
	if snap.Cutoff == 0 {
		return 0, errors.New("catalog generation has no index records")
	}
	if s.publicationHook != nil {
		s.publicationHook("generation-captured")
	}
	s.publicationWriteMu.Lock()
	defer s.publicationWriteMu.Unlock()
	if err = s.publicationStep("generation"); err != nil {
		return 0, err
	}
	if err = publicationAtomicFile(filepath.Join(s.publicationEntryDir(snap.Cutoff), "generation"), []byte("1\n")); err != nil {
		return 0, err
	}
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	for i, e := range s.publicationEntries {
		if e.Sequence == snap.Cutoff {
			s.publicationEntries[i].Generation = true
		}
	}
	return snap.Cutoff, nil
}

func (s *FlatSQLStore) retryCatalogGenerationsLocked() {
	v := s.publicationView()
	defer v.publicationRelease()
	for _, e := range v.publicationEntries {
		_, paired := v.catalogSource(e.Schema, e.Tags.ProviderID, e.Tags.SourceName)
		if paired && e.Applied && !e.Published && !e.Generation && e.GenerationFailure.Attempts < 3 && e.Schema == "CAT.fbs" && strings.HasSuffix(e.Tags.BatchID, ":complete") {
			if _, err := s.completeCatalogGenerationAtLocked(e.Tags.ProviderID, e.Tags.SourceName, e.Sequence); err != nil {
				log.Warnf("catalog generation remains incomplete: %v", err)
			}
		}
	}
}
