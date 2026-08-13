package datasync

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// PublicationLister is the minimal store surface publication discovery needs.
// *storage.FlatSQLStore satisfies it.
type PublicationLister interface {
	ListDatasetShardPublications(storage.DatasetShardPublicationQuery) ([]storage.DatasetShardPublication, error)
}

const (
	// DefaultPublicationDiscoveryRefreshInterval bounds how stale a served
	// publication list may be before a background refresh is triggered.
	// Publications change on publish events (hours apart on the live fleet),
	// so seconds of staleness is invisible to consumers — while the refresh
	// itself may block for the full length of a writer hold without ever
	// stalling a discovery answer.
	DefaultPublicationDiscoveryRefreshInterval = 3 * time.Second

	// publicationDiscoveryMaxEntries caps the cache map so anonymous traffic
	// cannot grow it without bound (the flatsql-sync protocol accepts
	// arbitrary schema/provider/source/batch strings from unauthenticated
	// browsers). Real deployments discover a handful of schemas; when the cap
	// is hit the least-recently-used entry is evicted.
	publicationDiscoveryMaxEntries = 256
)

// PublicationDiscoveryCache serves dataset-shard publication lists for the
// flatsql-sync DISCOVER hop without queueing behind the store's writers.
//
// WHY THIS EXISTS (task sdn-flatsql-sync-discovery-latency-resets): the
// store's ListDatasetShardPublications takes the store-wide s.mu.RLock, so an
// anonymous browser's list_published_shards frame queues behind any long
// writer hold. Measured live: 22-45 s answers for a 5-row table during ingest
// (client timeout 20 s), which is what made /beta show CATALOGUE UNAVAILABLE.
// The same 5,756-byte answer took 77 ms on an idle box — contention, not cost.
//
// The cache is a stale-while-revalidate snapshot per normalized query:
//
//   - a HIT answers from memory immediately — the hot path never touches the
//     store lock at all;
//   - a stale HIT additionally triggers ONE background refresh (single-flight
//     per entry), which may block behind a writer for as long as it likes
//     without any request waiting on it;
//   - only the FIRST request for a never-seen query blocks on the store
//     (there is nothing to serve yet); concurrent first requests coalesce.
//
// Serving a list up to refresh-interval stale is correct for discovery: the
// answer names immutable content-addressed shards, and the caller re-checks
// on-disk availability per publication at serve time, so a pruned shard is
// filtered even from a stale snapshot.
type PublicationDiscoveryCache struct {
	refreshInterval time.Duration
	maxEntries      int
	now             func() time.Time

	mu      sync.Mutex
	entries map[string]*publicationDiscoveryEntry
}

type publicationDiscoveryEntry struct {
	// ready is closed once the first fetch completes (pubs/firstErr valid).
	ready chan struct{}

	// All fields below are guarded by the cache mutex.
	fetched    bool
	firstErr   error
	pubs       []storage.DatasetShardPublication
	fetchedAt  time.Time
	lastAccess time.Time
	refreshing bool
}

// NewPublicationDiscoveryCache creates a discovery cache. A non-positive
// refreshInterval selects the default.
func NewPublicationDiscoveryCache(refreshInterval time.Duration) *PublicationDiscoveryCache {
	if refreshInterval <= 0 {
		refreshInterval = DefaultPublicationDiscoveryRefreshInterval
	}
	return &PublicationDiscoveryCache{
		refreshInterval: refreshInterval,
		maxEntries:      publicationDiscoveryMaxEntries,
		now:             time.Now,
		entries:         make(map[string]*publicationDiscoveryEntry),
	}
}

// Publications answers the publication list for query, from cache when
// possible. Only the first request for a given query shape ever waits on the
// store; every later request is answered from the last known snapshot while
// staleness past the refresh interval triggers a single background refresh.
//
// The returned slice is the caller's to mutate (callers filter it in place).
func (c *PublicationDiscoveryCache) Publications(lister PublicationLister, query storage.DatasetShardPublicationQuery) ([]storage.DatasetShardPublication, error) {
	if c == nil || lister == nil {
		return lister.ListDatasetShardPublications(query)
	}
	key := publicationDiscoveryKey(query)

	c.mu.Lock()
	entry, exists := c.entries[key]
	if !exists {
		entry = &publicationDiscoveryEntry{ready: make(chan struct{}), lastAccess: c.now()}
		c.evictForRoomLocked()
		c.entries[key] = entry
		c.mu.Unlock()

		// First fetch for this query shape: nothing to serve yet, so this
		// request pays the store read (and any writer hold in front of it).
		pubs, err := lister.ListDatasetShardPublications(query)

		c.mu.Lock()
		entry.fetched = err == nil
		entry.firstErr = err
		entry.pubs = pubs
		entry.fetchedAt = c.now()
		if err != nil {
			// Do not cache failures: drop the entry so the next request
			// retries, but only if a concurrent path has not replaced it.
			if c.entries[key] == entry {
				delete(c.entries, key)
			}
		}
		close(entry.ready)
		result := clonePublications(entry.pubs)
		c.mu.Unlock()
		return result, err
	}
	entry.lastAccess = c.now()
	if entry.fetched {
		stale := c.now().Sub(entry.fetchedAt) >= c.refreshInterval
		if stale && !entry.refreshing {
			entry.refreshing = true
			go c.refresh(lister, query, key, entry)
		}
		result := clonePublications(entry.pubs)
		c.mu.Unlock()
		return result, nil
	}
	c.mu.Unlock()

	// First fetch is in flight on another goroutine: coalesce onto it.
	<-entry.ready
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.firstErr != nil {
		return nil, entry.firstErr
	}
	return clonePublications(entry.pubs), nil
}

// ForceRefresh triggers one background refresh for query regardless of entry
// age (single-flight per entry; a refresh already in flight is enough). It
// never blocks. Callers use it when a served snapshot looked wrong at the
// edges — e.g. every listed shard failed the on-disk availability check right
// after a supersede — so the next request converges without ever stalling
// this one.
func (c *PublicationDiscoveryCache) ForceRefresh(lister PublicationLister, query storage.DatasetShardPublicationQuery) {
	if c == nil || lister == nil {
		return
	}
	key := publicationDiscoveryKey(query)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, exists := c.entries[key]
	if !exists || !entry.fetched || entry.refreshing {
		return
	}
	entry.refreshing = true
	go c.refresh(lister, query, key, entry)
}

// refresh re-reads the store for one entry. It runs off the request path and
// may block behind a writer hold indefinitely; requests keep being served
// from the previous snapshot the whole time. A refresh error keeps the last
// good snapshot (a stale list beats no list) and the next stale request
// retries.
func (c *PublicationDiscoveryCache) refresh(lister PublicationLister, query storage.DatasetShardPublicationQuery, key string, entry *publicationDiscoveryEntry) {
	pubs, err := lister.ListDatasetShardPublications(query)

	c.mu.Lock()
	defer c.mu.Unlock()
	entry.refreshing = false
	if err != nil {
		return
	}
	entry.pubs = pubs
	entry.fetchedAt = c.now()
}

// evictForRoomLocked makes room for one more entry, dropping the
// least-recently-used complete entry when the cache is at capacity. Entries
// whose first fetch is still in flight are never evicted (waiters hold them).
func (c *PublicationDiscoveryCache) evictForRoomLocked() {
	if len(c.entries) < c.maxEntries {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, entry := range c.entries {
		if !entry.fetched {
			continue
		}
		if oldestKey == "" || entry.lastAccess.Before(oldest) {
			oldestKey = key
			oldest = entry.lastAccess
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func publicationDiscoveryKey(query storage.DatasetShardPublicationQuery) string {
	fields := []string{
		strings.TrimSpace(query.SchemaName),
		strings.TrimSpace(query.ProviderID),
		strings.TrimSpace(query.SourceName),
		strings.TrimSpace(query.BatchID),
		strings.TrimSpace(query.QueryProfile),
		strconv.Itoa(query.Offset),
		strconv.Itoa(query.Limit),
		strconv.Itoa(query.RecordCount),
	}
	return strings.Join(fields, "\x1f")
}

func clonePublications(pubs []storage.DatasetShardPublication) []storage.DatasetShardPublication {
	if pubs == nil {
		return nil
	}
	return append([]storage.DatasetShardPublication(nil), pubs...)
}
