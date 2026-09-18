package channels

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// Concurrent parks must not lose each other's announcements.
//
// This lane is the ONE writer of its directory that is deliberately not
// serialized: node.go calls it exactly at the `!datasetCatalogMu.TryLock()`
// skip, which means another announcement goroutine is already in the path. So
// concurrency is not an exotic case here — it is the only case the lane exists
// for.
//
// The reaper used to delete any file carrying the temp prefix, and in-flight
// writes lived beside the finished ones, so two concurrent parks removed each
// other's half-written files. node.go swallows the resulting ENOENT at Debugf,
// which made the loss invisible: the lane silently dropped exactly the
// announcements it was built to preserve. In-flight writes now live in their
// own directory that the reaper never reads.
//
// None of the lane's other tests run concurrently, which is why this was missed.
func TestConcurrentPendingParksDoNotReapEachOther(t *testing.T) {
	store, dir := pendingStore(t)
	defer store.Close()

	const parkers = 24
	now := time.Now()
	pnm := announcementBytes(1)

	var wg sync.WaitGroup
	errs := make([]error, parkers)
	start := make(chan struct{})
	for i := 0; i < parkers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = RememberPendingDatasetAnnouncement(store, fmt.Sprintf("publisher-%02d", i), pnm, now)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("park %d: %v", i, err)
		}
	}

	entries, err := ReadPendingDatasetAnnouncements(store, now)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(entries) != parkers {
		t.Fatalf("parked %d announcements concurrently but only %d survived — concurrent writers are reaping each other (files on disk: %d)",
			parkers, len(entries), countPendingFiles(t, dir))
	}

	// Every publisher must be distinctly present: a count that happens to match
	// while two parks collided on one name would still be a loss.
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.PeerID] = true
	}
	if len(seen) != parkers {
		t.Fatalf("%d distinct publishers survived out of %d", len(seen), parkers)
	}
}

// A park racing a READER must also survive: ReadPendingDatasetAnnouncements
// runs the same reaping scan, and it is called from the node's replay path
// while announcements are still arriving.
func TestPendingParkSurvivesAConcurrentReader(t *testing.T) {
	store, _ := pendingStore(t)
	defer store.Close()

	now := time.Now()
	pnm := announcementBytes(1)

	stop := make(chan struct{})
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = ReadPendingDatasetAnnouncements(store, now)
			}
		}
	}()

	const parks = 50
	for i := 0; i < parks; i++ {
		if err := RememberPendingDatasetAnnouncement(store, fmt.Sprintf("racer-%02d", i), pnm, now); err != nil {
			close(stop)
			reader.Wait()
			t.Fatalf("park %d while a reader scans: %v", i, err)
		}
	}
	close(stop)
	reader.Wait()

	entries, err := ReadPendingDatasetAnnouncements(store, now)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(entries) != parks {
		t.Fatalf("%d of %d parks survived a concurrent reader — the reader's reaping scan is deleting live writes", len(entries), parks)
	}
}
