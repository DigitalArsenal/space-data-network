package storefront

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// --- FINDING A: crypto payment intent recipient must be server-authoritative ---

// TestCryptoIntentRecipientIsServerAuthoritative is the acceptance test for
// FINDING A: a buyer must not be able to steer a crypto payment intent's
// recipient by supplying their own address in the request. Before the fix,
// req.Recipient was used outright whenever present, so a buyer could set it
// to an address they control, pay themselves on-chain, and the confirm-time
// binding (validateVerifiedCryptoPayment) would faithfully validate the
// payment against that attacker-chosen recipient.
func TestCryptoIntentRecipientIsServerAuthoritative(t *testing.T) {
	t.Setenv("SDN_CRYPTO_ETHEREUM_RECIPIENT", "0xProviderWallet")
	svc, store := newTestService(t)
	ctx := context.Background()
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)

	pp := NewPaymentProcessor(store, "test-peer-id")

	// A buyer-supplied recipient that disagrees with the server-configured
	// address must be rejected outright.
	if _, err := pp.CreateCryptoBuyerIntent(ctx, &CreateCryptoIntentRequest{
		RequestID: purchase.RequestID,
		Chain:     "ethereum",
		Asset:     "ETH",
		Recipient: "0xAttackerWallet",
	}); err == nil {
		t.Fatal("expected CreateCryptoBuyerIntent to reject a client-supplied recipient that mismatches the server-configured one")
	} else if !strings.Contains(strings.ToLower(err.Error()), "recipient") {
		t.Fatalf("error = %q, want a recipient-mismatch rejection", err.Error())
	}

	// Nothing should have been persisted for the rejected attempt.
	updatedPurchase, err := store.GetPurchaseRequest(purchase.RequestID)
	if err != nil {
		t.Fatalf("GetPurchaseRequest failed: %v", err)
	}
	if updatedPurchase.PaymentIntentID != "" {
		t.Fatalf("purchase should not have a recorded payment intent after a rejected recipient: %#v", updatedPurchase)
	}

	// The honest path (no client-supplied recipient) must succeed, and the
	// resulting intent's recipient must be the server-derived address --
	// proving the server is authoritative rather than merely "validating" a
	// client-supplied value.
	intent, err := pp.CreateCryptoBuyerIntent(ctx, &CreateCryptoIntentRequest{
		RequestID: purchase.RequestID,
		Chain:     "ethereum",
		Asset:     "ETH",
	})
	if err != nil {
		t.Fatalf("CreateCryptoBuyerIntent failed: %v", err)
	}
	if intent.Recipient != "0xProviderWallet" {
		t.Fatalf("intent.Recipient = %q, want server-configured 0xProviderWallet", intent.Recipient)
	}

	// A client-supplied recipient that happens to MATCH the server-derived
	// value is harmless and must still succeed with the server value as the
	// recipient of record.
	purchase2 := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)
	intent2, err := pp.CreateCryptoBuyerIntent(ctx, &CreateCryptoIntentRequest{
		RequestID: purchase2.RequestID,
		Chain:     "ethereum",
		Asset:     "ETH",
		Recipient: "0xProviderWallet",
	})
	if err != nil {
		t.Fatalf("CreateCryptoBuyerIntent with matching recipient failed: %v", err)
	}
	if intent2.Recipient != "0xProviderWallet" {
		t.Fatalf("intent2.Recipient = %q, want 0xProviderWallet", intent2.Recipient)
	}
}

// TestCryptoIntentCreationFailsClosedWithoutServerRecipient is the second
// acceptance test for FINDING A: if the operator has not configured a
// recipient for a chain at all, intent creation must fail closed -- it must
// NOT fall back to accepting whatever recipient the client supplies.
func TestCryptoIntentCreationFailsClosedWithoutServerRecipient(t *testing.T) {
	// Deliberately no SDN_CRYPTO_ETHEREUM_RECIPIENT configured.
	t.Setenv("SDN_CRYPTO_ETHEREUM_RECIPIENT", "")
	svc, store := newTestService(t)
	ctx := context.Background()
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)

	pp := NewPaymentProcessor(store, "test-peer-id")

	if _, err := pp.CreateCryptoBuyerIntent(ctx, &CreateCryptoIntentRequest{
		RequestID: purchase.RequestID,
		Chain:     "ethereum",
		Asset:     "ETH",
		Recipient: "0xAttackerWallet",
	}); err == nil {
		t.Fatal("expected CreateCryptoBuyerIntent to fail closed with no server-configured recipient, even with a client-supplied one")
	}

	if _, err := pp.CreateCryptoBuyerIntent(ctx, &CreateCryptoIntentRequest{
		RequestID: purchase.RequestID,
		Chain:     "ethereum",
		Asset:     "ETH",
	}); err == nil {
		t.Fatal("expected CreateCryptoBuyerIntent to fail closed with no server-configured recipient and no client value supplied")
	}
}

// --- FINDING A: handleCreateCryptoIntent must enforce ownership like its siblings ---

// TestCreateCryptoIntentRejectsNonOwnerNonAdmin proves handleCreateCryptoIntent
// now checks caller authority the same way handleManualDevPaid does: an
// authenticated caller who neither owns the purchase nor holds admin trust
// must be rejected, consistent with the sibling handlers in api.go.
func TestCreateCryptoIntentRejectsNonOwnerNonAdmin(t *testing.T) {
	t.Setenv("SDN_CRYPTO_ETHEREUM_RECIPIENT", "0xProviderWallet")
	svc, store := newTestService(t)
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)

	pp := NewPaymentProcessor(store, "test-peer-id")
	handler := NewAPIHandler(svc, nil, nil, pp, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"chain": "ethereum",
		"asset": "ETH",
	})
	httpReq := httptest.NewRequest(http.MethodPost, "/api/storefront/purchases/"+purchase.RequestID+"/pay-crypto", bytes.NewReader(body))
	httpReq = httpReq.WithContext(auth.ContextWithSession(httpReq.Context(), &auth.Session{
		XPub:       "someone-else-entirely",
		TrustLevel: peers.Standard,
	}))
	rec := httptest.NewRecorder()
	handler.handleCreateCryptoIntent(rec, httpReq, purchase.RequestID)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want %d, body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	updated, err := store.GetPurchaseRequest(purchase.RequestID)
	if err != nil {
		t.Fatalf("GetPurchaseRequest failed: %v", err)
	}
	if updated.PaymentIntentID != "" {
		t.Fatalf("no intent should have been created for a non-owner/non-admin caller: %#v", updated)
	}
}

// TestCreateCryptoIntentWorksForOwnerAndAdmin covers the positive path: the
// purchase's own buyer, and a caller with peers.Admin trust, may both create
// a crypto payment intent for the purchase.
func TestCreateCryptoIntentWorksForOwnerAndAdmin(t *testing.T) {
	t.Setenv("SDN_CRYPTO_ETHEREUM_RECIPIENT", "0xProviderWallet")
	svc, store := newTestService(t)
	pp := NewPaymentProcessor(store, "test-peer-id")
	handler := NewAPIHandler(svc, nil, nil, pp, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"chain": "ethereum",
		"asset": "ETH",
	})

	// Owner.
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/storefront/purchases/"+purchase.RequestID+"/pay-crypto", bytes.NewReader(body))
	httpReq = httpReq.WithContext(auth.ContextWithSession(httpReq.Context(), &auth.Session{
		XPub:       purchase.BuyerPeerID,
		TrustLevel: peers.Standard,
	}))
	rec := httptest.NewRecorder()
	handler.handleCreateCryptoIntent(rec, httpReq, purchase.RequestID)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner code = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Admin, distinct XPub from the buyer.
	purchase2 := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)
	httpReq2 := httptest.NewRequest(http.MethodPost, "/api/storefront/purchases/"+purchase2.RequestID+"/pay-crypto", bytes.NewReader(body))
	httpReq2 = httpReq2.WithContext(auth.ContextWithSession(httpReq2.Context(), &auth.Session{
		XPub:       "admin-operator",
		TrustLevel: peers.Admin,
	}))
	rec2 := httptest.NewRecorder()
	handler.handleCreateCryptoIntent(rec2, httpReq2, purchase2.RequestID)
	if rec2.Code != http.StatusOK {
		t.Fatalf("admin code = %d, want %d body=%s", rec2.Code, http.StatusOK, rec2.Body.String())
	}
}

// --- FINDING B: crypto intent HMAC signing secret must never default to public data ---

// TestIntentSigningSecretNotPeerIDAndPersistedPerStore is the acceptance
// test for FINDING B: with SDN_CRYPTO_INTENT_SIGNING_SECRET unset, the
// signing secret must not be the (public) peer ID, must be stable across
// repeated calls against the same store (persisted, not regenerated per
// call), and must differ between two independent stores (it is genuinely
// randomly generated, not derived deterministically from the peer ID or
// anything else predictable).
func TestIntentSigningSecretNotPeerIDAndPersistedPerStore(t *testing.T) {
	t.Setenv("SDN_CRYPTO_INTENT_SIGNING_SECRET", "")

	store1 := newTestStore(t)
	pp1 := NewPaymentProcessor(store1, "shared-peer-id")

	secret1, err := pp1.intentSigningSecret()
	if err != nil {
		t.Fatalf("intentSigningSecret failed: %v", err)
	}
	if len(secret1) == 0 {
		t.Fatal("expected a non-empty generated secret")
	}
	if string(secret1) == "shared-peer-id" {
		t.Fatal("secret must not fall back to the (public) peer ID")
	}

	secret1Again, err := pp1.intentSigningSecret()
	if err != nil {
		t.Fatalf("intentSigningSecret second call failed: %v", err)
	}
	if !bytes.Equal(secret1, secret1Again) {
		t.Fatalf("secret not stable across calls on the same store: %x != %x", secret1, secret1Again)
	}

	// A second, independent store with the SAME peer ID must still get its
	// own independently generated secret -- proving the value comes from
	// random generation persisted per-store, not from the peer ID.
	store2 := newTestStore(t)
	pp2 := NewPaymentProcessor(store2, "shared-peer-id")
	secret2, err := pp2.intentSigningSecret()
	if err != nil {
		t.Fatalf("intentSigningSecret for second store failed: %v", err)
	}
	if bytes.Equal(secret1, secret2) {
		t.Fatal("expected distinct stores to have independently generated secrets")
	}
}

// TestIntentSigningSecretSurvivesStoreRestart proves the generated secret is
// actually persisted to durable storage (not just cached in-process): a
// fresh Store opened against the same underlying database must recover the
// SAME secret rather than generating a new one, which would otherwise
// invalidate every previously signed crypto buyer intent on every restart.
func TestIntentSigningSecretSurvivesStoreRestart(t *testing.T) {
	t.Setenv("SDN_CRYPTO_INTENT_SIGNING_SECRET", "")

	dir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	flatStore, err := storage.NewFlatSQLStore(dir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() { flatStore.Close() })

	store1, err := NewStore(flatStore)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	pp1 := NewPaymentProcessor(store1, "peer-restart-test")
	secret1, err := pp1.intentSigningSecret()
	if err != nil {
		t.Fatalf("intentSigningSecret failed: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("store1.Close failed: %v", err)
	}

	// Reopen a fresh Store against the same underlying database file --
	// simulating a process restart.
	store2, err := NewStore(flatStore)
	if err != nil {
		t.Fatalf("NewStore (reopen) failed: %v", err)
	}
	t.Cleanup(func() { store2.Close() })
	pp2 := NewPaymentProcessor(store2, "peer-restart-test")
	secret2, err := pp2.intentSigningSecret()
	if err != nil {
		t.Fatalf("intentSigningSecret after reopen failed: %v", err)
	}
	if !bytes.Equal(secret1, secret2) {
		t.Fatalf("secret did not survive store restart: %x != %x", secret1, secret2)
	}
}

// TestIntentSigningSecretFailsClosedWithoutStoreOrEnv proves the "fail
// closed, not silent weak default" requirement of FINDING B: with no env
// var configured AND no store available to persist a generated secret,
// intentSigningSecret must return an error rather than a weak/ephemeral
// fallback.
func TestIntentSigningSecretFailsClosedWithoutStoreOrEnv(t *testing.T) {
	t.Setenv("SDN_CRYPTO_INTENT_SIGNING_SECRET", "")
	pp := NewPaymentProcessor(nil, "some-peer-id")
	if _, err := pp.intentSigningSecret(); err == nil {
		t.Fatal("expected intentSigningSecret to fail closed with no env secret and no store to persist a generated one")
	}
}
