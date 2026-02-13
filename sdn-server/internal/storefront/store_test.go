package storefront

import (
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func newTestStoreHelper(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	flatStore, err := storage.NewFlatSQLStore(dir, validator)
	if err != nil {
		t.Fatalf("Failed to create FlatSQLStore: %v", err)
	}
	t.Cleanup(func() { flatStore.Close() })
	store, err := NewStore(flatStore)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestStoreListings(t *testing.T) {
	store := newTestStoreHelper(t)

	// Create a listing
	listing := &Listing{
		ListingID:      "test-listing-1",
		ProviderPeerID: "12D3KooWTestProvider",
		Title:          "LEO Conjunction Data",
		Description:    "Real-time conjunction data for LEO satellites",
		DataTypes:      []string{"CDM", "TCA"},
		Tags:           []string{"conjunction", "LEO", "realtime"},
		Coverage: DataCoverage{
			Spatial: SpatialCoverage{
				Type:    "region",
				Regions: []string{"LEO"},
			},
			Temporal: TemporalCoverage{
				UpdateFrequency: "realtime",
			},
		},
		AccessType:         AccessTypeSubscription,
		EncryptionRequired: true,
		DeliveryMethods:    []string{"PubSubStream", "DirectTransfer"},
		Pricing: []PricingTier{
			{
				Name:          "Basic",
				PriceAmount:   4900,
				PriceCurrency: "USD",
				DurationDays:  30,
				RateLimit:     100,
			},
			{
				Name:          "Pro",
				PriceAmount:   19900,
				PriceCurrency: "USD",
				DurationDays:  30,
				RateLimit:     1000,
			},
		},
		AcceptedPayments: []PaymentMethod{PaymentMethodCryptoETH, PaymentMethodSDNCredits},
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
		Version:          1,
		Active:           true,
	}

	// Store the listing
	if err := store.CreateListing(listing); err != nil {
		t.Fatalf("Failed to create listing: %v", err)
	}

	// Retrieve the listing
	retrieved, err := store.GetListing("test-listing-1")
	if err != nil {
		t.Fatalf("Failed to get listing: %v", err)
	}
	if retrieved == nil {
		t.Fatal("Listing not found")
	}

	if retrieved.Title != listing.Title {
		t.Errorf("Title mismatch: got %s, want %s", retrieved.Title, listing.Title)
	}
	if len(retrieved.DataTypes) != len(listing.DataTypes) {
		t.Errorf("DataTypes length mismatch: got %d, want %d", len(retrieved.DataTypes), len(listing.DataTypes))
	}
	if len(retrieved.Pricing) != len(listing.Pricing) {
		t.Errorf("Pricing length mismatch: got %d, want %d", len(retrieved.Pricing), len(listing.Pricing))
	}

	// Test search
	results, err := store.SearchListings(&SearchQuery{
		DataTypes: []string{"CDM"},
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("Failed to search listings: %v", err)
	}
	if results.Total != 1 {
		t.Errorf("Search total mismatch: got %d, want 1", results.Total)
	}
	if len(results.Listings) != 1 {
		t.Errorf("Search results length mismatch: got %d, want 1", len(results.Listings))
	}
}

func TestStoreGrants(t *testing.T) {
	store := newTestStoreHelper(t)

	// First create a listing
	listing := &Listing{
		ListingID:      "test-listing-1",
		ProviderPeerID: "12D3KooWTestProvider",
		Title:          "Test Listing",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := store.CreateListing(listing); err != nil {
		t.Fatalf("Failed to create listing: %v", err)
	}

	// Create a grant
	grant := &AccessGrant{
		GrantID:        "test-grant-1",
		ListingID:      "test-listing-1",
		TierName:       "Basic",
		BuyerPeerID:    "12D3KooWTestBuyer",
		AccessType:     AccessTypeSubscription,
		GrantedAt:      time.Now(),
		ExpiresAt:      time.Now().Add(30 * 24 * time.Hour),
		Status:         GrantStatusActive,
		PaymentMethod:  PaymentMethodSDNCredits,
		PaymentAmount:  4900,
		PaymentCurrency: "USD",
		ProviderPeerID: "12D3KooWTestProvider",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := store.CreateGrant(grant); err != nil {
		t.Fatalf("Failed to create grant: %v", err)
	}

	// Retrieve the grant
	retrieved, err := store.GetGrant("test-grant-1")
	if err != nil {
		t.Fatalf("Failed to get grant: %v", err)
	}
	if retrieved == nil {
		t.Fatal("Grant not found")
	}

	if retrieved.BuyerPeerID != grant.BuyerPeerID {
		t.Errorf("BuyerPeerID mismatch: got %s, want %s", retrieved.BuyerPeerID, grant.BuyerPeerID)
	}

	// Get grants by buyer
	grants, err := store.GetGrantsByBuyer("12D3KooWTestBuyer")
	if err != nil {
		t.Fatalf("Failed to get grants by buyer: %v", err)
	}
	if len(grants) != 1 {
		t.Errorf("Grants count mismatch: got %d, want 1", len(grants))
	}
}

func TestStoreReviews(t *testing.T) {
	store := newTestStoreHelper(t)

	// Create a listing
	listing := &Listing{
		ListingID:      "test-listing-1",
		ProviderPeerID: "12D3KooWTestProvider",
		Title:          "Test Listing",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := store.CreateListing(listing); err != nil {
		t.Fatalf("Failed to create listing: %v", err)
	}

	// Create reviews
	for i := 1; i <= 3; i++ {
		review := &Review{
			ReviewID:       "test-review-" + string(rune('0'+i)),
			ListingID:      "test-listing-1",
			ReviewerPeerID: "12D3KooWTestReviewer",
			Rating:         uint8(3 + (i % 3)),
			Title:          "Great data!",
			Content:        "Very reliable and accurate data.",
			VerifiedPurchase: true,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
			Status:         ReviewStatusPublished,
		}
		if err := store.CreateReview(review); err != nil {
			t.Fatalf("Failed to create review %d: %v", i, err)
		}
	}

	// Get reviews for listing
	reviews, stats, err := store.GetReviewsForListing("test-listing-1", 10, 0)
	if err != nil {
		t.Fatalf("Failed to get reviews: %v", err)
	}

	if len(reviews) != 3 {
		t.Errorf("Reviews count mismatch: got %d, want 3", len(reviews))
	}

	if stats.TotalReviews != 3 {
		t.Errorf("Stats total reviews mismatch: got %d, want 3", stats.TotalReviews)
	}

	if stats.VerifiedReviews != 3 {
		t.Errorf("Stats verified reviews mismatch: got %d, want 3", stats.VerifiedReviews)
	}
}

func TestStoreCredits(t *testing.T) {
	store := newTestStoreHelper(t)

	peerID := "12D3KooWTestPeer"

	// Initial balance should be zero
	balance, err := store.GetCreditsBalance(peerID)
	if err != nil {
		t.Fatalf("Failed to get credits balance: %v", err)
	}
	if balance.Balance != 0 {
		t.Errorf("Initial balance should be 0, got %d", balance.Balance)
	}

	// Deposit credits
	if err := store.UpdateCreditsBalance(peerID, 10000); err != nil {
		t.Fatalf("Failed to deposit credits: %v", err)
	}

	balance, err = store.GetCreditsBalance(peerID)
	if err != nil {
		t.Fatalf("Failed to get credits balance: %v", err)
	}
	if balance.Balance != 10000 {
		t.Errorf("Balance after deposit should be 10000, got %d", balance.Balance)
	}

	// Withdraw credits
	if err := store.UpdateCreditsBalance(peerID, -3000); err != nil {
		t.Fatalf("Failed to withdraw credits: %v", err)
	}

	balance, err = store.GetCreditsBalance(peerID)
	if err != nil {
		t.Fatalf("Failed to get credits balance: %v", err)
	}
	if balance.Balance != 7000 {
		t.Errorf("Balance after withdrawal should be 7000, got %d", balance.Balance)
	}
}
