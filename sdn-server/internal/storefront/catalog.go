package storefront

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DHTKey prefixes for storefront catalog
const (
	DHTKeyListingPrefix  = "/sdn/listing/"
	DHTKeyProviderPrefix = "/sdn/provider/"
	DHTKeyCategoryPrefix = "/sdn/category/"
)

// CatalogEntry represents a listing entry in the DHT catalog
type CatalogEntry struct {
	ListingID      string    `json:"listing_id"`
	ProviderPeerID string    `json:"provider_peer_id"`
	Title          string    `json:"title"`
	DataTypes      []string  `json:"data_types"`
	AccessType     int       `json:"access_type"`
	UpdatedAt      time.Time `json:"updated_at"`
	Active         bool      `json:"active"`
}

// DHTStore is an interface for DHT operations
type DHTStore interface {
	PutValue(ctx context.Context, key string, value []byte) error
	GetValue(ctx context.Context, key string) ([]byte, error)
}

// Catalog manages DHT-based listing discovery and local indexing
type Catalog struct {
	store   *Store
	dht     DHTStore
	entries map[string]*CatalogEntry // listingID -> entry
	mu      sync.RWMutex
}

// NewCatalog creates a new catalog for DHT-based discovery
func NewCatalog(store *Store, dht DHTStore) *Catalog {
	return &Catalog{
		store:   store,
		dht:     dht,
		entries: make(map[string]*CatalogEntry),
	}
}

// PublishListing publishes a listing to the DHT catalog
func (c *Catalog) PublishListing(ctx context.Context, listing *Listing) error {
	entry := &CatalogEntry{
		ListingID:      listing.ListingID,
		ProviderPeerID: listing.ProviderPeerID,
		Title:          listing.Title,
		DataTypes:      listing.DataTypes,
		AccessType:     int(listing.AccessType),
		UpdatedAt:      listing.UpdatedAt,
		Active:         listing.Active,
	}

	data, err := json.Marshal(listing)
	if err != nil {
		return fmt.Errorf("failed to marshal listing for DHT: %w", err)
	}

	// Publish listing by ID
	listingKey := DHTKeyListingPrefix + listing.ListingID
	if c.dht != nil {
		if err := c.dht.PutValue(ctx, listingKey, data); err != nil {
			log.Warnf("Failed to publish listing to DHT key %s: %v", listingKey, err)
		}
	}

	// Update provider index
	providerKey := DHTKeyProviderPrefix + listing.ProviderPeerID + "/listings"
	c.mu.Lock()
	c.entries[listing.ListingID] = entry
	providerListingIDs := c.getProviderListingIDsLocked(listing.ProviderPeerID)
	c.mu.Unlock()

	if c.dht != nil {
		indexData, _ := json.Marshal(providerListingIDs)
		if err := c.dht.PutValue(ctx, providerKey, indexData); err != nil {
			log.Warnf("Failed to publish provider index to DHT: %v", err)
		}
	}

	// Update category indexes
	for _, dt := range listing.DataTypes {
		categoryKey := DHTKeyCategoryPrefix + strings.ToLower(dt)
		c.mu.RLock()
		categoryIDs := c.getCategoryListingIDsLocked(dt)
		c.mu.RUnlock()

		if c.dht != nil {
			catData, _ := json.Marshal(categoryIDs)
			if err := c.dht.PutValue(ctx, categoryKey, catData); err != nil {
				log.Warnf("Failed to publish category index to DHT: %v", err)
			}
		}
	}

	log.Infof("Published listing %s to catalog", listing.ListingID)
	return nil
}

// FetchListing fetches a listing from the DHT by ID
func (c *Catalog) FetchListing(ctx context.Context, listingID string) (*Listing, error) {
	// Check local store first
	listing, err := c.store.GetListing(listingID)
	if err == nil && listing != nil {
		return listing, nil
	}

	// Try DHT
	if c.dht == nil {
		return nil, fmt.Errorf("listing not found and DHT not available")
	}

	key := DHTKeyListingPrefix + listingID
	data, err := c.dht.GetValue(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch listing from DHT: %w", err)
	}

	var fetched Listing
	if err := json.Unmarshal(data, &fetched); err != nil {
		return nil, fmt.Errorf("failed to unmarshal DHT listing: %w", err)
	}

	// Index locally
	if err := c.store.CreateListing(&fetched); err != nil {
		log.Warnf("Failed to index fetched listing locally: %v", err)
	}

	return &fetched, nil
}

// FetchProviderListings fetches all listing IDs for a provider from DHT
func (c *Catalog) FetchProviderListings(ctx context.Context, providerPeerID string) ([]string, error) {
	if c.dht == nil {
		return nil, fmt.Errorf("DHT not available")
	}

	key := DHTKeyProviderPrefix + providerPeerID + "/listings"
	data, err := c.dht.GetValue(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch provider listings: %w", err)
	}

	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("failed to unmarshal provider listing IDs: %w", err)
	}

	return ids, nil
}

// FetchCategoryListings fetches listing IDs for a data type category from DHT
func (c *Catalog) FetchCategoryListings(ctx context.Context, dataType string) ([]string, error) {
	if c.dht == nil {
		return nil, fmt.Errorf("DHT not available")
	}

	key := DHTKeyCategoryPrefix + strings.ToLower(dataType)
	data, err := c.dht.GetValue(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch category listings: %w", err)
	}

	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("failed to unmarshal category listing IDs: %w", err)
	}

	return ids, nil
}

// RemoveListing removes a listing from the catalog
func (c *Catalog) RemoveListing(ctx context.Context, listingID string) {
	c.mu.Lock()
	delete(c.entries, listingID)
	c.mu.Unlock()
}

// GetLocalEntries returns all locally cached catalog entries
func (c *Catalog) GetLocalEntries() []*CatalogEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entries := make([]*CatalogEntry, 0, len(c.entries))
	for _, e := range c.entries {
		entries = append(entries, e)
	}
	return entries
}

func (c *Catalog) getProviderListingIDsLocked(providerPeerID string) []string {
	var ids []string
	for _, e := range c.entries {
		if e.ProviderPeerID == providerPeerID && e.Active {
			ids = append(ids, e.ListingID)
		}
	}
	return ids
}

func (c *Catalog) getCategoryListingIDsLocked(dataType string) []string {
	var ids []string
	dt := strings.ToLower(dataType)
	for _, e := range c.entries {
		if !e.Active {
			continue
		}
		for _, d := range e.DataTypes {
			if strings.ToLower(d) == dt {
				ids = append(ids, e.ListingID)
				break
			}
		}
	}
	return ids
}

// Indexer subscribes to listing announcements and indexes them locally
type Indexer struct {
	store   *Store
	catalog *Catalog
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewIndexer creates a new listing indexer
func NewIndexer(store *Store, catalog *Catalog) *Indexer {
	return &Indexer{
		store:   store,
		catalog: catalog,
		done:    make(chan struct{}),
	}
}

// IndexListing indexes a single listing received from PubSub or DHT
func (idx *Indexer) IndexListing(listing *Listing) error {
	// Store in SQLite for search
	if err := idx.store.CreateListing(listing); err != nil {
		// May already exist, try update
		log.Debugf("Listing %s may already exist, attempting update", listing.ListingID)
	}

	// Update catalog entry
	idx.catalog.mu.Lock()
	idx.catalog.entries[listing.ListingID] = &CatalogEntry{
		ListingID:      listing.ListingID,
		ProviderPeerID: listing.ProviderPeerID,
		Title:          listing.Title,
		DataTypes:      listing.DataTypes,
		AccessType:     int(listing.AccessType),
		UpdatedAt:      listing.UpdatedAt,
		Active:         listing.Active,
	}
	idx.catalog.mu.Unlock()

	log.Infof("Indexed listing: %s", listing.ListingID)
	return nil
}

// ComputeFacets computes search facets for current listings
func (idx *Indexer) ComputeFacets(query *SearchQuery) (SearchFacets, error) {
	result, err := idx.store.SearchListings(&SearchQuery{
		DataTypes:       query.DataTypes,
		AccessTypes:     query.AccessTypes,
		ProviderPeerIDs: query.ProviderPeerIDs,
		Limit:           1000, // Get enough for faceting
	})
	if err != nil {
		return SearchFacets{}, err
	}

	facets := SearchFacets{
		DataTypes:   make(map[string]int),
		PriceRanges: make(map[string]int),
		Providers:   make(map[string]int),
		AccessTypes: make(map[string]int),
	}

	for _, l := range result.Listings {
		for _, dt := range l.DataTypes {
			facets.DataTypes[dt]++
		}
		facets.Providers[l.ProviderPeerID]++

		switch l.AccessType {
		case AccessTypeOneTime:
			facets.AccessTypes["OneTime"]++
		case AccessTypeSubscription:
			facets.AccessTypes["Subscription"]++
		case AccessTypeStreaming:
			facets.AccessTypes["Streaming"]++
		case AccessTypeQuery:
			facets.AccessTypes["Query"]++
		}

		// Compute price ranges
		if len(l.Pricing) > 0 {
			minPrice := l.Pricing[0].PriceAmount
			for _, p := range l.Pricing[1:] {
				if p.PriceAmount < minPrice {
					minPrice = p.PriceAmount
				}
			}
			priceRange := classifyPriceRange(minPrice)
			facets.PriceRanges[priceRange]++
		}
	}

	return facets, nil
}

func classifyPriceRange(amount uint64) string {
	switch {
	case amount == 0:
		return "Free"
	case amount < 1000: // < $10
		return "Under $10"
	case amount < 5000: // < $50
		return "$10-$50"
	case amount < 10000: // < $100
		return "$50-$100"
	case amount < 50000: // < $500
		return "$100-$500"
	default:
		return "$500+"
	}
}

// Close stops the indexer
func (idx *Indexer) Close() {
	if idx.cancel != nil {
		idx.cancel()
	}
	close(idx.done)
}
