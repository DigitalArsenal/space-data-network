package storefront

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCryptoBuyerIntentIsSignedAndTamperRejected(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)

	pp := NewPaymentProcessor(store, "test-peer-id", &mockChainVerifier{
		chain:  "ethereum",
		result: verifiedCryptoResult(),
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
	if intent.IntentDigest == "" || intent.IntentSig == "" {
		t.Fatalf("intent missing digest/signature: %#v", intent)
	}

	if _, err := store.db.Exec(`UPDATE storefront_crypto_intents SET recipient = ? WHERE reference = ?`, "0xAttacker", intent.Reference); err != nil {
		t.Fatalf("tamper intent failed: %v", err)
	}

	result, err := pp.SubmitCryptoPayment(ctx, ptrCryptoSubmission(cryptoSubmission(purchase.RequestID, intent.Reference, "ethereum", "ETH", 4900, "0xProviderWallet")))
	if err != nil {
		t.Fatalf("SubmitCryptoPayment returned error: %v", err)
	}
	if result.Verified {
		t.Fatal("payment verified against a tampered signed intent")
	}
	if !strings.Contains(strings.ToLower(result.Error), "signature") {
		t.Fatalf("error = %q, want signature rejection", result.Error)
	}
}

func TestCryptoPaymentCompletionIssuesGrantAndIsIdempotent(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)

	pp := NewPaymentProcessor(store, "test-peer-id", &mockChainVerifier{
		chain:  "ethereum",
		result: verifiedCryptoResult(),
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
	result, err := pp.SubmitCryptoPayment(ctx, ptrCryptoSubmission(cryptoSubmission(purchase.RequestID, intent.Reference, "ethereum", "ETH", 4900, "0xProviderWallet")))
	if err != nil {
		t.Fatalf("SubmitCryptoPayment failed: %v", err)
	}
	if !result.Verified {
		t.Fatalf("SubmitCryptoPayment not verified: %s", result.Error)
	}
	grant, err := svc.CompleteCryptoPayment(ctx, purchase.RequestID, result)
	if err != nil {
		t.Fatalf("CompleteCryptoPayment failed: %v", err)
	}
	if grant.GrantID == "" || grant.PaymentTxHash != "0xabc123" || grant.PaymentChain != "ethereum" {
		t.Fatalf("grant payment fields not populated: %#v", grant)
	}
	updated, err := store.GetPurchaseRequest(purchase.RequestID)
	if err != nil {
		t.Fatalf("GetPurchaseRequest failed: %v", err)
	}
	if updated.Status != PurchaseStatusCompleted || updated.GrantID != grant.GrantID || updated.ConfirmationBlock != 12345 {
		t.Fatalf("purchase not completed with grant/block: %#v", updated)
	}

	again, err := svc.CompleteCryptoPayment(ctx, purchase.RequestID, result)
	if err != nil {
		t.Fatalf("CompleteCryptoPayment duplicate failed: %v", err)
	}
	if again.GrantID != grant.GrantID {
		t.Fatalf("duplicate completion issued new grant: got %s want %s", again.GrantID, grant.GrantID)
	}
}

func TestCryptoVerifierPolicyRejectsOnChainMismatches(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()

	tests := []struct {
		name   string
		result *CryptoPaymentResult
		want   string
	}{
		{
			name:   "wrong recipient from verifier",
			result: cloneVerifiedCryptoResult(func(r *CryptoPaymentResult) { r.RecipientAddress = "0xWrongWallet" }),
			want:   "recipient",
		},
		{
			name:   "wrong amount from verifier",
			result: cloneVerifiedCryptoResult(func(r *CryptoPaymentResult) { r.Amount = 4899 }),
			want:   "amount",
		},
		{
			name:   "wrong token contract for native asset",
			result: cloneVerifiedCryptoResult(func(r *CryptoPaymentResult) { r.NativeAsset = false; r.AssetContract = "0xToken" }),
			want:   "token",
		},
		{
			name:   "stale block height",
			result: cloneVerifiedCryptoResult(func(r *CryptoPaymentResult) { r.CurrentBlock = 100; r.ConfirmationBlock = 12345 }),
			want:   "stale",
		},
		{
			name:   "insufficient confirmations",
			result: &CryptoPaymentResult{Verified: false, ConfirmationBlock: 12345, Confirmations: 1, Error: "insufficient confirmations: 1/12"},
			want:   "insufficient",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			purchase := createStorefrontPurchaseForTest(t, svc, PaymentMethodCryptoETH)
			pp := NewPaymentProcessor(store, "test-peer-id", &mockChainVerifier{
				chain:  "ethereum",
				result: tt.result,
			})
			intent, err := pp.CreateCryptoBuyerIntent(ctx, &CreateCryptoIntentRequest{
				RequestID: purchase.RequestID,
				Chain:     "ethereum",
				Asset:     "ETH",
				Recipient: "0xProviderWallet",
				ExpiresAt: time.Now().Add(5 * time.Minute),
			})
			if err != nil {
				t.Fatalf("CreateCryptoBuyerIntent failed: %v", err)
			}
			result, err := pp.SubmitCryptoPayment(ctx, ptrCryptoSubmission(cryptoSubmission(purchase.RequestID, intent.Reference, "ethereum", "ETH", 4900, "0xProviderWallet")))
			if err != nil {
				t.Fatalf("SubmitCryptoPayment returned error: %v", err)
			}
			if result.Verified {
				t.Fatal("payment verified with on-chain mismatch")
			}
			if !strings.Contains(strings.ToLower(result.Error), tt.want) {
				t.Fatalf("error = %q, want %q", result.Error, tt.want)
			}
		})
	}
}

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
		NativeAsset:      true,
	}
}

func ptrCryptoSubmission(req CryptoPaymentRequest) *CryptoPaymentRequest {
	return &req
}

func createStorefrontPurchaseForTest(t *testing.T, svc *Service, method PaymentMethod) *PurchaseRequest {
	t.Helper()
	listing := testListing()
	listing.ListingID = ""
	listing.AcceptedPayments = []PaymentMethod{method}
	if err := svc.CreateListing(context.Background(), listing); err != nil {
		t.Fatalf("CreateListing failed: %v", err)
	}
	purchase := &PurchaseRequest{
		ListingID:     listing.ListingID,
		TierName:      "Basic",
		BuyerPeerID:   "buyer-peer-" + generateToken(4),
		PaymentMethod: method,
	}
	if err := svc.CreatePurchaseRequest(context.Background(), purchase); err != nil {
		t.Fatalf("CreatePurchaseRequest failed: %v", err)
	}
	return purchase
}

func verifiedCryptoResult() *CryptoPaymentResult {
	return &CryptoPaymentResult{
		Verified:          true,
		ConfirmationBlock: 12345,
		CurrentBlock:      12360,
		Confirmations:     15,
		Chain:             "ethereum",
		Asset:             "ETH",
		NativeAsset:       true,
		Amount:            4900,
		RecipientAddress:  "0xProviderWallet",
	}
}

func cloneVerifiedCryptoResult(mutator func(*CryptoPaymentResult)) *CryptoPaymentResult {
	result := *verifiedCryptoResult()
	mutator(&result)
	return &result
}
