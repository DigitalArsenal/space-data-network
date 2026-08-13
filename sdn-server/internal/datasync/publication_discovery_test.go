package datasync

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// blockingLister is a PublicationLister whose calls park on a gate channel,
// simulating a store read queued behind a long writer hold.
type blockingLister struct {
	mu      sync.Mutex
	calls   int64
	gate    chan struct{} // nil = answer immediately
	pubs    []storage.DatasetShardPublication
	listErr error
}

func (l *blockingLister) ListDatasetShardPublications(storage.DatasetShardPublicationQuery) ([]storage.DatasetShardPublication, error) {
	atomic.AddInt64(&l.calls, 1)
	l.mu.Lock()
	gate := l.gate
	pubs := append([]storage.DatasetShardPublication(nil), l.pubs...)
	listErr := l.listErr
	l.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return pubs, listErr
}

func (l *blockingLister) setPubs(pubs []storage.DatasetShardPublication) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pubs = pubs
}

func (l *blockingLister) callCount() int64 { return atomic.LoadInt64(&l.calls) }

func testPublication(cid string) storage.DatasetShardPublication {
	return storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-1",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		ShardCID:     cid,
	}
}

func testDiscoveryQuery(schema string) storage.DatasetShardPublicationQuery {
	return storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		QueryProfile: storage.DatasetPublicationQueryProfile,
	}
}

// TestPublicationDiscoveryCacheAnswersBoundedWhileListerBlocks is the cache-
// level statement of this task's regression contract: once a snapshot exists,
// a discovery answer is BOUNDED no matter how long the store read blocks
// behind a writer. The live failure this encodes: 22-45 s
// list_published_shards answers against a 20 s browser timeout.
func TestPublicationDiscoveryCacheAnswersBoundedWhileListerBlocks(t *testing.T) {
	lister := &blockingLister{pubs: []storage.DatasetShardPublication{testPublication("bafkwarm")}}
	cache := NewPublicationDiscoveryCache(time.Nanosecond) // every hit is stale -> refresh every time

	// Warm fetch (lister unblocked).
	warm, err := cache.Publications(lister, testDiscoveryQuery("OMM.fbs"))
	if err != nil {
		t.Fatalf("warm fetch failed: %v", err)
	}
	if len(warm) != 1 || warm[0].ShardCID != "bafkwarm" {
		t.Fatalf("unexpected warm snapshot: %+v", warm)
	}

	// Now every store read parks indefinitely, exactly like a reader queued
	// behind ingest's write lock.
	gate := make(chan struct{})
	lister.mu.Lock()
	lister.gate = gate
	lister.pubs = []storage.DatasetShardPublication{testPublication("bafkfresh")}
	lister.mu.Unlock()
	defer close(gate)

	for probe := 0; probe < 8; probe++ {
		start := time.Now()
		pubs, err := cache.Publications(lister, testDiscoveryQuery("OMM.fbs"))
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("probe %d failed: %v", probe, err)
		}
		if len(pubs) != 1 || pubs[0].ShardCID != "bafkwarm" {
			t.Fatalf("probe %d: expected last-good snapshot, got %+v", probe, pubs)
		}
		if elapsed > 100*time.Millisecond {
			t.Fatalf("probe %d took %v with the store read blocked; discovery must answer from the snapshot without queueing behind writers", probe, elapsed)
		}
	}
}

// TestPublicationDiscoveryCacheBackgroundRefreshLands proves stale snapshots
// converge: after the blocked refresh completes, the next request sees the
// new publication list.
func TestPublicationDiscoveryCacheBackgroundRefreshLands(t *testing.T) {
	lister := &blockingLister{pubs: []storage.DatasetShardPublication{testPublication("bafkold")}}
	cache := NewPublicationDiscoveryCache(time.Nanosecond)

	if _, err := cache.Publications(lister, testDiscoveryQuery("OMM.fbs")); err != nil {
		t.Fatalf("warm fetch failed: %v", err)
	}
	lister.setPubs([]storage.DatasetShardPublication{testPublication("bafknew")})

	// A stale hit triggers one background refresh; poll until it lands.
	deadline := time.Now().Add(5 * time.Second)
	for {
		pubs, err := cache.Publications(lister, testDiscoveryQuery("OMM.fbs"))
		if err != nil {
			t.Fatalf("stale hit failed: %v", err)
		}
		if len(pubs) == 1 && pubs[0].ShardCID == "bafknew" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("background refresh never landed; still serving %+v", pubs)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestPublicationDiscoveryCacheCoalescesFirstFetch: concurrent first requests
// for one query shape produce exactly one store read.
func TestPublicationDiscoveryCacheCoalescesFirstFetch(t *testing.T) {
	gate := make(chan struct{})
	lister := &blockingLister{gate: gate, pubs: []storage.DatasetShardPublication{testPublication("bafkone")}}
	cache := NewPublicationDiscoveryCache(time.Hour)

	const callers = 16
	var wg sync.WaitGroup
	errs := make([]error, callers)
	counts := make([]int, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			pubs, err := cache.Publications(lister, testDiscoveryQuery("OMM.fbs"))
			errs[slot] = err
			counts[slot] = len(pubs)
		}(i)
	}
	time.Sleep(50 * time.Millisecond) // let every caller arrive at the entry
	close(gate)
	wg.Wait()

	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d failed: %v", i, errs[i])
		}
		if counts[i] != 1 {
			t.Fatalf("caller %d got %d publications, want 1", i, counts[i])
		}
	}
	if got := lister.callCount(); got != 1 {
		t.Fatalf("store reads = %d, want 1 (first fetch must coalesce)", got)
	}
}

// TestPublicationDiscoveryCacheFirstErrorIsNotCached: a failed first fetch
// propagates its error and the next request retries the store.
func TestPublicationDiscoveryCacheFirstErrorIsNotCached(t *testing.T) {
	lister := &blockingLister{listErr: errors.New("store is unavailable")}
	cache := NewPublicationDiscoveryCache(time.Hour)

	if _, err := cache.Publications(lister, testDiscoveryQuery("OMM.fbs")); err == nil {
		t.Fatalf("expected first fetch error")
	}
	lister.mu.Lock()
	lister.listErr = nil
	lister.pubs = []storage.DatasetShardPublication{testPublication("bafkrecovered")}
	lister.mu.Unlock()

	pubs, err := cache.Publications(lister, testDiscoveryQuery("OMM.fbs"))
	if err != nil {
		t.Fatalf("retry after error failed: %v", err)
	}
	if len(pubs) != 1 || pubs[0].ShardCID != "bafkrecovered" {
		t.Fatalf("unexpected retry snapshot: %+v", pubs)
	}
	if got := lister.callCount(); got != 2 {
		t.Fatalf("store reads = %d, want 2 (errors must not be cached)", got)
	}
}

// TestPublicationDiscoveryCacheReturnsCallerOwnedSlice: handlers filter the
// returned list in place (publications[:0] append trick), so the cache must
// hand out a copy or the snapshot would be corrupted for the next caller.
func TestPublicationDiscoveryCacheReturnsCallerOwnedSlice(t *testing.T) {
	lister := &blockingLister{pubs: []storage.DatasetShardPublication{
		testPublication("bafkfirst"),
		testPublication("bafksecond"),
	}}
	cache := NewPublicationDiscoveryCache(time.Hour)

	first, err := cache.Publications(lister, testDiscoveryQuery("OMM.fbs"))
	if err != nil {
		t.Fatalf("first fetch failed: %v", err)
	}
	// Simulate the handler's in-place availability filter dropping row 0.
	filtered := first[:0]
	filtered = append(filtered, first[1])
	_ = filtered

	second, err := cache.Publications(lister, testDiscoveryQuery("OMM.fbs"))
	if err != nil {
		t.Fatalf("second fetch failed: %v", err)
	}
	if len(second) != 2 || second[0].ShardCID != "bafkfirst" || second[1].ShardCID != "bafksecond" {
		t.Fatalf("cached snapshot was corrupted by a caller's in-place filter: %+v", second)
	}
	if got := lister.callCount(); got != 1 {
		t.Fatalf("store reads = %d, want 1 (both requests must be cache-served)", got)
	}
}

// TestPublicationDiscoveryCacheEvictsLeastRecentlyUsed: the entry map is
// capped against anonymous query-shape churn.
func TestPublicationDiscoveryCacheEvictsLeastRecentlyUsed(t *testing.T) {
	lister := &blockingLister{}
	cache := NewPublicationDiscoveryCache(time.Hour)
	cache.maxEntries = 8

	for i := 0; i < 40; i++ {
		if _, err := cache.Publications(lister, testDiscoveryQuery(fmt.Sprintf("SCHEMA-%d.fbs", i))); err != nil {
			t.Fatalf("fetch %d failed: %v", i, err)
		}
	}
	cache.mu.Lock()
	size := len(cache.entries)
	cache.mu.Unlock()
	if size > 8 {
		t.Fatalf("cache grew to %d entries, cap is 8", size)
	}
}

// TestPublicationDiscoveryForceRefreshIsSingleFlight: ForceRefresh never
// stacks refreshes and never blocks the caller.
func TestPublicationDiscoveryForceRefreshIsSingleFlight(t *testing.T) {
	lister := &blockingLister{pubs: []storage.DatasetShardPublication{testPublication("bafkwarm")}}
	cache := NewPublicationDiscoveryCache(time.Hour)
	query := testDiscoveryQuery("OMM.fbs")

	if _, err := cache.Publications(lister, query); err != nil {
		t.Fatalf("warm fetch failed: %v", err)
	}

	gate := make(chan struct{})
	lister.mu.Lock()
	lister.gate = gate
	lister.mu.Unlock()

	start := time.Now()
	for i := 0; i < 5; i++ {
		cache.ForceRefresh(lister, query) // first spawns, rest are no-ops
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("ForceRefresh blocked the caller for %v", elapsed)
	}
	close(gate)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if lister.callCount() == 2 { // warm + exactly one refresh
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("store reads = %d, want 2 (single-flight refresh)", lister.callCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Give any (buggy) extra refresh a moment to fire before the final check.
	time.Sleep(50 * time.Millisecond)
	if got := lister.callCount(); got != 2 {
		t.Fatalf("store reads = %d, want 2 (ForceRefresh must be single-flight)", got)
	}
}
