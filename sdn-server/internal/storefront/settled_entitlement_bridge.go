package storefront

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SettledEntitlementBridge projects a completed storefront purchase into the
// provider's application-blind module licensing registry. BuyerPeerID is the
// authenticated session xpub on the HTTP purchase path; the licensing runtime
// compares that exact xpub with PLG.ALLOWED_XPUBS.
type SettledEntitlementBridge interface {
	ProvisionSettledEntitlement(ctx context.Context, moduleID, buyerXPub string) error
	RevokeSettledEntitlement(ctx context.Context, moduleID, buyerXPub string) error
}

// SetSettledEntitlementBridge installs the provider-local projection used for
// protected module listings. A nil bridge preserves storefront-only operation.
func (s *Service) SetSettledEntitlementBridge(bridge SettledEntitlementBridge) {
	if s == nil {
		return
	}
	s.entitlementMu.Lock()
	s.entitlementBridge = bridge
	s.entitlementMu.Unlock()
}

// provisionSettledEntitlement re-reads durable state before crossing the
// licensing boundary. This makes completion retries useful: if the purchase
// was committed but a runtime refresh failed, the next retry reconciles it.
func (s *Service) provisionSettledEntitlement(ctx context.Context, requestID string) error {
	if s == nil || s.store == nil {
		return nil
	}
	purchase, err := s.store.GetPurchaseRequest(requestID)
	if err != nil {
		return fmt.Errorf("load settled purchase: %w", err)
	}
	if purchase == nil {
		return fmt.Errorf("settled purchase not found: %s", requestID)
	}
	if purchase.Status != PurchaseStatusPaymentConfirmed && purchase.Status != PurchaseStatusCompleted {
		return nil
	}
	listing, err := s.store.GetListing(purchase.ListingID)
	if err != nil {
		return fmt.Errorf("load settled purchase listing: %w", err)
	}
	if listing == nil {
		return fmt.Errorf("settled purchase listing not found: %s", purchase.ListingID)
	}
	moduleID := strings.TrimSpace(listing.ProtectedDelivery.ModuleID)
	if moduleID == "" {
		return nil
	}
	buyerXPub := strings.TrimSpace(purchase.BuyerPeerID)
	if buyerXPub == "" {
		return fmt.Errorf("settled module purchase %s has no buyer xpub", requestID)
	}

	s.entitlementMu.Lock()
	defer s.entitlementMu.Unlock()
	if s.entitlementBridge == nil {
		return nil
	}
	if err := s.entitlementBridge.ProvisionSettledEntitlement(ctx, moduleID, buyerXPub); err != nil {
		return fmt.Errorf("provision settled module entitlement: %w", err)
	}
	return nil
}

func (s *Service) revokeSettledEntitlement(ctx context.Context, grant *AccessGrant) error {
	if s == nil || s.store == nil || grant == nil {
		return nil
	}
	listing, err := s.store.GetListing(grant.ListingID)
	if err != nil {
		return fmt.Errorf("load revoked grant listing: %w", err)
	}
	if listing == nil {
		return fmt.Errorf("revoked grant listing not found: %s", grant.ListingID)
	}
	moduleID := strings.TrimSpace(listing.ProtectedDelivery.ModuleID)
	if moduleID == "" {
		return nil
	}
	buyerXPub := strings.TrimSpace(grant.BuyerPeerID)
	if buyerXPub == "" {
		return fmt.Errorf("module grant %s has no buyer xpub", grant.GrantID)
	}

	s.entitlementMu.Lock()
	defer s.entitlementMu.Unlock()
	if s.entitlementBridge == nil {
		return nil
	}

	// One xpub may hold the same module through several listings or purchases.
	// Revoking one grant must not remove an entitlement still backed by another
	// active, unexpired grant.
	grants, err := s.store.GetGrantsByBuyer(buyerXPub)
	if err != nil {
		return fmt.Errorf("load buyer grants before entitlement revocation: %w", err)
	}
	now := time.Now()
	for _, other := range grants {
		if other == nil || other.GrantID == grant.GrantID || other.Status != GrantStatusActive {
			continue
		}
		if !other.ExpiresAt.IsZero() && now.After(other.ExpiresAt) {
			continue
		}
		otherListing, err := s.store.GetListing(other.ListingID)
		if err != nil {
			return fmt.Errorf("load other active grant listing: %w", err)
		}
		if otherListing != nil && strings.TrimSpace(otherListing.ProtectedDelivery.ModuleID) == moduleID {
			return nil
		}
	}
	if err := s.entitlementBridge.RevokeSettledEntitlement(ctx, moduleID, buyerXPub); err != nil {
		return fmt.Errorf("revoke settled module entitlement: %w", err)
	}
	return nil
}
