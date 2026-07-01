package deliveryclient

import (
	"errors"
	"fmt"
	"strings"

	lgr "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LGR"
)

// LGR grant message-type wire values (generated enum constants are
// package-private).
const (
	grantMessageTypeRequest = 0
	grantMessageTypeGranted = 1
	grantMessageTypeDenied  = 2
)

// Grant is a decoded provider grant. On a Granted status it carries the module
// descriptor CID to fetch plus the wrapped content-key material the requester
// unwraps (content-key crypto lands in 4.3c) to decrypt the fetched bundle.
type Grant struct {
	MessageType      int
	RequestID        string
	ModuleID         string
	ModuleVersion    string
	GrantedDomain    string
	GrantedTimeoutMs uint64
	ExpiresAtMs      uint64
	RequiredScope    string
	GrantStatus      string
	DenialReason     string

	// ModuleDescriptorCID is the encrypted-bundle CID to fetch (from the
	// embedded PLG module descriptor's WASM_CID).
	ModuleDescriptorCID string

	// Opaque crypto material consumed by the content-key unwrap step (4.3c).
	WrappedContentKeyPayload []byte
	GrantVerifierPublicKey   []byte
	ProviderSignature        []byte
	HasWrappedContentKey     bool
}

// Granted reports whether the grant authorizes delivery.
func (g *Grant) Granted() bool {
	return g != nil && g.MessageType == grantMessageTypeGranted
}

// DecodeGrant parses an LGR grant frame into a Grant.
func DecodeGrant(data []byte) (*Grant, error) {
	if len(data) < 8 || !lgr.LGRBufferHasIdentifier(data) {
		return nil, errors.New("deliveryclient: not an $LGR frame")
	}
	msg := lgr.GetRootAsLGR(data, 0)

	g := &Grant{
		MessageType:      int(byte(msg.MESSAGE_TYPE())),
		RequestID:        string(msg.REQUEST_ID()),
		ModuleID:         string(msg.MODULE_ID()),
		ModuleVersion:    string(msg.MODULE_VERSION()),
		GrantedDomain:    string(msg.GRANTED_DOMAIN()),
		GrantedTimeoutMs: msg.GRANTED_TIMEOUT_MS(),
		ExpiresAtMs:      msg.EXPIRES_AT(),
		RequiredScope:    string(msg.REQUIRED_SCOPE()),
		GrantStatus:      string(msg.GRANT_STATUS()),
		DenialReason:     string(msg.DENIAL_REASON()),
	}
	if descriptor := msg.MODULE_DESCRIPTOR(nil); descriptor != nil {
		g.ModuleDescriptorCID = string(descriptor.WASM_CID())
	}
	if msg.WRAPPED_CONTENT_KEY_PAYLOADLength() > 0 {
		g.WrappedContentKeyPayload = append([]byte(nil), msg.WrappedContentKeyPayloadBytes()...)
	}
	if msg.WRAPPED_CONTENT_KEY_HEADER(nil) != nil {
		g.HasWrappedContentKey = true
	}
	if msg.GRANT_VERIFIER_PUBKEYLength() > 0 {
		g.GrantVerifierPublicKey = append([]byte(nil), msg.GrantVerifierPubkeyBytes()...)
	}
	if msg.PROVIDER_SIGNATURELength() > 0 {
		g.ProviderSignature = append([]byte(nil), msg.ProviderSignatureBytes()...)
	}
	return g, nil
}

// GrantExpectations are the requester's structural checks against a grant. It
// deliberately excludes provider-signature verification and content-key unwrap,
// which are the content-key crypto step (4.3c).
type GrantExpectations struct {
	RequestID          string
	ModuleID           string
	ModuleVersion      string // if set, must match when the grant echoes a version
	ExpectedDomain     string // if set, granted domain must match
	RequestedTimeoutMs uint64 // granted timeout must be <= this when both set
	NowMs              uint64 // if >0, grant must not be expired
}

// Validate checks the grant is a usable authorization: Granted status, matching
// request/module identity, domain/timeout within the request, non-expired, and
// carrying a module descriptor CID. A Denied grant returns its denial reason.
func (g *Grant) Validate(exp GrantExpectations) error {
	if g == nil {
		return errors.New("deliveryclient: nil grant")
	}
	if g.MessageType == grantMessageTypeDenied {
		reason := firstNonEmpty(g.DenialReason, g.GrantStatus, "unspecified")
		return fmt.Errorf("deliveryclient: grant denied: %s", reason)
	}
	if g.MessageType != grantMessageTypeGranted {
		return fmt.Errorf("deliveryclient: grant not granted (message type %d)", g.MessageType)
	}
	if exp.RequestID != "" && g.RequestID != "" && g.RequestID != exp.RequestID {
		return errors.New("deliveryclient: grant request id mismatch")
	}
	if exp.ModuleID != "" && g.ModuleID != "" && g.ModuleID != exp.ModuleID {
		return errors.New("deliveryclient: grant module id mismatch")
	}
	if exp.ModuleVersion != "" && g.ModuleVersion != "" && g.ModuleVersion != exp.ModuleVersion {
		return errors.New("deliveryclient: grant module version mismatch")
	}
	if exp.ExpectedDomain != "" && g.GrantedDomain != "" && g.GrantedDomain != exp.ExpectedDomain {
		return fmt.Errorf("deliveryclient: granted domain %q != expected %q", g.GrantedDomain, exp.ExpectedDomain)
	}
	if exp.RequestedTimeoutMs > 0 && g.GrantedTimeoutMs > exp.RequestedTimeoutMs {
		return fmt.Errorf("deliveryclient: granted timeout %d exceeds requested %d", g.GrantedTimeoutMs, exp.RequestedTimeoutMs)
	}
	if exp.NowMs > 0 && g.ExpiresAtMs > 0 && exp.NowMs > g.ExpiresAtMs {
		return errors.New("deliveryclient: grant expired")
	}
	if strings.TrimSpace(g.ModuleDescriptorCID) == "" {
		return errors.New("deliveryclient: grant missing module descriptor CID")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
