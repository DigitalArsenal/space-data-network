package storefront

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

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
