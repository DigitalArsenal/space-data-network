package deliveryclient

import (
	"errors"
	"strings"

	lpf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LPF"
	flatbuffers "github.com/google/flatbuffers/go"
)

// LPF proof message-type wire values (generated enum constants are
// package-private). ProofRequest is what the requester sends.
const (
	proofMessageTypeRequest  = 0
	proofMessageTypeAccepted = 1
	proofMessageTypeRejected = 2
)

// GrantProof is the requester's proof-of-possession: it echoes the challenge
// scoping and carries the requester's Signature over the entire challenge
// response frame (ChallengeResponse.RawBytes), proving control of the signing
// key the provider gates on.
type GrantProof struct {
	RequestID                   string
	ModuleID                    string
	ModuleVersion               string
	RequesterPeerID             string
	RequesterXPub               string
	RequestedDomain             string
	RequestedTimeoutMs          uint64
	RequesterEphemeralPublicKey []byte
	ChallengeNonce              []byte
	ChallengeExpiresAtMs        uint64
	ProviderPeerID              string
	Signature                   []byte
	RequesterSigningPublicKey   []byte
	TimestampMs                 uint64
}

// GrantProofFromChallenge assembles a GrantProof by carrying the request's
// scoping and the challenge's nonce/expiry forward, given the signature the
// caller computed over challenge.RawBytes with the requester's signing key.
func GrantProofFromChallenge(req ChallengeRequest, challenge *ChallengeResponse, signature, signingPublicKey []byte, timestampMs uint64) GrantProof {
	proof := GrantProof{
		RequestID:                   req.RequestID,
		ModuleID:                    req.ModuleID,
		ModuleVersion:               req.ModuleVersion,
		RequesterPeerID:             req.RequesterPeerID,
		RequesterXPub:               req.RequesterXPub,
		RequestedDomain:             req.RequestedDomain,
		RequestedTimeoutMs:          req.RequestedTimeoutMs,
		RequesterEphemeralPublicKey: req.RequesterEphemeralPublicKey,
		ProviderPeerID:              req.ProviderPeerID,
		Signature:                   signature,
		RequesterSigningPublicKey:   signingPublicKey,
		TimestampMs:                 timestampMs,
	}
	if challenge != nil {
		proof.ChallengeNonce = challenge.ChallengeNonce
		proof.ChallengeExpiresAtMs = challenge.ExpiresAtMs
		if challenge.ProviderPeerID != "" {
			proof.ProviderPeerID = challenge.ProviderPeerID
		}
	}
	return proof
}

// EncodeGrantProof serializes proof as an LPF proof-request frame ($LPF,
// MESSAGE_TYPE=ProofRequest), matching sdn-js encodeGrantProof. The caller must
// have computed Signature over the challenge response bytes beforehand.
func EncodeGrantProof(proof GrantProof) ([]byte, error) {
	if strings.TrimSpace(proof.RequestID) == "" {
		return nil, errors.New("deliveryclient: proof request id is required")
	}
	if len(proof.Signature) == 0 {
		return nil, errors.New("deliveryclient: proof signature is required")
	}

	b := flatbuffers.NewBuilder(512)
	requestID := b.CreateString(proof.RequestID)
	moduleID := b.CreateString(proof.ModuleID)
	moduleVersion := b.CreateString(proof.ModuleVersion)
	requesterPeerID := b.CreateString(proof.RequesterPeerID)
	requesterXPub := b.CreateString(proof.RequesterXPub)
	requestedDomain := b.CreateString(proof.RequestedDomain)
	providerPeerID := b.CreateString(proof.ProviderPeerID)

	var ephemeral, nonce, signingKey flatbuffers.UOffsetT
	if len(proof.RequesterEphemeralPublicKey) > 0 {
		ephemeral = b.CreateByteVector(proof.RequesterEphemeralPublicKey)
	}
	if len(proof.ChallengeNonce) > 0 {
		nonce = b.CreateByteVector(proof.ChallengeNonce)
	}
	signature := b.CreateByteVector(proof.Signature)
	if len(proof.RequesterSigningPublicKey) > 0 {
		signingKey = b.CreateByteVector(proof.RequesterSigningPublicKey)
	}

	lpf.LPFStart(b)
	lpf.LPFAddMESSAGE_TYPE(b, proofMessageTypeRequest)
	lpf.LPFAddREQUEST_ID(b, requestID)
	lpf.LPFAddMODULE_ID(b, moduleID)
	lpf.LPFAddMODULE_VERSION(b, moduleVersion)
	lpf.LPFAddREQUESTER_PEER_ID(b, requesterPeerID)
	lpf.LPFAddREQUESTER_XPUB(b, requesterXPub)
	lpf.LPFAddREQUESTED_DOMAIN(b, requestedDomain)
	lpf.LPFAddREQUESTED_TIMEOUT_MS(b, proof.RequestedTimeoutMs)
	if ephemeral != 0 {
		lpf.LPFAddREQUESTER_EPHEMERAL_PUBKEY(b, ephemeral)
	}
	if nonce != 0 {
		lpf.LPFAddCHALLENGE_NONCE(b, nonce)
	}
	lpf.LPFAddCHALLENGE_EXPIRES_AT(b, proof.ChallengeExpiresAtMs)
	lpf.LPFAddPROVIDER_PEER_ID(b, providerPeerID)
	lpf.LPFAddSIGNATURE(b, signature)
	if signingKey != 0 {
		lpf.LPFAddSIGNING_PUBKEY(b, signingKey)
	}
	lpf.LPFAddTIMESTAMP_MS(b, proof.TimestampMs)
	root := lpf.LPFEnd(b)
	lpf.FinishLPFBuffer(b, root)
	return b.FinishedBytes(), nil
}
