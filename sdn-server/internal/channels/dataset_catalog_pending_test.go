package channels

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func pendingStore(t *testing.T) (*storage.FlatSQLStore, string) {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	store, err := storage.NewFlatSQLStore(path, validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, pendingDatasetCatalogPath(store)
}

// announcementBytes is a stand-in for a signed PNM. The lane is deliberately
// opaque to its contents — verification happens before anything is parked —
// so the bounds tests only need bytes that are distinct and in range.
func announcementBytes(seed int) []byte {
	return []byte(fmt.Sprintf("pnm-announcement-%06d", seed))
}

func countPendingFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), pendingDatasetAnnouncementSuffix) {
			total++
		}
	}
	return total
}

func TestPendingDatasetAnnouncementRoundTripsAcrossRestart(t *testing.T) {
	store, _ := pendingStore(t)
	now := time.Now()
	pnm := announcementBytes(1)
	if err := RememberPendingDatasetAnnouncement(store, "publisher-a", pnm, now); err != nil {
		t.Fatal(err)
	}
	// Re-parking the same announcement is one slot, not one per message: a
	// peer that keeps re-announcing an unchanged catalog is normal traffic.
	for i := 0; i < 5; i++ {
		if err := RememberPendingDatasetAnnouncement(store, "publisher-a", pnm, now); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ReadPendingDatasetAnnouncements(store, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].PeerID != "publisher-a" || !bytes.Equal(entries[0].PNM, pnm) {
		t.Fatalf("parked announcement did not round-trip: %+v", entries)
	}
	if err := ForgetPendingDatasetAnnouncement(store, "publisher-a", pnm); err != nil {
		t.Fatal(err)
	}
	entries, err = ReadPendingDatasetAnnouncements(store, now)
	if err != nil || len(entries) != 0 {
		t.Fatalf("settled announcement survived: %+v %v", entries, err)
	}
	// Forgetting something already gone is not an error: the catch-up pass
	// settles announcements it may have settled on an earlier pass too.
	if err := ForgetPendingDatasetAnnouncement(store, "publisher-a", pnm); err != nil {
		t.Fatal(err)
	}
}

// An untrusted peer can reach this lane — that is the point of it — so disk
// growth is its obvious abuse. One peer must never be able to occupy more than
// its own share, nor evict another peer's announcements to make room.
func TestPendingDatasetAnnouncementsAreCappedPerPeer(t *testing.T) {
	store, dir := pendingStore(t)
	now := time.Now()
	// The victim parks first, so per-peer eviction would take it if the cap
	// were global rather than per peer.
	victim := announcementBytes(0)
	if err := RememberPendingDatasetAnnouncement(store, "honest-peer", victim, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= maxDatasetCatalogPeerEntries+40; i++ {
		if err := RememberPendingDatasetAnnouncement(store, "flooding-peer", announcementBytes(i), now); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ReadPendingDatasetAnnouncements(store, now)
	if err != nil {
		t.Fatal(err)
	}
	perPeer := map[string]int{}
	for _, entry := range entries {
		perPeer[entry.PeerID]++
	}
	if perPeer["flooding-peer"] > maxDatasetCatalogPeerEntries {
		t.Fatalf("one peer holds %d slots, cap is %d", perPeer["flooding-peer"], maxDatasetCatalogPeerEntries)
	}
	if perPeer["honest-peer"] != 1 {
		t.Fatalf("a flooding peer evicted another peer's announcement: %+v", perPeer)
	}
	if got := countPendingFiles(t, dir); got != len(entries) {
		t.Fatalf("%d files on disk but %d readable entries", got, len(entries))
	}
}

// Nothing settles some announcements — a CID that never resolves, a provider
// that vanished. Age, not luck, is what frees those slots.
func TestPendingDatasetAnnouncementsExpire(t *testing.T) {
	store, dir := pendingStore(t)
	now := time.Now()
	stale := announcementBytes(7)
	if err := RememberPendingDatasetAnnouncement(store, "vanished-peer", stale, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(pendingDatasetAnnouncementTTL + time.Minute)
	entries, err := ReadPendingDatasetAnnouncements(store, later)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expired announcement still parked: %+v", entries)
	}
	if got := countPendingFiles(t, dir); got != 0 {
		t.Fatalf("expired announcement left %d files on disk", got)
	}
}

// The file name is derived from BOTH the publisher and the bytes. A file whose
// contents were swapped must be dropped, not replayed as though the peer named
// inside it had announced it.
func TestPendingDatasetAnnouncementRejectsSwappedContents(t *testing.T) {
	store, dir := pendingStore(t)
	now := time.Now()
	original := announcementBytes(11)
	if err := RememberPendingDatasetAnnouncement(store, "publisher-a", original, now); err != nil {
		t.Fatal(err)
	}
	name := pendingDatasetAnnouncementName("publisher-a", original)
	forged := append([]byte("publisher-b\n"), announcementBytes(12)...)
	if err := os.WriteFile(filepath.Join(dir, name), forged, 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadPendingDatasetAnnouncements(store, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("swapped announcement was replayed: %+v", entries)
	}
	if got := countPendingFiles(t, dir); got != 0 {
		t.Fatalf("swapped announcement left %d files on disk", got)
	}
}

// Parked bytes must never be confusable with a record. The lane is a sibling
// directory of dataset-catalogs/, not a producer table, which is exactly why
// an untrusted peer may fill it and may not write a record.
func TestPendingDatasetAnnouncementsLiveBesideTheCatalogNotInTheStore(t *testing.T) {
	store, dir := pendingStore(t)
	if filepath.Dir(dir) != filepath.Dir(datasetCatalogPath(store)) {
		t.Fatalf("pending lane %s is not a sibling of %s", dir, datasetCatalogPath(store))
	}
	if dir == datasetCatalogPath(store) {
		t.Fatal("pending announcements must not share the served catalog directory")
	}
	if err := RememberPendingDatasetAnnouncement(store, "publisher-a", announcementBytes(3), time.Now()); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadDatasetCatalog(store, time.Now())
	if err != nil || len(entries) != 0 {
		t.Fatalf("a parked announcement surfaced as a catalog entry: %+v %v", entries, err)
	}
	summary, err := store.DataSummary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalRecords != 0 {
		t.Fatalf("a parked announcement became %d stored records", summary.TotalRecords)
	}
}

func TestPendingDatasetAnnouncementRejectsOutOfBoundsInput(t *testing.T) {
	store, _ := pendingStore(t)
	now := time.Now()
	for _, bad := range []struct {
		name      string
		publisher string
		pnm       []byte
	}{
		{"empty publisher", "", announcementBytes(1)},
		{"publisher with separator", "publisher\na", announcementBytes(1)},
		{"publisher with path", "../escape", announcementBytes(1)},
		{"short announcement", "publisher-a", []byte("tiny")},
		{"oversized announcement", "publisher-a", make([]byte, maxPendingDatasetAnnouncementBytes+1)},
	} {
		if err := RememberPendingDatasetAnnouncement(store, bad.publisher, bad.pnm, now); err == nil {
			t.Fatalf("%s was accepted", bad.name)
		}
	}
	if err := RememberPendingDatasetAnnouncement(nil, "publisher-a", announcementBytes(1), now); err == nil {
		t.Fatal("a nil store was accepted")
	}
}
