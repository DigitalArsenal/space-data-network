package storefront

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// spyChainVerifier records whether it was invoked, so tests can prove a
// request was rejected BEFORE any chain verification was attempted (as
// opposed to being rejected by the verifier itself).
type spyChainVerifier struct {
	chain  string
	called bool
}

func (m *spyChainVerifier) Chain() string { return m.chain }

func (m *spyChainVerifier) VerifyTransaction(ctx context.Context, req *CryptoPaymentRequest) (*CryptoPaymentResult, error) {
	m.called = true
	return &CryptoPaymentResult{
		Verified:         true,
		Chain:            m.chain,
		RecipientAddress: "0xProviderWallet",
		Amount:           4900,
		NativeAsset:      true,
		Asset:            "eth",
	}, nil
}

// TestConfirmPaymentRejectsEmptyReferenceWithoutTouchingVerifier is the B5
// acceptance test for the deleted auto-confirm branch: a confirm request
// with no "reference" must be rejected before any chain verifier (let alone
// a grant) is ever consulted.
func TestConfirmPaymentRejectsEmptyReferenceWithoutTouchingVerifier(t *testing.T) {
	svc, _ := newTestService(t)
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)

	spy := &spyChainVerifier{chain: "ethereum"}
	pp := NewPaymentProcessor(nil, "test-peer-id", spy)
	handler := NewAPIHandler(svc, nil, nil, pp, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"txHash": "0xabc123",
		"chain":  "ethereum",
		// reference intentionally omitted
	})
	req := httptest.NewRequest(http.MethodPost, "/api/storefront/purchases/"+purchase.RequestID+"/confirm", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.handleConfirmPayment(rec, req, purchase.RequestID)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if spy.called {
		t.Fatal("chain verifier was invoked for a reference-less confirm request")
	}

	updated, err := svc.store.GetPurchaseRequest(purchase.RequestID)
	if err != nil {
		t.Fatalf("GetPurchaseRequest failed: %v", err)
	}
	if updated.Status == PurchaseStatusCompleted || updated.GrantID != "" {
		t.Fatalf("purchase should not be completed/granted: %#v", updated)
	}
}

// TestConfirmPaymentRejectsWhenNoPaymentProcessorConfigured proves the
// previous "no h.payment configured -> fall through to auto-confirm" hole is
// gone: with no payment processor wired in, confirmation must fail closed,
// not silently issue a grant.
func TestConfirmPaymentRejectsWhenNoPaymentProcessorConfigured(t *testing.T) {
	svc, _ := newTestService(t)
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)

	handler := NewAPIHandler(svc, nil, nil, nil, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"txHash":    "0xabc123",
		"chain":     "ethereum",
		"reference": "crypto:whatever:xyz",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/storefront/purchases/"+purchase.RequestID+"/confirm", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.handleConfirmPayment(rec, req, purchase.RequestID)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want %d, body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}

	updated, err := svc.store.GetPurchaseRequest(purchase.RequestID)
	if err != nil {
		t.Fatalf("GetPurchaseRequest failed: %v", err)
	}
	if updated.Status == PurchaseStatusCompleted || updated.GrantID != "" {
		t.Fatalf("purchase should not be completed/granted: %#v", updated)
	}
}

// TestManualDevPaidUnreachableWithoutEnvFlag is the B5 acceptance test: the
// manual-dev-paid endpoint must behave as if it does not exist when
// DevPaymentsEnvVar is not set, regardless of who calls it.
func TestManualDevPaidUnreachableWithoutEnvFlag(t *testing.T) {
	svc, _ := newTestService(t)
	handler := NewAPIHandler(svc, nil, nil, nil, nil)

	req := &PurchaseRequest{
		ListingID:     mustSeedListingForManualDev(t, svc),
		TierName:      "Basic",
		BuyerPeerID:   "buyer-peer-123",
		PaymentMethod: PaymentMethodCryptoETH,
	}
	if err := svc.CreatePurchaseRequest(context.Background(), req); err != nil {
		t.Fatalf("CreatePurchaseRequest failed: %v", err)
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/api/storefront/purchases/"+req.RequestID+"/manual-dev-paid", bytes.NewReader([]byte(`{}`)))
	httpReq = httpReq.WithContext(auth.ContextWithSession(httpReq.Context(), &auth.Session{XPub: "buyer-peer-123", TrustLevel: peers.Admin}))
	rec := httptest.NewRecorder()
	handler.handleManualDevPaid(rec, httpReq, req.RequestID)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want %d (endpoint must look unregistered without the env flag), body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	updated, err := svc.store.GetPurchaseRequest(req.RequestID)
	if err != nil {
		t.Fatalf("GetPurchaseRequest failed: %v", err)
	}
	if updated.Status == PurchaseStatusCompleted || updated.GrantID != "" {
		t.Fatalf("purchase should not be completed/granted: %#v", updated)
	}
}

// TestManualDevPaidRejectsNonOwnerNonAdminEvenWithEnvFlag proves the flag
// alone is not enough: an authenticated caller who neither owns the
// purchase nor holds admin trust must still be rejected.
func TestManualDevPaidRejectsNonOwnerNonAdminEvenWithEnvFlag(t *testing.T) {
	t.Setenv(DevPaymentsEnvVar, "1")
	svc, _ := newTestService(t)
	handler := NewAPIHandler(svc, nil, nil, nil, nil)

	req := &PurchaseRequest{
		ListingID:     mustSeedListingForManualDev(t, svc),
		TierName:      "Basic",
		BuyerPeerID:   "buyer-peer-123",
		PaymentMethod: PaymentMethodCryptoETH,
	}
	if err := svc.CreatePurchaseRequest(context.Background(), req); err != nil {
		t.Fatalf("CreatePurchaseRequest failed: %v", err)
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/api/storefront/purchases/"+req.RequestID+"/manual-dev-paid", bytes.NewReader([]byte(`{}`)))
	httpReq = httpReq.WithContext(auth.ContextWithSession(httpReq.Context(), &auth.Session{XPub: "someone-else-entirely", TrustLevel: peers.Standard}))
	rec := httptest.NewRecorder()
	handler.handleManualDevPaid(rec, httpReq, req.RequestID)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want %d, body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	updated, err := svc.store.GetPurchaseRequest(req.RequestID)
	if err != nil {
		t.Fatalf("GetPurchaseRequest failed: %v", err)
	}
	if updated.Status == PurchaseStatusCompleted || updated.GrantID != "" {
		t.Fatalf("purchase should not be completed/granted: %#v", updated)
	}
}

// TestManualDevPaidWorksWithFlagAndOwner and TestManualDevPaidWorksWithFlagAndAdmin
// cover the positive path required by B5: WITH the flag AND an authorized
// caller (either the purchase's own buyer, or an admin), the endpoint
// issues a grant.
func TestManualDevPaidWorksWithFlagAndOwner(t *testing.T) {
	t.Setenv(DevPaymentsEnvVar, "1")
	svc, _ := newTestService(t)
	handler := NewAPIHandler(svc, nil, nil, nil, nil)

	req := &PurchaseRequest{
		ListingID:     mustSeedListingForManualDev(t, svc),
		TierName:      "Basic",
		BuyerPeerID:   "buyer-peer-123",
		PaymentMethod: PaymentMethodCryptoETH,
	}
	if err := svc.CreatePurchaseRequest(context.Background(), req); err != nil {
		t.Fatalf("CreatePurchaseRequest failed: %v", err)
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/api/storefront/purchases/"+req.RequestID+"/manual-dev-paid", bytes.NewReader([]byte(`{}`)))
	httpReq = httpReq.WithContext(auth.ContextWithSession(httpReq.Context(), &auth.Session{XPub: "buyer-peer-123", TrustLevel: peers.Standard}))
	rec := httptest.NewRecorder()
	handler.handleManualDevPaid(rec, httpReq, req.RequestID)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := svc.store.GetPurchaseRequest(req.RequestID)
	if err != nil {
		t.Fatalf("GetPurchaseRequest failed: %v", err)
	}
	if updated.Status != PurchaseStatusCompleted || updated.GrantID == "" {
		t.Fatalf("purchase should be completed with a grant: %#v", updated)
	}
}

func TestManualDevPaidWorksWithFlagAndAdmin(t *testing.T) {
	t.Setenv(DevPaymentsEnvVar, "1")
	svc, _ := newTestService(t)
	handler := NewAPIHandler(svc, nil, nil, nil, nil)

	req := &PurchaseRequest{
		ListingID:     mustSeedListingForManualDev(t, svc),
		TierName:      "Basic",
		BuyerPeerID:   "buyer-peer-123",
		PaymentMethod: PaymentMethodCryptoETH,
	}
	if err := svc.CreatePurchaseRequest(context.Background(), req); err != nil {
		t.Fatalf("CreatePurchaseRequest failed: %v", err)
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/api/storefront/purchases/"+req.RequestID+"/manual-dev-paid", bytes.NewReader([]byte(`{}`)))
	httpReq = httpReq.WithContext(auth.ContextWithSession(httpReq.Context(), &auth.Session{XPub: "admin-operator", TrustLevel: peers.Admin}))
	rec := httptest.NewRecorder()
	handler.handleManualDevPaid(rec, httpReq, req.RequestID)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// TestServiceCompleteManualDevPaymentFailsClosedWithoutEnvFlag proves the
// gate is enforced at the service layer too (defense in depth), not just at
// the HTTP handler.
func TestServiceCompleteManualDevPaymentFailsClosedWithoutEnvFlag(t *testing.T) {
	svc, _ := newTestService(t)

	req := &PurchaseRequest{
		ListingID:     mustSeedListingForManualDev(t, svc),
		TierName:      "Basic",
		BuyerPeerID:   "buyer-peer-123",
		PaymentMethod: PaymentMethodCryptoETH,
	}
	if err := svc.CreatePurchaseRequest(context.Background(), req); err != nil {
		t.Fatalf("CreatePurchaseRequest failed: %v", err)
	}

	if _, err := svc.CompleteManualDevPayment(context.Background(), req.RequestID, ManualDevPaymentConfirmation{}); err == nil {
		t.Fatal("expected CompleteManualDevPayment to fail closed without the env flag")
	}
}

func mustSeedListingForManualDev(t *testing.T, svc *Service) string {
	t.Helper()
	listing := testListing()
	listing.ListingID = ""
	listing.AcceptedPayments = []PaymentMethod{PaymentMethodCryptoETH}
	if err := svc.CreateListing(context.Background(), listing); err != nil {
		t.Fatalf("CreateListing failed: %v", err)
	}
	return listing.ListingID
}
