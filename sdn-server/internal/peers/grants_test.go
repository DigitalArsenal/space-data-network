package peers

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// newTestEd25519Identity generates a fresh Ed25519 libp2p keypair and its
// derived peer ID for use as a test granter/subject. Keys are generated via
// crypto/rand + libp2p's own key-generation helper — never hardcoded or
// real key material, per the task's test-key policy.
func newTestEd25519Identity(t *testing.T) (libp2pcrypto.PrivKey, peer.ID) {
	t.Helper()
	priv, pub, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey: %v", err)
	}
	return priv, id
}

func TestSignGrant_VerifySucceeds(t *testing.T) {
	granterPriv, granterID := newTestEd25519Identity(t)
	_, subjectID := newTestEd25519Identity(t)

	sg, err := SignGrant(granterPriv, Grant{
		Subject: subjectID,
		Level:   Trusted,
	})
	if err != nil {
		t.Fatalf("SignGrant: %v", err)
	}
	if sg.Granter != granterID {
		t.Fatalf("Granter = %s, want %s", sg.Granter, granterID)
	}
	if sg.IssuedAt.IsZero() {
		t.Fatal("IssuedAt should default to now when unset")
	}
	if err := sg.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestSignGrant_ExplicitGranterMustMatchSigningKey(t *testing.T) {
	granterPriv, _ := newTestEd25519Identity(t)
	_, otherID := newTestEd25519Identity(t)
	_, subjectID := newTestEd25519Identity(t)

	if _, err := SignGrant(granterPriv, Grant{
		Granter: otherID,
		Subject: subjectID,
		Level:   Trusted,
	}); err == nil {
		t.Fatal("expected error signing a grant whose explicit Granter does not match the signing key")
	}
}

func TestSignGrant_RejectsMissingSubject(t *testing.T) {
	granterPriv, _ := newTestEd25519Identity(t)
	if _, err := SignGrant(granterPriv, Grant{Level: Trusted}); !errors.Is(err, ErrGrantMissingFields) {
		t.Fatalf("SignGrant() error = %v, want ErrGrantMissingFields", err)
	}
}

func TestSignGrant_RejectsNonEd25519Key(t *testing.T) {
	priv, _, err := libp2pcrypto.GenerateKeyPair(libp2pcrypto.Secp256k1, 256)
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	_, subjectID := newTestEd25519Identity(t)

	_, err = SignGrant(priv, Grant{Subject: subjectID, Level: Trusted})
	if !errors.Is(err, ErrGrantNotEd25519) {
		t.Fatalf("SignGrant() error = %v, want ErrGrantNotEd25519", err)
	}
}

func TestSignedGrant_Verify_WrongKey(t *testing.T) {
	granterPriv, _ := newTestEd25519Identity(t)
	_, subjectID := newTestEd25519Identity(t)

	sg, err := SignGrant(granterPriv, Grant{Subject: subjectID, Level: Trusted})
	if err != nil {
		t.Fatalf("SignGrant: %v", err)
	}

	// Swap in a DIFFERENT granter identity (a different Ed25519 keypair)
	// without re-signing: the newly-embedded public key no longer matches
	// the original signature.
	_, wrongGranterID := newTestEd25519Identity(t)
	sg.Granter = wrongGranterID

	if err := sg.Verify(); !errors.Is(err, ErrGrantBadSignature) {
		t.Fatalf("Verify() error = %v, want ErrGrantBadSignature", err)
	}
}

func TestSignedGrant_Verify_TamperedFields(t *testing.T) {
	granterPriv, _ := newTestEd25519Identity(t)
	_, subjectID := newTestEd25519Identity(t)
	_, otherSubjectID := newTestEd25519Identity(t)

	base, err := SignGrant(granterPriv, Grant{
		Subject:   subjectID,
		Level:     Trusted,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("SignGrant: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(sg *SignedGrant)
	}{
		{"subject", func(sg *SignedGrant) { sg.Subject = otherSubjectID }},
		{"level", func(sg *SignedGrant) { sg.Level = Admin }},
		{"expiry", func(sg *SignedGrant) { sg.ExpiresAt = sg.ExpiresAt.Add(time.Hour) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tampered := base
			tc.mutate(&tampered)
			if err := tampered.Verify(); !errors.Is(err, ErrGrantBadSignature) {
				t.Fatalf("Verify() after tampering with %s: error = %v, want ErrGrantBadSignature", tc.name, err)
			}
		})
	}
}

func TestSignedGrant_Verify_Expired(t *testing.T) {
	granterPriv, _ := newTestEd25519Identity(t)
	_, subjectID := newTestEd25519Identity(t)

	now := time.Now()
	sg, err := SignGrant(granterPriv, Grant{
		Subject:   subjectID,
		Level:     Trusted,
		IssuedAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("SignGrant: %v", err)
	}

	if err := sg.VerifyAt(now); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("VerifyAt(now) error = %v, want ErrGrantExpired", err)
	}
	// One minute before the ExpiresAt instant, it must still be valid.
	if err := sg.VerifyAt(now.Add(-2 * time.Minute)); err != nil {
		t.Fatalf("VerifyAt() before expiry: %v", err)
	}
}

func TestSignedGrant_NoExpirySet_NeverExpires(t *testing.T) {
	granterPriv, _ := newTestEd25519Identity(t)
	_, subjectID := newTestEd25519Identity(t)

	sg, err := SignGrant(granterPriv, Grant{Subject: subjectID, Level: Trusted})
	if err != nil {
		t.Fatalf("SignGrant: %v", err)
	}
	if err := sg.VerifyAt(time.Now().AddDate(10, 0, 0)); err != nil {
		t.Fatalf("VerifyAt() far future with no ExpiresAt: %v", err)
	}
}

func TestSignedGrant_MarshalUnmarshal_RoundTrip(t *testing.T) {
	granterPriv, _ := newTestEd25519Identity(t)
	_, subjectID := newTestEd25519Identity(t)

	sg, err := SignGrant(granterPriv, Grant{
		Subject:   subjectID,
		Level:     Standard,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("SignGrant: %v", err)
	}

	encoded, err := sg.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	parsed, err := UnmarshalSignedGrant(encoded)
	if err != nil {
		t.Fatalf("UnmarshalSignedGrant: %v", err)
	}
	if err := parsed.Verify(); err != nil {
		t.Fatalf("Verify() after round trip: %v", err)
	}

	reEncoded, err := parsed.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary (re-encode): %v", err)
	}
	if !bytes.Equal(encoded, reEncoded) {
		t.Fatalf("round trip is not byte-identical:\n  original:   %x\n  re-encoded: %x", encoded, reEncoded)
	}
	if parsed.Granter != sg.Granter || parsed.Subject != sg.Subject || parsed.Level != sg.Level {
		t.Fatalf("parsed fields do not match original: %+v vs %+v", parsed.Grant, sg.Grant)
	}
	if !parsed.IssuedAt.Equal(sg.IssuedAt) || !parsed.ExpiresAt.Equal(sg.ExpiresAt) {
		t.Fatalf("parsed timestamps do not match original: %+v vs %+v", parsed.Grant, sg.Grant)
	}
}

func TestUnmarshalSignedGrant_RejectsTruncatedAndTrailingData(t *testing.T) {
	granterPriv, _ := newTestEd25519Identity(t)
	_, subjectID := newTestEd25519Identity(t)
	sg, err := SignGrant(granterPriv, Grant{Subject: subjectID, Level: Trusted})
	if err != nil {
		t.Fatalf("SignGrant: %v", err)
	}
	encoded, err := sg.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	if _, err := UnmarshalSignedGrant(encoded[:len(encoded)-1]); err == nil {
		t.Fatal("expected error unmarshaling truncated data")
	}
	if _, err := UnmarshalSignedGrant(append(encoded, 0xFF)); err == nil {
		t.Fatal("expected error unmarshaling data with trailing bytes")
	}
}

func TestBuildTrustGraphAt_MarginalAndFullThresholds(t *testing.T) {
	_, subjectID := newTestEd25519Identity(t)

	makeGrant := func(t *testing.T, level TrustLevel) SignedGrant {
		t.Helper()
		priv, _ := newTestEd25519Identity(t)
		sg, err := SignGrant(priv, Grant{Subject: subjectID, Level: level})
		if err != nil {
			t.Fatalf("SignGrant: %v", err)
		}
		return sg
	}

	t.Run("two_marginal_grants_not_valid", func(t *testing.T) {
		grants := []SignedGrant{makeGrant(t, Limited), makeGrant(t, Limited)}
		g, skipped := BuildTrustGraph(grants)
		if skipped != 0 {
			t.Fatalf("skipped = %d, want 0", skipped)
		}
		valid, marginal, full := ComputeValidity(g, subjectID.String())
		if valid {
			t.Fatalf("2 marginal trusters should not be valid (marginal=%d full=%d)", marginal, full)
		}
	})

	t.Run("three_marginal_grants_valid", func(t *testing.T) {
		grants := []SignedGrant{makeGrant(t, Limited), makeGrant(t, Standard), makeGrant(t, Limited)}
		g, skipped := BuildTrustGraph(grants)
		if skipped != 0 {
			t.Fatalf("skipped = %d, want 0", skipped)
		}
		valid, marginal, _ := ComputeValidity(g, subjectID.String())
		if !valid || marginal < MinMarginalTrusters {
			t.Fatalf("3 marginal trusters should be valid (marginal=%d)", marginal)
		}
	})

	t.Run("one_full_grant_valid", func(t *testing.T) {
		grants := []SignedGrant{makeGrant(t, Trusted)}
		g, skipped := BuildTrustGraph(grants)
		if skipped != 0 {
			t.Fatalf("skipped = %d, want 0", skipped)
		}
		valid, _, full := ComputeValidity(g, subjectID.String())
		if !valid || full < MinFullTrusters {
			t.Fatalf("1 full truster should be valid (full=%d)", full)
		}
	})

	t.Run("tampered_grant_contributes_no_edge", func(t *testing.T) {
		g1 := makeGrant(t, Limited)
		g2 := makeGrant(t, Limited)
		g3 := makeGrant(t, Limited)
		g3.Level = Trusted // tamper post-signature: canonicalBytes changes, Verify fails

		g, skipped := BuildTrustGraph([]SignedGrant{g1, g2, g3})
		if skipped != 1 {
			t.Fatalf("skipped = %d, want 1 (the tampered grant)", skipped)
		}
		valid, marginal, full := ComputeValidity(g, subjectID.String())
		if valid {
			t.Fatalf("tampered grant must not count: only 2 valid marginal trusters remain (marginal=%d full=%d)", marginal, full)
		}
	})

	t.Run("never_and_untrusted_contribute_no_edge", func(t *testing.T) {
		grants := []SignedGrant{makeGrant(t, Never), makeGrant(t, Untrusted)}
		g, skipped := BuildTrustGraph(grants)
		if skipped != 2 {
			t.Fatalf("skipped = %d, want 2", skipped)
		}
		if got := len(g.Edges()); got != 0 {
			t.Fatalf("edges = %d, want 0", got)
		}
	})
}

func TestBuildTrustGraphAt_LatestGrantWinsForSamePair(t *testing.T) {
	granterPriv, granterID := newTestEd25519Identity(t)
	_, subjectID := newTestEd25519Identity(t)

	older, err := SignGrant(granterPriv, Grant{
		Subject:  subjectID,
		Level:    Limited,
		IssuedAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("SignGrant (older): %v", err)
	}
	newer, err := SignGrant(granterPriv, Grant{
		Subject:  subjectID,
		Level:    Trusted,
		IssuedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("SignGrant (newer): %v", err)
	}

	g, skipped := BuildTrustGraph([]SignedGrant{older, newer})
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	edge, ok := g.Edge(granterID.String(), subjectID.String())
	if !ok {
		t.Fatal("expected an edge for the granter/subject pair")
	}
	if edge.Weight != FullEdgeWeight {
		t.Fatalf("edge weight = %v, want the newer (Full) grant's weight %v", edge.Weight, FullEdgeWeight)
	}
}

func TestBuildTrustGraphAt_SelfGrantSkipped(t *testing.T) {
	priv, id := newTestEd25519Identity(t)
	sg, err := SignGrant(priv, Grant{Subject: id, Level: Trusted})
	if err != nil {
		t.Fatalf("SignGrant: %v", err)
	}
	g, skipped := BuildTrustGraph([]SignedGrant{sg})
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if got := len(g.Edges()); got != 0 {
		t.Fatalf("edges = %d, want 0", got)
	}
}

func TestBuildTrustGraph_EmptyInput(t *testing.T) {
	g, skipped := BuildTrustGraph(nil)
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if g == nil {
		t.Fatal("expected a non-nil (empty) graph")
	}
	if got := len(g.Nodes()); got != 0 {
		t.Fatalf("nodes = %d, want 0", got)
	}
}
