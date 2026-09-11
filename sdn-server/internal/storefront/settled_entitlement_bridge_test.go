package storefront

import (
	"context"
	"sync"
	"testing"
)

type recordingEntitlementBridge struct {
	mu           sync.Mutex
	entitlements map[string]map[string]struct{}
	provisions   int
	revocations  int
}

func newRecordingEntitlementBridge() *recordingEntitlementBridge {
	return &recordingEntitlementBridge{entitlements: make(map[string]map[string]struct{})}
}

func (b *recordingEntitlementBridge) ProvisionSettledEntitlement(_ context.Context, moduleID, buyerXPub string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.provisions++
	if b.entitlements[moduleID] == nil {
		b.entitlements[moduleID] = make(map[string]struct{})
	}
	b.entitlements[moduleID][buyerXPub] = struct{}{}
	return nil
}

func (b *recordingEntitlementBridge) RevokeSettledEntitlement(_ context.Context, moduleID, buyerXPub string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revocations++
	delete(b.entitlements[moduleID], buyerXPub)
	return nil
}

func (b *recordingEntitlementBridge) has(moduleID, buyerXPub string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.entitlements[moduleID][buyerXPub]
	return ok
}

func (b *recordingEntitlementBridge) count(moduleID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entitlements[moduleID])
}

func newProtectedCryptoPurchase(t *testing.T, svc *Service) *PurchaseRequest {
	t.Helper()
	listing := testListing()
	listing.ListingID = ""
	listing.AcceptedPayments = []PaymentMethod{PaymentMethodCryptoSOL}
	listing.EncryptionRequired = true
	listing.ListingKind = ListingKindWASMModule
	listing.ProtectedDelivery = ProtectedDelivery{
		ModuleID:         "com.example.protected",
		ModuleVersion:    "1.0.0",
		LicenseModuleID:  "licensing",
		DeliveryProtocol: "/space-data-network/module-delivery/1.0.0",
	}
	if err := svc.CreateListing(context.Background(), listing); err != nil {
		t.Fatal(err)
	}
	purchase := &PurchaseRequest{
		ListingID:     listing.ListingID,
		TierName:      "Basic",
		BuyerPeerID:   "xpub-settled-buyer",
		PaymentMethod: PaymentMethodCryptoSOL,
	}
	if err := svc.CreatePurchaseRequest(context.Background(), purchase); err != nil {
		t.Fatal(err)
	}
	return purchase
}

func TestSettledPurchaseEntitlementIsIdempotentAndRevocable(t *testing.T) {
	t.Setenv(DevPaymentsEnvVar, "0")
	svc, store := newTestService(t)
	bridge := newRecordingEntitlementBridge()
	svc.SetSettledEntitlementBridge(bridge)
	purchase := newProtectedCryptoPurchase(t, svc)

	if err := store.UpdatePurchasePayment(purchase.RequestID, "devnet-tx", "solana", "sender"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdatePurchaseStatus(purchase.RequestID, PurchaseStatusPaymentDetected, "verified on chain"); err != nil {
		t.Fatal(err)
	}
	result := &CryptoPaymentResult{Verified: true, Chain: "solana", ConfirmationBlock: 42}
	grant, err := svc.CompleteCryptoPayment(context.Background(), purchase.RequestID, result)
	if err != nil {
		t.Fatalf("CompleteCryptoPayment() failed: %v", err)
	}
	if !bridge.has("com.example.protected", purchase.BuyerPeerID) {
		t.Fatal("settled purchase did not provision the buyer xpub")
	}

	again, err := svc.CompleteCryptoPayment(context.Background(), purchase.RequestID, result)
	if err != nil {
		t.Fatalf("idempotent CompleteCryptoPayment() failed: %v", err)
	}
	if again.GrantID != grant.GrantID {
		t.Fatalf("completion retry issued grant %q, want %q", again.GrantID, grant.GrantID)
	}
	if bridge.count("com.example.protected") != 1 {
		t.Fatalf("completion retry duplicated entitlement: %#v", bridge.entitlements)
	}

	if _, err := svc.RevokeGrant(context.Background(), grant.GrantID, purchase.BuyerPeerID, "provider", "refund"); err != nil {
		t.Fatalf("RevokeGrant() failed: %v", err)
	}
	if bridge.has("com.example.protected", purchase.BuyerPeerID) {
		t.Fatal("revoked grant left the buyer xpub entitled")
	}
	if _, err := svc.RevokeGrant(context.Background(), grant.GrantID, purchase.BuyerPeerID, "provider", "retry"); err != nil {
		t.Fatalf("idempotent RevokeGrant() failed: %v", err)
	}
}

func TestUnpaidOrUnverifiedPurchaseNeverProvisionsEntitlement(t *testing.T) {
	t.Setenv(DevPaymentsEnvVar, "0")
	svc, _ := newTestService(t)
	bridge := newRecordingEntitlementBridge()
	svc.SetSettledEntitlementBridge(bridge)
	purchase := newProtectedCryptoPurchase(t, svc)

	if _, err := svc.CompleteCryptoPayment(context.Background(), purchase.RequestID, &CryptoPaymentResult{Verified: false, Chain: "solana"}); err == nil {
		t.Fatal("unverified payment completed")
	}
	if _, err := svc.CompleteCryptoPayment(context.Background(), purchase.RequestID, &CryptoPaymentResult{Verified: true, Chain: "solana"}); err == nil {
		t.Fatal("unrecorded payment completed")
	}
	if bridge.provisions != 0 || bridge.has("com.example.protected", purchase.BuyerPeerID) {
		t.Fatalf("unpaid/unverified purchase provisioned entitlement: %+v", bridge)
	}
}

func TestConcurrentRevocationsRemoveTheLastModuleEntitlement(t *testing.T) {
	t.Setenv(DevPaymentsEnvVar, "0")
	svc, store := newTestService(t)
	bridge := newRecordingEntitlementBridge()
	svc.SetSettledEntitlementBridge(bridge)

	settle := func(purchase *PurchaseRequest, tx string) *AccessGrant {
		t.Helper()
		if err := store.UpdatePurchasePayment(purchase.RequestID, tx, "solana", "sender"); err != nil {
			t.Fatal(err)
		}
		if err := store.UpdatePurchaseStatus(purchase.RequestID, PurchaseStatusPaymentDetected, "verified on chain"); err != nil {
			t.Fatal(err)
		}
		grant, err := svc.CompleteCryptoPayment(context.Background(), purchase.RequestID, &CryptoPaymentResult{
			Verified: true, Chain: "solana", ConfirmationBlock: 42,
		})
		if err != nil {
			t.Fatal(err)
		}
		return grant
	}

	firstPurchase := newProtectedCryptoPurchase(t, svc)
	secondPurchase := newProtectedCryptoPurchase(t, svc)
	first := settle(firstPurchase, "devnet-tx-first")
	second := settle(secondPurchase, "devnet-tx-second")
	if bridge.count("com.example.protected") != 1 {
		t.Fatal("two grants for one buyer/module did not share one entitlement")
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for _, grant := range []*AccessGrant{first, second} {
		grant := grant
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.RevokeGrant(context.Background(), grant.GrantID, grant.BuyerPeerID, "provider", "concurrent revoke")
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent RevokeGrant() failed: %v", err)
		}
	}
	if bridge.has("com.example.protected", first.BuyerPeerID) {
		t.Fatal("concurrent revocations left the final buyer/module entitlement active")
	}
}
