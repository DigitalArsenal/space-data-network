package storefront

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	lgr "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LGR"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/google/uuid"
	ps "github.com/libp2p/go-libp2p-pubsub"
)

// StorefrontListingsTopic is the PubSub topic for listing announcements
const StorefrontListingsTopic = "/sdn/storefront/listings"

// StorefrontPurchasesTopic is the PubSub topic for purchase requests
const StorefrontPurchasesTopic = "/sdn/storefront/purchases"

// Service provides storefront business logic
type Service struct {
	store  *Store
	peerID string
	// signingKey signs LISTINGS — a publication attestation by the provider of
	// record, low volume, durable records. This is the node identity signing key
	// (the fleet update / publisher root).
	signingKey ed25519.PrivateKey
	// grantSigningKey signs ACCESS GRANTS and is advertised to every requester as
	// LGR.GRANT_VERIFIER_PUBKEY. It MUST be a distinct key from signingKey.
	//
	// OWNER RULING 2026-08-07, verbatim: "derive a grant-signing child from the
	// node identity, keep the update root isolated"
	// (graph/tasks/sdn-grant-verifier-key-domain-separation.md). Before that
	// ruling this lane signed with signingKey and stamped the fleet
	// code-authority public key into every grant handed to an anonymous client.
	//
	// Nil means grants are NOT signed. That is the pre-existing behaviour when no
	// key is available and it stays fail-closed: an unsigned grant is visibly
	// unsigned, whereas silently falling back to signingKey would recreate the
	// exact collision this field removes.
	grantSigningKey ed25519.PrivateKey
	providerEPMCID  string
	pubsub          *ps.PubSub
	listingTopic    *ps.Topic
	listingSub      *ps.Subscription
	purchaseTopic   *ps.Topic
	listingCancel   context.CancelFunc
	listingDone     chan struct{}
	pendingListings map[string]pendingListingAnnouncement
	pendingSTF      map[string]pendingListingSTF
	subscribers     map[string]chan *Listing // listingID -> channel
	inventory       *publishInventoryCache
	mu              sync.RWMutex
}

// NewService creates a new storefront service.
//
// signingKey signs listings; grantSigningKey signs access grants. They must not
// be the same key — see the Service field docs and the owner ruling of
// 2026-08-07. A caller that passes the same key for both gets an error rather
// than a node that quietly issues grants under its fleet code-authority key.
func NewService(store *Store, peerID string, signingKey, grantSigningKey ed25519.PrivateKey, pubsub *ps.PubSub) (*Service, error) {
	if len(signingKey) > 0 && len(grantSigningKey) > 0 && signingKey.Equal(grantSigningKey) {
		return nil, fmt.Errorf(
			"storefront REFUSED: the grant signing key is the same key as the listing/publisher signing key; grants and fleet publication authority must not share a key (graph/tasks/sdn-grant-verifier-key-domain-separation.md)",
		)
	}
	svc := &Service{
		store:           store,
		peerID:          peerID,
		signingKey:      signingKey,
		grantSigningKey: grantSigningKey,
		pubsub:          pubsub,
		subscribers:     make(map[string]chan *Listing),
		pendingListings: make(map[string]pendingListingAnnouncement),
		pendingSTF:      make(map[string]pendingListingSTF),
		listingDone:     make(chan struct{}),
		inventory:       &publishInventoryCache{},
	}

	// Join PubSub topics if available
	if pubsub != nil {
		var err error
		svc.listingTopic, err = pubsub.Join(StorefrontListingsTopic)
		if err != nil {
			log.Warnf("Failed to join listings topic: %v", err)
		} else {
			svc.listingSub, err = svc.listingTopic.Subscribe()
			if err != nil {
				log.Warnf("Failed to subscribe to listings topic: %v", err)
			} else {
				ctx, cancel := context.WithCancel(context.Background())
				svc.listingCancel = cancel
				go svc.runListingSubscription(ctx)
			}
		}

		svc.purchaseTopic, err = pubsub.Join(StorefrontPurchasesTopic)
		if err != nil {
			log.Warnf("Failed to join purchases topic: %v", err)
		}
	}

	return svc, nil
}

// CreateListing creates a new listing
func (s *Service) CreateListing(ctx context.Context, listing *Listing) error {
	// Generate listing ID if not provided
	if listing.ListingID == "" {
		listing.ListingID = uuid.New().String()
	}
	if listing.ListingKind == "" {
		listing.ListingKind = inferListingKind(listing)
	}

	// Set provider info
	listing.ProviderPeerID = s.peerID
	listing.CreatedAt = time.Now()
	listing.UpdatedAt = time.Now()
	listing.Version = 1
	listing.Active = true

	// Sign the listing
	if s.signingKey != nil {
		signature, err := s.signListing(listing)
		if err != nil {
			return fmt.Errorf("failed to sign listing: %w", err)
		}
		listing.Signature = signature
	}

	// Store the listing
	if err := s.store.CreateListing(listing); err != nil {
		return fmt.Errorf("failed to store listing: %w", err)
	}
	if err := s.recordStreamPolicyListingAudit(listing, PaymentAuditStreamPolicyPublished, ""); err != nil {
		log.Warnf("Failed to record stream policy listing audit event for %s: %v", listing.ListingID, err)
	}

	// Publish to PubSub
	if s.listingTopic != nil {
		if err := s.publishListing(ctx, listing); err != nil {
			log.Warnf("Failed to publish listing: %v", err)
		}
	}

	return nil
}

func (s *Service) RecordStreamPolicyChanged(ctx context.Context, listingID, actorPeerID string) error {
	_ = ctx
	listing, err := s.store.GetListing(listingID)
	if err != nil {
		return fmt.Errorf("failed to get listing: %w", err)
	}
	if listing == nil {
		return fmt.Errorf("listing not found: %s", listingID)
	}
	return s.recordStreamPolicyListingAudit(listing, PaymentAuditStreamPolicyChanged, actorPeerID)
}

func (s *Service) recordStreamPolicyListingAudit(listing *Listing, eventType, actorPeerID string) error {
	if listing == nil || listing.ProtectedDelivery.FieldStreamPolicy == nil {
		return nil
	}
	policy := listing.ProtectedDelivery.FieldStreamPolicy
	policyID := strings.TrimSpace(policy.PolicyID)
	if policyID == "" {
		return nil
	}
	actor := strings.TrimSpace(actorPeerID)
	if actor == "" {
		actor = strings.TrimSpace(listing.ProviderPeerID)
	}
	if actor == "" {
		actor = s.peerID
	}
	message := fmt.Sprintf("Field stream policy published for stream %s schema %s version %d",
		strings.TrimSpace(policy.StreamID),
		strings.TrimSpace(policy.SchemaCode),
		policy.PolicyVersion,
	)
	if eventType == PaymentAuditStreamPolicyChanged {
		message = fmt.Sprintf("Field stream policy changed for stream %s schema %s version %d",
			strings.TrimSpace(policy.StreamID),
			strings.TrimSpace(policy.SchemaCode),
			policy.PolicyVersion,
		)
	}
	return s.recordPaymentAudit(listing.ListingID, eventType, actor, policyID, message, PurchaseStatusCompleted)
}

func (s *Service) signListing(listing *Listing) ([]byte, error) {
	// Create a canonical representation for signing
	data := fmt.Sprintf("%s:%s:%s:%s:%d",
		listing.ListingID,
		listing.ProviderPeerID,
		listing.Title,
		listing.Description,
		listing.UpdatedAt.Unix(),
	)
	return ed25519.Sign(s.signingKey, []byte(data)), nil
}

func (s *Service) publishListing(ctx context.Context, listing *Listing) error {
	data, err := json.Marshal(listing)
	if err != nil {
		return fmt.Errorf("failed to marshal listing: %w", err)
	}
	return s.listingTopic.Publish(ctx, data)
}

// GetListing retrieves a listing by ID
func (s *Service) GetListing(ctx context.Context, listingID string) (*Listing, error) {
	return s.store.GetListing(listingID)
}

// SearchListings searches for listings
func (s *Service) SearchListings(ctx context.Context, query *SearchQuery) (*SearchResult, error) {
	return s.store.SearchListings(query)
}

// GetProviderListings retrieves all listings for a provider
func (s *Service) GetProviderListings(ctx context.Context, providerPeerID string) (*SearchResult, error) {
	return s.store.SearchListings(&SearchQuery{
		ProviderPeerIDs: []string{providerPeerID},
		Limit:           100,
	})
}

// CreatePurchaseRequest creates a new purchase request
func (s *Service) CreatePurchaseRequest(ctx context.Context, req *PurchaseRequest) error {
	// Generate request ID
	if req.RequestID == "" {
		req.RequestID = uuid.New().String()
	}

	// Get the listing
	listing, err := s.store.GetListing(req.ListingID)
	if err != nil {
		return fmt.Errorf("failed to get listing: %w", err)
	}
	if listing == nil {
		return fmt.Errorf("listing not found: %s", req.ListingID)
	}

	// Validate tier
	var tier *PricingTier
	for i := range listing.Pricing {
		if listing.Pricing[i].Name == req.TierName {
			tier = &listing.Pricing[i]
			break
		}
	}
	if tier == nil {
		return fmt.Errorf("tier not found: %s", req.TierName)
	}

	// Set request details
	req.ProviderPeerID = listing.ProviderPeerID
	req.PaymentAmount = tier.PriceAmount
	req.PaymentCurrency = tier.PriceCurrency
	req.Status = PurchaseStatusPending
	req.CreatedAt = time.Now()
	req.UpdatedAt = time.Now()
	req.PaymentDeadline = time.Now().Add(30 * time.Minute) // 30 min to pay

	// Store the request
	if err := s.store.CreatePurchaseRequest(req); err != nil {
		return fmt.Errorf("failed to store purchase request: %w", err)
	}
	if err := s.recordPaymentAudit(req.RequestID, PaymentAuditPurchaseCreated, req.BuyerPeerID, "", "Purchase request created", req.Status); err != nil {
		log.Warnf("Failed to record purchase audit event for %s: %v", req.RequestID, err)
	}

	// Publish to provider's topic
	if s.purchaseTopic != nil {
		data, _ := json.Marshal(req)
		s.purchaseTopic.Publish(ctx, data)
	}

	return nil
}

// CompleteManualDevPayment marks an out-of-band local/dev payment as paid and
// issues the corresponding storefront grant. It is intentionally explicit so
// callers cannot confuse it with Stripe or on-chain settlement.
//
// This is a no-payment grant path, so it fails closed unless
// DevPaymentsEnvVar is explicitly set to "1" — mirrored here (not just at
// the HTTP handler) so that any other caller of this method, present or
// future, gets the same fail-closed behavior rather than relying solely on
// the API layer to remember the gate.
func (s *Service) CompleteManualDevPayment(ctx context.Context, requestID string, confirmation ManualDevPaymentConfirmation) (*AccessGrant, error) {
	if !devPaymentsEnabled() {
		return nil, fmt.Errorf("manual/dev payments are disabled; set %s=1 to enable (never in production)", DevPaymentsEnvVar)
	}

	purchase, err := s.store.GetPurchaseRequest(requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to load purchase request: %w", err)
	}
	if purchase == nil {
		return nil, fmt.Errorf("purchase not found: %s", requestID)
	}
	if purchase.Status == PurchaseStatusCompleted && purchase.GrantID != "" {
		existing, err := s.store.GetGrant(purchase.GrantID)
		if err == nil && existing != nil {
			return existing, nil
		}
	}

	reference := strings.TrimSpace(confirmation.Reference)
	if reference == "" {
		reference = generateToken(8)
	}
	actor := strings.TrimSpace(confirmation.OperatorPeerID)
	if actor == "" {
		actor = s.peerID
	}
	note := strings.TrimSpace(confirmation.Note)
	if note == "" {
		note = "Manual/dev payment marked paid out of band"
	}
	txRef := "manual-dev:" + reference

	if err := s.store.UpdatePurchasePayment(requestID, txRef, "manual-dev", actor); err != nil {
		return nil, fmt.Errorf("failed to record manual/dev payment reference: %w", err)
	}
	if err := s.store.UpdatePurchaseStatus(requestID, PurchaseStatusPaymentDetected, "Manual/dev payment detected: "+reference); err != nil {
		return nil, fmt.Errorf("failed to set payment detected: %w", err)
	}
	if err := s.recordPaymentAudit(requestID, PaymentAuditPaymentDetected, actor, reference, note, PurchaseStatusPaymentDetected); err != nil {
		log.Warnf("Failed to record payment detected audit event for %s: %v", requestID, err)
	}
	if err := s.store.UpdatePurchaseStatus(requestID, PurchaseStatusPaymentConfirmed, "Manual/dev payment confirmed: "+reference); err != nil {
		return nil, fmt.Errorf("failed to set payment confirmed: %w", err)
	}
	if err := s.recordPaymentAudit(requestID, PaymentAuditPaymentConfirmed, actor, reference, note, PurchaseStatusPaymentConfirmed); err != nil {
		log.Warnf("Failed to record payment confirmed audit event for %s: %v", requestID, err)
	}

	grant, err := s.IssueGrant(ctx, requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to issue grant: %w", err)
	}
	if err := s.store.UpdatePurchaseGrant(requestID, grant.GrantID); err != nil {
		log.Warnf("Failed to attach grant to purchase %s: %v", requestID, err)
	}
	if err := s.store.UpdatePurchaseStatus(requestID, PurchaseStatusCompleted, fmt.Sprintf("Manual/dev payment complete; grant issued: %s", grant.GrantID)); err != nil {
		log.Warnf("Failed to set purchase completed for %s: %v", requestID, err)
	}
	if err := s.recordPaymentAudit(requestID, PaymentAuditGrantIssued, actor, grant.GrantID, "Grant issued after manual/dev paid state", PurchaseStatusCompleted); err != nil {
		log.Warnf("Failed to record grant audit event for %s: %v", requestID, err)
	}
	s.recordGrantDeliveryAudit(requestID, actor, grant, PurchaseStatusCompleted)

	return grant, nil
}

// CompleteCryptoPayment finalizes a verifier-approved on-chain payment and
// issues the purchase grant exactly once.
func (s *Service) CompleteCryptoPayment(ctx context.Context, requestID string, result *CryptoPaymentResult) (*AccessGrant, error) {
	purchase, err := s.store.GetPurchaseRequest(requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to load purchase request: %w", err)
	}
	if purchase == nil {
		return nil, fmt.Errorf("purchase not found: %s", requestID)
	}
	if purchase.Status == PurchaseStatusCompleted && purchase.GrantID != "" {
		existing, err := s.store.GetGrant(purchase.GrantID)
		if err == nil && existing != nil {
			return existing, nil
		}
	}

	txHash := purchase.PaymentTxHash
	chain := purchase.PaymentChain
	block := purchase.ConfirmationBlock
	if result != nil {
		if result.Chain != "" {
			chain = result.Chain
		}
		if result.ConfirmationBlock > 0 {
			block = result.ConfirmationBlock
		}
	}
	if err := s.store.UpdatePurchaseStatus(requestID, PurchaseStatusPaymentConfirmed, fmt.Sprintf("Crypto payment confirmed on %s at block %d", chain, block)); err != nil {
		return nil, fmt.Errorf("failed to confirm crypto payment: %w", err)
	}
	if err := s.recordPaymentAudit(requestID, PaymentAuditPaymentConfirmed, s.peerID, txHash, "Crypto payment confirmed on "+chain, PurchaseStatusPaymentConfirmed); err != nil {
		log.Warnf("Failed to record crypto payment audit event for %s: %v", requestID, err)
	}

	grant, err := s.IssueGrant(ctx, requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to issue grant: %w", err)
	}
	if err := s.store.UpdatePurchaseGrant(requestID, grant.GrantID); err != nil {
		log.Warnf("Failed to attach crypto grant to purchase %s: %v", requestID, err)
	}
	if err := s.store.UpdatePurchaseStatus(requestID, PurchaseStatusCompleted, fmt.Sprintf("Crypto payment complete; grant issued: %s", grant.GrantID)); err != nil {
		log.Warnf("Failed to set crypto purchase completed for %s: %v", requestID, err)
	}
	if err := s.recordPaymentAudit(requestID, PaymentAuditGrantIssued, s.peerID, grant.GrantID, "Grant issued after crypto payment", PurchaseStatusCompleted); err != nil {
		log.Warnf("Failed to record crypto grant audit event for %s: %v", requestID, err)
	}
	s.recordGrantDeliveryAudit(requestID, s.peerID, grant, PurchaseStatusCompleted)

	return grant, nil
}

// ProcessCreditsPayment processes a payment using SDN credits
func (s *Service) ProcessCreditsPayment(ctx context.Context, requestID string, buyerPeerID string) error {
	// TODO: Get actual amount from purchase request
	// For now, simplified implementation
	amount := uint64(100) // Placeholder

	// Atomically check balance and deduct credits in a single SQL UPDATE
	// to prevent TOCTOU race conditions
	if err := s.store.AtomicDeductCredits(buyerPeerID, amount); err != nil {
		return fmt.Errorf("credits payment failed: %w", err)
	}

	// Update purchase status
	if err := s.store.UpdatePurchaseStatus(requestID, PurchaseStatusPaymentConfirmed, "Credits deducted"); err != nil {
		// Refund on failure
		s.store.UpdateCreditsBalance(buyerPeerID, int64(amount))
		return err
	}

	// Issue grant
	grant, err := s.IssueGrant(ctx, requestID)
	if err != nil {
		// Refund on failure
		s.store.UpdateCreditsBalance(buyerPeerID, int64(amount))
		return fmt.Errorf("failed to issue grant: %w", err)
	}

	if err := s.store.UpdatePurchaseGrant(requestID, grant.GrantID); err != nil {
		log.Warnf("Failed to attach grant to purchase %s: %v", requestID, err)
	}
	if err := s.store.UpdatePurchaseStatus(requestID, PurchaseStatusCompleted, fmt.Sprintf("Grant issued: %s", grant.GrantID)); err != nil {
		log.Warnf("Failed to set purchase completed for %s: %v", requestID, err)
	}
	if err := s.recordPaymentAudit(requestID, PaymentAuditGrantIssued, s.peerID, grant.GrantID, "Grant issued after credits payment", PurchaseStatusCompleted); err != nil {
		log.Warnf("Failed to record grant audit event for %s: %v", requestID, err)
	}
	s.recordGrantDeliveryAudit(requestID, s.peerID, grant, PurchaseStatusCompleted)

	return nil
}

// CompleteCreditsPayment finalizes a confirmed SDN credits purchase and issues
// the grant exactly once.
func (s *Service) CompleteCreditsPayment(ctx context.Context, requestID string) (*AccessGrant, error) {
	purchase, err := s.store.GetPurchaseRequest(requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to load purchase request: %w", err)
	}
	if purchase == nil {
		return nil, fmt.Errorf("purchase not found: %s", requestID)
	}
	if purchase.Status == PurchaseStatusCompleted && purchase.GrantID != "" {
		existing, err := s.store.GetGrant(purchase.GrantID)
		if err == nil && existing != nil {
			return existing, nil
		}
	}
	if purchase.Status != PurchaseStatusPaymentConfirmed {
		return nil, fmt.Errorf("credits payment not confirmed for purchase: %s", requestID)
	}
	if err := s.recordPaymentAudit(requestID, PaymentAuditPaymentConfirmed, purchase.BuyerPeerID, purchase.CreditsTransactionID, "SDN credits payment confirmed", PurchaseStatusPaymentConfirmed); err != nil {
		log.Warnf("Failed to record credits payment audit event for %s: %v", requestID, err)
	}

	grant, err := s.IssueGrant(ctx, requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to issue grant: %w", err)
	}
	if err := s.store.UpdatePurchaseGrant(requestID, grant.GrantID); err != nil {
		log.Warnf("Failed to attach credits grant to purchase %s: %v", requestID, err)
	}
	if err := s.store.UpdatePurchaseStatus(requestID, PurchaseStatusCompleted, fmt.Sprintf("SDN credits payment complete; grant issued: %s", grant.GrantID)); err != nil {
		log.Warnf("Failed to set credits purchase completed for %s: %v", requestID, err)
	}
	if err := s.recordPaymentAudit(requestID, PaymentAuditGrantIssued, s.peerID, grant.GrantID, "Grant issued after SDN credits payment", PurchaseStatusCompleted); err != nil {
		log.Warnf("Failed to record credits grant audit event for %s: %v", requestID, err)
	}
	s.recordGrantDeliveryAudit(requestID, s.peerID, grant, PurchaseStatusCompleted)

	return grant, nil
}

// CompleteStripeCheckout finalizes a Stripe checkout flow and issues access.
func (s *Service) CompleteStripeCheckout(ctx context.Context, requestID, sessionID, subscriptionID, customerID string) (*AccessGrant, error) {
	purchase, err := s.store.GetPurchaseRequest(requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to load purchase request: %w", err)
	}
	if purchase == nil {
		return nil, fmt.Errorf("purchase not found: %s", requestID)
	}

	if sessionID != "" {
		if err := s.store.UpdatePurchaseFiatIntent(requestID, sessionID); err != nil {
			log.Warnf("Failed to store Stripe session for %s: %v", requestID, err)
		}
	}

	// Idempotent completion for duplicate webhook deliveries.
	if purchase.Status == PurchaseStatusCompleted && purchase.GrantID != "" {
		existing, err := s.store.GetGrant(purchase.GrantID)
		if err == nil && existing != nil {
			return existing, nil
		}
	}

	msg := "Stripe checkout completed"
	if subscriptionID != "" {
		msg += " subscription=" + subscriptionID
	}
	if customerID != "" {
		msg += " customer=" + customerID
	}
	if err := s.store.UpdatePurchaseStatus(requestID, PurchaseStatusPaymentConfirmed, msg); err != nil {
		return nil, fmt.Errorf("failed to update purchase status: %w", err)
	}
	if err := s.recordPaymentAudit(requestID, PaymentAuditPaymentConfirmed, s.peerID, sessionID, msg, PurchaseStatusPaymentConfirmed); err != nil {
		log.Warnf("Failed to record Stripe payment audit event for %s: %v", requestID, err)
	}

	grant, err := s.IssueGrant(ctx, requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to issue grant: %w", err)
	}
	if err := s.store.UpdatePurchaseGrant(requestID, grant.GrantID); err != nil {
		log.Warnf("Failed to attach grant to purchase %s: %v", requestID, err)
	}
	if err := s.store.UpdatePurchaseStatus(requestID, PurchaseStatusCompleted, fmt.Sprintf("Grant issued: %s", grant.GrantID)); err != nil {
		log.Warnf("Failed to set purchase completed for %s: %v", requestID, err)
	}
	if err := s.recordPaymentAudit(requestID, PaymentAuditGrantIssued, s.peerID, grant.GrantID, "Grant issued after Stripe checkout", PurchaseStatusCompleted); err != nil {
		log.Warnf("Failed to record Stripe grant audit event for %s: %v", requestID, err)
	}
	s.recordGrantDeliveryAudit(requestID, s.peerID, grant, PurchaseStatusCompleted)

	return grant, nil
}

// ApplyStripeWebhookAction applies a validated Stripe webhook exactly once.
func (s *Service) ApplyStripeWebhookAction(ctx context.Context, action *StripeWebhookAction) (*AccessGrant, error) {
	if action == nil || strings.TrimSpace(action.RequestID) == "" {
		return nil, nil
	}

	firstSeen, err := s.store.RecordPaymentWebhookEvent("stripe", action.EventID, action.RequestID, action.EventType)
	if err != nil {
		return nil, err
	}
	if !firstSeen {
		action.Processed = true
		purchase, err := s.store.GetPurchaseRequest(action.RequestID)
		if err == nil && purchase != nil && purchase.GrantID != "" {
			return s.store.GetGrant(purchase.GrantID)
		}
		return nil, err
	}

	if action.Failed || strings.Contains(action.EventType, "failed") || strings.Contains(action.EventType, "expired") {
		msg := strings.TrimSpace(action.FailureReason)
		if msg == "" {
			msg = action.EventType
		}
		if err := s.store.UpdatePurchaseStatus(action.RequestID, PurchaseStatusFailed, "Stripe checkout failed: "+msg); err != nil {
			return nil, err
		}
		if err := s.recordPaymentAudit(action.RequestID, PaymentAuditPaymentFailed, s.peerID, action.SessionID, msg, PurchaseStatusFailed); err != nil {
			log.Warnf("Failed to record Stripe failure audit event for %s: %v", action.RequestID, err)
		}
		action.Processed = true
		return nil, nil
	}

	if action.Paid {
		grant, err := s.CompleteStripeCheckout(ctx, action.RequestID, action.SessionID, action.SubscriptionID, action.CustomerID)
		if err != nil {
			return nil, err
		}
		action.Processed = true
		return grant, nil
	}

	action.Processed = true
	return nil, nil
}

// IssueGrant issues an access grant for a purchase
func (s *Service) IssueGrant(ctx context.Context, requestID string) (*AccessGrant, error) {
	now := time.Now()

	purchase, err := s.store.GetPurchaseRequest(requestID)
	if err != nil {
		return nil, fmt.Errorf("failed to get purchase request: %w", err)
	}
	if purchase == nil {
		// Backward-compatible fallback for older tests/flows that call IssueGrant directly.
		grant := &AccessGrant{
			GrantID:        uuid.New().String(),
			ListingID:      requestID,
			TierName:       "Basic",
			BuyerPeerID:    "buyer-" + requestID,
			AccessType:     AccessTypeOneTime,
			GrantedAt:      now,
			Status:         GrantStatusActive,
			ProviderPeerID: s.peerID,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if s.grantSigningKey != nil {
			signature, err := s.signGrant(grant)
			if err != nil {
				return nil, fmt.Errorf("failed to sign grant: %w", err)
			}
			grant.ProviderSignature = signature
		}
		if err := s.attachGrantResponse(grant); err != nil {
			return nil, err
		}
		if err := s.store.CreateGrant(grant); err != nil {
			return nil, fmt.Errorf("failed to store grant: %w", err)
		}
		return grant, nil
	}

	listing, err := s.store.GetListing(purchase.ListingID)
	if err != nil {
		return nil, fmt.Errorf("failed to get listing: %w", err)
	}
	if listing == nil {
		return nil, fmt.Errorf("listing not found: %s", purchase.ListingID)
	}

	tier := findPricingTierByName(listing, purchase.TierName)
	if tier == nil {
		return nil, fmt.Errorf("tier not found: %s", purchase.TierName)
	}
	if purchase.PaymentAmount > 0 && purchase.PaymentMethod != PaymentMethodFree &&
		purchase.Status != PurchaseStatusPaymentConfirmed &&
		purchase.Status != PurchaseStatusCompleted {
		return nil, fmt.Errorf("payment not confirmed for purchase: %s", requestID)
	}

	grant := &AccessGrant{
		GrantID:               uuid.New().String(),
		ListingID:             purchase.ListingID,
		TierName:              purchase.TierName,
		BuyerPeerID:           purchase.BuyerPeerID,
		BuyerEncryptionPubkey: purchase.BuyerEncryptionPubkey,
		KeyAlgorithm:          purchase.KeyAlgorithm,
		AccessType:            listing.AccessType,
		RateLimit:             tier.RateLimit,
		MaxRecordsPerRequest:  tier.MaxRecordsPerRequest,
		GrantedAt:             now,
		Status:                GrantStatusActive,
		PaymentTxHash:         purchase.PaymentTxHash,
		PaymentMethod:         purchase.PaymentMethod,
		PaymentAmount:         purchase.PaymentAmount,
		PaymentCurrency:       purchase.PaymentCurrency,
		PaymentChain:          purchase.PaymentChain,
		CreatedAt:             now,
		UpdatedAt:             now,
		ProviderPeerID:        purchase.ProviderPeerID,
	}
	if grant.ProviderPeerID == "" {
		grant.ProviderPeerID = listing.ProviderPeerID
	}
	if tier.DurationDays > 0 {
		grant.ExpiresAt = now.Add(time.Duration(tier.DurationDays) * 24 * time.Hour)
	}
	if grant.AccessType == AccessTypeSubscription && !grant.ExpiresAt.IsZero() {
		grant.NextRenewal = grant.ExpiresAt
		grant.AutoRenew = true
	}

	// Generate delivery topic for streaming
	if grant.AccessType == AccessTypeStreaming || grant.AccessType == AccessTypeSubscription {
		grant.DeliveryTopic = fmt.Sprintf("/sdn/data/%s/%s", grant.ListingID, grant.BuyerPeerID)
	}
	grant.FieldStreamPolicy = grantFieldStreamPolicyFromListing(listing, grant)

	// Sign the grant
	if s.grantSigningKey != nil {
		signature, err := s.signGrant(grant)
		if err != nil {
			return nil, fmt.Errorf("failed to sign grant: %w", err)
		}
		grant.ProviderSignature = signature
	}
	if err := s.attachGrantResponse(grant); err != nil {
		return nil, err
	}

	if err := s.store.CreateGrant(grant); err != nil {
		return nil, fmt.Errorf("failed to store grant: %w", err)
	}

	return grant, nil
}

func (s *Service) recordPaymentAudit(requestID, eventType, actorPeerID, reference, message string, status PurchaseStatus) error {
	return s.store.CreatePaymentAuditEvent(&PaymentAuditEvent{
		EventID:        uuid.New().String(),
		RequestID:      requestID,
		EventType:      eventType,
		ActorPeerID:    actorPeerID,
		Reference:      reference,
		Message:        message,
		PurchaseStatus: status,
		CreatedAt:      time.Now(),
	})
}

func (s *Service) recordGrantDeliveryAudit(requestID, actorPeerID string, grant *AccessGrant, status PurchaseStatus) {
	if grant == nil {
		return
	}
	listing, err := s.store.GetListing(grant.ListingID)
	if err != nil {
		log.Warnf("Failed to load listing for grant delivery audit %s: %v", grant.GrantID, err)
	}
	if shouldAuditKeyWrap(grant, listing) {
		msg := "Grant-specific wrapped key issued"
		if listing != nil && strings.TrimSpace(listing.ProtectedDelivery.ContentKeyID) != "" {
			msg += " for content key " + strings.TrimSpace(listing.ProtectedDelivery.ContentKeyID)
		}
		if err := s.recordPaymentAudit(requestID, PaymentAuditKeyWrapIssued, actorPeerID, grant.GrantID, msg, status); err != nil {
			log.Warnf("Failed to record key-wrap audit event for %s: %v", requestID, err)
		}
	}
	if shouldAuditDeliveryReady(grant, listing) {
		msg := "Protected delivery ready"
		if grant.DeliveryTopic != "" {
			msg += " on " + grant.DeliveryTopic
		}
		if err := s.recordPaymentAudit(requestID, PaymentAuditDeliveryReady, actorPeerID, grant.GrantID, msg, status); err != nil {
			log.Warnf("Failed to record delivery audit event for %s: %v", requestID, err)
		}
	}
	if grant.FieldStreamPolicy != nil {
		msg := "Field stream policy grant issued"
		if grant.FieldStreamPolicy.PolicyID != "" {
			msg += ": " + grant.FieldStreamPolicy.PolicyID
		}
		if err := s.recordPaymentAudit(requestID, PaymentAuditStreamPolicyGrantIssued, actorPeerID, grant.GrantID, msg, status); err != nil {
			log.Warnf("Failed to record stream policy grant audit event for %s: %v", requestID, err)
		}
	}
}

func shouldAuditKeyWrap(grant *AccessGrant, listing *Listing) bool {
	if grant == nil || len(grant.BuyerEncryptionPubkey) == 0 {
		return false
	}
	if listing == nil {
		return true
	}
	protected := listing.ProtectedDelivery
	return listing.EncryptionRequired ||
		strings.TrimSpace(protected.ContentKeyID) != "" ||
		strings.TrimSpace(protected.EncryptedCID) != "" ||
		strings.TrimSpace(protected.ManifestCID) != ""
}

func shouldAuditDeliveryReady(grant *AccessGrant, listing *Listing) bool {
	if grant == nil {
		return false
	}
	if strings.TrimSpace(grant.DeliveryTopic) != "" {
		return true
	}
	if listing == nil {
		return false
	}
	protected := listing.ProtectedDelivery
	return strings.TrimSpace(protected.DeliveryProtocol) != "" ||
		strings.TrimSpace(protected.EncryptedCID) != "" ||
		strings.TrimSpace(protected.ManifestCID) != ""
}

func inferListingKind(listing *Listing) ListingKind {
	for _, dataType := range listing.DataTypes {
		if strings.EqualFold(strings.TrimSpace(dataType), "WASM") {
			return ListingKindWASMModule
		}
	}
	return ListingKindDataStream
}

func (s *Service) signGrant(grant *AccessGrant) ([]byte, error) {
	return ed25519.Sign(s.grantSigningKey, s.grantSignaturePayload(grant)), nil
}

func (s *Service) attachGrantResponse(grant *AccessGrant) error {
	if grant == nil || len(grant.ProviderSignature) == 0 {
		return nil
	}
	responseBytes, err := s.encodeGrantResponse(grant)
	if err != nil {
		return fmt.Errorf("failed to encode grant response: %w", err)
	}
	grant.GrantResponseBase64 = base64.StdEncoding.EncodeToString(responseBytes)
	return nil
}

func (s *Service) encodeGrantResponse(grant *AccessGrant) ([]byte, error) {
	builder := flatbuffers.NewBuilder(256)
	requestID := builder.CreateByteString([]byte(grant.GrantID))
	moduleID := builder.CreateByteString([]byte(grant.ListingID))
	moduleVersion := builder.CreateByteString([]byte(grant.TierName))
	requesterPeerID := builder.CreateByteString([]byte(grant.BuyerPeerID))
	requestedDomain := builder.CreateByteString([]byte(fmt.Sprintf("access:%d", grant.AccessType)))
	grantedDomain := builder.CreateByteString([]byte(fmt.Sprintf("access:%d", grant.AccessType)))
	requiredScope := builder.CreateByteString([]byte(grant.TierName))
	grantStatus := builder.CreateByteString([]byte(fmt.Sprintf("status:%d", grant.Status)))
	capabilityToken := builder.CreateByteVector([]byte(grant.GrantID))
	providerSignature := builder.CreateByteVector(grant.ProviderSignature)

	// The verifier key advertised to the requester is the GRANT key, never the
	// listing/publisher key. Publishing the latter here would hand the cluster's
	// code-authority public key to every anonymous grant requester.
	var verifierPubkey flatbuffers.UOffsetT
	if s.grantSigningKey != nil {
		publicKey, ok := s.grantSigningKey.Public().(ed25519.PublicKey)
		if !ok || len(publicKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("provider signing public key unavailable")
		}
		verifierPubkey = builder.CreateByteVector(publicKey)
	}

	var expiresAt uint64
	if !grant.ExpiresAt.IsZero() {
		expiresAt = uint64(grant.ExpiresAt.UnixMilli())
	}

	lgr.LGRStart(builder)
	lgr.LGRAddMessageType(builder, 1)
	lgr.LGRAddRequestId(builder, requestID)
	lgr.LGRAddModuleId(builder, moduleID)
	lgr.LGRAddModuleVersion(builder, moduleVersion)
	lgr.LGRAddRequesterPeerId(builder, requesterPeerID)
	lgr.LGRAddRequestedDomain(builder, requestedDomain)
	lgr.LGRAddGrantedDomain(builder, grantedDomain)
	lgr.LGRAddExpiresAt(builder, expiresAt)
	lgr.LGRAddRequiredScope(builder, requiredScope)
	lgr.LGRAddGrantStatus(builder, grantStatus)
	lgr.LGRAddCapabilityToken(builder, capabilityToken)
	if verifierPubkey != 0 {
		lgr.LGRAddGrantVerifierPubkey(builder, verifierPubkey)
	}
	lgr.LGRAddProviderSignature(builder, providerSignature)
	root := lgr.LGREnd(builder)
	lgr.FinishLGRBuffer(builder, root)
	return append([]byte(nil), builder.FinishedBytes()...), nil
}

func (s *Service) verifyGrantProviderSignature(grant *AccessGrant) error {
	if grant == nil || s.grantSigningKey == nil || grant.ProviderPeerID != s.peerID {
		return nil
	}
	publicKey, ok := s.grantSigningKey.Public().(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("provider signing public key unavailable")
	}
	if len(grant.ProviderSignature) != ed25519.SignatureSize {
		return fmt.Errorf("provider signature missing or invalid")
	}
	if !ed25519.Verify(publicKey, s.grantSignaturePayload(grant), grant.ProviderSignature) {
		return fmt.Errorf("provider signature invalid")
	}
	return nil
}

func (s *Service) grantSignaturePayload(grant *AccessGrant) []byte {
	data := strings.Join([]string{
		grant.GrantID,
		grant.ListingID,
		grant.BuyerPeerID,
		grant.ProviderPeerID,
		fmt.Sprintf("%d", grant.GrantedAt.Unix()),
		fmt.Sprintf("%d", grant.ExpiresAt.Unix()),
		grant.DeliveryTopic,
		canonicalGrantFieldStreamPolicy(grant.FieldStreamPolicy),
	}, "\x1f")
	return []byte(data)
}

func grantFieldStreamPolicyFromListing(listing *Listing, grant *AccessGrant) *GrantFieldStreamPolicy {
	if listing == nil || grant == nil || listing.ProtectedDelivery.FieldStreamPolicy == nil {
		return nil
	}
	policy := cloneGrantFieldStreamPolicy(listing.ProtectedDelivery.FieldStreamPolicy)
	if policy.GrantScope == "" {
		policy.GrantScope = strings.TrimSpace(listing.ProtectedDelivery.GrantScope)
	}
	if policy.KeyEpoch == "" {
		policy.KeyEpoch = strings.TrimSpace(listing.ProtectedDelivery.ContentKeyID)
	}
	if policy.StreamID == "" {
		policy.StreamID = strings.TrimSpace(listing.ListingID)
	}
	if policy.SchemaCode == "" && len(listing.DataTypes) > 0 {
		policy.SchemaCode = strings.TrimSpace(listing.DataTypes[0])
	}
	if len(policy.AllowedOperations) == 0 {
		switch grant.AccessType {
		case AccessTypeStreaming, AccessTypeSubscription:
			policy.AllowedOperations = []string{"Subscribe", "Decrypt"}
		default:
			policy.AllowedOperations = []string{"Query"}
		}
	}
	return policy
}

func cloneGrantFieldStreamPolicy(policy *GrantFieldStreamPolicy) *GrantFieldStreamPolicy {
	if policy == nil {
		return nil
	}
	return &GrantFieldStreamPolicy{
		PolicyID:           strings.TrimSpace(policy.PolicyID),
		PolicyVersion:      policy.PolicyVersion,
		StreamID:           strings.TrimSpace(policy.StreamID),
		SchemaCode:         strings.TrimSpace(policy.SchemaCode),
		AllowedFieldPaths:  cloneTrimmedStrings(policy.AllowedFieldPaths),
		RedactedFieldPaths: cloneTrimmedStrings(policy.RedactedFieldPaths),
		KeyEpoch:           strings.TrimSpace(policy.KeyEpoch),
		GrantScope:         strings.TrimSpace(policy.GrantScope),
		AllowedOperations:  cloneTrimmedStrings(policy.AllowedOperations),
	}
}

func canonicalGrantFieldStreamPolicy(policy *GrantFieldStreamPolicy) string {
	if policy == nil {
		return ""
	}
	canonical := cloneGrantFieldStreamPolicy(policy)
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func cloneTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			cloned = append(cloned, value)
		}
	}
	return cloned
}

func findPricingTierByName(listing *Listing, tierName string) *PricingTier {
	if listing == nil {
		return nil
	}
	for i := range listing.Pricing {
		if listing.Pricing[i].Name == tierName {
			return &listing.Pricing[i]
		}
	}
	return nil
}

// RevokeGrant records a grant revocation and prevents future access checks from succeeding.
func (s *Service) RevokeGrant(ctx context.Context, grantID, buyerPeerID, actorPeerID, reason string) (*AccessGrant, error) {
	_ = ctx
	grant, err := s.store.GetGrant(grantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get grant: %w", err)
	}
	if grant == nil {
		return nil, fmt.Errorf("grant not found: %s", grantID)
	}
	if strings.TrimSpace(buyerPeerID) != "" && grant.BuyerPeerID != buyerPeerID {
		return nil, fmt.Errorf("buyer mismatch")
	}
	if grant.Status == GrantStatusRevoked {
		return grant, nil
	}
	actor := strings.TrimSpace(actorPeerID)
	if actor == "" {
		actor = s.peerID
	}
	note := strings.TrimSpace(reason)
	if note == "" {
		note = "Grant revoked"
	}
	if err := s.store.UpdateGrantStatus(grantID, GrantStatusRevoked, note); err != nil {
		return nil, err
	}
	grant.Status = GrantStatusRevoked
	grant.Notes = note
	grant.UpdatedAt = time.Now()

	if purchase, err := s.store.GetPurchaseRequestByGrantID(grantID); err == nil && purchase != nil {
		if err := s.recordPaymentAudit(purchase.RequestID, PaymentAuditGrantRevoked, actor, grantID, note, purchase.Status); err != nil {
			log.Warnf("Failed to record revocation audit event for %s: %v", purchase.RequestID, err)
		}
		if grant.FieldStreamPolicy != nil {
			if err := s.recordPaymentAudit(purchase.RequestID, PaymentAuditStreamPolicyRevoked, actor, grantID, note, purchase.Status); err != nil {
				log.Warnf("Failed to record stream policy revocation audit event for %s: %v", purchase.RequestID, err)
			}
		}
	} else if err != nil {
		log.Warnf("Failed to load purchase for grant revocation audit %s: %v", grantID, err)
	}

	return grant, nil
}

// AddGroupMember creates the missing private wrapped-key envelope for one group
// member. Existing active members are returned unchanged so add-member retries do
// not mint duplicate envelopes.
func (s *Service) AddGroupMember(ctx context.Context, member *GroupMember) (*GroupMember, error) {
	_ = ctx
	if member == nil {
		return nil, fmt.Errorf("group member required")
	}
	member.GroupID = strings.TrimSpace(member.GroupID)
	member.ListingID = strings.TrimSpace(member.ListingID)
	member.GrantID = strings.TrimSpace(member.GrantID)
	member.MemberPeerID = strings.TrimSpace(member.MemberPeerID)
	member.MemberKeyID = strings.TrimSpace(member.MemberKeyID)
	member.KeyEpoch = strings.TrimSpace(member.KeyEpoch)
	member.GrantScope = strings.TrimSpace(member.GrantScope)
	member.SignerPeerID = strings.TrimSpace(member.SignerPeerID)
	if member.GroupID == "" || member.ListingID == "" || member.GrantID == "" ||
		member.MemberPeerID == "" || member.MemberKeyID == "" || member.KeyEpoch == "" {
		return nil, fmt.Errorf("group_id, listing_id, grant_id, member_peer_id, member_key_id, and key_epoch are required")
	}
	if len(member.WrappedKeyEnvelope) == 0 && strings.TrimSpace(member.EnvelopeCID) == "" {
		return nil, fmt.Errorf("wrapped key envelope or envelope CID required")
	}
	grant, err := s.store.GetGrant(member.GrantID)
	if err != nil {
		return nil, fmt.Errorf("failed to load grant: %w", err)
	}
	if grant == nil {
		return nil, fmt.Errorf("grant not found: %s", member.GrantID)
	}
	if grant.ListingID != member.ListingID {
		return nil, fmt.Errorf("grant listing mismatch")
	}
	if grant.Status != GrantStatusActive {
		return nil, fmt.Errorf("grant not active: %v", grant.Status)
	}
	latestEpoch, err := s.store.GetLatestGroupKeyEpoch(member.GroupID)
	if err != nil {
		return nil, fmt.Errorf("failed to load group key epoch: %w", err)
	}
	if latestEpoch != nil && latestEpoch.PreviousEpoch == member.KeyEpoch {
		return nil, fmt.Errorf("group key epoch is stale after rotation")
	}
	if existing, err := s.store.GetRequesterGroupMember(member.GroupID, member.MemberPeerID, member.MemberKeyID); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}
	if member.SignerPeerID == "" {
		member.SignerPeerID = s.peerID
	}
	member.Status = GroupMemberStatusActive
	if err := s.store.UpsertGroupMember(member); err != nil {
		return nil, err
	}
	return s.store.GetRequesterGroupMember(member.GroupID, member.MemberPeerID, member.MemberKeyID)
}

// RemoveGroupMember removes a member from future group wraps. Existing artifact
// versions stay single-copy; callers rotate content keys for future windows by
// adding members under a new key epoch.
func (s *Service) RemoveGroupMember(ctx context.Context, groupID, memberPeerID, memberKeyID, reason string) (*GroupKeyEpoch, error) {
	_ = ctx
	groupID = strings.TrimSpace(groupID)
	memberPeerID = strings.TrimSpace(memberPeerID)
	memberKeyID = strings.TrimSpace(memberKeyID)
	if groupID == "" || memberPeerID == "" || memberKeyID == "" {
		return nil, fmt.Errorf("group_id, member_peer_id, and member_key_id are required")
	}
	member, err := s.store.GetRequesterGroupMember(groupID, memberPeerID, memberKeyID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		return nil, fmt.Errorf("group member not found")
	}
	note := strings.TrimSpace(reason)
	if note == "" {
		note = "Group member removed"
	}
	removedAt := time.Now()
	if err := s.store.RemoveGroupMember(groupID, memberPeerID, memberKeyID, note, removedAt); err != nil {
		return nil, err
	}
	epoch := &GroupKeyEpoch{
		GroupID:       groupID,
		ListingID:     member.ListingID,
		PreviousEpoch: member.KeyEpoch,
		PolicyID:      member.GrantScope,
		RotatedAt:     removedAt,
		RotatedBy:     s.peerID,
		Reason:        note,
	}
	if err := s.store.CreateGroupKeyEpoch(epoch); err != nil {
		return nil, err
	}
	s.recordGroupKeyRotationAudit(member, epoch)
	return epoch, nil
}

func (s *Service) recordGroupKeyRotationAudit(member *GroupMember, epoch *GroupKeyEpoch) {
	if member == nil || epoch == nil {
		return
	}
	requestID := strings.TrimSpace(member.GrantID)
	status := PurchaseStatusCompleted
	if purchase, err := s.store.GetPurchaseRequestByGrantID(member.GrantID); err == nil && purchase != nil {
		requestID = purchase.RequestID
		status = purchase.Status
	} else if err != nil {
		log.Warnf("Failed to load purchase for group key rotation audit %s: %v", epoch.EpochID, err)
	}
	if requestID == "" {
		return
	}
	actor := strings.TrimSpace(epoch.RotatedBy)
	if actor == "" {
		actor = s.peerID
	}
	parts := []string{"Field stream key rotated"}
	if groupID := strings.TrimSpace(epoch.GroupID); groupID != "" {
		parts = append(parts, "group "+groupID)
	}
	if listingID := strings.TrimSpace(epoch.ListingID); listingID != "" {
		parts = append(parts, "listing "+listingID)
	}
	if policyID := strings.TrimSpace(epoch.PolicyID); policyID != "" {
		parts = append(parts, "policy "+policyID)
	}
	message := strings.Join(parts, "; ")
	if err := s.recordPaymentAudit(requestID, PaymentAuditStreamKeyRotated, actor, epoch.EpochID, message, status); err != nil {
		log.Warnf("Failed to record group key rotation audit event for %s: %v", epoch.EpochID, err)
	}
}

// GetRequesterGroupEnvelope returns only the active requester-specific envelope.
func (s *Service) GetRequesterGroupEnvelope(ctx context.Context, groupID, memberPeerID, memberKeyID string) (*GroupMember, error) {
	_ = ctx
	return s.store.GetRequesterGroupMember(strings.TrimSpace(groupID), strings.TrimSpace(memberPeerID), strings.TrimSpace(memberKeyID))
}

// VerifyGrant verifies an access grant
func (s *Service) VerifyGrant(ctx context.Context, grantID string, buyerPeerID string) (*AccessGrant, error) {
	grant, err := s.store.GetGrant(grantID)
	if err != nil {
		return nil, fmt.Errorf("failed to get grant: %w", err)
	}
	if grant == nil {
		return nil, fmt.Errorf("grant not found: %s", grantID)
	}

	// Verify buyer
	if grant.BuyerPeerID != buyerPeerID {
		return nil, fmt.Errorf("buyer mismatch")
	}

	// Check status
	if grant.Status != GrantStatusActive {
		return nil, fmt.Errorf("grant not active: %v", grant.Status)
	}

	// Check expiration
	if !grant.ExpiresAt.IsZero() && time.Now().After(grant.ExpiresAt) {
		return nil, fmt.Errorf("grant expired")
	}

	if err := s.verifyGrantProviderSignature(grant); err != nil {
		return nil, err
	}

	return grant, nil
}

// GetBuyerGrants retrieves all grants for a buyer
func (s *Service) GetBuyerGrants(ctx context.Context, buyerPeerID string) ([]*AccessGrant, error) {
	return s.store.GetGrantsByBuyer(buyerPeerID)
}

// CreateReview creates a new review
func (s *Service) CreateReview(ctx context.Context, review *Review) error {
	// Generate review ID
	if review.ReviewID == "" {
		review.ReviewID = uuid.New().String()
	}

	// Verify purchase if grant ID provided
	if review.ACLGrantID != "" {
		grant, err := s.store.GetGrant(review.ACLGrantID)
		if err != nil || grant == nil {
			log.Warnf("Could not verify grant for review: %v", err)
		} else if grant.BuyerPeerID == review.ReviewerPeerID {
			review.VerifiedPurchase = true
		}
	}

	review.CreatedAt = time.Now()
	review.UpdatedAt = time.Now()
	review.Status = ReviewStatusPublished

	return s.store.CreateReview(review)
}

// GetListingReviews retrieves reviews for a listing
func (s *Service) GetListingReviews(ctx context.Context, listingID string, limit, offset int) ([]*Review, *ReviewStats, error) {
	if limit <= 0 {
		limit = 20
	}
	return s.store.GetReviewsForListing(listingID, limit, offset)
}

// GetCreditsBalance retrieves the credits balance for a peer
func (s *Service) GetCreditsBalance(ctx context.Context, peerID string) (*CreditsBalance, error) {
	return s.store.GetCreditsBalance(peerID)
}

// DepositCredits deposits credits to a peer's balance
func (s *Service) DepositCredits(ctx context.Context, peerID string, amount uint64) error {
	return s.store.UpdateCreditsBalance(peerID, int64(amount))
}

// WithdrawCredits withdraws credits from a peer's balance
func (s *Service) WithdrawCredits(ctx context.Context, peerID string, amount uint64) error {
	balance, err := s.store.GetCreditsBalance(peerID)
	if err != nil {
		return err
	}
	if balance.Balance < amount {
		return fmt.Errorf("insufficient balance: have %d, need %d", balance.Balance, amount)
	}
	return s.store.UpdateCreditsBalance(peerID, -int64(amount))
}

// SubscribeToListings subscribes to new listing announcements
func (s *Service) SubscribeToListings(ctx context.Context) (<-chan *Listing, error) {
	if s.listingTopic == nil {
		return nil, fmt.Errorf("pubsub not available")
	}

	sub, err := s.listingTopic.Subscribe()
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe: %w", err)
	}

	ch := make(chan *Listing, 100)

	go func() {
		defer close(ch)
		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				log.Warnf("Subscription error: %v", err)
				return
			}

			var listing Listing
			if err := json.Unmarshal(msg.Data, &listing); err != nil {
				log.Warnf("Failed to unmarshal listing: %v", err)
				continue
			}

			// Store the listing locally
			if err := s.store.CreateListing(&listing); err != nil {
				log.Warnf("Failed to store listing from pubsub: %v", err)
			}

			select {
			case ch <- &listing:
			default:
				log.Warn("Listing channel full, dropping message")
			}
		}
	}()

	return ch, nil
}

// IndexListingsFromDHT indexes listings from DHT (placeholder for DHT integration)
func (s *Service) IndexListingsFromDHT(ctx context.Context) error {
	// TODO: Implement DHT-based listing discovery
	// This would query /sdn/listing/{listing_id} keys from DHT
	// and index them locally
	log.Info("DHT listing indexing not yet implemented")
	return nil
}

// generateToken generates a random hex token
func generateToken(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Close closes the service
func (s *Service) Close() error {
	if s.listingCancel != nil {
		s.listingCancel()
		<-s.listingDone
	}
	if s.listingSub != nil {
		s.listingSub.Cancel()
	}
	if s.listingTopic != nil {
		s.listingTopic.Close()
	}
	if s.purchaseTopic != nil {
		s.purchaseTopic.Close()
	}
	return s.store.Close()
}
