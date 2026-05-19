package storefront

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func TestBuyerDashboardReturnsPurchasesGrantsAndDeliveries(t *testing.T) {
	svc, store := newTestService(t)
	handler := NewAPIHandler(svc, nil, nil, nil, nil)
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodFree)
	grant := &AccessGrant{
		GrantID:        "grant-dashboard-buyer",
		ListingID:      purchase.ListingID,
		TierName:       purchase.TierName,
		BuyerPeerID:    purchase.BuyerPeerID,
		ProviderPeerID: purchase.ProviderPeerID,
		AccessType:     AccessTypeSubscription,
		Status:         GrantStatusActive,
		PaymentMethod:  PaymentMethodFree,
		GrantedAt:      time.Now(),
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		DeliveryTopic:  "/sdn/data/listing/buyer",
	}
	if err := store.CreateGrant(grant); err != nil {
		t.Fatalf("CreateGrant failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/storefront/dashboard/buyer?peerId="+purchase.BuyerPeerID, nil)
	req = req.WithContext(auth.ContextWithSession(req.Context(), &auth.Session{XPub: purchase.BuyerPeerID, TrustLevel: peers.Standard}))
	rec := httptest.NewRecorder()
	handler.handleBuyerDashboard(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard code = %d body=%s", rec.Code, rec.Body.String())
	}
	var body BuyerDashboardResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if len(body.Purchases) != 1 || body.Purchases[0].RequestID != purchase.RequestID {
		t.Fatalf("dashboard purchases = %#v, want purchase %s", body.Purchases, purchase.RequestID)
	}
	if len(body.Grants) != 1 || body.Grants[0].GrantID != grant.GrantID {
		t.Fatalf("dashboard grants = %#v, want grant %s", body.Grants, grant.GrantID)
	}
	if len(body.Deliveries) != 1 || body.Deliveries[0].KeyWrapStatus != "issued" {
		t.Fatalf("dashboard deliveries = %#v, want issued delivery", body.Deliveries)
	}
}

func TestAdminDashboardReturnsModerationTrustAndPaymentSurfaces(t *testing.T) {
	svc, store := newTestService(t)
	handler := NewAPIHandler(svc, nil, nil, nil, NewTrustScorer(store, DefaultTrustWeights()))
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodFree)
	if err := store.UpdatePurchaseStatus(purchase.RequestID, PurchaseStatusRefundRequested, "buyer dispute"); err != nil {
		t.Fatalf("UpdatePurchaseStatus failed: %v", err)
	}
	review := &Review{
		ReviewID:       "review-admin-dashboard",
		ListingID:      purchase.ListingID,
		ReviewerPeerID: purchase.BuyerPeerID,
		Rating:         2,
		Title:          "Needs review",
		Content:        "delivery issue",
		Status:         ReviewStatusFlagged,
		FlaggedCount:   1,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := store.CreateReview(review); err != nil {
		t.Fatalf("CreateReview failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/storefront/dashboard/admin", nil)
	req = req.WithContext(auth.ContextWithSession(req.Context(), &auth.Session{XPub: "admin-peer", TrustLevel: peers.Admin}))
	rec := httptest.NewRecorder()
	handler.handleAdminDashboard(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin dashboard code = %d body=%s", rec.Code, rec.Body.String())
	}
	var body AdminDashboardResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode admin dashboard: %v", err)
	}
	if len(body.Moderation) != 1 || body.Moderation[0].ReviewID != review.ReviewID {
		t.Fatalf("moderation = %#v, want flagged review", body.Moderation)
	}
	if len(body.Disputes) != 1 || body.Disputes[0].ReviewID != review.ReviewID {
		t.Fatalf("disputes = %#v, want flagged review", body.Disputes)
	}
	if len(body.PaymentHolds) != 1 || body.PaymentHolds[0].RequestID != purchase.RequestID {
		t.Fatalf("payment holds = %#v, want purchase %s", body.PaymentHolds, purchase.RequestID)
	}
	if len(body.Trust) == 0 {
		t.Fatal("expected provider trust score surface")
	}
}
