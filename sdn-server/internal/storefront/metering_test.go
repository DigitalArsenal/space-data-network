package storefront

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestUsageEventRecordAndSummary(t *testing.T) {
	store := newTestStore(t)

	buyerPeerID := "buyer-metering-1"
	listingID := "listing-metering-1"
	grantID := "grant-metering-1"

	// Record multiple usage events for the same buyer/listing
	events := []*UsageEvent{
		{
			EventID:        "evt-1",
			GrantID:        grantID,
			BuyerPeerID:    buyerPeerID,
			ListingID:      listingID,
			RecordsServed:  1000,
			BytesDelivered: 512000,
			OccurredAt:     time.Now(),
		},
		{
			EventID:        "evt-2",
			GrantID:        grantID,
			BuyerPeerID:    buyerPeerID,
			ListingID:      listingID,
			RecordsServed:  2500,
			BytesDelivered: 1024000,
			OccurredAt:     time.Now(),
		},
		{
			EventID:        "evt-3",
			GrantID:        grantID,
			BuyerPeerID:    buyerPeerID,
			ListingID:      listingID,
			RecordsServed:  500,
			BytesDelivered: 256000,
			OccurredAt:     time.Now(),
		},
	}

	for _, ev := range events {
		if err := store.RecordUsageEvent(ev); err != nil {
			t.Fatalf("RecordUsageEvent failed: %v", err)
		}
	}

	// Duplicate insertion should be ignored (INSERT OR IGNORE)
	if err := store.RecordUsageEvent(events[0]); err != nil {
		t.Fatalf("duplicate RecordUsageEvent should not fail: %v", err)
	}

	from := time.Now().Add(-1 * time.Hour)
	to := time.Now().Add(1 * time.Hour)

	summary, err := store.GetUsageSummary(buyerPeerID, listingID, from, to)
	if err != nil {
		t.Fatalf("GetUsageSummary failed: %v", err)
	}

	if summary.TotalEvents != 3 {
		t.Errorf("expected 3 events, got %d", summary.TotalEvents)
	}
	if summary.TotalRecords != 4000 {
		t.Errorf("expected 4000 total records, got %d", summary.TotalRecords)
	}
	if summary.TotalBytes != 1792000 {
		t.Errorf("expected 1792000 total bytes, got %d", summary.TotalBytes)
	}

	// Querying outside the time range should return zeros
	futureFrom := time.Now().Add(24 * time.Hour)
	futureTo := time.Now().Add(48 * time.Hour)
	emptySummary, err := store.GetUsageSummary(buyerPeerID, listingID, futureFrom, futureTo)
	if err != nil {
		t.Fatalf("GetUsageSummary (future range) failed: %v", err)
	}
	if emptySummary.TotalEvents != 0 {
		t.Errorf("expected 0 events in future range, got %d", emptySummary.TotalEvents)
	}
}

func TestReportStripeUsageRecordCallsAPI(t *testing.T) {
	var capturedBody string
	var capturedAuth string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"si_usage_1","object":"usage_record"}`))
	}))
	defer ts.Close()

	// Override the Stripe API base URL for this test
	origBase := stripeAPIBase
	stripeAPIBase = ts.URL
	defer func() { stripeAPIBase = origBase }()

	t.Setenv("STRIPE_SECRET_KEY", "sk_test_xxx")

	store := newTestStore(t)
	pp := NewPaymentProcessor(store, "test-peer-id")

	err := pp.ReportStripeUsageRecord(context.Background(), "si_test_item_1", 4000, 1700000000)
	if err != nil {
		t.Fatalf("ReportStripeUsageRecord failed: %v", err)
	}

	if capturedAuth != "Bearer sk_test_xxx" {
		t.Errorf("unexpected auth header: %q", capturedAuth)
	}

	parsed, err := url.ParseQuery(capturedBody)
	if err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if parsed.Get("quantity") != "4000" {
		t.Errorf("expected quantity=4000, got %q", parsed.Get("quantity"))
	}
	if parsed.Get("timestamp") != "1700000000" {
		t.Errorf("expected timestamp=1700000000, got %q", parsed.Get("timestamp"))
	}
}

func TestReportStripeUsageRecordStubWhenNoKey(t *testing.T) {
	// Make sure STRIPE_SECRET_KEY is unset
	t.Setenv("STRIPE_SECRET_KEY", "")

	store := newTestStore(t)
	pp := NewPaymentProcessor(store, "test-peer-id")

	// Should succeed without hitting any server
	err := pp.ReportStripeUsageRecord(context.Background(), "si_stub_item", 100, time.Now().Unix())
	if err != nil {
		t.Fatalf("stub should return nil error, got: %v", err)
	}
}

func TestPaymentMethodEnumValues(t *testing.T) {
	if PaymentMethodUsageBased != 6 {
		t.Errorf("expected PaymentMethodUsageBased=6, got %d", PaymentMethodUsageBased)
	}
	if PaymentMethodEnterprise != 7 {
		t.Errorf("expected PaymentMethodEnterprise=7, got %d", PaymentMethodEnterprise)
	}
	// Verify existing values are not shifted
	if PaymentMethodFree != 5 {
		t.Errorf("expected PaymentMethodFree=5, got %d", PaymentMethodFree)
	}
	_ = strings.TrimSpace // suppress unused import error just in case
}
