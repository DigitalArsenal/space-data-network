package storefront

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenerateInvoiceFromUsage(t *testing.T) {
	store := newTestStore(t)
	pp := NewPaymentProcessor(store, "test-peer-id")

	buyerPeerID := "buyer-inv-1"
	providerPeerID := "provider-inv-1"
	listingID := "listing-inv-1"

	// Seed some usage events
	events := []*UsageEvent{
		{
			EventID:        "inv-evt-1",
			GrantID:        "grant-inv-1",
			BuyerPeerID:    buyerPeerID,
			ListingID:      listingID,
			RecordsServed:  3000,
			BytesDelivered: 2 * (1 << 20), // 2 MB
			OccurredAt:     time.Now(),
		},
		{
			EventID:        "inv-evt-2",
			GrantID:        "grant-inv-1",
			BuyerPeerID:    buyerPeerID,
			ListingID:      listingID,
			RecordsServed:  1500,
			BytesDelivered: 1 * (1 << 20), // 1 MB
			OccurredAt:     time.Now(),
		},
	}

	for _, ev := range events {
		if err := store.RecordUsageEvent(ev); err != nil {
			t.Fatalf("RecordUsageEvent: %v", err)
		}
	}

	periodStart := time.Now().Add(-1 * time.Hour)
	periodEnd := time.Now().Add(1 * time.Hour)

	inv, err := pp.GenerateInvoice(context.Background(), buyerPeerID, providerPeerID, listingID, periodStart, periodEnd, "USD")
	if err != nil {
		t.Fatalf("GenerateInvoice failed: %v", err)
	}

	if inv.InvoiceID == "" {
		t.Error("expected non-empty invoice ID")
	}
	if inv.Status != InvoiceStatusIssued {
		t.Errorf("expected status=issued, got %q", inv.Status)
	}
	if inv.BuyerPeerID != buyerPeerID {
		t.Errorf("unexpected buyer: %q", inv.BuyerPeerID)
	}
	if inv.ProviderPeerID != providerPeerID {
		t.Errorf("unexpected provider: %q", inv.ProviderPeerID)
	}
	if len(inv.LineItems) == 0 {
		t.Error("expected at least one line item")
	}
	if inv.TotalAmount == 0 {
		t.Error("expected non-zero total amount")
	}
	// 4500 records → 5 cents (ceiling(4500/1000)*1)
	// 3 MB bytes → 3 cents (ceiling(3MB/1MB)*1)
	// Total = 8 cents
	if inv.TotalAmount != 8 {
		t.Errorf("expected total_amount=8 cents, got %d", inv.TotalAmount)
	}

	// Verify the invoice was persisted
	loaded, err := store.GetInvoice(inv.InvoiceID)
	if err != nil {
		t.Fatalf("GetInvoice failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected persisted invoice, got nil")
	}
	if loaded.TotalAmount != inv.TotalAmount {
		t.Errorf("persisted total mismatch: got %d want %d", loaded.TotalAmount, inv.TotalAmount)
	}
}

func TestCreateStripeInvoiceCallsAPIAndFinalizes(t *testing.T) {
	var invoiceItemCalls int64
	var invoiceCalls int64
	var finalizeCalls int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/invoice_items":
			atomic.AddInt64(&invoiceItemCalls, 1)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id":"ii_test_1","object":"invoiceitem"}`))
		case r.URL.Path == "/v1/invoices":
			atomic.AddInt64(&invoiceCalls, 1)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id":"in_test_1","object":"invoice","status":"draft"}`))
		case r.URL.Path == "/v1/invoices/in_test_1/finalize":
			atomic.AddInt64(&finalizeCalls, 1)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id":"in_test_1","object":"invoice","status":"open"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"message":"not found"}}`)
		}
	}))
	defer ts.Close()

	origBase := stripeAPIBase
	stripeAPIBase = ts.URL
	defer func() { stripeAPIBase = origBase }()

	t.Setenv("STRIPE_SECRET_KEY", "sk_test_invoice_xxx")

	store := newTestStore(t)
	pp := NewPaymentProcessor(store, "test-peer-id")

	inv := &Invoice{
		InvoiceID:      "local-inv-1",
		BuyerPeerID:    "buyer-stripe-1",
		ProviderPeerID: "provider-stripe-1",
		PeriodStart:    time.Now().Add(-30 * 24 * time.Hour),
		PeriodEnd:      time.Now(),
		LineItems: []InvoiceLineItem{
			{Description: "Records delivered", Quantity: 5000, UnitAmount: 1, Amount: 5},
			{Description: "Data transferred", Quantity: 3145728, UnitAmount: 1, Amount: 3},
		},
		TotalAmount: 8,
		Currency:    "USD",
		Status:      InvoiceStatusIssued,
		IssuedAt:    time.Now(),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	stripeID, err := pp.CreateStripeInvoice(context.Background(), inv, "cus_test_123")
	if err != nil {
		t.Fatalf("CreateStripeInvoice failed: %v", err)
	}

	if stripeID != "in_test_1" {
		t.Errorf("unexpected stripe invoice ID: %q", stripeID)
	}
	if atomic.LoadInt64(&invoiceItemCalls) != 2 {
		t.Errorf("expected 2 invoice_items calls, got %d", invoiceItemCalls)
	}
	if atomic.LoadInt64(&invoiceCalls) != 1 {
		t.Errorf("expected 1 invoices call, got %d", invoiceCalls)
	}
	if atomic.LoadInt64(&finalizeCalls) != 1 {
		t.Errorf("expected 1 finalize call, got %d", finalizeCalls)
	}
}

func TestMarkInvoicePaidAndVoid(t *testing.T) {
	store := newTestStore(t)
	pp := NewPaymentProcessor(store, "test-peer-id")

	// Generate an invoice with zero usage (no events)
	periodStart := time.Now().Add(-24 * time.Hour)
	periodEnd := time.Now()
	inv, err := pp.GenerateInvoice(context.Background(), "buyer-lifecycle-1", "provider-lifecycle-1", "listing-lc-1", periodStart, periodEnd, "USD")
	if err != nil {
		t.Fatalf("GenerateInvoice failed: %v", err)
	}

	if inv.Status != InvoiceStatusIssued {
		t.Fatalf("expected issued, got %q", inv.Status)
	}

	// Mark as paid with a PO reference
	paid, err := pp.MarkInvoicePaid(context.Background(), inv.InvoiceID, "PO-2026-001")
	if err != nil {
		t.Fatalf("MarkInvoicePaid failed: %v", err)
	}
	if paid.Status != InvoiceStatusPaid {
		t.Errorf("expected status=paid, got %q", paid.Status)
	}
	if paid.POReference != "PO-2026-001" {
		t.Errorf("unexpected PO reference: %q", paid.POReference)
	}
	if paid.PaidAt.IsZero() {
		t.Error("expected non-zero paid_at timestamp")
	}

	// Generate a second invoice and void it
	inv2, err := pp.GenerateInvoice(context.Background(), "buyer-lifecycle-2", "provider-lifecycle-2", "listing-lc-2", periodStart, periodEnd, "USD")
	if err != nil {
		t.Fatalf("GenerateInvoice (inv2) failed: %v", err)
	}

	voided, err := pp.MarkInvoiceVoid(context.Background(), inv2.InvoiceID)
	if err != nil {
		t.Fatalf("MarkInvoiceVoid failed: %v", err)
	}
	if voided.Status != InvoiceStatusVoid {
		t.Errorf("expected status=void, got %q", voided.Status)
	}

	// GetBuyerInvoices round-trip check
	_ = json.Marshal // keep import used
	invoices, err := store.GetBuyerInvoices("buyer-lifecycle-1", 10, 0)
	if err != nil {
		t.Fatalf("GetBuyerInvoices failed: %v", err)
	}
	if len(invoices) != 1 {
		t.Errorf("expected 1 invoice for buyer-lifecycle-1, got %d", len(invoices))
	}
}
