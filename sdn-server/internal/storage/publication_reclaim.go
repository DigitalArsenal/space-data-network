package storage

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PublicationJournalStats counts resident payloads, references and reclaimed entries.
type PublicationJournalStats struct {
	Entries, Records, Pins       int
	ReferenceBytes, PayloadBytes int64
	Reclaimed                    uint64
	Skipped                      uint64
	LastSkip                     string
}

func (s *FlatSQLStore) PublicationJournalStats() PublicationJournalStats {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	out := PublicationJournalStats{Entries: len(s.publicationEntries), Reclaimed: s.publicationReclaimed, Skipped: s.publicationReclaimSkips, LastSkip: s.publicationReclaimReason}
	out.Records = s.publicationRecordCount
	out.ReferenceBytes = s.publicationReferenceCountBytes
	out.PayloadBytes = s.publicationPayloadCountBytes
	for _, n := range s.publicationPins {
		out.Pins += n
	}
	return out
}
func (s *FlatSQLStore) pinPublicationEntriesLocked(entries []publicationEntry) func() {
	if s.publicationPins == nil {
		s.publicationPins = map[string]int{}
	}
	paths := map[string]bool{}
	for _, e := range entries {
		if e.Sequence != 0 {
			paths[filepath.Join(s.publicationEntryDir(e.Sequence), "records.sds")] = true
		} else {
			for _, r := range e.Refs {
				paths[r.Path()] = true
			}
		}
	}
	for path := range paths {
		s.publicationPins[path]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.publicationMu.Lock()
			defer s.publicationMu.Unlock()
			for path := range paths {
				s.publicationPins[path]--
				if s.publicationPins[path] == 0 {
					delete(s.publicationPins, path)
				}
			}
		})
	}
}
func (s *FlatSQLStore) publicationReceiptPath(schema, provider, source, batch string) string {
	sum := sha256.Sum256([]byte(schema + "\x00" + provider + "\x00" + source + "\x00" + batch))
	return filepath.Join(s.publicationDir(), fmt.Sprintf("receipt-%x", sum))
}
func (s *FlatSQLStore) publicationReceiptExists(schema, provider, source, batch string) bool {
	id := sha256.Sum256([]byte(schema + "\x00" + provider + "\x00" + source + "\x00" + batch))
	if s.publicationReceipts[id] != 0 {
		return true
	}
	_, err := os.Stat(s.publicationReceiptPath(schema, provider, source, batch))
	return err == nil
}

type publicationReclaimCheckpoint struct {
	HighWater, Reclaimed uint64
	Pending              []uint64
}

func (s *FlatSQLStore) recoverPublicationReclamation() error {
	if err := s.recoverPublicationReceipts(); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(s.publicationDir(), "reclamation.fb"))
	if err == nil {
		var c publicationReclaimCheckpoint
		if err = unmarshalPublication(raw, &c); err != nil {
			// The independent watermarks and receipt checkpoint preserve allocation
			// and completion; this sweep plan is derived metadata.
			c = publicationReclaimCheckpoint{HighWater: s.publicationHighWater, Reclaimed: uint64(len(s.publicationReceipts))}
			log.Warn("rebuilt damaged publication reclamation plan")
		}
		s.publicationHighWater = max(s.publicationHighWater, c.HighWater)
		reclaimed := c.Reclaimed
		for _, seq := range c.Pending {
			if _, statErr := os.Stat(s.publicationEntryDir(seq)); os.IsNotExist(statErr) {
				reclaimed++
			} else if statErr != nil {
				return statErr
			}
		}
		c.Reclaimed = reclaimed
		c.Pending = nil
		settled, _ := marshalPublication(c)
		if err = publicationAtomicFile(filepath.Join(s.publicationDir(), "reclamation.fb"), settled); err != nil {
			return err
		}
		s.publicationMu.Lock()
		s.publicationReclaimed = reclaimed
		s.publicationMu.Unlock()
	} else if !os.IsNotExist(err) {
		return err
	}
	dirs, err := os.ReadDir(s.publicationDir())
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if strings.HasPrefix(d.Name(), ".reclaimed-") {
			if err = os.RemoveAll(filepath.Join(s.publicationDir(), d.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReclaimPublication removes completed payloads outside the latest source view,
// last complete catalog generation, pending closure, and active selections.
func (s *FlatSQLStore) ReclaimPublication() error {
	return s.reclaimPublicationLocked()
}
func (s *FlatSQLStore) reclaimPublicationLocked() (result error) {
	defer func() {
		if result != nil {
			s.noteReclamationSkip(result.Error())
		}
	}()
	v := s.publicationView()
	defer v.publicationRelease()
	keep := map[string]bool{}
	blocked := map[publicationSourceKey]bool{}
	key := func(e publicationEntry) publicationSourceKey {
		return publicationSourceKey{e.Schema, e.Tags.ProviderID, e.Tags.SourceName}
	}
	block := func(x PublicationSource, err error) {
		blocked[publicationSourceKey{x.Schema, x.Provider, x.Source}] = true
		if x.ProductSource != "" {
			blocked[publicationSourceKey{"OCM.fbs", x.Provider, x.ProductSource}] = true
		}
		s.noteReclamationSkip(err.Error())
	}
	add := func(p *PublicationSelection) {
		for _, r := range p.refs {
			keep[r.Path()] = true
		}
	}
	for _, x := range v.publicationSources {
		if err := v.publicationFailures[publicationSourceKey{x.Schema, x.Provider, x.Source}]; err != nil {
			if pair, ok := v.catalogSource(x.Schema, x.Provider, x.Source); ok {
				block(pair, err)
			} else {
				block(x, err)
			}
			continue
		}
		if x.Schema != "CAT.fbs" {
			if _, paired := v.catalogSource(x.Schema, x.Provider, x.Source); !paired {
				block(x, errors.New("unpaired product retention requires an index"))
			}
			continue
		}
		p, err := v.retainedSelectionLocked(x.Schema, x.Provider, x.Source, "", v.publicationCutoffLocked())
		if err != nil {
			block(x, err)
			continue
		}
		add(p)
		if x.ProductSource != "" {
			cutoff := v.latestCatalogGeneration(x)
			keep[filepath.Join(v.publicationEntryDir(cutoff), "records.sds")] = true
			if cutoff != 0 {
				p, err = v.retainedSelectionLocked(x.Schema, x.Provider, x.Source, "", cutoff)
				if err != nil {
					block(x, err)
					continue
				}
				add(p)
			}
			for _, e := range v.publicationEntries {
				if !e.Aborted && e.Schema == "OCM.fbs" && e.Tags.ProviderID == x.Provider && e.Tags.SourceName == x.ProductSource && e.Sequence > cutoff {
					keep[filepath.Join(v.publicationEntryDir(e.Sequence), "records.sds")] = true
				}
			}
		}
	}
	if s.publicationHook != nil {
		s.publicationHook("reclamation-captured")
	}
	s.publicationWriteMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.publicationWriteMu.Unlock()
		}
	}()
	// Append-only changes do not invalidate the pinned view. Preserve every newer
	// entry and its closure; only entries complete in the captured view can retire.
	captured := map[uint64]publicationEntry{}
	for _, e := range v.publicationEntries {
		captured[e.Sequence] = e
	}
	s.publicationMu.Lock()
	wanted := map[string]map[[32]byte]bool{}
	for _, e := range s.publicationEntries {
		path := filepath.Join(s.publicationEntryDir(e.Sequence), "records.sds")
		old, exists := captured[e.Sequence]
		if !e.Aborted && (!exists || !old.Applied || !old.Published) {
			keep[path] = true
		}
		if e.Aborted || e.Schema != "CAT.fbs" || !e.Applied || (!keep[path] && e.Published) {
			continue
		}
		if len(e.Refs) == 0 {
			continue
		}
		f := e.Refs[0].file
		if f.ProductsError != nil {
			if x, ok := v.catalogSource(e.Schema, e.Tags.ProviderID, e.Tags.SourceName); ok {
				blocked[publicationSourceKey{x.Schema, x.Provider, x.Source}] = true
				blocked[publicationSourceKey{"OCM.fbs", x.Provider, x.ProductSource}] = true
			}
			s.publicationReclaimSkips++
			s.publicationReclaimReason = f.ProductsError.Error()
			log.Warnf("publication reclamation source skipped: %v", f.ProductsError)
			continue
		}
		ids := wanted[e.Tags.ProviderID]
		if ids == nil {
			ids = map[[32]byte]bool{}
			wanted[e.Tags.ProviderID] = ids
		}
		for _, id := range f.Products {
			ids[id] = true
		}
	}
	for _, e := range s.publicationEntries {
		if e.Schema == "OCM.fbs" {
			for _, r := range e.Refs {
				if wanted[e.Tags.ProviderID][r.Digest] || wanted[e.Tags.ProviderID][r.FramedDigest] {
					keep[r.Path()] = true
					break
				}
			}
		}
	}
	var candidates []publicationEntry
	for _, e := range s.publicationEntries {
		path := filepath.Join(s.publicationEntryDir(e.Sequence), "records.sds")
		old, exists := captured[e.Sequence]
		if exists && (old.Aborted || old.Applied && old.Published) && (e.Aborted || e.Applied && e.Published) && !blocked[key(e)] && !keep[path] && s.publicationPins[path] <= 1 {
			candidates = append(candidates, e)
		}
	}
	plan := publicationReclaimCheckpoint{HighWater: s.publicationHighWater, Reclaimed: s.publicationReclaimed}
	s.publicationMu.Unlock()
	if len(candidates) == 0 {
		return nil
	}
	// Limit serialized metadata I/O per pass; reconciliation continues the sweep.
	if len(candidates) > 64 {
		candidates = candidates[:64]
		s.noteReclamationSkip("reclamation pass limited to 64 entries; remaining entries retained")
	}
	for _, e := range candidates {
		plan.Pending = append(plan.Pending, e.Sequence)
	}
	checkpoint, _ := marshalPublication(plan)
	if err := publicationAtomicFile(filepath.Join(s.publicationDir(), "reclamation.fb"), checkpoint); err != nil {
		return err
	}
	var tombs []string
	for _, e := range candidates {
		if err := s.savePublicationReceipt(e); err != nil {
			return err
		}
		if err := s.publicationStep("reclaim-receipt"); err != nil {
			return err
		}
		dir := s.publicationEntryDir(e.Sequence)
		tomb := filepath.Join(s.publicationDir(), ".reclaimed-"+filepath.Base(dir))
		s.publicationMu.Lock()
		// A capture acquired during the durable receipt write keeps this entry.
		if s.publicationPins[filepath.Join(dir, "records.sds")] > 1 {
			s.publicationMu.Unlock()
			continue
		}
		if err := os.Rename(dir, tomb); err != nil {
			s.publicationMu.Unlock()
			return err
		}
		for i, old := range s.publicationEntries {
			if old.Sequence == e.Sequence {
				s.indexPublicationTags(old, -1)
				s.publicationEntries = append(s.publicationEntries[:i], s.publicationEntries[i+1:]...)
				break
			}
		}
		s.publicationReclaimed++
		s.publicationMu.Unlock()
		if err := publicationSyncDir(s.publicationDir()); err != nil {
			return err
		}
		if err := s.publicationStep("reclaim-renamed"); err != nil {
			return err
		}
		tombs = append(tombs, tomb)
	}
	s.publicationWriteMu.Unlock()
	locked = false
	for _, tomb := range tombs {
		if err := os.RemoveAll(tomb); err != nil {
			return err
		}
	}
	return publicationSyncDir(s.publicationDir())
}
func (s *FlatSQLStore) noteReclamationSkip(reason string) {
	s.publicationMu.Lock()
	s.publicationReclaimSkips++
	s.publicationReclaimReason = reason
	s.publicationMu.Unlock()
	log.Warnf("publication reclamation source skipped: %s", reason)
}

func catalogProductCID(cat *CAT.CAT, i int) (string, error) {
	var p CAT.CATProduct
	if !cat.PRODUCTS(&p, i) {
		return "", errors.New("invalid catalog product")
	}
	return string(p.RECORD_CID()), nil
}
