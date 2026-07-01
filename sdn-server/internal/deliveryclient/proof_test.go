package deliveryclient

import (
	"bytes"
	"testing"

	lpf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LPF"
)

func TestEncodeGrantProofRoundTrip(t *testing.T) {
	proof := GrantProof{
		RequestID:                   "req-9",
		ModuleID:                    "com.orbpro.starlink-source",
		ModuleVersion:               "1.0.0",
		RequesterPeerID:             "peerA",
		RequesterXPub:               "xpubA",
		RequestedDomain:             "orbpro.default",
		RequestedTimeoutMs:          30_000,
		RequesterEphemeralPublicKey: []byte{5, 6, 7, 8},
		ChallengeNonce:              []byte{9, 9, 9},
		ChallengeExpiresAtMs:        42,
		ProviderPeerID:              "peerP",
		Signature:                   []byte{0xaa, 0xbb, 0xcc},
		RequesterSigningPublicKey:   []byte{1, 2, 3, 4},
		TimestampMs:                 7777,
	}
	frame, err := EncodeGrantProof(proof)
	if err != nil {
		t.Fatalf("EncodeGrantProof() error = %v", err)
	}
	if !lpf.LPFBufferHasIdentifier(frame) {
		t.Fatal("frame missing $LPF identifier")
	}

	m := lpf.GetRootAsLPF(frame, 0)
	if got := byte(m.MESSAGE_TYPE()); got != proofMessageTypeRequest {
		t.Errorf("MESSAGE_TYPE = %d, want ProofRequest(%d)", got, proofMessageTypeRequest)
	}
	if got := string(m.REQUEST_ID()); got != "req-9" {
		t.Errorf("REQUEST_ID = %q", got)
	}
	if got := string(m.MODULE_ID()); got != "com.orbpro.starlink-source" {
		t.Errorf("MODULE_ID = %q", got)
	}
	if got := m.SignatureBytes(); !bytes.Equal(got, []byte{0xaa, 0xbb, 0xcc}) {
		t.Errorf("SIGNATURE = %v", got)
	}
	if got := m.SigningPubkeyBytes(); !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Errorf("SIGNING_PUBKEY = %v", got)
	}
	if got := m.ChallengeNonceBytes(); !bytes.Equal(got, []byte{9, 9, 9}) {
		t.Errorf("CHALLENGE_NONCE = %v", got)
	}
	if got := m.RequesterEphemeralPubkeyBytes(); !bytes.Equal(got, []byte{5, 6, 7, 8}) {
		t.Errorf("REQUESTER_EPHEMERAL_PUBKEY = %v", got)
	}
	if got := m.CHALLENGE_EXPIRES_AT(); got != 42 {
		t.Errorf("CHALLENGE_EXPIRES_AT = %d", got)
	}
	if got := m.TIMESTAMP_MS(); got != 7777 {
		t.Errorf("TIMESTAMP_MS = %d", got)
	}
}

func TestEncodeGrantProofValidation(t *testing.T) {
	if _, err := EncodeGrantProof(GrantProof{Signature: []byte{1}}); err == nil {
		t.Error("expected error for missing request id")
	}
	if _, err := EncodeGrantProof(GrantProof{RequestID: "r"}); err == nil {
		t.Error("expected error for missing signature")
	}
}

func TestGrantProofFromChallenge(t *testing.T) {
	req := ChallengeRequest{
		RequestID: "req-9", ModuleID: "m", ModuleVersion: "1.0.0",
		RequesterPeerID: "peerA", RequesterXPub: "xpubA",
		RequestedDomain: "orbpro.default", RequestedTimeoutMs: 30_000,
		RequesterEphemeralPublicKey: []byte{5, 6, 7, 8}, ProviderPeerID: "peerReq",
	}
	challenge := &ChallengeResponse{
		ChallengeNonce: []byte{1, 1}, ExpiresAtMs: 99, ProviderPeerID: "peerP",
	}
	proof := GrantProofFromChallenge(req, challenge, []byte{0xde}, []byte{0xad}, 555)

	if proof.RequestID != "req-9" || proof.ModuleID != "m" {
		t.Errorf("scoping not carried: %+v", proof)
	}
	if !bytes.Equal(proof.ChallengeNonce, []byte{1, 1}) || proof.ChallengeExpiresAtMs != 99 {
		t.Errorf("challenge fields not carried: %+v", proof)
	}
	// The challenge's provider peer id wins over the request's.
	if proof.ProviderPeerID != "peerP" {
		t.Errorf("ProviderPeerID = %q, want peerP (from challenge)", proof.ProviderPeerID)
	}
	if !bytes.Equal(proof.Signature, []byte{0xde}) || proof.TimestampMs != 555 {
		t.Errorf("signature/timestamp not set: %+v", proof)
	}
}
