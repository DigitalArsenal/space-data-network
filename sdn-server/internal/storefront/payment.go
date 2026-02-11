package storefront

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
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

const (
	stripeCheckoutSessionsURL = "https://api.stripe.com/v1/checkout/sessions"
	stripeSigTolerance        = 5 * time.Minute
)

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
	RequestID     string            `json:"request_id"`
	Amount        uint64            `json:"amount"`   // In cents
	Currency      string            `json:"currency"` // USD, EUR
	BuyerPeerID   string            `json:"buyer_peer_id"`
	BuyerEmail    string            `json:"buyer_email"`
	Description   string            `json:"description"`
	SuccessURL    string            `json:"success_url"`
	CancelURL     string            `json:"cancel_url"`
	StripePriceID string            `json:"stripe_price_id,omitempty"`
	Mode          string            `json:"mode,omitempty"` // "payment" or "subscription"
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// FiatGatewayResult represents the result of creating a fiat payment intent
type FiatGatewayResult struct {
	PaymentIntentID string `json:"payment_intent_id"`
	ClientSecret    string `json:"client_secret"`
	CheckoutURL     string `json:"checkout_url"`
}

// StripeWebhookAction describes what a validated webhook means for purchase flow.
type StripeWebhookAction struct {
	EventType      string `json:"event_type"`
	RequestID      string `json:"request_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	CustomerID     string `json:"customer_id,omitempty"`
	Paid           bool   `json:"paid"`
}

type stripeEvent struct {
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

type stripeCheckoutSession struct {
	ID                string            `json:"id"`
	ClientReferenceID string            `json:"client_reference_id"`
	Metadata          map[string]string `json:"metadata"`
	Mode              string            `json:"mode"`
	PaymentStatus     string            `json:"payment_status"`
	Status            string            `json:"status"`
	Subscription      interface{}       `json:"subscription"`
	Customer          interface{}       `json:"customer"`
}

type stripeCheckoutResponse struct {
	ID           string `json:"id"`
	URL          string `json:"url"`
	ClientSecret string `json:"client_secret"`
	Error        *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// CreateFiatPaymentIntent creates a fiat payment intent (Stripe stub)
// In production, this would integrate with the Stripe API.
func (pp *PaymentProcessor) CreateFiatPaymentIntent(ctx context.Context, req *FiatGatewayRequest) (*FiatGatewayResult, error) {
	secret := strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY"))
	if secret == "" {
		// Keep local/dev fallback behavior when Stripe is not configured.
		intentID := "pi_stub_" + uuid.New().String()[:8]
		log.Infof("Created fiat payment intent (stub): %s (amount: %d %s)", intentID, req.Amount, req.Currency)
		_ = pp.store.UpdatePurchaseFiatIntent(req.RequestID, intentID)
		return &FiatGatewayResult{
			PaymentIntentID: intentID,
			ClientSecret:    "secret_" + intentID,
			CheckoutURL:     fmt.Sprintf("https://checkout.stripe.com/pay/%s", intentID),
		}, nil
	}

	purchase, listing, tier, err := pp.resolveCheckoutContext(req.RequestID)
	if err != nil {
		return nil, err
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode != "payment" && mode != "subscription" {
		if listing != nil && listing.AccessType == AccessTypeSubscription {
			mode = "subscription"
		} else {
			mode = "payment"
		}
	}

	amount := req.Amount
	if amount == 0 && purchase != nil {
		amount = purchase.PaymentAmount
	}
	if amount == 0 {
		return nil, fmt.Errorf("amount must be greater than zero")
	}

	currency := strings.ToLower(strings.TrimSpace(req.Currency))
	if currency == "" && purchase != nil {
		currency = strings.ToLower(strings.TrimSpace(purchase.PaymentCurrency))
	}
	if currency == "" {
		currency = "usd"
	}

	successURL := strings.TrimSpace(req.SuccessURL)
	if successURL == "" {
		successURL = strings.TrimSpace(os.Getenv("STRIPE_SUCCESS_URL"))
	}
	cancelURL := strings.TrimSpace(req.CancelURL)
	if cancelURL == "" {
		cancelURL = strings.TrimSpace(os.Getenv("STRIPE_CANCEL_URL"))
	}
	if successURL == "" || cancelURL == "" {
		return nil, fmt.Errorf("success_url and cancel_url are required (request or STRIPE_SUCCESS_URL/STRIPE_CANCEL_URL)")
	}

	buyerEmail := strings.TrimSpace(req.BuyerEmail)
	if buyerEmail == "" && purchase != nil {
		buyerEmail = strings.TrimSpace(purchase.BuyerEmail)
	}

	description := strings.TrimSpace(req.Description)
	if description == "" {
		description = "Space Data Network purchase"
		if listing != nil {
			description = listing.Title
		}
		if purchase != nil && purchase.TierName != "" {
			description += " - " + purchase.TierName
		}
	}

	values := url.Values{}
	values.Set("mode", mode)
	values.Set("success_url", successURL)
	values.Set("cancel_url", cancelURL)
	values.Set("client_reference_id", req.RequestID)
	values.Set("metadata[request_id]", req.RequestID)
	if purchase != nil {
		values.Set("metadata[listing_id]", purchase.ListingID)
		values.Set("metadata[tier_name]", purchase.TierName)
		values.Set("metadata[buyer_peer_id]", purchase.BuyerPeerID)
	}
	for k, v := range req.Metadata {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		values.Set("metadata["+key+"]", v)
	}
	if buyerEmail != "" {
		values.Set("customer_email", buyerEmail)
	}

	priceID := strings.TrimSpace(req.StripePriceID)
	if priceID != "" {
		values.Set("line_items[0][price]", priceID)
		values.Set("line_items[0][quantity]", "1")
	} else {
		values.Set("line_items[0][price_data][currency]", currency)
		values.Set("line_items[0][price_data][unit_amount]", strconv.FormatUint(amount, 10))
		values.Set("line_items[0][price_data][product_data][name]", description)
		if mode == "subscription" {
			interval, intervalCount := recurringInterval(tier)
			values.Set("line_items[0][price_data][recurring][interval]", interval)
			if intervalCount > 1 {
				values.Set("line_items[0][price_data][recurring][interval_count]", strconv.Itoa(intervalCount))
			}
		}
		values.Set("line_items[0][quantity]", "1")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, stripeCheckoutSessionsURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build stripe checkout request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+secret)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stripe checkout request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read stripe response: %w", err)
	}

	var stripeResp stripeCheckoutResponse
	_ = json.Unmarshal(body, &stripeResp)

	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(body))
		if stripeResp.Error != nil && stripeResp.Error.Message != "" {
			msg = stripeResp.Error.Message
		}
		if len(msg) > 512 {
			msg = msg[:512]
		}
		return nil, fmt.Errorf("stripe checkout failed: status=%d message=%s", resp.StatusCode, msg)
	}
	if stripeResp.ID == "" {
		return nil, fmt.Errorf("stripe checkout response missing session id")
	}

	_ = pp.store.UpdatePurchaseFiatIntent(req.RequestID, stripeResp.ID)
	_ = pp.store.UpdatePurchaseStatus(req.RequestID, PurchaseStatusPending, "Stripe checkout session created")

	log.Infof("Created Stripe checkout session: %s mode=%s amount=%d %s", stripeResp.ID, mode, amount, strings.ToUpper(currency))

	return &FiatGatewayResult{
		PaymentIntentID: stripeResp.ID,
		ClientSecret:    stripeResp.ClientSecret,
		CheckoutURL:     stripeResp.URL,
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

// HandleStripeWebhook validates and interprets a Stripe webhook payload.
func (pp *PaymentProcessor) HandleStripeWebhook(ctx context.Context, signatureHeader string, payload []byte) (*StripeWebhookAction, error) {
	_ = ctx

	webhookSecret := strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET"))
	if webhookSecret == "" {
		return nil, fmt.Errorf("STRIPE_WEBHOOK_SECRET is not configured")
	}
	if err := verifyStripeSignature(payload, signatureHeader, webhookSecret, stripeSigTolerance); err != nil {
		return nil, err
	}

	var evt stripeEvent
	if err := json.Unmarshal(payload, &evt); err != nil {
		return nil, fmt.Errorf("invalid stripe event payload: %w", err)
	}

	action := &StripeWebhookAction{EventType: evt.Type}

	switch evt.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		var session stripeCheckoutSession
		if err := json.Unmarshal(evt.Data.Object, &session); err != nil {
			return nil, fmt.Errorf("invalid checkout session payload: %w", err)
		}

		action.SessionID = session.ID
		action.SubscriptionID = asString(session.Subscription)
		action.CustomerID = asString(session.Customer)
		action.RequestID = strings.TrimSpace(session.ClientReferenceID)
		if action.RequestID == "" && session.Metadata != nil {
			action.RequestID = strings.TrimSpace(session.Metadata["request_id"])
		}
		action.Paid = session.PaymentStatus == "paid" || session.PaymentStatus == "no_payment_required" || session.Status == "complete"

	default:
		// No purchase action required for other events in this launch phase.
	}

	return action, nil
}

func (pp *PaymentProcessor) resolveCheckoutContext(requestID string) (*PurchaseRequest, *Listing, *PricingTier, error) {
	if strings.TrimSpace(requestID) == "" {
		return nil, nil, nil, fmt.Errorf("request_id is required")
	}

	purchase, err := pp.store.GetPurchaseRequest(requestID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to load purchase request: %w", err)
	}
	if purchase == nil {
		return nil, nil, nil, fmt.Errorf("purchase request not found: %s", requestID)
	}

	listing, err := pp.store.GetListing(purchase.ListingID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to load listing: %w", err)
	}
	if listing == nil {
		return nil, nil, nil, fmt.Errorf("listing not found: %s", purchase.ListingID)
	}

	var tier *PricingTier
	for i := range listing.Pricing {
		if listing.Pricing[i].Name == purchase.TierName {
			tier = &listing.Pricing[i]
			break
		}
	}
	if tier == nil {
		return nil, nil, nil, fmt.Errorf("tier not found: %s", purchase.TierName)
	}

	return purchase, listing, tier, nil
}

func recurringInterval(tier *PricingTier) (string, int) {
	if tier == nil || tier.DurationDays == 0 {
		return "month", 1
	}

	days := int(tier.DurationDays)
	switch {
	case days >= 365 && days%365 == 0:
		return "year", max(1, days/365)
	case days >= 30 && days%30 == 0:
		return "month", max(1, days/30)
	case days >= 7 && days%7 == 0:
		return "week", max(1, days/7)
	default:
		return "day", max(1, days)
	}
}

func verifyStripeSignature(payload []byte, signatureHeader, secret string, tolerance time.Duration) error {
	timestamp, signatures := parseStripeSignatureHeader(signatureHeader)
	if timestamp == "" || len(signatures) == 0 {
		return fmt.Errorf("missing stripe signature")
	}

	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid stripe timestamp: %w", err)
	}
	eventTime := time.Unix(ts, 0)
	now := time.Now()
	if now.Sub(eventTime) > tolerance || eventTime.Sub(now) > tolerance {
		return fmt.Errorf("stripe signature timestamp outside tolerance")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)

	for _, sigHex := range signatures {
		actual, err := hex.DecodeString(sigHex)
		if err != nil {
			continue
		}
		if hmac.Equal(actual, expected) {
			return nil
		}
	}

	return fmt.Errorf("stripe signature verification failed")
}

func parseStripeSignatureHeader(header string) (string, []string) {
	var timestamp string
	signatures := make([]string, 0, 1)

	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.TrimSpace(kv[0]) {
		case "t":
			timestamp = strings.TrimSpace(kv[1])
		case "v1":
			signatures = append(signatures, strings.TrimSpace(kv[1]))
		}
	}

	return timestamp, signatures
}

func asString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", t))
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
