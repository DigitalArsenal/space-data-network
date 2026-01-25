package storefront

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	_ "github.com/mattn/go-sqlite3"
)

var log = logging.Logger("storefront")

// Store provides SQLite-based storage for storefront data
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewStore creates a new storefront store
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	store := &Store{db: db}
	if err := store.initTables(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize tables: %w", err)
	}

	return store, nil
}

func (s *Store) initTables() error {
	// Listings table
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_listings (
			listing_id TEXT PRIMARY KEY,
			provider_peer_id TEXT NOT NULL,
			provider_epm_cid TEXT,
			title TEXT NOT NULL,
			description TEXT,
			data_types TEXT,
			tags TEXT,
			coverage TEXT,
			sample_cid TEXT,
			sample_record_count INTEGER DEFAULT 0,
			access_type INTEGER DEFAULT 0,
			encryption_required INTEGER DEFAULT 1,
			delivery_methods TEXT,
			pricing TEXT,
			accepted_payments TEXT,
			reputation TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			version INTEGER DEFAULT 1,
			active INTEGER DEFAULT 1,
			expires_at INTEGER,
			terms_cid TEXT,
			license TEXT,
			signature BLOB,
			UNIQUE(listing_id)
		);
		CREATE INDEX IF NOT EXISTS idx_listings_provider ON storefront_listings(provider_peer_id);
		CREATE INDEX IF NOT EXISTS idx_listings_active ON storefront_listings(active);
		CREATE INDEX IF NOT EXISTS idx_listings_updated ON storefront_listings(updated_at DESC);
	`)
	if err != nil {
		return fmt.Errorf("failed to create listings table: %w", err)
	}

	// Full-text search for listings
	_, err = s.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS storefront_listings_fts USING fts5(
			listing_id,
			title,
			description,
			data_types,
			tags,
			content=storefront_listings,
			content_rowid=rowid
		);
	`)
	if err != nil {
		log.Warnf("Failed to create FTS table (may already exist): %v", err)
	}

	// Access grants table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_grants (
			grant_id TEXT PRIMARY KEY,
			listing_id TEXT NOT NULL,
			tier_name TEXT NOT NULL,
			buyer_peer_id TEXT NOT NULL,
			buyer_encryption_pubkey BLOB,
			key_algorithm TEXT,
			access_type INTEGER DEFAULT 0,
			rate_limit INTEGER DEFAULT 0,
			max_records_per_request INTEGER DEFAULT 0,
			granted_at INTEGER NOT NULL,
			expires_at INTEGER,
			status INTEGER DEFAULT 0,
			payment_tx_hash TEXT,
			payment_method INTEGER,
			payment_amount INTEGER,
			payment_currency TEXT,
			payment_chain TEXT,
			next_renewal INTEGER,
			auto_renew INTEGER DEFAULT 0,
			renewal_count INTEGER DEFAULT 0,
			total_requests INTEGER DEFAULT 0,
			total_records INTEGER DEFAULT 0,
			last_access INTEGER,
			delivery_topic TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			notes TEXT,
			provider_signature BLOB,
			provider_peer_id TEXT NOT NULL,
			FOREIGN KEY (listing_id) REFERENCES storefront_listings(listing_id)
		);
		CREATE INDEX IF NOT EXISTS idx_grants_buyer ON storefront_grants(buyer_peer_id);
		CREATE INDEX IF NOT EXISTS idx_grants_listing ON storefront_grants(listing_id);
		CREATE INDEX IF NOT EXISTS idx_grants_status ON storefront_grants(status);
	`)
	if err != nil {
		return fmt.Errorf("failed to create grants table: %w", err)
	}

	// Purchase requests table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_purchases (
			request_id TEXT PRIMARY KEY,
			listing_id TEXT NOT NULL,
			tier_name TEXT NOT NULL,
			buyer_peer_id TEXT NOT NULL,
			buyer_encryption_pubkey BLOB,
			key_algorithm TEXT,
			buyer_email TEXT,
			payment_method INTEGER NOT NULL,
			payment_amount INTEGER NOT NULL,
			payment_currency TEXT NOT NULL,
			payment_tx_hash TEXT,
			payment_chain TEXT,
			sender_address TEXT,
			confirmation_block INTEGER,
			payment_intent_id TEXT,
			credits_transaction_id TEXT,
			status INTEGER DEFAULT 0,
			status_message TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			payment_deadline INTEGER,
			payment_confirmed_at INTEGER,
			grant_issued_at INTEGER,
			grant_id TEXT,
			provider_peer_id TEXT,
			provider_acknowledged_at INTEGER,
			preferred_delivery_method TEXT,
			webhook_url TEXT,
			buyer_signature BLOB,
			provider_signature BLOB,
			FOREIGN KEY (listing_id) REFERENCES storefront_listings(listing_id)
		);
		CREATE INDEX IF NOT EXISTS idx_purchases_buyer ON storefront_purchases(buyer_peer_id);
		CREATE INDEX IF NOT EXISTS idx_purchases_listing ON storefront_purchases(listing_id);
		CREATE INDEX IF NOT EXISTS idx_purchases_status ON storefront_purchases(status);
	`)
	if err != nil {
		return fmt.Errorf("failed to create purchases table: %w", err)
	}

	// Reviews table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_reviews (
			review_id TEXT PRIMARY KEY,
			listing_id TEXT NOT NULL,
			reviewer_peer_id TEXT NOT NULL,
			rating INTEGER NOT NULL,
			title TEXT,
			content TEXT,
			quality_metrics TEXT,
			acl_grant_id TEXT,
			verified_purchase INTEGER DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			status INTEGER DEFAULT 0,
			helpful_count INTEGER DEFAULT 0,
			not_helpful_count INTEGER DEFAULT 0,
			provider_response TEXT,
			provider_response_at INTEGER,
			flagged_count INTEGER DEFAULT 0,
			moderation_notes TEXT,
			reviewer_signature BLOB,
			FOREIGN KEY (listing_id) REFERENCES storefront_listings(listing_id)
		);
		CREATE INDEX IF NOT EXISTS idx_reviews_listing ON storefront_reviews(listing_id);
		CREATE INDEX IF NOT EXISTS idx_reviews_reviewer ON storefront_reviews(reviewer_peer_id);
	`)
	if err != nil {
		return fmt.Errorf("failed to create reviews table: %w", err)
	}

	// Credits balance table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_credits (
			peer_id TEXT PRIMARY KEY,
			balance INTEGER DEFAULT 0,
			pending_credits INTEGER DEFAULT 0,
			total_earned INTEGER DEFAULT 0,
			total_spent INTEGER DEFAULT 0,
			updated_at INTEGER NOT NULL
		);
	`)
	if err != nil {
		return fmt.Errorf("failed to create credits table: %w", err)
	}

	// Credits transactions table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_credits_transactions (
			transaction_id TEXT PRIMARY KEY,
			from_peer_id TEXT,
			to_peer_id TEXT,
			amount INTEGER NOT NULL,
			type TEXT NOT NULL,
			reference TEXT,
			created_at INTEGER NOT NULL,
			status TEXT DEFAULT 'completed'
		);
		CREATE INDEX IF NOT EXISTS idx_credits_tx_from ON storefront_credits_transactions(from_peer_id);
		CREATE INDEX IF NOT EXISTS idx_credits_tx_to ON storefront_credits_transactions(to_peer_id);
	`)
	if err != nil {
		return fmt.Errorf("failed to create credits transactions table: %w", err)
	}

	log.Info("Storefront tables initialized")
	return nil
}

// CreateListing creates a new listing
func (s *Store) CreateListing(listing *Listing) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dataTypesJSON, _ := json.Marshal(listing.DataTypes)
	tagsJSON, _ := json.Marshal(listing.Tags)
	coverageJSON, _ := json.Marshal(listing.Coverage)
	deliveryMethodsJSON, _ := json.Marshal(listing.DeliveryMethods)
	pricingJSON, _ := json.Marshal(listing.Pricing)
	acceptedPaymentsJSON, _ := json.Marshal(listing.AcceptedPayments)
	reputationJSON, _ := json.Marshal(listing.Reputation)

	_, err := s.db.Exec(`
		INSERT INTO storefront_listings (
			listing_id, provider_peer_id, provider_epm_cid, title, description,
			data_types, tags, coverage, sample_cid, sample_record_count,
			access_type, encryption_required, delivery_methods, pricing,
			accepted_payments, reputation, created_at, updated_at, version,
			active, expires_at, terms_cid, license, signature
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		listing.ListingID, listing.ProviderPeerID, listing.ProviderEPMCID,
		listing.Title, listing.Description,
		string(dataTypesJSON), string(tagsJSON), string(coverageJSON),
		listing.SampleCID, listing.SampleRecordCount,
		listing.AccessType, listing.EncryptionRequired,
		string(deliveryMethodsJSON), string(pricingJSON),
		string(acceptedPaymentsJSON), string(reputationJSON),
		listing.CreatedAt.Unix(), listing.UpdatedAt.Unix(),
		listing.Version, listing.Active, listing.ExpiresAt.Unix(),
		listing.TermsCID, listing.License, listing.Signature,
	)
	if err != nil {
		return fmt.Errorf("failed to create listing: %w", err)
	}

	// Update FTS index
	_, err = s.db.Exec(`
		INSERT INTO storefront_listings_fts (listing_id, title, description, data_types, tags)
		VALUES (?, ?, ?, ?, ?)
	`, listing.ListingID, listing.Title, listing.Description,
		strings.Join(listing.DataTypes, " "), strings.Join(listing.Tags, " "))
	if err != nil {
		log.Warnf("Failed to update FTS index: %v", err)
	}

	log.Infof("Created listing: %s", listing.ListingID)
	return nil
}

// GetListing retrieves a listing by ID
func (s *Store) GetListing(listingID string) (*Listing, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`
		SELECT listing_id, provider_peer_id, provider_epm_cid, title, description,
			data_types, tags, coverage, sample_cid, sample_record_count,
			access_type, encryption_required, delivery_methods, pricing,
			accepted_payments, reputation, created_at, updated_at, version,
			active, expires_at, terms_cid, license, signature
		FROM storefront_listings WHERE listing_id = ?
	`, listingID)

	return s.scanListing(row)
}

func (s *Store) scanListing(row *sql.Row) (*Listing, error) {
	var listing Listing
	var dataTypesJSON, tagsJSON, coverageJSON, deliveryMethodsJSON string
	var pricingJSON, acceptedPaymentsJSON, reputationJSON string
	var createdAt, updatedAt, expiresAt int64

	err := row.Scan(
		&listing.ListingID, &listing.ProviderPeerID, &listing.ProviderEPMCID,
		&listing.Title, &listing.Description,
		&dataTypesJSON, &tagsJSON, &coverageJSON,
		&listing.SampleCID, &listing.SampleRecordCount,
		&listing.AccessType, &listing.EncryptionRequired,
		&deliveryMethodsJSON, &pricingJSON,
		&acceptedPaymentsJSON, &reputationJSON,
		&createdAt, &updatedAt, &listing.Version,
		&listing.Active, &expiresAt,
		&listing.TermsCID, &listing.License, &listing.Signature,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to scan listing: %w", err)
	}

	json.Unmarshal([]byte(dataTypesJSON), &listing.DataTypes)
	json.Unmarshal([]byte(tagsJSON), &listing.Tags)
	json.Unmarshal([]byte(coverageJSON), &listing.Coverage)
	json.Unmarshal([]byte(deliveryMethodsJSON), &listing.DeliveryMethods)
	json.Unmarshal([]byte(pricingJSON), &listing.Pricing)
	json.Unmarshal([]byte(acceptedPaymentsJSON), &listing.AcceptedPayments)
	json.Unmarshal([]byte(reputationJSON), &listing.Reputation)
	listing.CreatedAt = time.Unix(createdAt, 0)
	listing.UpdatedAt = time.Unix(updatedAt, 0)
	listing.ExpiresAt = time.Unix(expiresAt, 0)

	return &listing, nil
}

// SearchListings searches listings with filters
func (s *Store) SearchListings(query *SearchQuery) (*SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var conditions []string
	var args []interface{}

	conditions = append(conditions, "active = 1")

	// Data types filter
	if len(query.DataTypes) > 0 {
		placeholders := make([]string, len(query.DataTypes))
		for i, dt := range query.DataTypes {
			placeholders[i] = "data_types LIKE ?"
			args = append(args, "%"+dt+"%")
		}
		conditions = append(conditions, "("+strings.Join(placeholders, " OR ")+")")
	}

	// Access types filter
	if len(query.AccessTypes) > 0 {
		placeholders := make([]string, len(query.AccessTypes))
		for i, at := range query.AccessTypes {
			placeholders[i] = "access_type = ?"
			args = append(args, at)
		}
		conditions = append(conditions, "("+strings.Join(placeholders, " OR ")+")")
	}

	// Provider filter
	if len(query.ProviderPeerIDs) > 0 {
		placeholders := make([]string, len(query.ProviderPeerIDs))
		for i, pid := range query.ProviderPeerIDs {
			placeholders[i] = "provider_peer_id = ?"
			args = append(args, pid)
		}
		conditions = append(conditions, "("+strings.Join(placeholders, " OR ")+")")
	}

	// Full-text search
	var listingIDs []string
	if query.SearchText != "" {
		rows, err := s.db.Query(`
			SELECT listing_id FROM storefront_listings_fts
			WHERE storefront_listings_fts MATCH ?
		`, query.SearchText)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					listingIDs = append(listingIDs, id)
				}
			}
		}
		if len(listingIDs) > 0 {
			placeholders := make([]string, len(listingIDs))
			for i, id := range listingIDs {
				placeholders[i] = "?"
				args = append(args, id)
			}
			conditions = append(conditions, "listing_id IN ("+strings.Join(placeholders, ",")+")")
		} else if query.SearchText != "" {
			// No FTS results, return empty
			return &SearchResult{Listings: []Listing{}, Total: 0}, nil
		}
	}

	whereClause := strings.Join(conditions, " AND ")

	// Get total count
	var total int
	countQuery := "SELECT COUNT(*) FROM storefront_listings WHERE " + whereClause
	s.db.QueryRow(countQuery, args...).Scan(&total)

	// Sort
	orderBy := "updated_at DESC"
	switch query.SortBy {
	case "price":
		orderBy = "pricing"
	case "rating":
		orderBy = "reputation"
	case "updated":
		orderBy = "updated_at"
	}
	if query.SortDesc {
		orderBy += " DESC"
	}

	// Pagination
	limit := 20
	if query.Limit > 0 && query.Limit <= 100 {
		limit = query.Limit
	}
	offset := query.Offset

	// Execute query
	querySQL := fmt.Sprintf(`
		SELECT listing_id, provider_peer_id, provider_epm_cid, title, description,
			data_types, tags, coverage, sample_cid, sample_record_count,
			access_type, encryption_required, delivery_methods, pricing,
			accepted_payments, reputation, created_at, updated_at, version,
			active, expires_at, terms_cid, license, signature
		FROM storefront_listings WHERE %s ORDER BY %s LIMIT ? OFFSET ?
	`, whereClause, orderBy)

	args = append(args, limit, offset)
	rows, err := s.db.Query(querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search listings: %w", err)
	}
	defer rows.Close()

	var listings []Listing
	for rows.Next() {
		var listing Listing
		var dataTypesJSON, tagsJSON, coverageJSON, deliveryMethodsJSON string
		var pricingJSON, acceptedPaymentsJSON, reputationJSON string
		var createdAt, updatedAt, expiresAt int64

		err := rows.Scan(
			&listing.ListingID, &listing.ProviderPeerID, &listing.ProviderEPMCID,
			&listing.Title, &listing.Description,
			&dataTypesJSON, &tagsJSON, &coverageJSON,
			&listing.SampleCID, &listing.SampleRecordCount,
			&listing.AccessType, &listing.EncryptionRequired,
			&deliveryMethodsJSON, &pricingJSON,
			&acceptedPaymentsJSON, &reputationJSON,
			&createdAt, &updatedAt, &listing.Version,
			&listing.Active, &expiresAt,
			&listing.TermsCID, &listing.License, &listing.Signature,
		)
		if err != nil {
			log.Warnf("Failed to scan listing row: %v", err)
			continue
		}

		json.Unmarshal([]byte(dataTypesJSON), &listing.DataTypes)
		json.Unmarshal([]byte(tagsJSON), &listing.Tags)
		json.Unmarshal([]byte(coverageJSON), &listing.Coverage)
		json.Unmarshal([]byte(deliveryMethodsJSON), &listing.DeliveryMethods)
		json.Unmarshal([]byte(pricingJSON), &listing.Pricing)
		json.Unmarshal([]byte(acceptedPaymentsJSON), &listing.AcceptedPayments)
		json.Unmarshal([]byte(reputationJSON), &listing.Reputation)
		listing.CreatedAt = time.Unix(createdAt, 0)
		listing.UpdatedAt = time.Unix(updatedAt, 0)
		listing.ExpiresAt = time.Unix(expiresAt, 0)

		listings = append(listings, listing)
	}

	// TODO: Compute facets

	return &SearchResult{
		Listings: listings,
		Total:    total,
		Facets:   SearchFacets{},
	}, nil
}

// CreateGrant creates a new access grant
func (s *Store) CreateGrant(grant *AccessGrant) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO storefront_grants (
			grant_id, listing_id, tier_name, buyer_peer_id, buyer_encryption_pubkey,
			key_algorithm, access_type, rate_limit, max_records_per_request,
			granted_at, expires_at, status, payment_tx_hash, payment_method,
			payment_amount, payment_currency, payment_chain, next_renewal,
			auto_renew, renewal_count, total_requests, total_records,
			last_access, delivery_topic, created_at, updated_at, notes,
			provider_signature, provider_peer_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		grant.GrantID, grant.ListingID, grant.TierName, grant.BuyerPeerID,
		grant.BuyerEncryptionPubkey, grant.KeyAlgorithm, grant.AccessType,
		grant.RateLimit, grant.MaxRecordsPerRequest,
		grant.GrantedAt.Unix(), grant.ExpiresAt.Unix(), grant.Status,
		grant.PaymentTxHash, grant.PaymentMethod, grant.PaymentAmount,
		grant.PaymentCurrency, grant.PaymentChain, grant.NextRenewal.Unix(),
		grant.AutoRenew, grant.RenewalCount, grant.TotalRequests,
		grant.TotalRecords, grant.LastAccess.Unix(), grant.DeliveryTopic,
		grant.CreatedAt.Unix(), grant.UpdatedAt.Unix(), grant.Notes,
		grant.ProviderSignature, grant.ProviderPeerID,
	)
	if err != nil {
		return fmt.Errorf("failed to create grant: %w", err)
	}

	log.Infof("Created grant: %s for buyer: %s", grant.GrantID, grant.BuyerPeerID)
	return nil
}

// GetGrant retrieves a grant by ID
func (s *Store) GetGrant(grantID string) (*AccessGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var grant AccessGrant
	var grantedAt, expiresAt, nextRenewal, lastAccess, createdAt, updatedAt int64

	err := s.db.QueryRow(`
		SELECT grant_id, listing_id, tier_name, buyer_peer_id, buyer_encryption_pubkey,
			key_algorithm, access_type, rate_limit, max_records_per_request,
			granted_at, expires_at, status, payment_tx_hash, payment_method,
			payment_amount, payment_currency, payment_chain, next_renewal,
			auto_renew, renewal_count, total_requests, total_records,
			last_access, delivery_topic, created_at, updated_at, notes,
			provider_signature, provider_peer_id
		FROM storefront_grants WHERE grant_id = ?
	`, grantID).Scan(
		&grant.GrantID, &grant.ListingID, &grant.TierName, &grant.BuyerPeerID,
		&grant.BuyerEncryptionPubkey, &grant.KeyAlgorithm, &grant.AccessType,
		&grant.RateLimit, &grant.MaxRecordsPerRequest,
		&grantedAt, &expiresAt, &grant.Status,
		&grant.PaymentTxHash, &grant.PaymentMethod, &grant.PaymentAmount,
		&grant.PaymentCurrency, &grant.PaymentChain, &nextRenewal,
		&grant.AutoRenew, &grant.RenewalCount, &grant.TotalRequests,
		&grant.TotalRecords, &lastAccess, &grant.DeliveryTopic,
		&createdAt, &updatedAt, &grant.Notes,
		&grant.ProviderSignature, &grant.ProviderPeerID,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get grant: %w", err)
	}

	grant.GrantedAt = time.Unix(grantedAt, 0)
	grant.ExpiresAt = time.Unix(expiresAt, 0)
	grant.NextRenewal = time.Unix(nextRenewal, 0)
	grant.LastAccess = time.Unix(lastAccess, 0)
	grant.CreatedAt = time.Unix(createdAt, 0)
	grant.UpdatedAt = time.Unix(updatedAt, 0)

	return &grant, nil
}

// GetGrantsByBuyer retrieves all grants for a buyer
func (s *Store) GetGrantsByBuyer(buyerPeerID string) ([]*AccessGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT grant_id, listing_id, tier_name, buyer_peer_id, buyer_encryption_pubkey,
			key_algorithm, access_type, rate_limit, max_records_per_request,
			granted_at, expires_at, status, payment_tx_hash, payment_method,
			payment_amount, payment_currency, payment_chain, next_renewal,
			auto_renew, renewal_count, total_requests, total_records,
			last_access, delivery_topic, created_at, updated_at, notes,
			provider_signature, provider_peer_id
		FROM storefront_grants WHERE buyer_peer_id = ?
	`, buyerPeerID)
	if err != nil {
		return nil, fmt.Errorf("failed to query grants: %w", err)
	}
	defer rows.Close()

	var grants []*AccessGrant
	for rows.Next() {
		var grant AccessGrant
		var grantedAt, expiresAt, nextRenewal, lastAccess, createdAt, updatedAt int64

		err := rows.Scan(
			&grant.GrantID, &grant.ListingID, &grant.TierName, &grant.BuyerPeerID,
			&grant.BuyerEncryptionPubkey, &grant.KeyAlgorithm, &grant.AccessType,
			&grant.RateLimit, &grant.MaxRecordsPerRequest,
			&grantedAt, &expiresAt, &grant.Status,
			&grant.PaymentTxHash, &grant.PaymentMethod, &grant.PaymentAmount,
			&grant.PaymentCurrency, &grant.PaymentChain, &nextRenewal,
			&grant.AutoRenew, &grant.RenewalCount, &grant.TotalRequests,
			&grant.TotalRecords, &lastAccess, &grant.DeliveryTopic,
			&createdAt, &updatedAt, &grant.Notes,
			&grant.ProviderSignature, &grant.ProviderPeerID,
		)
		if err != nil {
			log.Warnf("Failed to scan grant row: %v", err)
			continue
		}

		grant.GrantedAt = time.Unix(grantedAt, 0)
		grant.ExpiresAt = time.Unix(expiresAt, 0)
		grant.NextRenewal = time.Unix(nextRenewal, 0)
		grant.LastAccess = time.Unix(lastAccess, 0)
		grant.CreatedAt = time.Unix(createdAt, 0)
		grant.UpdatedAt = time.Unix(updatedAt, 0)

		grants = append(grants, &grant)
	}

	return grants, nil
}

// CreatePurchaseRequest creates a new purchase request
func (s *Store) CreatePurchaseRequest(req *PurchaseRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO storefront_purchases (
			request_id, listing_id, tier_name, buyer_peer_id, buyer_encryption_pubkey,
			key_algorithm, buyer_email, payment_method, payment_amount, payment_currency,
			payment_tx_hash, payment_chain, sender_address, confirmation_block,
			payment_intent_id, credits_transaction_id, status, status_message,
			created_at, updated_at, payment_deadline, payment_confirmed_at,
			grant_issued_at, grant_id, provider_peer_id, provider_acknowledged_at,
			preferred_delivery_method, webhook_url, buyer_signature, provider_signature
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		req.RequestID, req.ListingID, req.TierName, req.BuyerPeerID,
		req.BuyerEncryptionPubkey, req.KeyAlgorithm, req.BuyerEmail,
		req.PaymentMethod, req.PaymentAmount, req.PaymentCurrency,
		req.PaymentTxHash, req.PaymentChain, req.SenderAddress, req.ConfirmationBlock,
		req.PaymentIntentID, req.CreditsTransactionID, req.Status, req.StatusMessage,
		req.CreatedAt.Unix(), req.UpdatedAt.Unix(), req.PaymentDeadline.Unix(),
		req.PaymentConfirmedAt.Unix(), req.GrantIssuedAt.Unix(), req.GrantID,
		req.ProviderPeerID, req.ProviderAcknowledgedAt.Unix(),
		req.PreferredDeliveryMethod, req.WebhookURL,
		req.BuyerSignature, req.ProviderSignature,
	)
	if err != nil {
		return fmt.Errorf("failed to create purchase request: %w", err)
	}

	log.Infof("Created purchase request: %s", req.RequestID)
	return nil
}

// UpdatePurchaseStatus updates the status of a purchase request
func (s *Store) UpdatePurchaseStatus(requestID string, status PurchaseStatus, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE storefront_purchases SET status = ?, status_message = ?, updated_at = ?
		WHERE request_id = ?
	`, status, message, time.Now().Unix(), requestID)
	if err != nil {
		return fmt.Errorf("failed to update purchase status: %w", err)
	}

	return nil
}

// CreateReview creates a new review
func (s *Store) CreateReview(review *Review) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	qualityMetricsJSON, _ := json.Marshal(review.QualityMetrics)

	_, err := s.db.Exec(`
		INSERT INTO storefront_reviews (
			review_id, listing_id, reviewer_peer_id, rating, title, content,
			quality_metrics, acl_grant_id, verified_purchase, created_at,
			updated_at, status, helpful_count, not_helpful_count,
			provider_response, provider_response_at, flagged_count,
			moderation_notes, reviewer_signature
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		review.ReviewID, review.ListingID, review.ReviewerPeerID, review.Rating,
		review.Title, review.Content, string(qualityMetricsJSON),
		review.ACLGrantID, review.VerifiedPurchase, review.CreatedAt.Unix(),
		review.UpdatedAt.Unix(), review.Status, review.HelpfulCount,
		review.NotHelpfulCount, review.ProviderResponse,
		review.ProviderResponseAt.Unix(), review.FlaggedCount,
		review.ModerationNotes, review.ReviewerSignature,
	)
	if err != nil {
		return fmt.Errorf("failed to create review: %w", err)
	}

	log.Infof("Created review: %s for listing: %s", review.ReviewID, review.ListingID)
	return nil
}

// GetReviewsForListing retrieves reviews for a listing
func (s *Store) GetReviewsForListing(listingID string, limit, offset int) ([]*Review, *ReviewStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Get reviews
	rows, err := s.db.Query(`
		SELECT review_id, listing_id, reviewer_peer_id, rating, title, content,
			quality_metrics, acl_grant_id, verified_purchase, created_at,
			updated_at, status, helpful_count, not_helpful_count,
			provider_response, provider_response_at, flagged_count,
			moderation_notes, reviewer_signature
		FROM storefront_reviews WHERE listing_id = ? AND status = 0
		ORDER BY created_at DESC LIMIT ? OFFSET ?
	`, listingID, limit, offset)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query reviews: %w", err)
	}
	defer rows.Close()

	var reviews []*Review
	for rows.Next() {
		var review Review
		var qualityMetricsJSON string
		var createdAt, updatedAt, providerResponseAt int64

		err := rows.Scan(
			&review.ReviewID, &review.ListingID, &review.ReviewerPeerID,
			&review.Rating, &review.Title, &review.Content,
			&qualityMetricsJSON, &review.ACLGrantID, &review.VerifiedPurchase,
			&createdAt, &updatedAt, &review.Status, &review.HelpfulCount,
			&review.NotHelpfulCount, &review.ProviderResponse,
			&providerResponseAt, &review.FlaggedCount,
			&review.ModerationNotes, &review.ReviewerSignature,
		)
		if err != nil {
			log.Warnf("Failed to scan review row: %v", err)
			continue
		}

		json.Unmarshal([]byte(qualityMetricsJSON), &review.QualityMetrics)
		review.CreatedAt = time.Unix(createdAt, 0)
		review.UpdatedAt = time.Unix(updatedAt, 0)
		review.ProviderResponseAt = time.Unix(providerResponseAt, 0)

		reviews = append(reviews, &review)
	}

	// Get stats
	stats := &ReviewStats{ListingID: listingID}
	s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(AVG(rating) * 10, 0),
			SUM(CASE WHEN verified_purchase = 1 THEN 1 ELSE 0 END),
			MAX(created_at)
		FROM storefront_reviews WHERE listing_id = ? AND status = 0
	`, listingID).Scan(&stats.TotalReviews, &stats.AverageRatingX10,
		&stats.VerifiedReviews, &stats.LastReviewAt)

	// Rating distribution
	for i := 1; i <= 5; i++ {
		var count uint32
		s.db.QueryRow(`
			SELECT COUNT(*) FROM storefront_reviews
			WHERE listing_id = ? AND status = 0 AND rating = ?
		`, listingID, i).Scan(&count)
		stats.RatingDistribution[i-1] = count
	}

	return reviews, stats, nil
}

// GetCreditsBalance retrieves the credits balance for a peer
func (s *Store) GetCreditsBalance(peerID string) (*CreditsBalance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var balance CreditsBalance
	var updatedAt int64

	err := s.db.QueryRow(`
		SELECT peer_id, balance, pending_credits, total_earned, total_spent, updated_at
		FROM storefront_credits WHERE peer_id = ?
	`, peerID).Scan(
		&balance.PeerID, &balance.Balance, &balance.PendingCredits,
		&balance.TotalEarned, &balance.TotalSpent, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			// Return zero balance for new peers
			return &CreditsBalance{PeerID: peerID}, nil
		}
		return nil, fmt.Errorf("failed to get credits balance: %w", err)
	}

	balance.UpdatedAt = time.Unix(updatedAt, 0)
	return &balance, nil
}

// UpdateCreditsBalance updates a peer's credits balance
func (s *Store) UpdateCreditsBalance(peerID string, delta int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()

	_, err := s.db.Exec(`
		INSERT INTO storefront_credits (peer_id, balance, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(peer_id) DO UPDATE SET
			balance = balance + ?,
			updated_at = ?
	`, peerID, delta, now, delta, now)
	if err != nil {
		return fmt.Errorf("failed to update credits balance: %w", err)
	}

	return nil
}

// Close closes the store
func (s *Store) Close() error {
	return s.db.Close()
}
