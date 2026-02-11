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
