// Package license provides stream-based licensing and capability tokens for SpaceAware services.
package license

const (
	// ProtocolID is the libp2p stream protocol for OrbPro/SpaceAware license exchange.
	ProtocolID = "/orbpro/license/1.0.0"

	msgTypeChallengeRequest  = "challenge_request"
	msgTypeChallengeResponse = "challenge_response"
	msgTypeProofRequest      = "proof_request"
	msgTypeGrantResponse     = "grant_response"
	msgTypeErrorResponse     = "error_response"

	entitlementStatusActive    = "active"
	entitlementStatusCancelled = "cancelled"
	entitlementStatusPastDue   = "past_due"
	entitlementStatusSuspended = "suspended"
)

// ChallengeRequest asks the license service to mint a nonce for proof-of-possession.
type ChallengeRequest struct {
	Type            string `json:"type"`
	ReqID           string `json:"req_id"`
	XPub            string `json:"xpub"`
	PeerID          string `json:"peer_id"`
	ClientPubKeyHex string `json:"client_pubkey_hex"`
	TS              int64  `json:"ts"`
}

// ChallengeResponse carries the challenge nonce and short expiry.
type ChallengeResponse struct {
	Type         string `json:"type"`
	ReqID        string `json:"req_id"`
	Challenge    string `json:"challenge"`
	ExpiresAt    int64  `json:"expires_at"`
	ServerPeerID string `json:"server_peer_id"`
}

// ProofRequest presents the signed challenge.
type ProofRequest struct {
	Type         string `json:"type"`
	ReqID        string `json:"req_id"`
	XPub         string `json:"xpub"`
	PeerID       string `json:"peer_id"`
	Challenge    string `json:"challenge"`
	SignatureHex string `json:"signature_hex"`
	TS           int64  `json:"ts"`
}

// Entitlement captures billing/plan status for a wallet identity.
type Entitlement struct {
	XPub                 string `json:"xpub"`
	PeerID               string `json:"peer_id,omitempty"`
	Plan                 string `json:"plan"`
	Status               string `json:"status"`
	StripeCustomerID     string `json:"stripe_customer_id,omitempty"`
	StripeSubscriptionID string `json:"stripe_subscription_id,omitempty"`
	ExpiresAt            int64  `json:"expires_at,omitempty"`
	UpdatedAt            int64  `json:"updated_at"`
}

// GrantResponse returns current entitlement and a short-lived capability token.
type GrantResponse struct {
	Type            string      `json:"type"`
	ReqID           string      `json:"req_id"`
	Entitlement     Entitlement `json:"entitlement"`
	CapabilityToken string      `json:"capability_token"`
	ExpiresAt       int64       `json:"expires_at"`
}

// ErrorResponse standardizes protocol and HTTP-level error payloads.
type ErrorResponse struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
