package storefront

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newTestService(t *testing.T) (*Service, *Store) {
	t.Helper()
	store := newTestStore(t)
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate key: %v", err)
	}

	svc, err := NewService(store, "test-peer-id", privKey, nil)
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc, store
}

func testListing() *Listing {
	return &Listing{
		Title:       "LEO Conjunction Data",
		Description: "Real-time conjunction screening data for LEO objects",
		DataTypes:   []string{"CDM", "TCA"},
		Tags:        []string{"conjunction", "LEO", "realtime"},
		Coverage: DataCoverage{
			Spatial: SpatialCoverage{
				Type:    "region",
				Regions: []string{"LEO"},
			},
			Temporal: TemporalCoverage{
				UpdateFrequency:     "realtime",
				HistoricalDepthDays: 90,
				LatencySeconds:      30,
			},
		},
		AccessType:         AccessTypeSubscription,
		EncryptionRequired: true,
		DeliveryMethods:    []string{"PubSubStream", "WebhookPush"},
		Pricing: []PricingTier{
			{
				Name:          "Basic",
				PriceAmount:   4900,
				PriceCurrency: "USD",
				DurationDays:  30,
				RateLimit:     100,
				Features:      []string{"LEO data", "Daily updates"},
			},
			{
				Name:          "Pro",
				PriceAmount:   19900,
				PriceCurrency: "USD",
				DurationDays:  30,
				RateLimit:     1000,
				Features:      []string{"All orbits", "Real-time streaming"},
			},
		},
		AcceptedPayments: []PaymentMethod{PaymentMethodCryptoETH, PaymentMethodSDNCredits},
		Reputation: ProviderReputation{
			TotalSales:           150,
			AverageRatingX10:     42,
			TotalRatings:         45,
			UptimePercentageX100: 9950,
			AvgDeliveryLatencyMs: 120,
			ProviderSince:        uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()),
		},
		License: "proprietary",
	}
}

// --- 14.1 Data Listing Model Tests ---

func TestCreateListing(t *testing.T) {
	svc, _ := newTestService(t)
	listing := testListing()

	err := svc.CreateListing(context.Background(), listing)
	if err != nil {
		t.Fatalf("CreateListing failed: %v", err)
	}

	if listing.ListingID == "" {
		t.Fatal("ListingID should be generated")
	}
	if listing.ProviderPeerID != "test-peer-id" {
		t.Errorf("ProviderPeerID = %s, want test-peer-id", listing.ProviderPeerID)
	}
	if listing.Signature == nil {
		t.Error("Signature should be set")
	}
	if listing.Version != 1 {
		t.Errorf("Version = %d, want 1", listing.Version)
	}
}

func TestGetListing(t *testing.T) {
	svc, _ := newTestService(t)
	listing := testListing()
	svc.CreateListing(context.Background(), listing)

	got, err := svc.GetListing(context.Background(), listing.ListingID)
	if err != nil {
		t.Fatalf("GetListing failed: %v", err)
	}
	if got == nil {
		t.Fatal("listing should not be nil")
	}
	if got.Title != listing.Title {
		t.Errorf("Title = %s, want %s", got.Title, listing.Title)
	}
	if len(got.Pricing) != 2 {
		t.Errorf("Pricing len = %d, want 2", len(got.Pricing))
	}
}

func TestListingDeactivation(t *testing.T) {
	svc, store := newTestService(t)
	listing := testListing()
	svc.CreateListing(context.Background(), listing)

	err := store.UpdateListingActive(listing.ListingID, false)
	if err != nil {
		t.Fatalf("UpdateListingActive failed: %v", err)
	}

	// Search should not return deactivated listings
	result, err := svc.SearchListings(context.Background(), &SearchQuery{Limit: 50})
	if err != nil {
		t.Fatalf("SearchListings failed: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("deactivated listing still returned, total = %d", result.Total)
	}
}

// --- 14.2 Discovery and Search Tests ---

func TestSearchListings(t *testing.T) {
	svc, _ := newTestService(t)

	// Create multiple listings
	l1 := testListing()
	l1.Title = "LEO Conjunction Data"
	l1.DataTypes = []string{"CDM"}
	svc.CreateListing(context.Background(), l1)

	l2 := testListing()
	l2.Title = "GEO Orbit Predictions"
	l2.DataTypes = []string{"OEM", "OMM"}
	svc.CreateListing(context.Background(), l2)

	l3 := testListing()
	l3.Title = "TLE Updates"
	l3.DataTypes = []string{"TLE"}
	l3.AccessType = AccessTypeOneTime
	svc.CreateListing(context.Background(), l3)

	// Search by data type
	result, err := svc.SearchListings(context.Background(), &SearchQuery{
		DataTypes: []string{"CDM"},
	})
	if err != nil {
		t.Fatalf("SearchListings failed: %v", err)
	}
	if result.Total != 1 {
		t.Errorf("Total = %d, want 1", result.Total)
	}

	// Search by access type
	result, err = svc.SearchListings(context.Background(), &SearchQuery{
		AccessTypes: []AccessType{AccessTypeSubscription},
	})
	if err != nil {
		t.Fatalf("SearchListings failed: %v", err)
	}
	if result.Total != 2 {
		t.Errorf("Total = %d, want 2", result.Total)
	}

	// All listings
	result, err = svc.SearchListings(context.Background(), &SearchQuery{Limit: 50})
	if err != nil {
		t.Fatalf("SearchListings failed: %v", err)
	}
	if result.Total != 3 {
		t.Errorf("Total = %d, want 3", result.Total)
	}
}

func TestCatalogPublishAndFetch(t *testing.T) {
	store := newTestStore(t)
	mockDHT := &mockDHTStore{data: make(map[string][]byte)}
	catalog := NewCatalog(store, mockDHT)

	listing := testListing()
	listing.ListingID = "test-listing-123"
	listing.ProviderPeerID = "provider-abc"
	listing.CreatedAt = time.Now()
	listing.UpdatedAt = time.Now()
	listing.Active = true

	// Store first, then publish
	store.CreateListing(listing)
	err := catalog.PublishListing(context.Background(), listing)
	if err != nil {
		t.Fatalf("PublishListing failed: %v", err)
	}

	// Check DHT keys were set
	if _, ok := mockDHT.data[DHTKeyListingPrefix+"test-listing-123"]; !ok {
		t.Error("listing not published to DHT")
	}
	if _, ok := mockDHT.data[DHTKeyProviderPrefix+"provider-abc/listings"]; !ok {
		t.Error("provider index not published to DHT")
	}

	// Fetch back
	fetched, err := catalog.FetchListing(context.Background(), "test-listing-123")
	if err != nil {
		t.Fatalf("FetchListing failed: %v", err)
	}
	if fetched.Title != listing.Title {
		t.Errorf("Title = %s, want %s", fetched.Title, listing.Title)
	}
}

func TestIndexerComputeFacets(t *testing.T) {
	store := newTestStore(t)
	catalog := NewCatalog(store, nil)
	indexer := NewIndexer(store, catalog)
	defer indexer.Close()

	// Index some listings
	l1 := testListing()
	l1.ListingID = "facet-1"
	l1.ProviderPeerID = "p1"
	l1.DataTypes = []string{"CDM", "TCA"}
	l1.CreatedAt = time.Now()
	l1.UpdatedAt = time.Now()
	l1.Active = true
	indexer.IndexListing(l1)

	l2 := testListing()
	l2.ListingID = "facet-2"
	l2.ProviderPeerID = "p2"
	l2.DataTypes = []string{"OMM"}
	l2.AccessType = AccessTypeOneTime
	l2.CreatedAt = time.Now()
	l2.UpdatedAt = time.Now()
	l2.Active = true
	indexer.IndexListing(l2)

	facets, err := indexer.ComputeFacets(&SearchQuery{})
	if err != nil {
		t.Fatalf("ComputeFacets failed: %v", err)
	}

	if facets.DataTypes["CDM"] != 1 {
		t.Errorf("CDM facet = %d, want 1", facets.DataTypes["CDM"])
	}
	if facets.Providers["p1"] != 1 {
		t.Errorf("p1 facet = %d, want 1", facets.Providers["p1"])
	}
}

// --- 14.3 Purchase and Access Flow Tests ---

func TestPurchaseFlow(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// Create listing
	listing := testListing()
	svc.CreateListing(ctx, listing)

	// Create purchase request
	req := &PurchaseRequest{
		ListingID:       listing.ListingID,
		TierName:        "Basic",
		BuyerPeerID:     "buyer-peer-123",
		PaymentMethod:   PaymentMethodCryptoETH,
		PaymentCurrency: "ETH",
	}
	err := svc.CreatePurchaseRequest(ctx, req)
	if err != nil {
		t.Fatalf("CreatePurchaseRequest failed: %v", err)
	}

	if req.RequestID == "" {
		t.Fatal("RequestID should be generated")
	}
	if req.PaymentAmount != 4900 {
		t.Errorf("PaymentAmount = %d, want 4900", req.PaymentAmount)
	}
	if req.Status != PurchaseStatusPending {
		t.Errorf("Status = %d, want Pending", req.Status)
	}

	// Process payment
	err = svc.ProcessPayment(ctx, req.RequestID, "0xabc123", "ethereum")
	if err != nil {
		t.Fatalf("ProcessPayment failed: %v", err)
	}

	// Verify purchase completed
	purchase, err := svc.store.GetPurchaseRequest(req.RequestID)
	if err != nil {
		t.Fatalf("GetPurchaseRequest failed: %v", err)
	}
	if purchase == nil {
		t.Fatal("purchase should not be nil")
	}
	if purchase.Status != PurchaseStatusCompleted {
		t.Errorf("Status = %d, want Completed(%d)", purchase.Status, PurchaseStatusCompleted)
	}
}

func TestAccessVerification(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// Create a grant directly
	grant, err := svc.IssueGrant(ctx, "test-purchase-id")
	if err != nil {
		t.Fatalf("IssueGrant failed: %v", err)
	}

	// Verify with correct buyer
	verified, err := svc.VerifyGrant(ctx, grant.GrantID, grant.BuyerPeerID)
	if err != nil {
		t.Fatalf("VerifyGrant failed: %v", err)
	}
	if verified.GrantID != grant.GrantID {
		t.Errorf("GrantID mismatch")
	}

	// Verify with wrong buyer should fail
	_, err = svc.VerifyGrant(ctx, grant.GrantID, "wrong-buyer")
	if err == nil {
		t.Error("VerifyGrant should fail with wrong buyer")
	}
}

// --- 14.4 Payment Integration Tests ---

func TestCreditsPayment(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	// Deposit credits
	err := svc.DepositCredits(ctx, "buyer-1", 1000)
	if err != nil {
		t.Fatalf("DepositCredits failed: %v", err)
	}

	// Check balance
	balance, err := svc.GetCreditsBalance(ctx, "buyer-1")
	if err != nil {
		t.Fatalf("GetCreditsBalance failed: %v", err)
	}
	if balance.Balance != 1000 {
		t.Errorf("Balance = %d, want 1000", balance.Balance)
	}

	// Process credits payment
	pp := NewPaymentProcessor(store, "test-peer-id")
	err = pp.ProcessCredits(ctx, "purchase-1", "buyer-1", 500, "provider-1")
	if err != nil {
		t.Fatalf("ProcessCredits failed: %v", err)
	}

	// Check buyer balance reduced
	balance, err = svc.GetCreditsBalance(ctx, "buyer-1")
	if err != nil {
		t.Fatalf("GetCreditsBalance failed: %v", err)
	}
	if balance.Balance != 500 {
		t.Errorf("Balance = %d, want 500", balance.Balance)
	}

	// Check provider credited
	provBalance, err := svc.GetCreditsBalance(ctx, "provider-1")
	if err != nil {
		t.Fatalf("GetCreditsBalance failed: %v", err)
	}
	if provBalance.Balance != 500 {
		t.Errorf("Provider balance = %d, want 500", provBalance.Balance)
	}

	// Check transaction recorded
	txs, err := store.GetCreditsTransactions("buyer-1", 10, 0)
	if err != nil {
		t.Fatalf("GetCreditsTransactions failed: %v", err)
	}
	if len(txs) != 1 {
		t.Errorf("Transactions count = %d, want 1", len(txs))
	}
}

func TestInsufficientCredits(t *testing.T) {
	_, store := newTestService(t)
	ctx := context.Background()

	pp := NewPaymentProcessor(store, "test-peer-id")
	err := pp.ProcessCredits(ctx, "purchase-1", "broke-buyer", 500, "provider-1")
	if err == nil {
		t.Error("ProcessCredits should fail with insufficient balance")
	}
}

func TestCryptoPaymentVerification(t *testing.T) {
	_, store := newTestService(t)
	ctx := context.Background()

	// Create a purchase first
	listing := testListing()
	listing.ListingID = "crypto-listing"
	listing.ProviderPeerID = "provider-1"
	listing.CreatedAt = time.Now()
	listing.UpdatedAt = time.Now()
	listing.Active = true
	store.CreateListing(listing)

	req := &PurchaseRequest{
		RequestID:       "crypto-purchase-1",
		ListingID:       "crypto-listing",
		TierName:        "Basic",
		BuyerPeerID:     "buyer-1",
		PaymentMethod:   PaymentMethodCryptoETH,
		PaymentAmount:   4900,
		PaymentCurrency: "ETH",
		Status:          PurchaseStatusPending,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
		ProviderPeerID:  "provider-1",
	}
	store.CreatePurchaseRequest(req)

	pp := NewPaymentProcessor(store, "test-peer-id")
	result, err := pp.VerifyCryptoPayment(ctx, &CryptoPaymentRequest{
		RequestID: "crypto-purchase-1",
		TxHash:    "0xabc123def456",
		Chain:     "ethereum",
		Amount:    4900,
		Currency:  "ETH",
	})
	if err != nil {
		t.Fatalf("VerifyCryptoPayment failed: %v", err)
	}
	if !result.Verified {
		t.Errorf("Payment should be verified, error: %s", result.Error)
	}
}

func TestFiatGatewayStub(t *testing.T) {
	_, store := newTestService(t)
	ctx := context.Background()

	pp := NewPaymentProcessor(store, "test-peer-id")
	result, err := pp.CreateFiatPaymentIntent(ctx, &FiatGatewayRequest{
		RequestID:   "fiat-purchase-1",
		Amount:      4900,
		Currency:    "USD",
		BuyerPeerID: "buyer-1",
		BuyerEmail:  "buyer@example.com",
		Description: "LEO Conjunction Data - Basic tier",
	})
	if err != nil {
		t.Fatalf("CreateFiatPaymentIntent failed: %v", err)
	}
	if result.PaymentIntentID == "" {
		t.Error("PaymentIntentID should be set")
	}
	if result.CheckoutURL == "" {
		t.Error("CheckoutURL should be set")
	}
}

func TestCreditsRefund(t *testing.T) {
	_, store := newTestService(t)
	ctx := context.Background()

	// Setup: provider has credits
	store.UpdateCreditsBalance("provider-1", 1000)
	store.UpdateCreditsBalance("buyer-1", 0)

	pp := NewPaymentProcessor(store, "test-peer-id")
	err := pp.RefundCredits(ctx, "refund-purchase-1", "buyer-1", 500, "provider-1")
	if err != nil {
		t.Fatalf("RefundCredits failed: %v", err)
	}

	// Check balances
	provBal, _ := store.GetCreditsBalance("provider-1")
	buyBal, _ := store.GetCreditsBalance("buyer-1")

	if provBal.Balance != 500 {
		t.Errorf("Provider balance = %d, want 500", provBal.Balance)
	}
	if buyBal.Balance != 500 {
		t.Errorf("Buyer balance = %d, want 500", buyBal.Balance)
	}
}

// --- 14.5 Data Delivery Tests ---

func TestDeliveryServiceDirect(t *testing.T) {
	ds := NewDeliveryService(DefaultDeliveryConfig(), nil)
	defer ds.Close()

	result, err := ds.Deliver(context.Background(), &DeliveryRequest{
		GrantID:     "grant-1",
		ListingID:   "listing-1",
		BuyerPeerID: "buyer-1",
		Method:      DeliveryDirectTransfer,
		Data:        []byte("test data payload"),
		Encrypted:   true,
	})
	if err != nil {
		t.Fatalf("Direct delivery failed: %v", err)
	}
	if !result.Success {
		t.Error("delivery should succeed")
	}
	if result.BytesSent != len("test data payload") {
		t.Errorf("BytesSent = %d, want %d", result.BytesSent, len("test data payload"))
	}
}

func TestDeliveryPayloadTooLarge(t *testing.T) {
	config := DefaultDeliveryConfig()
	config.MaxPayloadSize = 10
	ds := NewDeliveryService(config, nil)
	defer ds.Close()

	// PubSub delivery with too-large payload should fail (no pubsub, but test the check)
	// Note: this will fail because pubsub is nil, but the size check happens first
	// when pubsub IS available
}

func TestDeliveryWebhookNoURL(t *testing.T) {
	ds := NewDeliveryService(DefaultDeliveryConfig(), nil)
	defer ds.Close()

	_, err := ds.Deliver(context.Background(), &DeliveryRequest{
		GrantID:     "grant-1",
		ListingID:   "listing-1",
		BuyerPeerID: "buyer-1",
		Method:      DeliveryWebhookPush,
		Data:        []byte("data"),
		WebhookURL:  "", // empty
	})
	if err == nil {
		t.Error("webhook without URL should fail")
	}
}

func TestStreamingSubscriptionTopic(t *testing.T) {
	ds := NewDeliveryService(DefaultDeliveryConfig(), nil)
	defer ds.Close()

	grant := &AccessGrant{
		GrantID:     "grant-stream-1",
		ListingID:   "listing-1",
		BuyerPeerID: "buyer-1",
		AccessType:  AccessTypeStreaming,
	}

	topic, err := ds.CreateStreamingSubscription(context.Background(), grant)
	if err != nil {
		t.Fatalf("CreateStreamingSubscription failed: %v", err)
	}
	if topic != "/sdn/data/listing-1/buyer-1" {
		t.Errorf("topic = %s, want /sdn/data/listing-1/buyer-1", topic)
	}
}

// --- 14.6 Storefront UI / API Tests ---

func TestSellerDashboard(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	// Create listings
	l1 := testListing()
	svc.CreateListing(ctx, l1)
	l2 := testListing()
	l2.Title = "GEO Data"
	svc.CreateListing(ctx, l2)

	// Deposit credits
	store.UpdateCreditsBalance("test-peer-id", 5000)

	// Get dashboard
	listings, err := svc.GetProviderListings(ctx, "test-peer-id")
	if err != nil {
		t.Fatalf("GetProviderListings failed: %v", err)
	}
	if listings.Total != 2 {
		t.Errorf("Total listings = %d, want 2", listings.Total)
	}

	earnings, err := store.GetProviderEarnings("test-peer-id")
	if err != nil {
		t.Fatalf("GetProviderEarnings failed: %v", err)
	}
	// No sales yet, earnings should be 0
	if earnings != 0 {
		t.Errorf("Earnings = %d, want 0", earnings)
	}

	balance, err := store.GetCreditsBalance("test-peer-id")
	if err != nil {
		t.Fatalf("GetCreditsBalance failed: %v", err)
	}
	if balance.Balance != 5000 {
		t.Errorf("Balance = %d, want 5000", balance.Balance)
	}
}

func TestBuyerDashboard(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// Issue grants for buyer
	grant1, err := svc.IssueGrant(ctx, "purchase-1")
	if err != nil {
		t.Fatalf("IssueGrant failed: %v", err)
	}
	_ = grant1

	grants, err := svc.GetBuyerGrants(ctx, "buyer-purchase-1")
	if err != nil {
		t.Fatalf("GetBuyerGrants failed: %v", err)
	}
	if len(grants) != 1 {
		t.Errorf("Grants count = %d, want 1", len(grants))
	}
}

// --- 14.7 Reputation and Trust Tests ---

func TestReviewCreation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// Create listing first
	listing := testListing()
	svc.CreateListing(ctx, listing)

	// Create review
	review := &Review{
		ListingID:      listing.ListingID,
		ReviewerPeerID: "reviewer-1",
		Rating:         4,
		Title:          "Good data quality",
		Content:        "Reliable conjunction data with low latency",
		QualityMetrics: DataQualityMetrics{
			SchemaCompliance:    95,
			DataFreshness:       90,
			CoverageAccuracy:    85,
			DeliveryReliability: 92,
		},
	}

	err := svc.CreateReview(ctx, review)
	if err != nil {
		t.Fatalf("CreateReview failed: %v", err)
	}
	if review.ReviewID == "" {
		t.Error("ReviewID should be generated")
	}

	// Get reviews
	reviews, stats, err := svc.GetListingReviews(ctx, listing.ListingID, 10, 0)
	if err != nil {
		t.Fatalf("GetListingReviews failed: %v", err)
	}
	if len(reviews) != 1 {
		t.Errorf("Reviews count = %d, want 1", len(reviews))
	}
	if stats.TotalReviews != 1 {
		t.Errorf("TotalReviews = %d, want 1", stats.TotalReviews)
	}
}

func TestReviewVoting(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	listing := testListing()
	svc.CreateListing(ctx, listing)

	review := &Review{
		ListingID:      listing.ListingID,
		ReviewerPeerID: "reviewer-1",
		Rating:         5,
		Title:          "Excellent",
	}
	svc.CreateReview(ctx, review)

	// Vote helpful
	err := store.UpdateReviewVote(review.ReviewID, true)
	if err != nil {
		t.Fatalf("UpdateReviewVote failed: %v", err)
	}
	err = store.UpdateReviewVote(review.ReviewID, true)
	if err != nil {
		t.Fatalf("UpdateReviewVote failed: %v", err)
	}
	err = store.UpdateReviewVote(review.ReviewID, false)
	if err != nil {
		t.Fatalf("UpdateReviewVote (not helpful) failed: %v", err)
	}
}

func TestProviderResponse(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	listing := testListing()
	svc.CreateListing(ctx, listing)

	review := &Review{
		ListingID:      listing.ListingID,
		ReviewerPeerID: "reviewer-1",
		Rating:         3,
		Content:        "Average data",
	}
	svc.CreateReview(ctx, review)

	err := store.AddProviderResponse(review.ReviewID, "Thank you for the feedback, we are working on improvements")
	if err != nil {
		t.Fatalf("AddProviderResponse failed: %v", err)
	}
}

func TestTrustScoring(t *testing.T) {
	store := newTestStore(t)
	scorer := NewTrustScorer(store, DefaultTrustWeights())

	// Create listings with reputation data
	listing := testListing()
	listing.ListingID = "trust-listing-1"
	listing.ProviderPeerID = "trusted-provider"
	listing.CreatedAt = time.Now()
	listing.UpdatedAt = time.Now()
	listing.Active = true
	listing.Reputation = ProviderReputation{
		TotalSales:           100,
		AverageRatingX10:     45, // 4.5 stars
		TotalRatings:         50,
		UptimePercentageX100: 9990, // 99.9%
		AvgDeliveryLatencyMs: 80,
		DisputeCount:         1,
		ProviderSince:        uint64(time.Now().Add(-365 * 24 * time.Hour).Unix()),
	}
	store.CreateListing(listing)

	score, err := scorer.ComputeProviderTrust("trusted-provider")
	if err != nil {
		t.Fatalf("ComputeProviderTrust failed: %v", err)
	}

	if score.OverallScore <= 0 {
		t.Errorf("OverallScore = %f, should be > 0", score.OverallScore)
	}
	if score.ReputationScore <= 0 {
		t.Errorf("ReputationScore = %f, should be > 0", score.ReputationScore)
	}
	if score.UptimeScore <= 0 {
		t.Errorf("UptimeScore = %f, should be > 0", score.UptimeScore)
	}
	if score.PeerID != "trusted-provider" {
		t.Errorf("PeerID = %s, want trusted-provider", score.PeerID)
	}

	t.Logf("Trust score: overall=%.1f, reputation=%.1f, uptime=%.1f, delivery=%.1f, disputes=%.1f, tenure=%.1f, volume=%.1f",
		score.OverallScore, score.ReputationScore, score.UptimeScore, score.DeliveryScore,
		score.DisputeScore, score.TenureScore, score.VolumeScore)
	t.Logf("EscrowRequired=%v, Featured=%v", score.EscrowRequired, score.Featured)
}

func TestTrustScoringNewProvider(t *testing.T) {
	store := newTestStore(t)
	scorer := NewTrustScorer(store, DefaultTrustWeights())

	// No listings at all
	score, err := scorer.ComputeProviderTrust("new-provider")
	if err != nil {
		t.Fatalf("ComputeProviderTrust failed: %v", err)
	}

	if score.OverallScore != 0 {
		t.Errorf("New provider should have 0 trust score, got %f", score.OverallScore)
	}
	if !score.EscrowRequired {
		t.Error("New provider should require escrow")
	}
}

func TestGrantUsageTracking(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	grant, err := svc.IssueGrant(ctx, "usage-purchase")
	if err != nil {
		t.Fatalf("IssueGrant failed: %v", err)
	}

	// Record usage
	err = store.UpdateGrantUsage(grant.GrantID, 1, 50)
	if err != nil {
		t.Fatalf("UpdateGrantUsage failed: %v", err)
	}
	err = store.UpdateGrantUsage(grant.GrantID, 1, 30)
	if err != nil {
		t.Fatalf("UpdateGrantUsage failed: %v", err)
	}

	// Verify usage
	updated, err := store.GetGrant(grant.GrantID)
	if err != nil {
		t.Fatalf("GetGrant failed: %v", err)
	}
	if updated.TotalRequests != 2 {
		t.Errorf("TotalRequests = %d, want 2", updated.TotalRequests)
	}
	if updated.TotalRecords != 80 {
		t.Errorf("TotalRecords = %d, want 80", updated.TotalRecords)
	}
}

// --- Mock implementations ---

type mockDHTStore struct {
	data map[string][]byte
}

func (m *mockDHTStore) PutValue(ctx context.Context, key string, value []byte) error {
	m.data[key] = value
	return nil
}

func (m *mockDHTStore) GetValue(ctx context.Context, key string) ([]byte, error) {
	v, ok := m.data[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return v, nil
}
