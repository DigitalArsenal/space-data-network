package storefront

import (
	"context"
	"strings"
	"testing"
)

func TestCryptoIntentValidationRejectsWrongPaymentDetails(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	listing := testListing()
	listing.AcceptedPayments = []PaymentMethod{PaymentMethodCryptoETH}
	if err := svc.CreateListing(ctx, listing); err != nil {
		t.Fatalf("CreateListing failed: %v", err)
	}

	purchase := &PurchaseRequest{
		ListingID:     listing.ListingID,
		TierName:      "Basic",
		BuyerPeerID:   "buyer-peer-1",
		PaymentMethod: PaymentMethodCryptoETH,
	}
	if err := svc.CreatePurchaseRequest(ctx, purchase); err != nil {
		t.Fatalf("CreatePurchaseRequest failed: %v", err)
	}

	pp := NewPaymentProcessor(store, "test-peer-id", &mockChainVerifier{
		chain:  "ethereum",
		result: &CryptoPaymentResult{Verified: true, ConfirmationBlock: 12345},
	})
	intent, err := pp.CreateCryptoBuyerIntent(ctx, &CreateCryptoIntentRequest{
		RequestID: purchase.RequestID,
		Chain:     "ethereum",
		Asset:     "ETH",
		Recipient: "0xProviderWallet",
	})
	if err != nil {
		t.Fatalf("CreateCryptoBuyerIntent failed: %v", err)
	}

	tests := []struct {
		name string
		req  CryptoPaymentRequest
		want string
	}{
		{
			name: "wrong recipient",
			req:  cryptoSubmission(purchase.RequestID, intent.Reference, "ethereum", "ETH", 4900, "0xWrongWallet"),
			want: "recipient",
		},
		{
			name: "wrong amount",
			req:  cryptoSubmission(purchase.RequestID, intent.Reference, "ethereum", "ETH", 4899, "0xProviderWallet"),
			want: "amount",
		},
		{
			name: "wrong chain",
			req:  cryptoSubmission(purchase.RequestID, intent.Reference, "solana", "ETH", 4900, "0xProviderWallet"),
			want: "chain",
		},
		{
			name: "wrong asset",
			req:  cryptoSubmission(purchase.RequestID, intent.Reference, "ethereum", "USDC", 4900, "0xProviderWallet"),
			want: "asset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := pp.SubmitCryptoPayment(ctx, &tt.req)
			if err != nil {
				t.Fatalf("SubmitCryptoPayment returned error: %v", err)
			}
			if result.Verified {
				t.Fatal("payment verified with invalid details")
			}
			if !strings.Contains(strings.ToLower(result.Error), tt.want) {
				t.Fatalf("error = %q, want to contain %q", result.Error, tt.want)
			}
		})
	}
}

func TestCryptoIntentValidationRejectsReusedReference(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	listing := testListing()
	listing.AcceptedPayments = []PaymentMethod{PaymentMethodCryptoETH}
	if err := svc.CreateListing(ctx, listing); err != nil {
		t.Fatalf("CreateListing failed: %v", err)
	}

	first := &PurchaseRequest{
		ListingID:     listing.ListingID,
		TierName:      "Basic",
		BuyerPeerID:   "buyer-peer-1",
		PaymentMethod: PaymentMethodCryptoETH,
	}
	if err := svc.CreatePurchaseRequest(ctx, first); err != nil {
		t.Fatalf("CreatePurchaseRequest first failed: %v", err)
	}
	second := &PurchaseRequest{
		ListingID:     listing.ListingID,
		TierName:      "Basic",
		BuyerPeerID:   "buyer-peer-2",
		PaymentMethod: PaymentMethodCryptoETH,
	}
	if err := svc.CreatePurchaseRequest(ctx, second); err != nil {
		t.Fatalf("CreatePurchaseRequest second failed: %v", err)
	}

	pp := NewPaymentProcessor(store, "test-peer-id", &mockChainVerifier{
		chain:  "ethereum",
		result: &CryptoPaymentResult{Verified: true, ConfirmationBlock: 12345},
	})
	intent, err := pp.CreateCryptoBuyerIntent(ctx, &CreateCryptoIntentRequest{
		RequestID: first.RequestID,
		Chain:     "ethereum",
		Asset:     "ETH",
		Recipient: "0xProviderWallet",
	})
	if err != nil {
		t.Fatalf("CreateCryptoBuyerIntent failed: %v", err)
	}

	result, err := pp.SubmitCryptoPayment(ctx, ptrCryptoSubmission(cryptoSubmission(first.RequestID, intent.Reference, "ethereum", "ETH", 4900, "0xProviderWallet")))
	if err != nil {
		t.Fatalf("SubmitCryptoPayment first failed: %v", err)
	}
	if !result.Verified {
		t.Fatalf("first payment not verified: %s", result.Error)
	}

	reused := cryptoSubmission(second.RequestID, intent.Reference, "ethereum", "ETH", 4900, "0xProviderWallet")
	reused.TxHash = "0xsecond"
	result, err = pp.SubmitCryptoPayment(ctx, &reused)
	if err != nil {
		t.Fatalf("SubmitCryptoPayment reused returned error: %v", err)
	}
	if result.Verified {
		t.Fatal("payment verified with reused reference")
	}
	if !strings.Contains(strings.ToLower(result.Error), "reused") {
		t.Fatalf("error = %q, want reused reference rejection", result.Error)
	}
}

func cryptoSubmission(requestID, reference, chain, asset string, amount uint64, recipient string) CryptoPaymentRequest {
	return CryptoPaymentRequest{
		RequestID:        requestID,
		TxHash:           "0xabc123",
		Chain:            chain,
		RecipientAddress: recipient,
		Reference:        reference,
		Amount:           amount,
		Currency:         asset,
	}
}

func ptrCryptoSubmission(req CryptoPaymentRequest) *CryptoPaymentRequest {
	return &req
}
