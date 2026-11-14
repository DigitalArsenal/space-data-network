package storage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"path/filepath"
	"sort"
)

// CatalogSnapshot binds a complete CAT view and its exact OCM closure to the
// same durable journal cutoff. Product-only commits leave the CAT view intact.
type CatalogSnapshot struct {
	Cutoff          uint64
	Index, Products *PublicationSelection
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
			break
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
	products, err := s.retainedSelectionLocked("OCM.fbs", x.Provider, x.ProductSource, "", cutoff)
	if err != nil {
		return nil, err
	}
	available := map[string]DatasetExportRecord{}
	for _, r := range products.records {
		available[r.CID] = r
		// Catalog controllers identify the size-prefixed SDS record. The store
		// indexes its unframed payload, so resolve both exact byte encodings.
		framed := make([]byte, 4+len(r.Data))
		binary.LittleEndian.PutUint32(framed, uint32(len(r.Data)))
		copy(framed[4:], r.Data)
		available[computeCID(framed)] = r
	}
	selected := map[string]DatasetExportRecord{}
	for _, r := range index.records {
		cat, err := parseCAT(r.Data)
		if err != nil {
			return nil, err
		}
		for i := 0; i < cat.PRODUCTSLength(); i++ {
			var product CAT.CATProduct
			if !cat.PRODUCTS(&product, i) {
				return nil, errors.New("invalid catalog product")
			}
			id := string(product.RECORD_CID())
			p, ok := available[id]
			if !ok {
				return nil, errors.New("catalog snapshot has an unresolved product reference")
			}
			selected[id] = p
		}
	}
	products.records = nil
	for _, r := range selected {
		products.records = append(products.records, r)
	}
	sort.Slice(products.records, func(i, j int) bool { return products.records[i].CID < products.records[j].CID })
	return &CatalogSnapshot{Cutoff: cutoff, Index: index, Products: products}, nil
}

// CaptureCatalogSnapshot validates all references before returning either set.
// A cutoff of zero selects the latest completed journal transaction. Nonzero
// cutoffs make a retry reproduce the same selection after concurrent ingestion.
func (s *FlatSQLStore) CaptureCatalogSnapshot(provider, indexSource string, cutoff uint64) (*CatalogSnapshot, error) {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	x, ok := s.catalogSource("CAT.fbs", provider, indexSource)
	if !ok {
		return nil, errors.New("catalog snapshot source is not configured")
	}
	head := s.publicationCutoffLocked()
	if cutoff == 0 {
		cutoff = head
	}
	if cutoff > head {
		return nil, errors.New("catalog cutoff exceeds committed head")
	}
	return s.captureCatalogLocked(x, cutoff)
}

// SelectPublication returns nil for an ordinary source, preserving its existing
// export behavior. Retained batches and catalog snapshots use journal bytes.
func (s *FlatSQLStore) SelectPublication(schema, provider, source, batch string) (*PublicationSelection, error) {
	if !s.publicationEnabled.Load() {
		return nil, nil
	}
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	if !s.retainsPublication(schema, provider, source) {
		return nil, nil
	}
	cutoff := s.publicationCutoffLocked()
	if batch == "" {
		if x, ok := s.catalogSource(schema, provider, source); ok {
			snap, err := s.captureCatalogLocked(x, cutoff)
			if err != nil {
				return nil, err
			}
			if schema == "CAT.fbs" {
				return snap.Index, nil
			}
			return snap.Products, nil
		}
	}
	return s.retainedSelectionLocked(schema, provider, source, batch, cutoff)
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
		if len(p.records) == 0 {
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
	start := min(q.Offset, len(p.records))
	end := len(p.records)
	if q.Limit > 0 {
		end = start + min(q.Limit, end-start)
	}
	return p.records[start:end], nil
}
func (p *PublicationSelection) ByteWindow(q IndexedRecordQuery, budget int64) (int, bool, error) {
	rows, err := p.window(q)
	if err != nil {
		return 0, false, err
	}
	var bytes int64
	for i, r := range rows {
		bytes += int64(len(r.Data))
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
