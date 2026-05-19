package storefront

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestHandleStripeWebhookCheckoutCompleted(t *testing.T) {
	_, store := newTestService(t)
	pp := NewPaymentProcessor(store, "test-peer-id")

	secret := "whsec_test_secret"
	t.Setenv("STRIPE_WEBHOOK_SECRET", secret)

	payload := []byte(`{
		"type":"checkout.session.completed",
		"data":{
			"object":{
				"id":"cs_test_123",
				"client_reference_id":"purchase-123",
				"metadata":{"request_id":"purchase-123"},
				"payment_status":"paid",
				"status":"complete",
				"subscription":"sub_test_123",
				"customer":"cus_test_123"
			}
		}
	}`)

	header := signedStripeHeader(payload, secret, time.Now().Unix())
	action, err := pp.HandleStripeWebhook(context.Background(), header, payload)
	if err != nil {
		t.Fatalf("HandleStripeWebhook failed: %v", err)
	}
	if action == nil {
		t.Fatal("expected action, got nil")
	}
	if action.EventType != "checkout.session.completed" {
		t.Fatalf("unexpected event type: %s", action.EventType)
	}
	if action.RequestID != "purchase-123" {
		t.Fatalf("unexpected request id: %s", action.RequestID)
	}
	if !action.Paid {
		t.Fatal("expected paid=true")
	}
	if action.SubscriptionID != "sub_test_123" {
		t.Fatalf("unexpected subscription id: %s", action.SubscriptionID)
	}
	if action.CustomerID != "cus_test_123" {
		t.Fatalf("unexpected customer id: %s", action.CustomerID)
	}
}

func TestApplyStripeWebhookActionIssuesGrantOnce(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	listing := testListing()
	listing.AcceptedPayments = []PaymentMethod{PaymentMethodFiatStripe}
	if err := svc.CreateListing(ctx, listing); err != nil {
		t.Fatalf("CreateListing failed: %v", err)
	}

	purchase := &PurchaseRequest{
		ListingID:     listing.ListingID,
		TierName:      "Basic",
		BuyerPeerID:   "buyer-peer-1",
		PaymentMethod: PaymentMethodFiatStripe,
	}
	if err := svc.CreatePurchaseRequest(ctx, purchase); err != nil {
		t.Fatalf("CreatePurchaseRequest failed: %v", err)
	}

	action := &StripeWebhookAction{
		EventID:   "evt_checkout_completed_1",
		EventType: "checkout.session.completed",
		RequestID: purchase.RequestID,
		SessionID: "cs_test_123",
		Paid:      true,
	}

	grant, err := svc.ApplyStripeWebhookAction(ctx, action)
	if err != nil {
		t.Fatalf("ApplyStripeWebhookAction failed: %v", err)
	}
	if grant == nil {
		t.Fatal("expected grant")
	}

	duplicate, err := svc.ApplyStripeWebhookAction(ctx, action)
	if err != nil {
		t.Fatalf("duplicate ApplyStripeWebhookAction failed: %v", err)
	}
	if duplicate == nil || duplicate.GrantID != grant.GrantID {
		t.Fatalf("duplicate grant = %#v, want %s", duplicate, grant.GrantID)
	}

	grants, err := store.GetGrantsByBuyer("buyer-peer-1")
	if err != nil {
		t.Fatalf("GetGrantsByBuyer failed: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("grant count = %d, want 1", len(grants))
	}

	events, err := store.GetPaymentAuditEvents(purchase.RequestID)
	if err != nil {
		t.Fatalf("GetPaymentAuditEvents failed: %v", err)
	}
	grantIssued := 0
	for _, event := range events {
		if event.EventType == PaymentAuditGrantIssued {
			grantIssued++
		}
	}
	if grantIssued != 1 {
		t.Fatalf("grant-issued audit count = %d, want 1", grantIssued)
	}
}

func TestApplyStripeWebhookActionMarksFailureWithoutGrant(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	listing := testListing()
	listing.AcceptedPayments = []PaymentMethod{PaymentMethodFiatStripe}
	if err := svc.CreateListing(ctx, listing); err != nil {
		t.Fatalf("CreateListing failed: %v", err)
	}

	purchase := &PurchaseRequest{
		ListingID:     listing.ListingID,
		TierName:      "Basic",
		BuyerPeerID:   "buyer-peer-1",
		PaymentMethod: PaymentMethodFiatStripe,
	}
	if err := svc.CreatePurchaseRequest(ctx, purchase); err != nil {
		t.Fatalf("CreatePurchaseRequest failed: %v", err)
	}

	grant, err := svc.ApplyStripeWebhookAction(ctx, &StripeWebhookAction{
		EventID:       "evt_checkout_failed_1",
		EventType:     "checkout.session.async_payment_failed",
		RequestID:     purchase.RequestID,
		SessionID:     "cs_test_failed",
		FailureReason: "card_declined",
	})
	if err != nil {
		t.Fatalf("ApplyStripeWebhookAction failed: %v", err)
	}
	if grant != nil {
		t.Fatalf("failure action issued grant: %#v", grant)
	}

	updated, err := store.GetPurchaseRequest(purchase.RequestID)
	if err != nil {
		t.Fatalf("GetPurchaseRequest failed: %v", err)
	}
	if updated.Status != PurchaseStatusFailed {
		t.Fatalf("purchase status = %d, want failed", updated.Status)
	}
	if updated.GrantID != "" {
		t.Fatalf("failure purchase grant = %q, want empty", updated.GrantID)
	}
}

func TestHandleStripeWebhookRejectsBadSignature(t *testing.T) {
	_, store := newTestService(t)
	pp := NewPaymentProcessor(store, "test-peer-id")

	secret := "whsec_test_secret"
	t.Setenv("STRIPE_WEBHOOK_SECRET", secret)

	payload := []byte(`{"type":"checkout.session.completed","data":{"object":{"id":"cs_bad"}}}`)
	header := "t=1700000000,v1=deadbeef"

	if _, err := pp.HandleStripeWebhook(context.Background(), header, payload); err == nil {
		t.Fatal("expected signature verification error")
	}
}

func signedStripeHeader(payload []byte, secret string, timestamp int64) string {
	msg := fmt.Sprintf("%d.%s", timestamp, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%d,v1=%s", timestamp, sig)
}
