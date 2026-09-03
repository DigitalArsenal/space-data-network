package storefront

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	storefrontReadLockBudget = 250 * time.Millisecond
	inventoryFreshFor        = 5 * time.Second
)

var ErrStorefrontReadBusy = errors.New("storefront index is busy; try again")

// DatasetSelection is the stored standard/source/batch chosen by the operator.
// It scopes DPM construction without changing the SDS listing record itself.
type DatasetSelection struct {
	SchemaName string `json:"schema_name"`
	ProviderID string `json:"provider_id,omitempty"`
	SourceName string `json:"source_name,omitempty"`
	BatchID    string `json:"batch_id,omitempty"`
}

// UploadReference identifies bytes pinned before the listing is published.
type UploadReference struct {
	CID        string `json:"cid"`
	SHA256     string `json:"sha256"`
	ByteLength int64  `json:"byte_length"`
	FileName   string `json:"file_name"`
	MediaType  string `json:"media_type,omitempty"`
}

type PublicationOptions struct {
	AnnounceTo    []string `json:"announce_to"`
	PinRecords    bool     `json:"pin_records"`
	PinManifest   bool     `json:"pin_manifest"`
	RetentionDays uint32   `json:"retention_days"`
}

type ListingPublicationRequest struct {
	Listing     ListingDraft
	Dataset     *DatasetSelection
	Upload      *UploadReference
	Publication PublicationOptions
}

func (request *ListingPublicationRequest) validate() error {
	if err := request.Listing.validate(); err != nil {
		return err
	}
	if request.Publication.RetentionDays > 36500 {
		return errors.New("retention_days cannot exceed 36500")
	}
	for _, network := range request.Publication.AnnounceTo {
		if strings.TrimSpace(network) != "storefront" {
			return fmt.Errorf("announcement network %q is not available on this node", network)
		}
	}

	isDataset := strings.TrimSpace(request.Listing.ListingKind) == "DataStream"
	if !isDataset && (request.Dataset != nil || request.Upload != nil) {
		return errors.New("dataset selection and uploads are only valid for DataStream listings")
	}
	if request.Dataset != nil {
		schema := canonicalListingSchema(request.Dataset.SchemaName)
		if err := sds.ValidateSchemaName(schema); err != nil {
			return fmt.Errorf("dataset schema: %w", err)
		}
		matched := false
		for _, dataType := range request.Listing.DataTypes {
			matched = matched || canonicalListingSchema(dataType) == schema
		}
		if !matched {
			return errors.New("dataset schema must be included in DATA_TYPES")
		}
		request.Dataset.SchemaName = schema
	}
	if request.Upload != nil {
		request.Upload.CID = strings.TrimSpace(request.Upload.CID)
		request.Upload.SHA256 = strings.ToLower(strings.TrimSpace(request.Upload.SHA256))
		request.Upload.FileName = strings.TrimSpace(request.Upload.FileName)
		digest, err := hex.DecodeString(request.Upload.SHA256)
		if request.Upload.CID == "" || request.Upload.FileName == "" || request.Upload.ByteLength <= 0 || err != nil || len(digest) != 32 {
			return errors.New("upload reference requires CID, file name, byte length, and SHA-256")
		}
	}
	return nil
}

// announcesTo reports whether a publication announces on the named network.
// No explicit targets means the default: the storefront listing topic, which
// is what every publish did before the wizard let operators narrow it.
func (options PublicationOptions) announcesTo(network string) bool {
	if len(options.AnnounceTo) == 0 {
		return network == "storefront"
	}
	for _, candidate := range options.AnnounceTo {
		if strings.TrimSpace(candidate) == network {
			return true
		}
	}
	return false
}

func canonicalListingSchema(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "$")
	if value != "" && !strings.HasSuffix(strings.ToLower(value), ".fbs") {
		value += ".fbs"
	}
	return value
}

// PublishableDataset is one locally stored provenance lane that can become a
// dataset listing. Count and bytes come from the maintained source summary.
type PublishableDataset struct {
	SchemaName  string `json:"schema_name"`
	Standard    string `json:"standard"`
	ProviderID  string `json:"provider_id,omitempty"`
	SourceName  string `json:"source_name,omitempty"`
	BatchID     string `json:"batch_id,omitempty"`
	RecordCount int64  `json:"record_count"`
	TotalBytes  int64  `json:"total_bytes"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

type PublishableInventory struct {
	Datasets []PublishableDataset `json:"datasets"`
	Stale    bool                 `json:"stale"`
	AsOf     string               `json:"as_of,omitempty"`
}

type OwnListing struct {
	ListingID   string      `json:"listing_id"`
	ListingKind ListingKind `json:"listing_kind"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	DataTypes   []string    `json:"data_types"`
	Active      bool        `json:"active"`
	State       string      `json:"state"`
	STFCID      string      `json:"stf_cid,omitempty"`
	PNMCID      string      `json:"pnm_cid,omitempty"`
	DPMCID      string      `json:"dpm_cid,omitempty"`
	UpdatedAt   string      `json:"updated_at"`
}

type OwnListingResult struct {
	Listings []OwnListing `json:"listings"`
}

type publishInventoryCache struct {
	mu         sync.Mutex
	value      PublishableInventory
	asOf       time.Time
	refreshing bool
	done       chan struct{}
}

func (s *Service) PublishableInventory(ctx context.Context) (PublishableInventory, error) {
	if s == nil || s.store == nil || s.store.flatStore == nil {
		return PublishableInventory{Datasets: []PublishableDataset{}}, errors.New("stored dataset inventory is unavailable")
	}
	if s.inventory == nil {
		s.mu.Lock()
		if s.inventory == nil {
			s.inventory = &publishInventoryCache{}
		}
		s.mu.Unlock()
	}
	cache := s.inventory
	now := time.Now()
	cache.mu.Lock()
	if !cache.asOf.IsZero() && now.Sub(cache.asOf) < inventoryFreshFor {
		value := clonePublishableInventory(cache.value)
		cache.mu.Unlock()
		return value, nil
	}
	if !cache.refreshing {
		cache.refreshing = true
		cache.done = make(chan struct{})
		done := cache.done
		go s.refreshPublishableInventory(cache, done)
	}
	done := cache.done
	stale := clonePublishableInventory(cache.value)
	hasStale := !cache.asOf.IsZero()
	cache.mu.Unlock()

	timer := time.NewTimer(storefrontReadLockBudget)
	defer timer.Stop()
	select {
	case <-done:
		cache.mu.Lock()
		value := clonePublishableInventory(cache.value)
		asOf := cache.asOf
		cache.mu.Unlock()
		if asOf.IsZero() {
			return PublishableInventory{Datasets: []PublishableDataset{}}, errors.New("stored dataset inventory could not be read")
		}
		return value, nil
	case <-ctx.Done():
		return PublishableInventory{}, ctx.Err()
	case <-timer.C:
		if hasStale {
			stale.Stale = true
			return stale, nil
		}
		return PublishableInventory{Datasets: []PublishableDataset{}}, ErrStorefrontReadBusy
	}
}

func (s *Service) refreshPublishableInventory(cache *publishInventoryCache, done chan struct{}) {
	rows, err := s.store.flatStore.SourceBatchProgress()
	now := time.Now().UTC()
	value := PublishableInventory{Datasets: make([]PublishableDataset, 0, len(rows))}
	if err == nil {
		for _, row := range rows {
			if row.Count <= 0 || strings.TrimSpace(row.SchemaName) == "" {
				continue
			}
			standard := strings.TrimSuffix(strings.TrimSpace(row.SchemaName), ".fbs")
			item := PublishableDataset{
				SchemaName: row.SchemaName, Standard: strings.ToUpper(standard),
				ProviderID: row.ProviderID, SourceName: row.SourceName, BatchID: row.BatchID,
				RecordCount: row.Count, TotalBytes: row.TotalBytes,
			}
			if row.UpdatedAtUnix > 0 {
				item.UpdatedAt = time.Unix(row.UpdatedAtUnix, 0).UTC().Format(time.RFC3339)
			}
			value.Datasets = append(value.Datasets, item)
		}
		sort.Slice(value.Datasets, func(i, j int) bool {
			a, b := value.Datasets[i], value.Datasets[j]
			if a.SchemaName != b.SchemaName {
				return a.SchemaName < b.SchemaName
			}
			if a.ProviderID != b.ProviderID {
				return a.ProviderID < b.ProviderID
			}
			if a.SourceName != b.SourceName {
				return a.SourceName < b.SourceName
			}
			return a.BatchID < b.BatchID
		})
		value.AsOf = now.Format(time.RFC3339)
	}
	cache.mu.Lock()
	if err == nil {
		cache.value = value
		cache.asOf = now
	}
	cache.refreshing = false
	close(done)
	cache.mu.Unlock()
}

func clonePublishableInventory(value PublishableInventory) PublishableInventory {
	value.Datasets = append([]PublishableDataset(nil), value.Datasets...)
	if value.Datasets == nil {
		value.Datasets = []PublishableDataset{}
	}
	return value
}

// OwnListings reads only the storefront's small index projection. Lock
// acquisition is time-bounded, and the SQL query itself is context-bound and
// capped, so maintenance can never make a dashboard request wait indefinitely.
func (s *Store) OwnListings(ctx context.Context, providerPeerID string) ([]OwnListing, error) {
	release, err := s.acquireReadLock(ctx, storefrontReadLockBudget)
	if err != nil {
		return nil, err
	}
	defer release()

	rows, err := s.db.QueryContext(ctx, `
		SELECT l.listing_id, l.listing_kind, l.title, l.description, l.data_types,
		       l.active, l.updated_at,
		       COALESCE(NULLIF(p.stf_cid, ''), l.cid, ''),
		       COALESCE(p.pnm_cid, ''), COALESCE(p.dpm_cid, '')
		FROM storefront_listings l
		LEFT JOIN storefront_listing_publications p ON p.listing_id = l.listing_id
		WHERE l.provider_peer_id = ? AND COALESCE(l.source_peer_id, '') = ''
		ORDER BY l.updated_at DESC, l.listing_id ASC
		LIMIT 200
	`, strings.TrimSpace(providerPeerID))
	if err != nil {
		return nil, fmt.Errorf("read current listings: %w", err)
	}
	defer rows.Close()
	result := make([]OwnListing, 0)
	for rows.Next() {
		var item OwnListing
		var dataTypesJSON string
		var updatedAt int64
		if err := rows.Scan(
			&item.ListingID, &item.ListingKind, &item.Title, &item.Description,
			&dataTypesJSON, &item.Active, &updatedAt, &item.STFCID, &item.PNMCID, &item.DPMCID,
		); err != nil {
			return nil, fmt.Errorf("scan current listing: %w", err)
		}
		_ = json.Unmarshal([]byte(dataTypesJSON), &item.DataTypes)
		if item.DataTypes == nil {
			item.DataTypes = []string{}
		}
		item.UpdatedAt = time.Unix(updatedAt, 0).UTC().Format(time.RFC3339)
		switch {
		case !item.Active:
			item.State = "unpublished"
		case item.PNMCID == "":
			item.State = "failed"
		default:
			item.State = "published"
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current listings: %w", err)
	}
	return result, nil
}

func (s *Store) acquireReadLock(ctx context.Context, budget time.Duration) (func(), error) {
	deadline := time.NewTimer(budget)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if s.mu.TryRLock() {
			return s.mu.RUnlock, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, ErrStorefrontReadBusy
		case <-ticker.C:
		}
	}
}

func (s *Service) OwnListings(ctx context.Context) (OwnListingResult, error) {
	rows, err := s.store.OwnListings(ctx, s.peerID)
	if err != nil {
		return OwnListingResult{}, err
	}
	return OwnListingResult{Listings: rows}, nil
}

func (s *Service) WithdrawOwnListing(ctx context.Context, listingID string) (OwnListing, error) {
	listing, err := s.store.GetListing(strings.TrimSpace(listingID))
	if err != nil {
		return OwnListing{}, err
	}
	if listing == nil {
		return OwnListing{}, errors.New("listing not found")
	}
	if listing.ProviderPeerID != s.peerID || listing.SourcePeerID != "" {
		return OwnListing{}, errors.New("only a listing published by this node can be withdrawn")
	}
	if listing.Active {
		if err := s.store.UpdateListingActive(listing.ListingID, false); err != nil {
			return OwnListing{}, err
		}
	}
	rows, err := s.store.OwnListings(ctx, s.peerID)
	if err != nil {
		return OwnListing{}, err
	}
	for _, item := range rows {
		if item.ListingID == listing.ListingID {
			return item, nil
		}
	}
	return OwnListing{}, sql.ErrNoRows
}

func (selection *DatasetSelection) indexedQuery(schema string, offset int) storage.IndexedRecordQuery {
	query := storage.IndexedRecordQuery{
		SchemaName: schema, AllowLargeResultSet: true, OrderByCID: true,
		Limit: 250000, Offset: offset,
	}
	if selection != nil {
		query.ProviderID = strings.TrimSpace(selection.ProviderID)
		query.SourceName = strings.TrimSpace(selection.SourceName)
		query.BatchID = strings.TrimSpace(selection.BatchID)
	}
	return query
}
