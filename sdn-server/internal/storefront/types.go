package storefront

import (
	"time"
)

// AccessType represents the type of data access
type AccessType int

const (
	AccessTypeOneTime AccessType = iota
	AccessTypeSubscription
	AccessTypeStreaming
	AccessTypeQuery
)

// PaymentMethod represents supported payment methods
type PaymentMethod int

const (
	PaymentMethodCryptoETH PaymentMethod = iota
	PaymentMethodCryptoSOL
	PaymentMethodCryptoBTC
	PaymentMethodCryptoUSDC
	PaymentMethodSDNCredits
	PaymentMethodFiatStripe
	PaymentMethodFree
)

// GrantStatus represents the status of an access grant
type GrantStatus int

const (
	GrantStatusActive GrantStatus = iota
	GrantStatusRevoked
	GrantStatusExpired
	GrantStatusSuspended
	GrantStatusPending
)

// PurchaseStatus represents the status of a purchase request
type PurchaseStatus int

const (
	PurchaseStatusPending PurchaseStatus = iota
	PurchaseStatusPaymentDetected
	PurchaseStatusPaymentConfirmed
	PurchaseStatusCompleted
	PurchaseStatusFailed
	PurchaseStatusCancelled
	PurchaseStatusRefundRequested
	PurchaseStatusRefunded
	PurchaseStatusExpired
)

// ReviewStatus represents the status of a review
type ReviewStatus int

const (
	ReviewStatusPublished ReviewStatus = iota
	ReviewStatusPending
	ReviewStatusFlagged
	ReviewStatusHidden
	ReviewStatusRemoved
)

// SpatialCoverage defines the spatial coverage of data
type SpatialCoverage struct {
	Type          string   `json:"type"`           // global, region, object_list, custom
	Regions       []string `json:"regions"`        // LEO, MEO, GEO, HEO
	ObjectIDs     []string `json:"object_ids"`     // NORAD catalog IDs
	MinAltitudeKm float64  `json:"min_altitude_km"`
	MaxAltitudeKm float64  `json:"max_altitude_km"`
	GeoBounds     []float64 `json:"geo_bounds"` // [min_lat, min_lon, max_lat, max_lon]
}

// TemporalCoverage defines the temporal coverage of data
type TemporalCoverage struct {
	StartEpoch          string `json:"start_epoch"`           // ISO 8601
	EndEpoch            string `json:"end_epoch"`             // ISO 8601
	UpdateFrequency     string `json:"update_frequency"`      // realtime, hourly, daily
	HistoricalDepthDays uint32 `json:"historical_depth_days"`
	LatencySeconds      uint32 `json:"latency_seconds"`
}

// DataCoverage combines spatial and temporal coverage
type DataCoverage struct {
	Spatial  SpatialCoverage  `json:"spatial"`
	Temporal TemporalCoverage `json:"temporal"`
}

// PricingTier represents a pricing tier for a listing
type PricingTier struct {
	Name                 string   `json:"name"`
	PriceAmount          uint64   `json:"price_amount"`
	PriceCurrency        string   `json:"price_currency"`
	DurationDays         uint32   `json:"duration_days"`
	RateLimit            uint32   `json:"rate_limit"`
	MaxRecordsPerRequest uint32   `json:"max_records_per_request"`
	Features             []string `json:"features"`
	Description          string   `json:"description"`
}

// ProviderReputation represents provider reputation metrics
type ProviderReputation struct {
	TotalSales            uint64 `json:"total_sales"`
	AverageRatingX10      uint16 `json:"average_rating_x10"` // 42 = 4.2 stars
	TotalRatings          uint32 `json:"total_ratings"`
	UptimePercentageX100  uint16 `json:"uptime_percentage_x100"` // 9950 = 99.50%
	AvgDeliveryLatencyMs  uint32 `json:"avg_delivery_latency_ms"`
	DisputeCount          uint32 `json:"dispute_count"`
	ProviderSince         uint64 `json:"provider_since"`
}

// Listing represents a storefront listing (STF)
type Listing struct {
	ListingID         string             `json:"listing_id"`
	ProviderPeerID    string             `json:"provider_peer_id"`
	ProviderEPMCID    string             `json:"provider_epm_cid"`
	Title             string             `json:"title"`
	Description       string             `json:"description"`
	DataTypes         []string           `json:"data_types"`
	Tags              []string           `json:"tags"`
	Coverage          DataCoverage       `json:"coverage"`
	SampleCID         string             `json:"sample_cid"`
	SampleRecordCount uint32             `json:"sample_record_count"`
	AccessType        AccessType         `json:"access_type"`
	EncryptionRequired bool              `json:"encryption_required"`
	DeliveryMethods   []string           `json:"delivery_methods"`
	Pricing           []PricingTier      `json:"pricing"`
	AcceptedPayments  []PaymentMethod    `json:"accepted_payments"`
	Reputation        ProviderReputation `json:"reputation"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
	Version           uint32             `json:"version"`
	Active            bool               `json:"active"`
	ExpiresAt         time.Time          `json:"expires_at"`
	TermsCID          string             `json:"terms_cid"`
	License           string             `json:"license"`
	Signature         []byte             `json:"signature"`
	SourcePeerID      string             `json:"source_peer_id,omitempty"` // empty = local, set = discovered from remote peer
}

// AccessGrant represents a data access grant (ACL)
type AccessGrant struct {
	GrantID              string        `json:"grant_id"`
	ListingID            string        `json:"listing_id"`
	TierName             string        `json:"tier_name"`
	BuyerPeerID          string        `json:"buyer_peer_id"`
	BuyerEncryptionPubkey []byte       `json:"buyer_encryption_pubkey"`
	KeyAlgorithm         string        `json:"key_algorithm"`
	AccessType           AccessType    `json:"access_type"`
	RateLimit            uint32        `json:"rate_limit"`
	MaxRecordsPerRequest uint32        `json:"max_records_per_request"`
	GrantedAt            time.Time     `json:"granted_at"`
	ExpiresAt            time.Time     `json:"expires_at"`
	Status               GrantStatus   `json:"status"`
	PaymentTxHash        string        `json:"payment_tx_hash"`
	PaymentMethod        PaymentMethod `json:"payment_method"`
	PaymentAmount        uint64        `json:"payment_amount"`
	PaymentCurrency      string        `json:"payment_currency"`
	PaymentChain         string        `json:"payment_chain"`
	NextRenewal          time.Time     `json:"next_renewal"`
	AutoRenew            bool          `json:"auto_renew"`
	RenewalCount         uint32        `json:"renewal_count"`
	TotalRequests        uint64        `json:"total_requests"`
	TotalRecords         uint64        `json:"total_records"`
	LastAccess           time.Time     `json:"last_access"`
	DeliveryTopic        string        `json:"delivery_topic"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
	Notes                string        `json:"notes"`
	ProviderSignature    []byte        `json:"provider_signature"`
	ProviderPeerID       string        `json:"provider_peer_id"`
}

// PurchaseRequest represents a purchase request (PUR)
type PurchaseRequest struct {
	RequestID             string         `json:"request_id"`
	ListingID             string         `json:"listing_id"`
	TierName              string         `json:"tier_name"`
	BuyerPeerID           string         `json:"buyer_peer_id"`
	BuyerEncryptionPubkey []byte         `json:"buyer_encryption_pubkey"`
	KeyAlgorithm          string         `json:"key_algorithm"`
	BuyerEmail            string         `json:"buyer_email"`
	PaymentMethod         PaymentMethod  `json:"payment_method"`
	PaymentAmount         uint64         `json:"payment_amount"`
	PaymentCurrency       string         `json:"payment_currency"`
	PaymentTxHash         string         `json:"payment_tx_hash"`
	PaymentChain          string         `json:"payment_chain"`
	SenderAddress         string         `json:"sender_address"`
	ConfirmationBlock     uint64         `json:"confirmation_block"`
	PaymentIntentID       string         `json:"payment_intent_id"`
	CreditsTransactionID  string         `json:"credits_transaction_id"`
	Status                PurchaseStatus `json:"status"`
	StatusMessage         string         `json:"status_message"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
	PaymentDeadline       time.Time      `json:"payment_deadline"`
	PaymentConfirmedAt    time.Time      `json:"payment_confirmed_at"`
	GrantIssuedAt         time.Time      `json:"grant_issued_at"`
	GrantID               string         `json:"grant_id"`
	ProviderPeerID        string         `json:"provider_peer_id"`
	ProviderAcknowledgedAt time.Time     `json:"provider_acknowledged_at"`
	PreferredDeliveryMethod string       `json:"preferred_delivery_method"`
	WebhookURL            string         `json:"webhook_url"`
	BuyerSignature        []byte         `json:"buyer_signature"`
	ProviderSignature     []byte         `json:"provider_signature"`
}

// DataQualityMetrics represents data quality assessment
type DataQualityMetrics struct {
	SchemaCompliance    uint8 `json:"schema_compliance"`    // 0-100
	DataFreshness       uint8 `json:"data_freshness"`       // 0-100
	CoverageAccuracy    uint8 `json:"coverage_accuracy"`    // 0-100
	DeliveryReliability uint8 `json:"delivery_reliability"` // 0-100
}

// Review represents a listing review (REV)
type Review struct {
	ReviewID         string             `json:"review_id"`
	ListingID        string             `json:"listing_id"`
	ReviewerPeerID   string             `json:"reviewer_peer_id"`
	Rating           uint8              `json:"rating"` // 1-5
	Title            string             `json:"title"`
	Content          string             `json:"content"`
	QualityMetrics   DataQualityMetrics `json:"quality_metrics"`
	ACLGrantID       string             `json:"acl_grant_id"`
	VerifiedPurchase bool               `json:"verified_purchase"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
	Status           ReviewStatus       `json:"status"`
	HelpfulCount     uint32             `json:"helpful_count"`
	NotHelpfulCount  uint32             `json:"not_helpful_count"`
	ProviderResponse string             `json:"provider_response"`
	ProviderResponseAt time.Time        `json:"provider_response_at"`
	FlaggedCount     uint32             `json:"flagged_count"`
	ModerationNotes  string             `json:"moderation_notes"`
	ReviewerSignature []byte            `json:"reviewer_signature"`
}

// ReviewStats represents aggregated review statistics
type ReviewStats struct {
	ListingID          string             `json:"listing_id"`
	TotalReviews       uint32             `json:"total_reviews"`
	VerifiedReviews    uint32             `json:"verified_reviews"`
	AverageRatingX10   uint16             `json:"average_rating_x10"`
	RatingDistribution [5]uint32          `json:"rating_distribution"` // [1-star, 2-star, ...]
	LastReviewAt       time.Time          `json:"last_review_at"`
	AvgQualityMetrics  DataQualityMetrics `json:"avg_quality_metrics"`
}

// SearchQuery represents a storefront search query
type SearchQuery struct {
	DataTypes       []string     `json:"data_types"`
	PriceMax        float64      `json:"price_max"`
	AccessTypes     []AccessType `json:"access_types"`
	SpatialCoverage []string     `json:"spatial_coverage"` // regions
	ObjectIDs       []string     `json:"object_ids"`
	ProviderPeerIDs []string     `json:"provider_peer_ids"`
	SearchText      string       `json:"search_text"` // full-text search
	SortBy          string       `json:"sort_by"`     // price, rating, updated, relevance
	SortDesc        bool         `json:"sort_desc"`
	Limit           int          `json:"limit"`
	Offset          int          `json:"offset"`
}

// SearchResult represents search results
type SearchResult struct {
	Listings []Listing           `json:"listings"`
	Total    int                 `json:"total"`
	Facets   SearchFacets        `json:"facets"`
}

// SearchFacets represents search facets for filtering
type SearchFacets struct {
	DataTypes   map[string]int `json:"data_types"`
	PriceRanges map[string]int `json:"price_ranges"`
	Providers   map[string]int `json:"providers"`
	AccessTypes map[string]int `json:"access_types"`
}

// DeliveryMethod represents data delivery methods
type DeliveryMethod string

const (
	DeliveryPubSubStream   DeliveryMethod = "PubSubStream"
	DeliveryDirectTransfer DeliveryMethod = "DirectTransfer"
	DeliveryIPFSPin        DeliveryMethod = "IPFSPin"
	DeliveryWebhookPush    DeliveryMethod = "WebhookPush"
)

// CreditsBalance represents a peer's SDN credits balance
type CreditsBalance struct {
	PeerID         string    `json:"peer_id"`
	Balance        uint64    `json:"balance"`
	PendingCredits uint64    `json:"pending_credits"` // In escrow
	TotalEarned    uint64    `json:"total_earned"`
	TotalSpent     uint64    `json:"total_spent"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreditsTransaction represents a credits transaction
type CreditsTransaction struct {
	TransactionID string        `json:"transaction_id"`
	FromPeerID    string        `json:"from_peer_id"`
	ToPeerID      string        `json:"to_peer_id"`
	Amount        uint64        `json:"amount"`
	Type          string        `json:"type"` // purchase, refund, deposit, withdrawal
	Reference     string        `json:"reference"` // purchase_id, etc.
	CreatedAt     time.Time     `json:"created_at"`
	Status        string        `json:"status"`
}
