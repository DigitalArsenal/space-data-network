package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"
)

func TestServiceChallengeAndProofFlowReturnsGrant(t *testing.T) {
	baseDir := t.TempDir()
	svc, err := NewService(baseDir, "test-license")
	if err != nil {
		t.Fatalf("create license service: %v", err)
	}
	defer svc.Close()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client keypair: %v", err)
	}

	peerID, err := peerIDFromEd25519(publicKey)
	if err != nil {
		t.Fatalf("derive peer id: %v", err)
	}

	now := time.Now().Unix()
	challengeResponse, challengeErr := svc.handleChallengeRequest(ChallengeRequest{
		Type:            msgTypeChallengeRequest,
		ReqID:           "req-123",
		XPub:            "xpub-license-client",
		PeerID:          peerID,
		ClientPubKeyHex: hex.EncodeToString(publicKey),
		TS:              now,
	}, "server-peer-id", "remote-peer-id")
	if challengeErr != nil {
		t.Fatalf("challenge request failed: %+v", challengeErr)
	}
	if challengeResponse.Type != msgTypeChallengeResponse {
		t.Fatalf("unexpected challenge response type: %q", challengeResponse.Type)
	}

	challengeBytes, err := base64.RawStdEncoding.DecodeString(challengeResponse.Challenge)
	if err != nil {
		t.Fatalf("decode challenge: %v", err)
	}

	signature := ed25519.Sign(privateKey, challengeBytes)
	grantResponse, grantErr := svc.handleProofRequest(ProofRequest{
		Type:         msgTypeProofRequest,
		ReqID:        "req-123",
		XPub:         "xpub-license-client",
		PeerID:       peerID,
		Challenge:    challengeResponse.Challenge,
		SignatureHex: hex.EncodeToString(signature),
		TS:           now,
	})
	if grantErr != nil {
		t.Fatalf("proof request failed: %+v", grantErr)
	}

	if grantResponse.Type != msgTypeGrantResponse {
		t.Fatalf("unexpected grant response type: %q", grantResponse.Type)
	}
	if grantResponse.ReqID != "req-123" {
		t.Fatalf("unexpected req id: %q", grantResponse.ReqID)
	}
	if grantResponse.Entitlement.XPub != "xpub-license-client" {
		t.Fatalf("unexpected entitlement xpub: %q", grantResponse.Entitlement.XPub)
	}
	if grantResponse.Entitlement.Status != entitlementStatusActive {
		t.Fatalf("unexpected entitlement status: %q", grantResponse.Entitlement.Status)
	}
	if grantResponse.CapabilityToken == "" {
		t.Fatalf("expected non-empty capability token")
	}
	if grantResponse.ExpiresAt <= now {
		t.Fatalf("expected future grant expiry, got %d", grantResponse.ExpiresAt)
	}
}
