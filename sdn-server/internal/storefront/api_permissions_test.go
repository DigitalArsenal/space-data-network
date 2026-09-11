package storefront

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func TestCreatePurchaseBindsAuthenticatedBuyer(t *testing.T) {
	svc, _ := newTestService(t)
	handler := NewAPIHandler(svc, nil, nil, nil, nil)
	listing := testListing()
	if err := svc.CreateListing(context.Background(), listing); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(PurchaseRequest{
		ListingID: listing.ListingID, TierName: "Basic", BuyerPeerID: "buyer-beta",
		PaymentMethod: PaymentMethodCryptoSOL,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/storefront/purchases", bytes.NewReader(body))
	req = req.WithContext(auth.ContextWithSession(req.Context(), &auth.Session{XPub: "buyer-alpha", TrustLevel: peers.Standard}))
	rec := httptest.NewRecorder()
	handler.handleCreatePurchase(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign buyer purchase code = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	body, _ = json.Marshal(PurchaseRequest{
		ListingID: listing.ListingID, TierName: "Basic", PaymentMethod: PaymentMethodCryptoSOL,
	})
	req = httptest.NewRequest(http.MethodPost, "/api/storefront/purchases", bytes.NewReader(body))
	req = req.WithContext(auth.ContextWithSession(req.Context(), &auth.Session{XPub: "buyer-alpha", TrustLevel: peers.Standard}))
	rec = httptest.NewRecorder()
	handler.handleCreatePurchase(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("self purchase code = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var purchase PurchaseRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &purchase); err != nil {
		t.Fatal(err)
	}
	if purchase.BuyerPeerID != "buyer-alpha" {
		t.Fatalf("BuyerPeerID = %q, want authenticated buyer", purchase.BuyerPeerID)
	}
}

func TestConfirmPaymentRejectsDifferentAuthenticatedBuyerBeforeChainCall(t *testing.T) {
	svc, store := newTestService(t)
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)
	spy := &spyChainVerifier{chain: "ethereum"}
	handler := NewAPIHandler(svc, nil, nil, NewPaymentProcessor(store, "test-peer-id", spy), nil)

	body, _ := json.Marshal(map[string]interface{}{
		"txHash": "0xabc123", "chain": "ethereum", "reference": "crypto:private-reference",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/storefront/purchases/"+purchase.RequestID+"/confirm", bytes.NewReader(body))
	req = req.WithContext(auth.ContextWithSession(req.Context(), &auth.Session{XPub: "buyer-beta", TrustLevel: peers.Standard}))
	rec := httptest.NewRecorder()
	handler.handleConfirmPayment(rec, req, purchase.RequestID)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("confirm code = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if spy.called {
		t.Fatal("chain verifier called for a different authenticated buyer")
	}
}

func TestDashboardPeerQueryRejectsNonAdminTampering(t *testing.T) {
	svc, store := newTestService(t)
	handler := NewAPIHandler(svc, nil, nil, nil, nil)

	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodFree)
	grant := &AccessGrant{
		GrantID:        "grant-permission-test",
		ListingID:      purchase.ListingID,
		TierName:       purchase.TierName,
		BuyerPeerID:    "buyer-alpha",
		ProviderPeerID: purchase.ProviderPeerID,
		AccessType:     AccessTypeSubscription,
		Status:         GrantStatusActive,
		PaymentMethod:  PaymentMethodFree,
		GrantedAt:      time.Now(),
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := store.CreateGrant(grant); err != nil {
		t.Fatalf("CreateGrant failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/storefront/dashboard/buyer?peerId=buyer-alpha", nil)
	req = req.WithContext(auth.ContextWithSession(req.Context(), &auth.Session{
		XPub:       "buyer-beta",
		TrustLevel: peers.Standard,
	}))
	rec := httptest.NewRecorder()
	handler.handleBuyerDashboard(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("buyer dashboard code = %d, want %d", rec.Code, http.StatusForbidden)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/storefront/dashboard/buyer?peerId=buyer-alpha", nil)
	req = req.WithContext(auth.ContextWithSession(req.Context(), &auth.Session{
		XPub:       "admin-peer",
		TrustLevel: peers.Admin,
	}))
	rec = httptest.NewRecorder()
	handler.handleBuyerDashboard(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin buyer dashboard code = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestGrantQueryRejectsNonAdminBuyerTampering(t *testing.T) {
	svc, store := newTestService(t)
	handler := NewAPIHandler(svc, nil, nil, nil, nil)
	grant := &AccessGrant{
		GrantID:        "grant-buyer-alpha",
		ListingID:      "listing-1",
		TierName:       "Basic",
		BuyerPeerID:    "buyer-alpha",
		ProviderPeerID: "provider-1",
		AccessType:     AccessTypeOneTime,
		Status:         GrantStatusActive,
		PaymentMethod:  PaymentMethodFree,
		GrantedAt:      time.Now(),
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := store.CreateGrant(grant); err != nil {
		t.Fatalf("CreateGrant failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/storefront/grants?buyer=buyer-alpha", nil)
	req = req.WithContext(auth.ContextWithSession(context.Background(), &auth.Session{
		XPub:       "buyer-beta",
		TrustLevel: peers.Standard,
	}))
	rec := httptest.NewRecorder()
	handler.handleGrants(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("buyer grants code = %d, want %d", rec.Code, http.StatusForbidden)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/storefront/grants?buyer=buyer-alpha", nil)
	req = req.WithContext(auth.ContextWithSession(context.Background(), &auth.Session{
		XPub:       "buyer-alpha",
		TrustLevel: peers.Standard,
	}))
	rec = httptest.NewRecorder()
	handler.handleGrants(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("own buyer grants code = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}
