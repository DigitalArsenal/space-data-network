package license

import "time"

const (
	entitlementStatusActive    = "active"
	entitlementStatusCancelled = "cancelled"
	entitlementStatusPastDue   = "past_due"
	entitlementStatusSuspended = "suspended"
	entitlementStatusExpired   = "expired"
	errorResponseType          = "error_response"
)

// Entitlement captures billing or plan status for a wallet identity.
type Entitlement struct {
	EntitlementID        string `json:"entitlement_id,omitempty"`
	XPub                 string `json:"xpub"`
	PeerID               string `json:"peer_id,omitempty"`
	Plan                 string `json:"plan"`
	Status               string `json:"status"`
	StripeCustomerID     string `json:"stripe_customer_id,omitempty"`
	StripeSubscriptionID string `json:"stripe_subscription_id,omitempty"`
	ExpiresAt            int64  `json:"expires_at,omitempty"`
	CreatedAt            int64  `json:"created_at,omitempty"`
	UpdatedAt            int64  `json:"updated_at"`
	ProviderPeerID       string `json:"provider_peer_id,omitempty"`
	ProviderSignature    []byte `json:"provider_signature,omitempty"`
}

// ErrorResponse standardizes JSON error payloads returned by helper HTTP APIs.
type ErrorResponse struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// IsActive reports whether the entitlement currently permits access.
func (e *Entitlement) IsActive(now time.Time) bool {
	if e == nil {
		return false
	}
	if e.Status != entitlementStatusActive {
		return false
	}
	if e.ExpiresAt <= 0 {
		return true
	}
	return now.Unix() < e.ExpiresAt
}
