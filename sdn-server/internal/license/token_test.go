package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

func TestSignAndVerifyCapabilityToken(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	now := time.Now().UTC()
	claims := CapabilityClaims{
		Iss:    "test-issuer",
		Sub:    "xpub-test",
		PeerID: "12D3KooWTest",
		Plan:   "explorer",
		Scopes: []string{"api:data:read:free", "api:data:read:premium"},
		Iat:    now.Unix(),
		Exp:    now.Add(5 * time.Minute).Unix(),
		JTI:    "jti-123",
	}
	token, err := SignCapabilityToken(claims, priv)
	if err != nil {
		t.Fatalf("SignCapabilityToken failed: %v", err)
	}

	got, err := VerifyCapabilityToken(token, pub, VerifyOptions{
		Now:            now,
		Issuer:         "test-issuer",
		ExpectedPeerID: "12D3KooWTest",
		RequiredScopes: []string{"api:data:read:premium"},
	})
	if err != nil {
		t.Fatalf("VerifyCapabilityToken failed: %v", err)
	}
	if got.Sub != claims.Sub {
		t.Fatalf("sub mismatch: got=%s want=%s", got.Sub, claims.Sub)
	}
}

func TestVerifyCapabilityTokenMissingScope(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	now := time.Now().UTC()
	claims := CapabilityClaims{
		Iss:    "test-issuer",
		Sub:    "xpub-test",
		PeerID: "12D3KooWTest",
		Plan:   "free",
		Scopes: []string{"api:data:read:free"},
		Iat:    now.Unix(),
		Exp:    now.Add(5 * time.Minute).Unix(),
		JTI:    "jti-123",
	}
	token, err := SignCapabilityToken(claims, priv)
	if err != nil {
		t.Fatalf("SignCapabilityToken failed: %v", err)
	}

	_, err = VerifyCapabilityToken(token, pub, VerifyOptions{
		Now:            now,
		Issuer:         "test-issuer",
		ExpectedPeerID: "12D3KooWTest",
		RequiredScopes: []string{"api:data:read:premium"},
	})
	if err == nil {
		t.Fatal("expected missing scope error")
	}
	if !errors.Is(err, ErrTokenMissingScope) {
		t.Fatalf("unexpected error: %v", err)
	}
}
