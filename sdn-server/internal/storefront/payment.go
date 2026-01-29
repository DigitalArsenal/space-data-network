package storefront

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PaymentProcessor handles payment verification and processing
type PaymentProcessor struct {
	store  *Store
	peerID string
}

// NewPaymentProcessor creates a new payment processor
func NewPaymentProcessor(store *Store, peerID string) *PaymentProcessor {
	return &PaymentProcessor{
		store:  store,
		peerID: peerID,
	}
}

// CryptoPaymentRequest represents a crypto payment verification request
type CryptoPaymentRequest struct {
	RequestID     string        `json:"request_id"`
	TxHash        string        `json:"tx_hash"`
	Chain         string        `json:"chain"` // ethereum, solana, bitcoin
	SenderAddress string        `json:"sender_address"`
	Amount        uint64        `json:"amount"`
	Currency      string        `json:"currency"`
	Method        PaymentMethod `json:"method"`
}

// CryptoPaymentResult represents the result of crypto payment verification
type CryptoPaymentResult struct {
	Verified          bool   `json:"verified"`
	ConfirmationBlock uint64 `json:"confirmation_block"`
	Error             string `json:"error,omitempty"`
}

// VerifyCryptoPayment verifies a crypto payment on chain
// In production, this would connect to blockchain RPC nodes.
// Currently implements verification stub with status tracking.
func (pp *PaymentProcessor) VerifyCryptoPayment(ctx context.Context, req *CryptoPaymentRequest) (*CryptoPaymentResult, error) {
	if req.TxHash == "" {
		return &CryptoPaymentResult{Verified: false, Error: "tx_hash required"}, nil
	}

	// Update purchase with payment info
	if err := pp.store.UpdatePurchasePayment(req.RequestID, req.TxHash, req.Chain, req.SenderAddress); err != nil {
		return nil, fmt.Errorf("failed to update purchase payment: %w", err)
	}

	// Mark payment as detected
	if err := pp.store.UpdatePurchaseStatus(req.RequestID, PurchaseStatusPaymentDetected, "Payment detected on "+req.Chain); err != nil {
		return nil, err
	}

	// Chain-specific verification
	switch req.Chain {
	case "ethereum":
		return pp.verifyEthereumPayment(ctx, req)
	case "solana":
		return pp.verifySolanaPayment(ctx, req)
	case "bitcoin":
		return pp.verifyBitcoinPayment(ctx, req)
	default:
		return &CryptoPaymentResult{Verified: false, Error: fmt.Sprintf("unsupported chain: %s", req.Chain)}, nil
	}
}

func (pp *PaymentProcessor) verifyEthereumPayment(ctx context.Context, req *CryptoPaymentRequest) (*CryptoPaymentResult, error) {
	// Stub: In production, use eth_getTransactionReceipt RPC call
	// Verify: recipient address, amount, token contract (for ERC-20), confirmation count
	log.Infof("Verifying Ethereum tx: %s (amount: %d %s)", req.TxHash, req.Amount, req.Currency)

	return &CryptoPaymentResult{
		Verified:          true,
		ConfirmationBlock: 0, // Would be actual block number
	}, nil
}

func (pp *PaymentProcessor) verifySolanaPayment(ctx context.Context, req *CryptoPaymentRequest) (*CryptoPaymentResult, error) {
	// Stub: In production, use getTransaction RPC call
	// Verify: recipient, amount, SPL token (for USDC), finality
	log.Infof("Verifying Solana tx: %s (amount: %d %s)", req.TxHash, req.Amount, req.Currency)

	return &CryptoPaymentResult{
		Verified:          true,
		ConfirmationBlock: 0,
	}, nil
}

func (pp *PaymentProcessor) verifyBitcoinPayment(ctx context.Context, req *CryptoPaymentRequest) (*CryptoPaymentResult, error) {
	// Stub: In production, use getrawtransaction or block explorer API
	// Verify: output address, amount, confirmation count (>= 3 recommended)
	log.Infof("Verifying Bitcoin tx: %s (amount: %d %s)", req.TxHash, req.Amount, req.Currency)

	return &CryptoPaymentResult{
		Verified:          true,
		ConfirmationBlock: 0,
	}, nil
}

// ProcessCredits processes a payment using SDN credits
func (pp *PaymentProcessor) ProcessCredits(ctx context.Context, requestID string, buyerPeerID string, amount uint64, providerPeerID string) error {
	// Check balance
	balance, err := pp.store.GetCreditsBalance(buyerPeerID)
	if err != nil {
		return fmt.Errorf("failed to get buyer balance: %w", err)
	}

	if balance.Balance < amount {
		return fmt.Errorf("insufficient credits: have %d, need %d", balance.Balance, amount)
	}

	// Create transaction record
	txID := uuid.New().String()
	tx := &CreditsTransaction{
		TransactionID: txID,
		FromPeerID:    buyerPeerID,
		ToPeerID:      providerPeerID,
		Amount:        amount,
		Type:          "purchase",
		Reference:     requestID,
		CreatedAt:     time.Now(),
		Status:        "completed",
	}

	if err := pp.store.CreateCreditsTransaction(tx); err != nil {
		return fmt.Errorf("failed to create credits transaction: %w", err)
	}

	// Deduct from buyer
	if err := pp.store.UpdateCreditsBalance(buyerPeerID, -int64(amount)); err != nil {
		return fmt.Errorf("failed to deduct credits: %w", err)
	}

	// Credit to provider
	if err := pp.store.UpdateCreditsBalance(providerPeerID, int64(amount)); err != nil {
		// Refund buyer on failure
		pp.store.UpdateCreditsBalance(buyerPeerID, int64(amount))
		return fmt.Errorf("failed to credit provider: %w", err)
	}

	// Update purchase with credits tx ID
	pp.store.UpdatePurchaseCreditsTransaction(requestID, txID)

	return nil
}

// FiatGatewayRequest represents a fiat payment request
type FiatGatewayRequest struct {
	RequestID     string `json:"request_id"`
	Amount        uint64 `json:"amount"`        // In cents
	Currency      string `json:"currency"`      // USD, EUR
	BuyerPeerID   string `json:"buyer_peer_id"`
	BuyerEmail    string `json:"buyer_email"`
	Description   string `json:"description"`
	SuccessURL    string `json:"success_url"`
	CancelURL     string `json:"cancel_url"`
}

// FiatGatewayResult represents the result of creating a fiat payment intent
type FiatGatewayResult struct {
	PaymentIntentID string `json:"payment_intent_id"`
	ClientSecret    string `json:"client_secret"`
	CheckoutURL     string `json:"checkout_url"`
}

// CreateFiatPaymentIntent creates a fiat payment intent (Stripe stub)
// In production, this would integrate with the Stripe API.
func (pp *PaymentProcessor) CreateFiatPaymentIntent(ctx context.Context, req *FiatGatewayRequest) (*FiatGatewayResult, error) {
	// Stripe integration stub
	// In production:
	// 1. Create Stripe PaymentIntent with amount/currency
	// 2. Return client_secret for Stripe.js frontend
	// 3. Listen for webhook confirmation

	intentID := "pi_stub_" + uuid.New().String()[:8]

	log.Infof("Created fiat payment intent: %s (amount: %d %s)", intentID, req.Amount, req.Currency)

	// Update purchase with intent ID
	pp.store.UpdatePurchaseFiatIntent(req.RequestID, intentID)

	return &FiatGatewayResult{
		PaymentIntentID: intentID,
		ClientSecret:    "secret_" + intentID,
		CheckoutURL:     fmt.Sprintf("https://checkout.stripe.com/pay/%s", intentID),
	}, nil
}

// RefundCredits processes a credits refund
func (pp *PaymentProcessor) RefundCredits(ctx context.Context, requestID string, buyerPeerID string, amount uint64, providerPeerID string) error {
	txID := uuid.New().String()
	tx := &CreditsTransaction{
		TransactionID: txID,
		FromPeerID:    providerPeerID,
		ToPeerID:      buyerPeerID,
		Amount:        amount,
		Type:          "refund",
		Reference:     requestID,
		CreatedAt:     time.Now(),
		Status:        "completed",
	}

	if err := pp.store.CreateCreditsTransaction(tx); err != nil {
		return fmt.Errorf("failed to create refund transaction: %w", err)
	}

	// Deduct from provider
	if err := pp.store.UpdateCreditsBalance(providerPeerID, -int64(amount)); err != nil {
		return fmt.Errorf("failed to deduct from provider: %w", err)
	}

	// Credit to buyer
	if err := pp.store.UpdateCreditsBalance(buyerPeerID, int64(amount)); err != nil {
		pp.store.UpdateCreditsBalance(providerPeerID, int64(amount))
		return fmt.Errorf("failed to credit buyer: %w", err)
	}

	return nil
}
