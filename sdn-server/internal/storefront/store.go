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

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

var log = logging.Logger("storefront")

// FlatSQL schema names for storefront record types.
const (
	SchemaSTF = "STF.fbs"
	SchemaACL = "ACL.fbs"
	SchemaPUR = "PUR.fbs"
	SchemaREV = "REV.fbs"
)

// sanitizeFTS5Query escapes special FTS5 operators from user input by quoting
// each whitespace-delimited token. This prevents FTS5 MATCH injection where
// operators like AND, OR, NOT, NEAR, *, or column filters could be abused.
func sanitizeFTS5Query(input string) string {
	tokens := strings.Fields(input)
	if len(tokens) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		tok = strings.ReplaceAll(tok, "\"", "")
		if tok == "" {
			continue
		}
		quoted = append(quoted, "\""+tok+"\"")
	}
	return strings.Join(quoted, " ")
}

// Store provides FlatSQL-backed storage for storefront data.
// Canonical record data (STF, ACL, PUR, REV) is stored through FlatSQLStore
// as content-addressed blobs. Lightweight index tables in the same database
// provide rich query support (search, filter, pagination).
type Store struct {
	flatStore *storage.FlatSQLStore
	db        *sql.DB // own connection for index tables
	mu        sync.RWMutex
}

// NewStore creates a new storefront store backed by FlatSQL.
// It opens its own connection to the same sdn.db for index tables,
// while using flatStore for content-addressed record storage.
func NewStore(flatStore *storage.FlatSQLStore) (*Store, error) {
	db, err := sql.Open("sqlite3", flatStore.Path()+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open index database: %w", err)
	}

	store := &Store{
		flatStore: flatStore,
		db:        db,
	}
	if err := store.initTables(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize index tables: %w", err)
	}

	return store, nil
}

func (s *Store) initTables() error {
	// Listing index — lightweight searchable projection of STF records
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_listings (
			listing_id TEXT PRIMARY KEY,
			cid TEXT DEFAULT '',
			listing_kind TEXT DEFAULT 'data_stream',
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
			protected_delivery TEXT,
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
			source_peer_id TEXT DEFAULT '',
			UNIQUE(listing_id)
		);
		CREATE INDEX IF NOT EXISTS idx_listings_provider ON storefront_listings(provider_peer_id);
		CREATE INDEX IF NOT EXISTS idx_listings_active ON storefront_listings(active);
		CREATE INDEX IF NOT EXISTS idx_listings_updated ON storefront_listings(updated_at DESC);
		CREATE INDEX IF NOT EXISTS idx_listings_cid ON storefront_listings(cid);
	`)
	if err != nil {
		return fmt.Errorf("failed to create listings index table: %w", err)
	}

	// Migration: add cid and source_peer_id columns to existing tables
	s.db.Exec(`ALTER TABLE storefront_listings ADD COLUMN cid TEXT DEFAULT ''`)
	s.db.Exec(`ALTER TABLE storefront_listings ADD COLUMN listing_kind TEXT DEFAULT 'data_stream'`)
	s.db.Exec(`ALTER TABLE storefront_listings ADD COLUMN protected_delivery TEXT`)
	s.db.Exec(`ALTER TABLE storefront_listings ADD COLUMN source_peer_id TEXT DEFAULT ''`)

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

	// Grant index
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_grants (
			grant_id TEXT PRIMARY KEY,
			cid TEXT DEFAULT '',
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
			provider_peer_id TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_grants_buyer ON storefront_grants(buyer_peer_id);
		CREATE INDEX IF NOT EXISTS idx_grants_listing ON storefront_grants(listing_id);
		CREATE INDEX IF NOT EXISTS idx_grants_status ON storefront_grants(status);
	`)
	if err != nil {
		return fmt.Errorf("failed to create grants index table: %w", err)
	}

	s.db.Exec(`ALTER TABLE storefront_grants ADD COLUMN cid TEXT DEFAULT ''`)

	// Purchase index
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_purchases (
			request_id TEXT PRIMARY KEY,
			cid TEXT DEFAULT '',
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
			provider_signature BLOB
		);
		CREATE INDEX IF NOT EXISTS idx_purchases_buyer ON storefront_purchases(buyer_peer_id);
		CREATE INDEX IF NOT EXISTS idx_purchases_listing ON storefront_purchases(listing_id);
		CREATE INDEX IF NOT EXISTS idx_purchases_status ON storefront_purchases(status);
	`)
	if err != nil {
		return fmt.Errorf("failed to create purchases index table: %w", err)
	}

	s.db.Exec(`ALTER TABLE storefront_purchases ADD COLUMN cid TEXT DEFAULT ''`)

	// Reviews index
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_reviews (
			review_id TEXT PRIMARY KEY,
			cid TEXT DEFAULT '',
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
			reviewer_signature BLOB
		);
		CREATE INDEX IF NOT EXISTS idx_reviews_listing ON storefront_reviews(listing_id);
		CREATE INDEX IF NOT EXISTS idx_reviews_reviewer ON storefront_reviews(reviewer_peer_id);
	`)
	if err != nil {
		return fmt.Errorf("failed to create reviews index table: %w", err)
	}

	s.db.Exec(`ALTER TABLE storefront_reviews ADD COLUMN cid TEXT DEFAULT ''`)

	// Credits balance table (not a FlatBuffer type — local ledger only)
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

	// Credits transactions table (local ledger)
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

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_payment_audit (
			event_id TEXT PRIMARY KEY,
			request_id TEXT NOT NULL,
			event_type TEXT NOT NULL,
			actor_peer_id TEXT,
			reference TEXT,
			message TEXT,
			purchase_status INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_payment_audit_request ON storefront_payment_audit(request_id, created_at);
	`)
	if err != nil {
		return fmt.Errorf("failed to create payment audit table: %w", err)
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_payment_webhook_events (
			event_id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			request_id TEXT,
			event_type TEXT NOT NULL,
			processed_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_payment_webhook_request ON storefront_payment_webhook_events(request_id, processed_at);
	`)
	if err != nil {
		return fmt.Errorf("failed to create payment webhook event table: %w", err)
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS storefront_crypto_intents (
			reference TEXT PRIMARY KEY,
			request_id TEXT NOT NULL,
			chain TEXT NOT NULL,
			asset TEXT NOT NULL,
			asset_contract TEXT DEFAULT '',
			native_asset INTEGER DEFAULT 0,
			amount INTEGER NOT NULL,
			recipient TEXT NOT NULL,
			method INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			used_at INTEGER DEFAULT 0,
			tx_hash TEXT DEFAULT '',
			intent_digest TEXT DEFAULT '',
			intent_signature TEXT DEFAULT '',
			UNIQUE(request_id, reference)
		);
		CREATE INDEX IF NOT EXISTS idx_crypto_intents_request ON storefront_crypto_intents(request_id);
		CREATE INDEX IF NOT EXISTS idx_crypto_intents_tx ON storefront_crypto_intents(tx_hash);
	`)
	if err != nil {
		return fmt.Errorf("failed to create crypto intent table: %w", err)
	}
	s.db.Exec(`ALTER TABLE storefront_crypto_intents ADD COLUMN asset_contract TEXT DEFAULT ''`)
	s.db.Exec(`ALTER TABLE storefront_crypto_intents ADD COLUMN native_asset INTEGER DEFAULT 0`)
	s.db.Exec(`ALTER TABLE storefront_crypto_intents ADD COLUMN intent_digest TEXT DEFAULT ''`)
	s.db.Exec(`ALTER TABLE storefront_crypto_intents ADD COLUMN intent_signature TEXT DEFAULT ''`)

	log.Info("Storefront index tables initialized (FlatSQL-backed)")
	return nil
}

// storeRecordToFlatSQL marshals data to JSON and stores it through FlatSQL.
// Returns the content identifier (CID).
func (s *Store) storeRecordToFlatSQL(schemaName string, data interface{}, peerID string, signature []byte) (string, error) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal record: %w", err)
	}
	cid, err := s.flatStore.Store(schemaName, jsonData, peerID, signature)
	if err != nil {
		return "", fmt.Errorf("failed to store %s record in FlatSQL: %w", schemaName, err)
	}
	return cid, nil
}

// CreateListing creates a new listing. Stores the full record through FlatSQL
// and updates the index table for queryability.
func (s *Store) CreateListing(listing *Listing) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Store canonical record through FlatSQL (content-addressed)
	peerID := listing.ProviderPeerID
	if listing.SourcePeerID != "" {
		peerID = listing.SourcePeerID
	}
	cid, err := s.storeRecordToFlatSQL(SchemaSTF, listing, peerID, listing.Signature)
	if err != nil {
		log.Warnf("FlatSQL store failed for listing %s: %v", listing.ListingID, err)
	}

	// Update index table
	dataTypesJSON, _ := json.Marshal(listing.DataTypes)
	tagsJSON, _ := json.Marshal(listing.Tags)
	coverageJSON, _ := json.Marshal(listing.Coverage)
	deliveryMethodsJSON, _ := json.Marshal(listing.DeliveryMethods)
	protectedDeliveryJSON, _ := json.Marshal(listing.ProtectedDelivery)
	pricingJSON, _ := json.Marshal(listing.Pricing)
	acceptedPaymentsJSON, _ := json.Marshal(listing.AcceptedPayments)
	reputationJSON, _ := json.Marshal(listing.Reputation)

	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO storefront_listings (
			listing_id, cid, listing_kind, provider_peer_id, provider_epm_cid, title, description,
			data_types, tags, coverage, sample_cid, sample_record_count,
			access_type, encryption_required, delivery_methods, protected_delivery, pricing,
			accepted_payments, reputation, created_at, updated_at, version,
			active, expires_at, terms_cid, license, signature, source_peer_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		listing.ListingID, cid, listing.ListingKind, listing.ProviderPeerID, listing.ProviderEPMCID,
		listing.Title, listing.Description,
		string(dataTypesJSON), string(tagsJSON), string(coverageJSON),
		listing.SampleCID, listing.SampleRecordCount,
		listing.AccessType, listing.EncryptionRequired,
		string(deliveryMethodsJSON), string(protectedDeliveryJSON), string(pricingJSON),
		string(acceptedPaymentsJSON), string(reputationJSON),
		listing.CreatedAt.Unix(), listing.UpdatedAt.Unix(),
		listing.Version, listing.Active, listing.ExpiresAt.Unix(),
		listing.TermsCID, listing.License, listing.Signature, listing.SourcePeerID,
	)
	if err != nil {
		return fmt.Errorf("failed to index listing: %w", err)
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

	log.Infof("Created listing: %s (CID: %s)", listing.ListingID, cid)
	return nil
}

// GetListing retrieves a listing by ID from the index.
func (s *Store) GetListing(listingID string) (*Listing, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`
		SELECT listing_id, listing_kind, provider_peer_id, provider_epm_cid, title, description,
			data_types, tags, coverage, sample_cid, sample_record_count,
			access_type, encryption_required, delivery_methods, protected_delivery, pricing,
			accepted_payments, reputation, created_at, updated_at, version,
			active, expires_at, terms_cid, license, signature
		FROM storefront_listings WHERE listing_id = ?
	`, listingID)

	return s.scanListing(row)
}

func (s *Store) scanListing(row *sql.Row) (*Listing, error) {
	var listing Listing
	var dataTypesJSON, tagsJSON, coverageJSON, deliveryMethodsJSON string
	var protectedDeliveryJSON, pricingJSON, acceptedPaymentsJSON, reputationJSON string
	var createdAt, updatedAt, expiresAt int64

	err := row.Scan(
		&listing.ListingID, &listing.ListingKind, &listing.ProviderPeerID, &listing.ProviderEPMCID,
		&listing.Title, &listing.Description,
		&dataTypesJSON, &tagsJSON, &coverageJSON,
		&listing.SampleCID, &listing.SampleRecordCount,
		&listing.AccessType, &listing.EncryptionRequired,
		&deliveryMethodsJSON, &protectedDeliveryJSON, &pricingJSON,
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
	json.Unmarshal([]byte(protectedDeliveryJSON), &listing.ProtectedDelivery)
	json.Unmarshal([]byte(pricingJSON), &listing.Pricing)
	json.Unmarshal([]byte(acceptedPaymentsJSON), &listing.AcceptedPayments)
	json.Unmarshal([]byte(reputationJSON), &listing.Reputation)
	listing.CreatedAt = time.Unix(createdAt, 0)
	listing.UpdatedAt = time.Unix(updatedAt, 0)
	listing.ExpiresAt = time.Unix(expiresAt, 0)
	if listing.ListingKind == "" {
		listing.ListingKind = ListingKindDataStream
	}

	return &listing, nil
}

// SearchListings searches listings with filters using the index tables.
func (s *Store) SearchListings(query *SearchQuery) (*SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var conditions []string
	var args []interface{}

	conditions = append(conditions, "active = 1")

	if len(query.DataTypes) > 0 {
		placeholders := make([]string, len(query.DataTypes))
		for i, dt := range query.DataTypes {
			placeholders[i] = "data_types LIKE ?"
			args = append(args, "%"+dt+"%")
		}
		conditions = append(conditions, "("+strings.Join(placeholders, " OR ")+")")
	}

	if len(query.AccessTypes) > 0 {
		placeholders := make([]string, len(query.AccessTypes))
		for i, at := range query.AccessTypes {
			placeholders[i] = "access_type = ?"
			args = append(args, at)
		}
		conditions = append(conditions, "("+strings.Join(placeholders, " OR ")+")")
	}

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
		sanitized := sanitizeFTS5Query(query.SearchText)
		if sanitized == "" {
			return &SearchResult{Listings: []Listing{}, Total: 0}, nil
		}
		rows, err := s.db.Query(`
			SELECT listing_id FROM storefront_listings_fts
			WHERE storefront_listings_fts MATCH ?
		`, sanitized)
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
			return &SearchResult{Listings: []Listing{}, Total: 0}, nil
		}
	}

	whereClause := strings.Join(conditions, " AND ")

	var total int
	countQuery := "SELECT COUNT(*) FROM storefront_listings WHERE " + whereClause
	s.db.QueryRow(countQuery, args...).Scan(&total)

	// Sort — strict allowlist to prevent SQL injection via ORDER BY
	var orderByAllowlist = map[string]string{
		"price":   "pricing",
		"rating":  "reputation",
		"updated": "updated_at",
	}
	orderBy := "updated_at DESC"
	if col, ok := orderByAllowlist[query.SortBy]; ok {
		orderBy = col
		if query.SortDesc {
			orderBy += " DESC"
		}
	}

	limit := 20
	if query.Limit > 0 && query.Limit <= 100 {
		limit = query.Limit
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > 10000 {
		offset = 10000
	}

	querySQL := fmt.Sprintf(`
		SELECT listing_id, listing_kind, provider_peer_id, provider_epm_cid, title, description,
			data_types, tags, coverage, sample_cid, sample_record_count,
			access_type, encryption_required, delivery_methods, protected_delivery, pricing,
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
		var protectedDeliveryJSON, pricingJSON, acceptedPaymentsJSON, reputationJSON string
		var createdAt, updatedAt, expiresAt int64

		err := rows.Scan(
			&listing.ListingID, &listing.ListingKind, &listing.ProviderPeerID, &listing.ProviderEPMCID,
			&listing.Title, &listing.Description,
			&dataTypesJSON, &tagsJSON, &coverageJSON,
			&listing.SampleCID, &listing.SampleRecordCount,
			&listing.AccessType, &listing.EncryptionRequired,
			&deliveryMethodsJSON, &protectedDeliveryJSON, &pricingJSON,
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
		json.Unmarshal([]byte(protectedDeliveryJSON), &listing.ProtectedDelivery)
		json.Unmarshal([]byte(pricingJSON), &listing.Pricing)
		json.Unmarshal([]byte(acceptedPaymentsJSON), &listing.AcceptedPayments)
		json.Unmarshal([]byte(reputationJSON), &listing.Reputation)
		listing.CreatedAt = time.Unix(createdAt, 0)
		listing.UpdatedAt = time.Unix(updatedAt, 0)
		listing.ExpiresAt = time.Unix(expiresAt, 0)
		if listing.ListingKind == "" {
			listing.ListingKind = ListingKindDataStream
		}

		listings = append(listings, listing)
	}

	return &SearchResult{
		Listings: listings,
		Total:    total,
		Facets:   SearchFacets{},
	}, nil
}

// CreateGrant creates a new access grant. Stores through FlatSQL and updates the index.
func (s *Store) CreateGrant(grant *AccessGrant) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cid, err := s.storeRecordToFlatSQL(SchemaACL, grant, grant.ProviderPeerID, grant.ProviderSignature)
	if err != nil {
		log.Warnf("FlatSQL store failed for grant %s: %v", grant.GrantID, err)
	}

	_, err = s.db.Exec(`
		INSERT INTO storefront_grants (
			grant_id, cid, listing_id, tier_name, buyer_peer_id, buyer_encryption_pubkey,
			key_algorithm, access_type, rate_limit, max_records_per_request,
			granted_at, expires_at, status, payment_tx_hash, payment_method,
			payment_amount, payment_currency, payment_chain, next_renewal,
			auto_renew, renewal_count, total_requests, total_records,
			last_access, delivery_topic, created_at, updated_at, notes,
			provider_signature, provider_peer_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		grant.GrantID, cid, grant.ListingID, grant.TierName, grant.BuyerPeerID,
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
		return fmt.Errorf("failed to index grant: %w", err)
	}

	log.Infof("Created grant: %s (CID: %s)", grant.GrantID, cid)
	return nil
}

// GetGrant retrieves a grant by ID from the index.
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

// GetGrantsByBuyer retrieves all grants for a buyer.
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

// CreatePurchaseRequest creates a new purchase request. Stores through FlatSQL.
func (s *Store) CreatePurchaseRequest(req *PurchaseRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cid, err := s.storeRecordToFlatSQL(SchemaPUR, req, req.BuyerPeerID, req.BuyerSignature)
	if err != nil {
		log.Warnf("FlatSQL store failed for purchase %s: %v", req.RequestID, err)
	}

	_, err = s.db.Exec(`
		INSERT INTO storefront_purchases (
			request_id, cid, listing_id, tier_name, buyer_peer_id, buyer_encryption_pubkey,
			key_algorithm, buyer_email, payment_method, payment_amount, payment_currency,
			payment_tx_hash, payment_chain, sender_address, confirmation_block,
			payment_intent_id, credits_transaction_id, status, status_message,
			created_at, updated_at, payment_deadline, payment_confirmed_at,
			grant_issued_at, grant_id, provider_peer_id, provider_acknowledged_at,
			preferred_delivery_method, webhook_url, buyer_signature, provider_signature
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		req.RequestID, cid, req.ListingID, req.TierName, req.BuyerPeerID,
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
		return fmt.Errorf("failed to index purchase request: %w", err)
	}

	log.Infof("Created purchase request: %s (CID: %s)", req.RequestID, cid)
	return nil
}

// UpdatePurchaseStatus updates the status of a purchase request in the index.
func (s *Store) UpdatePurchaseStatus(requestID string, status PurchaseStatus, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	if status == PurchaseStatusPaymentConfirmed || status == PurchaseStatusCompleted {
		_, err := s.db.Exec(`
			UPDATE storefront_purchases
			SET status = ?, status_message = ?, payment_confirmed_at = CASE
					WHEN payment_confirmed_at IS NULL OR payment_confirmed_at = 0 THEN ?
					ELSE payment_confirmed_at
				END,
				updated_at = ?
			WHERE request_id = ?
		`, status, message, now, now, requestID)
		if err != nil {
			return fmt.Errorf("failed to update purchase status: %w", err)
		}
		return nil
	}

	_, err := s.db.Exec(`
		UPDATE storefront_purchases SET status = ?, status_message = ?, updated_at = ?
		WHERE request_id = ?
	`, status, message, now, requestID)
	if err != nil {
		return fmt.Errorf("failed to update purchase status: %w", err)
	}

	return nil
}

func (s *Store) CreatePaymentAuditEvent(event *PaymentAuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO storefront_payment_audit (
			event_id, request_id, event_type, actor_peer_id, reference,
			message, purchase_status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, event.EventID, event.RequestID, event.EventType, event.ActorPeerID,
		event.Reference, event.Message, event.PurchaseStatus, event.CreatedAt.Unix())
	if err != nil {
		return fmt.Errorf("failed to insert payment audit event: %w", err)
	}
	return nil
}

func (s *Store) GetPaymentAuditEvents(requestID string) ([]*PaymentAuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT event_id, request_id, event_type, actor_peer_id, reference,
			message, purchase_status, created_at
		FROM storefront_payment_audit
		WHERE request_id = ?
		ORDER BY created_at ASC, rowid ASC
	`, requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to query payment audit events: %w", err)
	}
	defer rows.Close()

	var events []*PaymentAuditEvent
	for rows.Next() {
		var event PaymentAuditEvent
		var createdAt int64
		if err := rows.Scan(&event.EventID, &event.RequestID, &event.EventType,
			&event.ActorPeerID, &event.Reference, &event.Message,
			&event.PurchaseStatus, &createdAt); err != nil {
			return nil, fmt.Errorf("failed to scan payment audit event: %w", err)
		}
		event.CreatedAt = time.Unix(createdAt, 0)
		events = append(events, &event)
	}
	return events, nil
}

func (s *Store) RecordPaymentWebhookEvent(provider, eventID, requestID, eventType string) (bool, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return true, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		INSERT OR IGNORE INTO storefront_payment_webhook_events (
			event_id, provider, request_id, event_type, processed_at
		) VALUES (?, ?, ?, ?, ?)
	`, eventID, provider, requestID, eventType, time.Now().Unix())
	if err != nil {
		return false, fmt.Errorf("failed to record webhook event: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to read webhook event insert result: %w", err)
	}
	return affected == 1, nil
}

func (s *Store) CreateCryptoBuyerIntent(intent *CryptoBuyerIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO storefront_crypto_intents (
			reference, request_id, chain, asset, asset_contract, native_asset,
			amount, recipient, method, created_at, expires_at, used_at, tx_hash,
			intent_digest, intent_signature
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, intent.Reference, intent.RequestID, intent.Chain, intent.Asset, intent.AssetContract,
		intent.NativeAsset, intent.Amount, intent.Recipient, intent.Method,
		intent.CreatedAt.Unix(), intent.ExpiresAt.Unix(), unixOrZero(intent.UsedAt),
		intent.TxHash, intent.IntentDigest, intent.IntentSig)
	if err != nil {
		return fmt.Errorf("failed to create crypto intent: %w", err)
	}
	return nil
}

func (s *Store) GetCryptoBuyerIntent(reference string) (*CryptoBuyerIntent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var intent CryptoBuyerIntent
	var createdAt, expiresAt, usedAt int64
	var nativeAsset int
	err := s.db.QueryRow(`
		SELECT reference, request_id, chain, asset, asset_contract, native_asset,
			amount, recipient, method, created_at, expires_at, used_at, tx_hash,
			intent_digest, intent_signature
		FROM storefront_crypto_intents WHERE reference = ?
	`, reference).Scan(&intent.Reference, &intent.RequestID, &intent.Chain,
		&intent.Asset, &intent.AssetContract, &nativeAsset, &intent.Amount,
		&intent.Recipient, &intent.Method, &createdAt, &expiresAt, &usedAt,
		&intent.TxHash, &intent.IntentDigest, &intent.IntentSig)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get crypto intent: %w", err)
	}
	intent.CreatedAt = time.Unix(createdAt, 0)
	intent.ExpiresAt = time.Unix(expiresAt, 0)
	intent.UsedAt = time.Unix(usedAt, 0)
	intent.NativeAsset = nativeAsset != 0
	return &intent, nil
}

func (s *Store) MarkCryptoBuyerIntentUsed(reference, requestID, txHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		UPDATE storefront_crypto_intents
		SET used_at = ?, tx_hash = ?
		WHERE reference = ? AND request_id = ? AND COALESCE(used_at, 0) = 0
	`, time.Now().Unix(), txHash, reference, requestID)
	if err != nil {
		return fmt.Errorf("failed to mark crypto intent used: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to read crypto intent update result: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("crypto payment reference reused or not found")
	}
	return nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// CreateReview creates a new review. Stores through FlatSQL.
func (s *Store) CreateReview(review *Review) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cid, err := s.storeRecordToFlatSQL(SchemaREV, review, review.ReviewerPeerID, review.ReviewerSignature)
	if err != nil {
		log.Warnf("FlatSQL store failed for review %s: %v", review.ReviewID, err)
	}

	qualityMetricsJSON, _ := json.Marshal(review.QualityMetrics)

	_, err = s.db.Exec(`
		INSERT INTO storefront_reviews (
			review_id, cid, listing_id, reviewer_peer_id, rating, title, content,
			quality_metrics, acl_grant_id, verified_purchase, created_at,
			updated_at, status, helpful_count, not_helpful_count,
			provider_response, provider_response_at, flagged_count,
			moderation_notes, reviewer_signature
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		review.ReviewID, cid, review.ListingID, review.ReviewerPeerID, review.Rating,
		review.Title, review.Content, string(qualityMetricsJSON),
		review.ACLGrantID, review.VerifiedPurchase, review.CreatedAt.Unix(),
		review.UpdatedAt.Unix(), review.Status, review.HelpfulCount,
		review.NotHelpfulCount, review.ProviderResponse,
		review.ProviderResponseAt.Unix(), review.FlaggedCount,
		review.ModerationNotes, review.ReviewerSignature,
	)
	if err != nil {
		return fmt.Errorf("failed to index review: %w", err)
	}

	log.Infof("Created review: %s (CID: %s)", review.ReviewID, cid)
	return nil
}

// GetReviewsForListing retrieves reviews for a listing from the index.
func (s *Store) GetReviewsForListing(listingID string, limit, offset int) ([]*Review, *ReviewStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

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

	// Aggregate stats
	stats := &ReviewStats{ListingID: listingID}
	s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(AVG(rating) * 10, 0),
			SUM(CASE WHEN verified_purchase = 1 THEN 1 ELSE 0 END),
			MAX(created_at)
		FROM storefront_reviews WHERE listing_id = ? AND status = 0
	`, listingID).Scan(&stats.TotalReviews, &stats.AverageRatingX10,
		&stats.VerifiedReviews, &stats.LastReviewAt)

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

// GetModerationReviews retrieves reviews needing moderation or admin attention.
func (s *Store) GetModerationReviews(limit, offset int) ([]*Review, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM storefront_reviews WHERE status != 0 OR flagged_count > 0`).Scan(&total)
	rows, err := s.db.Query(`
		SELECT review_id, listing_id, reviewer_peer_id, rating, title, content,
			quality_metrics, acl_grant_id, verified_purchase, created_at,
			updated_at, status, helpful_count, not_helpful_count,
			provider_response, provider_response_at, flagged_count,
			moderation_notes, reviewer_signature
		FROM storefront_reviews
		WHERE status != 0 OR flagged_count > 0
		ORDER BY updated_at DESC LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query moderation reviews: %w", err)
	}
	defer rows.Close()

	reviews, err := scanReviewRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return reviews, total, nil
}

// GetCreditsBalance retrieves the credits balance for a peer.
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
			return &CreditsBalance{PeerID: peerID}, nil
		}
		return nil, fmt.Errorf("failed to get credits balance: %w", err)
	}

	balance.UpdatedAt = time.Unix(updatedAt, 0)
	return &balance, nil
}

// UpdateCreditsBalance updates a peer's credits balance.
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

// AtomicDeductCredits atomically checks and deducts credits.
func (s *Store) AtomicDeductCredits(peerID string, amount uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()

	result, err := s.db.Exec(`
		UPDATE storefront_credits
		SET balance = balance - ?, total_spent = total_spent + ?, updated_at = ?
		WHERE peer_id = ? AND balance >= ?
	`, amount, amount, now, peerID, amount)
	if err != nil {
		return fmt.Errorf("failed to deduct credits: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check deduction result: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("insufficient credits for peer %s (need %d)", peerID, amount)
	}

	return nil
}

// GetPurchaseRequest retrieves a purchase request by ID.
func (s *Store) GetPurchaseRequest(requestID string) (*PurchaseRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var req PurchaseRequest
	var createdAt, updatedAt, paymentDeadline, paymentConfirmedAt, grantIssuedAt, providerAcknowledgedAt int64

	err := s.db.QueryRow(`
		SELECT request_id, listing_id, tier_name, buyer_peer_id, buyer_encryption_pubkey,
			key_algorithm, buyer_email, payment_method, payment_amount, payment_currency,
			payment_tx_hash, payment_chain, sender_address, confirmation_block,
			payment_intent_id, credits_transaction_id, status, status_message,
			created_at, updated_at, payment_deadline, payment_confirmed_at,
			grant_issued_at, grant_id, provider_peer_id, provider_acknowledged_at,
			preferred_delivery_method, webhook_url, buyer_signature, provider_signature
		FROM storefront_purchases WHERE request_id = ?
	`, requestID).Scan(
		&req.RequestID, &req.ListingID, &req.TierName, &req.BuyerPeerID,
		&req.BuyerEncryptionPubkey, &req.KeyAlgorithm, &req.BuyerEmail,
		&req.PaymentMethod, &req.PaymentAmount, &req.PaymentCurrency,
		&req.PaymentTxHash, &req.PaymentChain, &req.SenderAddress, &req.ConfirmationBlock,
		&req.PaymentIntentID, &req.CreditsTransactionID, &req.Status, &req.StatusMessage,
		&createdAt, &updatedAt, &paymentDeadline, &paymentConfirmedAt,
		&grantIssuedAt, &req.GrantID, &req.ProviderPeerID, &providerAcknowledgedAt,
		&req.PreferredDeliveryMethod, &req.WebhookURL,
		&req.BuyerSignature, &req.ProviderSignature,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get purchase request: %w", err)
	}

	req.CreatedAt = time.Unix(createdAt, 0)
	req.UpdatedAt = time.Unix(updatedAt, 0)
	req.PaymentDeadline = time.Unix(paymentDeadline, 0)
	req.PaymentConfirmedAt = time.Unix(paymentConfirmedAt, 0)
	req.GrantIssuedAt = time.Unix(grantIssuedAt, 0)
	req.ProviderAcknowledgedAt = time.Unix(providerAcknowledgedAt, 0)

	return &req, nil
}

// UpdatePurchasePayment updates payment details on a purchase request.
func (s *Store) UpdatePurchasePayment(requestID, txHash, chain, senderAddress string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE storefront_purchases
		SET payment_tx_hash = ?, payment_chain = ?, sender_address = ?, updated_at = ?
		WHERE request_id = ?
	`, txHash, chain, senderAddress, time.Now().Unix(), requestID)
	if err != nil {
		return fmt.Errorf("failed to update purchase payment: %w", err)
	}
	return nil
}

// UpdatePurchaseConfirmationBlock records the chain block/slot at which payment settled.
func (s *Store) UpdatePurchaseConfirmationBlock(requestID string, confirmationBlock uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE storefront_purchases
		SET confirmation_block = ?, updated_at = ?
		WHERE request_id = ?
	`, confirmationBlock, time.Now().Unix(), requestID)
	if err != nil {
		return fmt.Errorf("failed to update purchase confirmation block: %w", err)
	}
	return nil
}

// UpdatePurchaseCreditsTransaction updates the credits transaction ID.
func (s *Store) UpdatePurchaseCreditsTransaction(requestID, txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE storefront_purchases
		SET credits_transaction_id = ?, updated_at = ?
		WHERE request_id = ?
	`, txID, time.Now().Unix(), requestID)
	if err != nil {
		return fmt.Errorf("failed to update credits transaction: %w", err)
	}
	return nil
}

// UpdatePurchaseFiatIntent updates the fiat payment intent ID.
func (s *Store) UpdatePurchaseFiatIntent(requestID, intentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE storefront_purchases
		SET payment_intent_id = ?, updated_at = ?
		WHERE request_id = ?
	`, intentID, time.Now().Unix(), requestID)
	if err != nil {
		return fmt.Errorf("failed to update fiat intent: %w", err)
	}
	return nil
}

// UpdatePurchaseGrant updates the grant ID on a purchase request.
func (s *Store) UpdatePurchaseGrant(requestID, grantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE storefront_purchases
		SET grant_id = ?, grant_issued_at = ?, status = ?, updated_at = ?
		WHERE request_id = ?
	`, grantID, time.Now().Unix(), PurchaseStatusCompleted, time.Now().Unix(), requestID)
	if err != nil {
		return fmt.Errorf("failed to update purchase grant: %w", err)
	}
	return nil
}

// GetProviderPurchases retrieves all purchases for a provider.
func (s *Store) GetProviderPurchases(providerPeerID string, limit, offset int) ([]*PurchaseRequest, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM storefront_purchases WHERE provider_peer_id = ?`, providerPeerID).Scan(&total)

	rows, err := s.db.Query(`
		SELECT request_id, listing_id, tier_name, buyer_peer_id, buyer_encryption_pubkey,
			key_algorithm, buyer_email, payment_method, payment_amount, payment_currency,
			payment_tx_hash, payment_chain, sender_address, confirmation_block,
			payment_intent_id, credits_transaction_id, status, status_message,
			created_at, updated_at, payment_deadline, payment_confirmed_at,
			grant_issued_at, grant_id, provider_peer_id, provider_acknowledged_at,
			preferred_delivery_method, webhook_url, buyer_signature, provider_signature
		FROM storefront_purchases WHERE provider_peer_id = ?
		ORDER BY created_at DESC LIMIT ? OFFSET ?
	`, providerPeerID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query provider purchases: %w", err)
	}
	defer rows.Close()

	var purchases []*PurchaseRequest
	for rows.Next() {
		var req PurchaseRequest
		var createdAt, updatedAt, paymentDeadline, paymentConfirmedAt, grantIssuedAt, providerAcknowledgedAt int64

		err := rows.Scan(
			&req.RequestID, &req.ListingID, &req.TierName, &req.BuyerPeerID,
			&req.BuyerEncryptionPubkey, &req.KeyAlgorithm, &req.BuyerEmail,
			&req.PaymentMethod, &req.PaymentAmount, &req.PaymentCurrency,
			&req.PaymentTxHash, &req.PaymentChain, &req.SenderAddress, &req.ConfirmationBlock,
			&req.PaymentIntentID, &req.CreditsTransactionID, &req.Status, &req.StatusMessage,
			&createdAt, &updatedAt, &paymentDeadline, &paymentConfirmedAt,
			&grantIssuedAt, &req.GrantID, &req.ProviderPeerID, &providerAcknowledgedAt,
			&req.PreferredDeliveryMethod, &req.WebhookURL,
			&req.BuyerSignature, &req.ProviderSignature,
		)
		if err != nil {
			log.Warnf("Failed to scan purchase row: %v", err)
			continue
		}

		req.CreatedAt = time.Unix(createdAt, 0)
		req.UpdatedAt = time.Unix(updatedAt, 0)
		req.PaymentDeadline = time.Unix(paymentDeadline, 0)
		req.PaymentConfirmedAt = time.Unix(paymentConfirmedAt, 0)
		req.GrantIssuedAt = time.Unix(grantIssuedAt, 0)
		req.ProviderAcknowledgedAt = time.Unix(providerAcknowledgedAt, 0)

		purchases = append(purchases, &req)
	}

	return purchases, total, nil
}

// GetBuyerPurchases retrieves all purchases for a buyer.
func (s *Store) GetBuyerPurchases(buyerPeerID string, limit, offset int) ([]*PurchaseRequest, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM storefront_purchases WHERE buyer_peer_id = ?`, buyerPeerID).Scan(&total)

	rows, err := s.db.Query(`
		SELECT request_id, listing_id, tier_name, buyer_peer_id, buyer_encryption_pubkey,
			key_algorithm, buyer_email, payment_method, payment_amount, payment_currency,
			payment_tx_hash, payment_chain, sender_address, confirmation_block,
			payment_intent_id, credits_transaction_id, status, status_message,
			created_at, updated_at, payment_deadline, payment_confirmed_at,
			grant_issued_at, grant_id, provider_peer_id, provider_acknowledged_at,
			preferred_delivery_method, webhook_url, buyer_signature, provider_signature
		FROM storefront_purchases WHERE buyer_peer_id = ?
		ORDER BY created_at DESC LIMIT ? OFFSET ?
	`, buyerPeerID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query buyer purchases: %w", err)
	}
	defer rows.Close()

	purchases, err := scanPurchaseRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return purchases, total, nil
}

// GetPurchasesByStatuses retrieves purchases matching any status for admin surfaces.
func (s *Store) GetPurchasesByStatuses(statuses []PurchaseStatus, limit, offset int) ([]*PurchaseRequest, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(statuses) == 0 {
		return []*PurchaseRequest{}, 0, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]interface{}, 0, len(statuses)+2)
	for i, status := range statuses {
		placeholders[i] = "?"
		args = append(args, status)
	}
	where := "status IN (" + strings.Join(placeholders, ",") + ")"

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM storefront_purchases WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count purchases by status: %w", err)
	}
	args = append(args, limit, offset)
	rows, err := s.db.Query(`
		SELECT request_id, listing_id, tier_name, buyer_peer_id, buyer_encryption_pubkey,
			key_algorithm, buyer_email, payment_method, payment_amount, payment_currency,
			payment_tx_hash, payment_chain, sender_address, confirmation_block,
			payment_intent_id, credits_transaction_id, status, status_message,
			created_at, updated_at, payment_deadline, payment_confirmed_at,
			grant_issued_at, grant_id, provider_peer_id, provider_acknowledged_at,
			preferred_delivery_method, webhook_url, buyer_signature, provider_signature
		FROM storefront_purchases WHERE `+where+`
		ORDER BY updated_at DESC LIMIT ? OFFSET ?
	`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query purchases by status: %w", err)
	}
	defer rows.Close()

	purchases, err := scanPurchaseRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return purchases, total, nil
}

func scanPurchaseRows(rows *sql.Rows) ([]*PurchaseRequest, error) {
	var purchases []*PurchaseRequest
	for rows.Next() {
		var req PurchaseRequest
		var createdAt, updatedAt, paymentDeadline, paymentConfirmedAt, grantIssuedAt, providerAcknowledgedAt int64

		err := rows.Scan(
			&req.RequestID, &req.ListingID, &req.TierName, &req.BuyerPeerID,
			&req.BuyerEncryptionPubkey, &req.KeyAlgorithm, &req.BuyerEmail,
			&req.PaymentMethod, &req.PaymentAmount, &req.PaymentCurrency,
			&req.PaymentTxHash, &req.PaymentChain, &req.SenderAddress, &req.ConfirmationBlock,
			&req.PaymentIntentID, &req.CreditsTransactionID, &req.Status, &req.StatusMessage,
			&createdAt, &updatedAt, &paymentDeadline, &paymentConfirmedAt,
			&grantIssuedAt, &req.GrantID, &req.ProviderPeerID, &providerAcknowledgedAt,
			&req.PreferredDeliveryMethod, &req.WebhookURL,
			&req.BuyerSignature, &req.ProviderSignature,
		)
		if err != nil {
			log.Warnf("Failed to scan purchase row: %v", err)
			continue
		}

		req.CreatedAt = time.Unix(createdAt, 0)
		req.UpdatedAt = time.Unix(updatedAt, 0)
		req.PaymentDeadline = time.Unix(paymentDeadline, 0)
		req.PaymentConfirmedAt = time.Unix(paymentConfirmedAt, 0)
		req.GrantIssuedAt = time.Unix(grantIssuedAt, 0)
		req.ProviderAcknowledgedAt = time.Unix(providerAcknowledgedAt, 0)

		purchases = append(purchases, &req)
	}
	return purchases, rows.Err()
}

func scanReviewRows(rows *sql.Rows) ([]*Review, error) {
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
	return reviews, rows.Err()
}

// CreateCreditsTransaction records a credits transaction.
func (s *Store) CreateCreditsTransaction(tx *CreditsTransaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO storefront_credits_transactions (
			transaction_id, from_peer_id, to_peer_id, amount, type, reference, created_at, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, tx.TransactionID, tx.FromPeerID, tx.ToPeerID, tx.Amount,
		tx.Type, tx.Reference, tx.CreatedAt.Unix(), tx.Status)
	if err != nil {
		return fmt.Errorf("failed to create credits transaction: %w", err)
	}
	return nil
}

// GetCreditsTransactions retrieves credit transactions for a peer.
func (s *Store) GetCreditsTransactions(peerID string, limit, offset int) ([]*CreditsTransaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT transaction_id, from_peer_id, to_peer_id, amount, type, reference, created_at, status
		FROM storefront_credits_transactions
		WHERE from_peer_id = ? OR to_peer_id = ?
		ORDER BY created_at DESC LIMIT ? OFFSET ?
	`, peerID, peerID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query transactions: %w", err)
	}
	defer rows.Close()

	var txs []*CreditsTransaction
	for rows.Next() {
		var tx CreditsTransaction
		var createdAt int64
		err := rows.Scan(&tx.TransactionID, &tx.FromPeerID, &tx.ToPeerID,
			&tx.Amount, &tx.Type, &tx.Reference, &createdAt, &tx.Status)
		if err != nil {
			continue
		}
		tx.CreatedAt = time.Unix(createdAt, 0)
		txs = append(txs, &tx)
	}
	return txs, nil
}

// UpdateGrantUsage updates usage tracking on a grant.
func (s *Store) UpdateGrantUsage(grantID string, requestsIncrement, recordsIncrement int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE storefront_grants
		SET total_requests = total_requests + ?,
			total_records = total_records + ?,
			last_access = ?,
			updated_at = ?
		WHERE grant_id = ?
	`, requestsIncrement, recordsIncrement, time.Now().Unix(), time.Now().Unix(), grantID)
	if err != nil {
		return fmt.Errorf("failed to update grant usage: %w", err)
	}
	return nil
}

// UpdateListingReputation updates the reputation snapshot on a listing.
func (s *Store) UpdateListingReputation(listingID string, rep ProviderReputation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	reputationJSON, _ := json.Marshal(rep)
	_, err := s.db.Exec(`
		UPDATE storefront_listings SET reputation = ?, updated_at = ? WHERE listing_id = ?
	`, string(reputationJSON), time.Now().Unix(), listingID)
	if err != nil {
		return fmt.Errorf("failed to update listing reputation: %w", err)
	}
	return nil
}

// GetProviderGrants retrieves all grants issued by a provider.
func (s *Store) GetProviderGrants(providerPeerID string, limit, offset int) ([]*AccessGrant, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM storefront_grants WHERE provider_peer_id = ?`, providerPeerID).Scan(&total)

	rows, err := s.db.Query(`
		SELECT grant_id, listing_id, tier_name, buyer_peer_id, buyer_encryption_pubkey,
			key_algorithm, access_type, rate_limit, max_records_per_request,
			granted_at, expires_at, status, payment_tx_hash, payment_method,
			payment_amount, payment_currency, payment_chain, next_renewal,
			auto_renew, renewal_count, total_requests, total_records,
			last_access, delivery_topic, created_at, updated_at, notes,
			provider_signature, provider_peer_id
		FROM storefront_grants WHERE provider_peer_id = ?
		ORDER BY created_at DESC LIMIT ? OFFSET ?
	`, providerPeerID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query provider grants: %w", err)
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
			log.Warnf("Failed to scan provider grant row: %v", err)
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

	return grants, total, nil
}

// UpdateReviewVote updates the helpfulness vote count on a review.
func (s *Store) UpdateReviewVote(reviewID string, helpful bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var col string
	if helpful {
		col = "helpful_count"
	} else {
		col = "not_helpful_count"
	}

	_, err := s.db.Exec(fmt.Sprintf(`
		UPDATE storefront_reviews SET %s = %s + 1, updated_at = ? WHERE review_id = ?
	`, col, col), time.Now().Unix(), reviewID)
	if err != nil {
		return fmt.Errorf("failed to update review vote: %w", err)
	}
	return nil
}

// AddProviderResponse adds a provider response to a review.
func (s *Store) AddProviderResponse(reviewID, response string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE storefront_reviews
		SET provider_response = ?, provider_response_at = ?, updated_at = ?
		WHERE review_id = ?
	`, response, time.Now().Unix(), time.Now().Unix(), reviewID)
	if err != nil {
		return fmt.Errorf("failed to add provider response: %w", err)
	}
	return nil
}

// UpdateListingActive updates the active status of a listing.
func (s *Store) UpdateListingActive(listingID string, active bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE storefront_listings SET active = ?, updated_at = ? WHERE listing_id = ?
	`, active, time.Now().Unix(), listingID)
	if err != nil {
		return fmt.Errorf("failed to update listing active: %w", err)
	}
	return nil
}

// GetProviderEarnings returns total earnings for a provider.
func (s *Store) GetProviderEarnings(providerPeerID string) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var total uint64
	err := s.db.QueryRow(`
		SELECT COALESCE(SUM(payment_amount), 0)
		FROM storefront_grants WHERE provider_peer_id = ? AND status = 0
	`, providerPeerID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("failed to get provider earnings: %w", err)
	}
	return total, nil
}

// FlatStore returns the underlying FlatSQLStore for direct access (e.g., DHT exchange).
func (s *Store) FlatStore() *storage.FlatSQLStore {
	return s.flatStore
}

// Close closes the index database connection.
// Does NOT close FlatSQLStore (it's shared with the rest of the system).
func (s *Store) Close() error {
	return s.db.Close()
}
