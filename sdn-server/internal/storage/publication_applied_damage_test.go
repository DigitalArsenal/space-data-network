package storage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appliedDamagePut(t *testing.T, s *FlatSQLStore, schema, source, batch string, records [][]byte) {
	t.Helper()
	if _, err := s.StoreBatchWithSourceTags(schema, records, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: source, BatchID: batch}); err != nil {
		t.Fatal(err)
	}
}

func damageAppliedFile(t *testing.T, path, damage string, original []byte) {
	t.Helper()
	var err error
	switch damage {
	case "missing":
		err = os.Remove(path)
	case "truncated":
		err = os.Truncate(path, 5)
	case "checksum":
		raw := append([]byte(nil), original...)
		raw[len(raw)-1] ^= 1
		err = os.WriteFile(path, raw, 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func appliedDamageCatalog(t *testing.T, older bool) (*FlatSQLStore, string, publicationEntry) {
	t.Helper()
	dir := t.TempDir()
	s := reclamationStore(t, dir)
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index", ProductSource: "products"}, {Schema: "OCM.fbs", Provider: "synthetic", Source: "products"}, {Schema: "CAT.fbs", Provider: "synthetic", Source: "other"}}); err != nil {
		t.Fatal(err)
	}
	products, indexes := reclamationRecords(0, 2)
	appliedDamagePut(t, s, "OCM.fbs", "products", "products-0", products)
	appliedDamagePut(t, s, "CAT.fbs", "index", "chunk-a", indexes[:1])
	entry := s.publicationEntries[len(s.publicationEntries)-1]
	appliedDamagePut(t, s, "CAT.fbs", "index", "chunk-b:complete", indexes[1:])
	if older {
		products, indexes = reclamationRecords(1, 2)
		appliedDamagePut(t, s, "OCM.fbs", "products", "products-1", products[1:])
		appliedDamagePut(t, s, "CAT.fbs", "index", "new-b:complete", indexes[1:])
	}
	terminalPublish(t, s)
	return s, dir, entry
}

func requireAppliedSnapshotCount(t *testing.T, s *FlatSQLStore, count int) {
	t.Helper()
	snap, err := s.CaptureCatalogSnapshot("synthetic", "index", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if len(snap.Index.refs) != count || len(snap.Products.refs) != count {
		t.Fatalf("incomplete snapshot: index=%d products=%d want=%d", len(snap.Index.refs), len(snap.Products.refs), count)
	}
}

func TestPublicationAppliedIndexDamagePausesSnapshots(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	for _, older := range []bool{false, true} {
		for _, damage := range []string{"missing", "truncated", "checksum"} {
			t.Run(fmt.Sprintf("older=%t/%s", older, damage), func(t *testing.T) {
				s, dir, entry := appliedDamageCatalog(t, older)
				defer func() { s.Close() }()
				requireAppliedSnapshotCount(t, s, 2)
				path := filepath.Join(s.publicationEntryDir(entry.Sequence), "records.sds")
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				// Keep an exported good snapshot as the previously announced artifact.
				good, exports, err := s.ExportCatalogSnapshot(t.TempDir(), "synthetic", "index", 0)
				if err != nil || len(exports) != 2 {
					t.Fatalf("good export: %v", err)
				}
				good.Close()
				goodBytes, err := os.ReadFile(exports[1].ShardPath)
				if err != nil {
					t.Fatal(err)
				}
				cutoff := s.latestCatalogGeneration(s.publicationSources[0])
				s.Close()
				damageAppliedFile(t, path, damage, original)
				s = journalTestStore(t, dir)
				for attempt := 0; attempt < 2; attempt++ {
					if snap, err := s.CaptureCatalogSnapshot("synthetic", "index", 0); err == nil {
						snap.Close()
						t.Fatal("damaged applied chunk produced a snapshot")
					}
					if p, err := s.SelectPublication("CAT.fbs", "synthetic", "index", "chunk-a"); err == nil {
						p.Close()
						t.Fatal("damaged applied chunk produced a batch")
					}
					if snap, _, err := s.ExportCatalogSnapshot(t.TempDir(), "synthetic", "index", 0); err == nil {
						snap.Close()
						t.Fatal("damaged snapshot exported")
					}
					status := fmt.Sprint(s.PublicationRecoveryStatus())
					if !strings.Contains(status, fmt.Sprintf("entry %d", entry.Sequence)) || !strings.Contains(status, "chunk-a") {
						t.Fatalf("unreadable entry absent from status: %s", status)
					}
					if s.latestCatalogGeneration(s.publicationSources[0]) != cutoff {
						t.Fatal("lost last completed generation")
					}
					s.RetryPublicationRecovery()
				}
				stillGood, err := os.ReadFile(exports[1].ShardPath)
				if err != nil || !bytes.Equal(goodBytes, stillGood) {
					t.Fatal("last good artifact changed", err)
				}
				// Selection must defend itself even without the recovery status map.
				v := s.publicationView()
				v.publicationFailures = nil
				if _, err := v.retainedSelectionLocked("CAT.fbs", "synthetic", "index", "", cutoff); err == nil {
					t.Fatal("selection silently skipped unreadable references")
				}
				v.publicationRelease()
				_, indexes := reclamationRecords(4, 2)
				appliedDamagePut(t, s, "CAT.fbs", "other", "independent", indexes[1:])
				if p, err := s.SelectPublication("CAT.fbs", "synthetic", "other", "independent"); err != nil {
					t.Fatal("other source paused", err)
				} else {
					p.Close()
				}
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
				s.RetryPublicationRecovery()
				requireAppliedSnapshotCount(t, s, 2)
				if len(s.PublicationRecoveryStatus()) != 0 {
					t.Fatal("repair did not clear pause", s.PublicationRecoveryStatus())
				}
				// A published chunk remains explicitly discardable when unreadable.
				s.Close()
				damageAppliedFile(t, path, damage, original)
				s = journalTestStore(t, dir)
				if err := s.ManagePublicationBatch("CAT.fbs", "synthetic", "index", "chunk-a", "discard"); err != nil {
					t.Fatal(err)
				}
				s.RetryPublicationRecovery()
				requireAppliedSnapshotCount(t, s, 1)
				s.Close()
				s = journalTestStore(t, dir)
				requireAppliedSnapshotCount(t, s, 1)
				status := fmt.Sprint(s.PublicationRecoveryStatus())
				if !strings.Contains(status, "missing until the producer writes them again") {
					t.Fatalf("discard consequence absent: %s", status)
				}
				products, indexes := reclamationRecords(2, 2)
				appliedDamagePut(t, s, "OCM.fbs", "products", "replacement", products[:1])
				appliedDamagePut(t, s, "CAT.fbs", "index", "replacement:complete", indexes[:1])
				requireAppliedSnapshotCount(t, s, 2)
			})
		}
	}
}

func TestPublicationAppliedProductDamageRefusesClosure(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	for _, damage := range []string{"missing", "truncated", "checksum"} {
		t.Run(damage, func(t *testing.T) {
			s, dir, _ := appliedDamageCatalog(t, true)
			defer func() { s.Close() }()
			path := filepath.Join(s.publicationEntryDir(1), "records.sds")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			damageAppliedFile(t, path, damage, original)
			s = journalTestStore(t, dir)
			// Remove pause metadata to exercise the product closure check itself.
			v := s.publicationView()
			v.publicationFailures = nil
			snap, err := v.captureCatalogLocked(v.publicationSources[0], v.publicationCutoffLocked())
			v.publicationRelease()
			if err == nil {
				snap.Close()
				t.Fatal("missing product escaped closure validation")
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			s.RetryPublicationRecovery()
			requireAppliedSnapshotCount(t, s, 2)
		})
	}
}

func TestPublicationProductClosureRejectsMissingReferences(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s, _, _ := appliedDamageCatalog(t, true)
	defer s.Close()
	v := s.publicationView()
	defer v.publicationRelease()
	// Model the old recovery state: an applied product chunk has no references
	// and no source pause. Bypass the new readiness guard to test closure itself.
	v.publicationFailures = nil
	v.publicationEntries[0].Refs = nil
	v.publicationEntries[0].ReadReady = true
	snap, err := v.captureCatalogLocked(v.publicationSources[0], v.publicationCutoffLocked())
	if err == nil {
		snap.Close()
		t.Fatal("missing product escaped closure validation")
	}
	if !strings.Contains(err.Error(), "unresolved product reference") {
		t.Fatalf("wrong control: %v", err)
	}
}

func TestPublicationAppliedRuntimeDamageAndTerminalDiscard(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s, dir, _ := appliedDamageCatalog(t, false)
	defer func() { s.Close() }()
	entry := s.publicationEntries[len(s.publicationEntries)-1]
	if !entry.Generation {
		t.Fatal("fixture is not a terminal chunk")
	}
	path := filepath.Join(s.publicationEntryDir(entry.Sequence), "records.sds")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	damageAppliedFile(t, path, "checksum", original)
	p, err := s.SelectPublication("CAT.fbs", "synthetic", "index", entry.Tags.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := p.Record(0)
	p.Close()
	if !IsPublicationContentError(readErr) {
		t.Fatal("runtime corruption not detected", readErr)
	}
	if _, err := s.RecordPublicationFailure("CAT.fbs", "synthetic", "index", entry.Tags.BatchID, readErr); err != nil {
		t.Fatal(err)
	}
	if snap, err := s.CaptureCatalogSnapshot("synthetic", "index", 0); err == nil {
		snap.Close()
		t.Fatal("runtime corruption did not pause source")
	}
	if err := s.ManagePublicationBatch("CAT.fbs", "synthetic", "index", entry.Tags.BatchID, "requeue"); err != nil {
		t.Fatal(err)
	}
	s.RetryPublicationRecovery()
	if snap, err := s.CaptureCatalogSnapshot("synthetic", "index", 0); err == nil {
		snap.Close()
		t.Fatal("requeue bypassed corrupt bytes")
	}
	// Old quarantine receipts cannot delay recovery of an applied stream.
	if err := s.quarantineUnreadablePublication(entry, readErr); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	s.RetryPublicationRecovery()
	requireAppliedSnapshotCount(t, s, 2)
	damageAppliedFile(t, path, "missing", original)
	s.Close()
	s = journalTestStore(t, dir)
	if err := s.ManagePublicationBatch("CAT.fbs", "synthetic", "index", entry.Tags.BatchID, "discard"); err != nil {
		t.Fatal(err)
	}
	s.RetryPublicationRecovery()
	requireAppliedSnapshotCount(t, s, 1)
	if s.latestCatalogGeneration(s.publicationSources[0]) != entry.Sequence {
		t.Fatal("terminal discard changed generation cutoff")
	}
	s.Close()
	s = journalTestStore(t, dir)
	requireAppliedSnapshotCount(t, s, 1)
}
