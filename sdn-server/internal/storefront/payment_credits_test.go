package storefront

import (
	"context"
	"testing"
)

func TestCreditsPaymentCompletesGrantAndIsIdempotent(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodSDNCredits)

	if err := svc.DepositCredits(ctx, purchase.BuyerPeerID, 10000); err != nil {
		t.Fatalf("DepositCredits failed: %v", err)
	}
	pp := NewPaymentProcessor(store, "test-peer-id")
	if err := pp.ProcessCredits(ctx, purchase.RequestID, purchase.BuyerPeerID, purchase.PaymentAmount, purchase.ProviderPeerID); err != nil {
		t.Fatalf("ProcessCredits failed: %v", err)
	}
	grant, err := svc.CompleteCreditsPayment(ctx, purchase.RequestID)
	if err != nil {
		t.Fatalf("CompleteCreditsPayment failed: %v", err)
	}
	if grant.GrantID == "" || grant.PaymentMethod != PaymentMethodSDNCredits || grant.PaymentAmount != purchase.PaymentAmount {
		t.Fatalf("grant missing credits payment fields: %#v", grant)
	}

	buyerBalance, err := store.GetCreditsBalance(purchase.BuyerPeerID)
	if err != nil {
		t.Fatalf("GetCreditsBalance buyer failed: %v", err)
	}
	providerBalance, err := store.GetCreditsBalance(purchase.ProviderPeerID)
	if err != nil {
		t.Fatalf("GetCreditsBalance provider failed: %v", err)
	}
	if buyerBalance.Balance != 10000-purchase.PaymentAmount {
		t.Fatalf("buyer balance = %d, want %d", buyerBalance.Balance, 10000-purchase.PaymentAmount)
	}
	if providerBalance.Balance != purchase.PaymentAmount {
		t.Fatalf("provider balance = %d, want %d", providerBalance.Balance, purchase.PaymentAmount)
	}

	if err := pp.ProcessCredits(ctx, purchase.RequestID, purchase.BuyerPeerID, purchase.PaymentAmount, purchase.ProviderPeerID); err != nil {
		t.Fatalf("duplicate ProcessCredits failed: %v", err)
	}
	again, err := svc.CompleteCreditsPayment(ctx, purchase.RequestID)
	if err != nil {
		t.Fatalf("duplicate CompleteCreditsPayment failed: %v", err)
	}
	if again.GrantID != grant.GrantID {
		t.Fatalf("duplicate completion issued new grant: got %s want %s", again.GrantID, grant.GrantID)
	}
	buyerBalance, _ = store.GetCreditsBalance(purchase.BuyerPeerID)
	providerBalance, _ = store.GetCreditsBalance(purchase.ProviderPeerID)
	if buyerBalance.Balance != 10000-purchase.PaymentAmount || providerBalance.Balance != purchase.PaymentAmount {
		t.Fatalf("duplicate credits flow changed balances: buyer=%d provider=%d", buyerBalance.Balance, providerBalance.Balance)
	}
}

func TestCreditsPaymentRejectsMismatchedPurchaseTerms(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodSDNCredits)
	if err := svc.DepositCredits(ctx, purchase.BuyerPeerID, 10000); err != nil {
		t.Fatalf("DepositCredits failed: %v", err)
	}
	pp := NewPaymentProcessor(store, "test-peer-id")
	if err := pp.ProcessCredits(ctx, purchase.RequestID, purchase.BuyerPeerID, purchase.PaymentAmount+1, purchase.ProviderPeerID); err == nil {
		t.Fatal("ProcessCredits accepted a mismatched amount")
	}
	if err := pp.ProcessCredits(ctx, purchase.RequestID, "other-buyer", purchase.PaymentAmount, purchase.ProviderPeerID); err == nil {
		t.Fatal("ProcessCredits accepted a mismatched buyer")
	}
}
