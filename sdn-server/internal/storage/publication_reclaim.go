package storage

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// PublicationJournalStats counts resident payloads, references and reclaimed entries.
type PublicationJournalStats struct {
	Entries, Records, Pins       int
	ReferenceBytes, PayloadBytes int64
	Reclaimed                    uint64
}

func (s *FlatSQLStore) PublicationJournalStats() PublicationJournalStats {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	out := PublicationJournalStats{Entries: len(s.publicationEntries), Reclaimed: s.publicationReclaimed}
	for _, e := range s.publicationEntries {
		out.Records += len(e.Refs)
		out.ReferenceBytes += e.ReferenceBytes
		for _, r := range e.Refs {
			out.PayloadBytes += int64(r.Size) + 4
		}
	}
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
		for _, r := range e.Refs {
			paths[r.Path()] = true
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
	_, err := os.Stat(s.publicationReceiptPath(schema, provider, source, batch))
	return err == nil
}

type publicationReclaimCheckpoint struct {
	HighWater, Reclaimed uint64
	Pending              []uint64
}

func (s *FlatSQLStore) recoverPublicationReclamation() error {
	raw, err := os.ReadFile(filepath.Join(s.publicationDir(), "reclamation.json"))
	if err == nil {
		var c publicationReclaimCheckpoint
		if err = json.Unmarshal(raw, &c); err != nil {
			return err
		}
		s.publicationHighWater = c.HighWater
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
		settled, _ := json.Marshal(c)
		if err = publicationAtomicFile(filepath.Join(s.publicationDir(), "reclamation.json"), settled); err != nil {
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
func (s *FlatSQLStore) reclaimPublicationLocked() error {
	v := s.publicationView()
	defer v.publicationRelease()
	// Pin an immutable view for all closure reads without holding the write lock.
	keep := map[string]bool{}
	add := func(p *PublicationSelection) {
		for _, r := range p.refs {
			keep[r.Path()] = true
		}
	}
	for _, x := range v.publicationSources {
		if v.publicationFailures[publicationSourceKey{x.Schema, x.Provider, x.Source}] != nil {
			return nil
		}
		if x.Schema == "CAT.fbs" {
			p, err := v.retainedSelectionLocked(x.Schema, x.Provider, x.Source, "", v.publicationCutoffLocked())
			if err != nil {
				return err
			}
			add(p)
			if x.ProductSource != "" {
				cutoff := v.latestCatalogGeneration(x)
				keep[filepath.Join(v.publicationEntryDir(cutoff), "records.sds")] = true
				for _, e := range v.publicationEntries {
					if e.Schema == "OCM.fbs" && e.Tags.ProviderID == x.Provider && e.Tags.SourceName == x.ProductSource && e.Sequence > cutoff {
						for _, r := range e.Refs {
							keep[r.Path()] = true
						}
					}
				}
				if cutoff != 0 {
					snap, err := v.captureCatalogLocked(x, cutoff)
					if err != nil {
						return err
					}
					add(snap.Index)
					add(snap.Products)
				}
			}
		} else {
			paired := false
			for _, c := range v.publicationSources {
				if c.Provider == x.Provider && c.ProductSource == x.Source {
					paired = true
				}
			}
			if !paired {
				p, err := v.retainedSelectionLocked(x.Schema, x.Provider, x.Source, "", v.publicationCutoffLocked())
				if err != nil {
					return err
				}
				add(p)
			}
		}
	}
	// Every pending or live index retains its referenced products, including an
	// incomplete generation whose products arrived before its terminal index.
	wanted := map[string]bool{}
	for _, e := range v.publicationEntries {
		if e.Schema != "CAT.fbs" || !e.Applied {
			continue
		}
		for _, r := range e.Refs {
			if !e.Published || keep[r.Path()] {
				data, err := r.read()
				if err != nil {
					return err
				}
				cat, err := parseCAT(data)
				if err != nil {
					return err
				}
				for i := 0; i < cat.PRODUCTSLength(); i++ {
					id, err := catalogProductCID(cat, i)
					if err != nil {
						return err
					}
					wanted[e.Tags.ProviderID+"\x00"+id] = true
				}
			}
		}
	}
	for _, e := range v.publicationEntries {
		if e.Schema == "OCM.fbs" {
			for _, r := range e.Refs {
				if wanted[e.Tags.ProviderID+"\x00"+r.CID()] || wanted[e.Tags.ProviderID+"\x00"+r.FramedCID()] {
					keep[r.Path()] = true
				}
			}
		}
	}
	s.publicationWriteMu.Lock()
	defer s.publicationWriteMu.Unlock()
	// If ingestion or completion changed the captured view, a later sweep retries.
	if len(s.publicationEntries) != len(v.publicationEntries) {
		return nil
	}
	for i, e := range s.publicationEntries {
		old := v.publicationEntries[i]
		if e.Sequence != old.Sequence || e.Applied != old.Applied || e.Published != old.Published || e.Generation != old.Generation || len(e.Refs) != len(old.Refs) {
			return nil
		}
	}
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	var candidates []publicationEntry
	for _, e := range s.publicationEntries {
		path := filepath.Join(s.publicationEntryDir(e.Sequence), "records.sds")
		if e.Applied && e.Published && !keep[path] && s.publicationPins[path] <= 1 {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	plan := publicationReclaimCheckpoint{HighWater: s.publicationHighWater, Reclaimed: s.publicationReclaimed}
	for _, e := range candidates {
		plan.Pending = append(plan.Pending, e.Sequence)
	}
	checkpoint, _ := json.Marshal(plan)
	if err := publicationAtomicFile(filepath.Join(s.publicationDir(), "reclamation.json"), checkpoint); err != nil {
		return err
	}
	for _, e := range candidates {
		if err := publicationAtomicFile(s.publicationReceiptPath(e.Schema, e.Tags.ProviderID, e.Tags.SourceName, e.Tags.BatchID), []byte(strconv.FormatUint(e.Sequence, 10)+"\n")); err != nil {
			return err
		}
		if err := s.publicationStep("reclaim-receipt"); err != nil {
			return err
		}
		dir := s.publicationEntryDir(e.Sequence)
		tomb := filepath.Join(s.publicationDir(), ".reclaimed-"+filepath.Base(dir))
		if err := os.Rename(dir, tomb); err != nil {
			return err
		}
		// Remove references before a caller can acquire a new pin.
		for i, old := range s.publicationEntries {
			if old.Sequence == e.Sequence {
				s.indexPublicationTags(old, -1)
				s.publicationEntries = append(s.publicationEntries[:i], s.publicationEntries[i+1:]...)
				break
			}
		}
		s.publicationReclaimed++
		if err := publicationSyncDir(s.publicationDir()); err != nil {
			return err
		}
		if err := s.publicationStep("reclaim-renamed"); err != nil {
			return err
		}
		if err := os.RemoveAll(tomb); err != nil {
			return err
		}
	}
	return publicationSyncDir(s.publicationDir())
}

func catalogProductCID(cat *CAT.CAT, i int) (string, error) {
	var p CAT.CATProduct
	if !cat.PRODUCTS(&p, i) {
		return "", errors.New("invalid catalog product")
	}
	return string(p.RECORD_CID()), nil
}
