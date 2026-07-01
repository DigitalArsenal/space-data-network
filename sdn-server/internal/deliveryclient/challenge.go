// Package deliveryclient implements the requester (consumer) side of the SDN
// module-delivery grant protocol — the Go peer of the sdn-js requestModuleGrant
// client. The consumer runs challenge -> proof -> grant -> fetch -> decrypt
// against a provider to pull an encrypted module bundle it is authorized for.
// This is what turns the dependency resolver (internal/deps) into a working
// package manager: each planned module is pulled through this flow.
//
// This file covers the first leg — the LCH challenge exchange. The requester
// encodes an LCH challenge request and decodes the provider's LCH challenge
// response, preserving the exact response bytes so the proof step can sign them
// verbatim. The proof (LPF) + grant (LGR) legs and the content-key crypto build
// on top of this in sibling files.
package deliveryclient

import (
	"errors"
	"fmt"
	"strings"

	lch "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	flatbuffers "github.com/google/flatbuffers/go"
)

// LCH message-type / role wire values. The generated FlatBuffers Go enum
// constants are package-private, so we mirror the stable wire integers. They are
// covered by the round-trip tests, which read them back through the generated
// accessors.
const (
	challengeMessageTypeRequest  = 0
	challengeMessageTypeResponse = 1
	challengeMessageTypeError    = 2

	challengeRoleRequester = 0
	challengeRoleProvider  = 1
)

// ChallengeRequest is the requester's opening message: the module it wants plus
// the identity and keys the provider gates on and wraps a grant to.
type ChallengeRequest struct {
	RequestID                   string
	ModuleID                    string
	ModuleVersion               string
	RequesterPeerID             string
	RequesterXPub               string
	RequesterSigningPublicKey   []byte
	RequesterEphemeralPublicKey []byte
	RequestedDomain             string
	RequestedTimeoutMs          uint64
	RequestedAtMs               uint64
	ProviderPeerID              string
}

// EncodeChallengeRequest serializes req as an LCH challenge-request frame
// (MESSAGE_TYPE=Request, ROLE=Requester) with the $LCH file identifier, matching
// the sdn-js encodeChallengeRequest wire layout.
func EncodeChallengeRequest(req ChallengeRequest) ([]byte, error) {
	if strings.TrimSpace(req.RequestID) == "" {
		return nil, errors.New("deliveryclient: request id is required")
	}
	if strings.TrimSpace(req.ModuleID) == "" {
		return nil, errors.New("deliveryclient: module id is required")
	}

	b := flatbuffers.NewBuilder(512)

	requestID := b.CreateString(req.RequestID)
	moduleID := b.CreateString(req.ModuleID)
	moduleVersion := b.CreateString(req.ModuleVersion)
	requesterPeerID := b.CreateString(req.RequesterPeerID)
	requesterXPub := b.CreateString(req.RequesterXPub)
	requestedDomain := b.CreateString(req.RequestedDomain)
	providerPeerID := b.CreateString(req.ProviderPeerID)

	var signingKey flatbuffers.UOffsetT
	if len(req.RequesterSigningPublicKey) > 0 {
		signingKey = b.CreateByteVector(req.RequesterSigningPublicKey)
	}
	var ephemeralKey flatbuffers.UOffsetT
	if len(req.RequesterEphemeralPublicKey) > 0 {
		ephemeralKey = b.CreateByteVector(req.RequesterEphemeralPublicKey)
	}

	lch.LCHStart(b)
	lch.LCHAddMESSAGE_TYPE(b, challengeMessageTypeRequest)
	lch.LCHAddROLE(b, challengeRoleRequester)
	lch.LCHAddREQUEST_ID(b, requestID)
	lch.LCHAddMODULE_ID(b, moduleID)
	lch.LCHAddMODULE_VERSION(b, moduleVersion)
	lch.LCHAddREQUESTER_PEER_ID(b, requesterPeerID)
	lch.LCHAddREQUESTER_XPUB(b, requesterXPub)
	if signingKey != 0 {
		lch.LCHAddREQUESTER_SIGNING_PUBKEY(b, signingKey)
	}
	if ephemeralKey != 0 {
		lch.LCHAddREQUESTER_EPHEMERAL_PUBKEY(b, ephemeralKey)
	}
	lch.LCHAddREQUESTED_DOMAIN(b, requestedDomain)
	lch.LCHAddREQUESTED_TIMEOUT_MS(b, req.RequestedTimeoutMs)
	lch.LCHAddREQUESTED_AT(b, req.RequestedAtMs)
	lch.LCHAddPROVIDER_PEER_ID(b, providerPeerID)
	root := lch.LCHEnd(b)
	lch.FinishLCHBuffer(b, root)

	return b.FinishedBytes(), nil
}

// ChallengeResponse is the provider's answer: the challenge nonce the requester
// must prove possession over, plus grant-scoping echoes. RawBytes is the exact
// response frame — the proof step signs it verbatim, so it must be preserved
// byte-for-byte.
type ChallengeResponse struct {
	RequestID      string
	ModuleID       string
	ModuleVersion  string
	ProviderPeerID string
	ChallengeNonce []byte
	ExpiresAtMs    uint64
	ErrorCode      string
	ErrorMessage   string
	RawBytes       []byte
}

// DecodeChallengeResponse parses an LCH challenge-response frame. It errors if
// the frame is not a valid $LCH buffer, is not a Response message, or carries a
// provider error.
func DecodeChallengeResponse(data []byte) (*ChallengeResponse, error) {
	if len(data) < 8 || !lch.LCHBufferHasIdentifier(data) {
		return nil, errors.New("deliveryclient: not an $LCH frame")
	}
	msg := lch.GetRootAsLCH(data, 0)

	switch byte(msg.MESSAGE_TYPE()) {
	case challengeMessageTypeResponse:
		// expected
	case challengeMessageTypeError:
		return nil, fmt.Errorf("deliveryclient: provider challenge error %q: %s",
			string(msg.ERROR_CODE()), string(msg.ERROR_MESSAGE()))
	default:
		return nil, fmt.Errorf("deliveryclient: expected challenge response, got message type %d",
			byte(msg.MESSAGE_TYPE()))
	}

	resp := &ChallengeResponse{
		RequestID:      string(msg.REQUEST_ID()),
		ModuleID:       string(msg.MODULE_ID()),
		ModuleVersion:  string(msg.MODULE_VERSION()),
		ProviderPeerID: string(msg.PROVIDER_PEER_ID()),
		ExpiresAtMs:    msg.EXPIRES_AT(),
		ErrorCode:      string(msg.ERROR_CODE()),
		ErrorMessage:   string(msg.ERROR_MESSAGE()),
		RawBytes:       append([]byte(nil), data...),
	}
	if msg.CHALLENGE_NONCELength() > 0 {
		resp.ChallengeNonce = append([]byte(nil), msg.ChallengeNonceBytes()...)
	}
	return resp, nil
}
