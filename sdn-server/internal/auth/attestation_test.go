package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// newTestAttestationKeypair generates a fresh Ed25519 keypair via
// crypto/rand (never a hardcoded/real seed or mnemonic) for use as a test
// wallet's signing key.
func newTestAttestationKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return pub, priv
}

// newTestAttestationStore creates a UserStore in a temp dir with one
// registered user (xpub) whose signing key is already bound to pub —
// mirroring a wallet user who has completed TOFU (see tofu.go) or was
// config-provisioned with signing_pubkey_hex set.
func newTestAttestationStore(t *testing.T, xpub, name string, trust peers.TrustLevel, pub ed25519.PublicKey) *UserStore {
	t.Helper()
	dir := t.TempDir()
	store, err := NewUserStore(filepath.Join(dir, "users.db"), nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.AddUser(xpub, name, trust, hex.EncodeToString(pub)); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	return store
}

func TestSignAttestation_DefaultsIssuedAtAndNonce(t *testing.T) {
	_, priv := newTestAttestationKeypair(t)

	att, sig, err := SignAttestation(priv, Attestation{XPub: "xpub-defaults", Claim: "self"})
	if err != nil {
		t.Fatalf("SignAttestation: %v", err)
	}
	if att.IssuedAt.IsZero() {
		t.Fatal("SignAttestation must default IssuedAt when left zero")
	}
	if len(att.Nonce) == 0 {
		t.Fatal("SignAttestation must generate a Nonce when left empty")
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("signature length = %d, want %d", len(sig), ed25519.SignatureSize)
	}
}

func TestSignAttestation_RejectsMissingFields(t *testing.T) {
	_, priv := newTestAttestationKeypair(t)

	if _, _, err := SignAttestation(priv, Attestation{Claim: "self"}); !errors.Is(err, ErrAttestationMissingFields) {
		t.Fatalf("SignAttestation with empty XPub: err = %v, want ErrAttestationMissingFields", err)
	}
	if _, _, err := SignAttestation(priv, Attestation{XPub: "xpub-x"}); !errors.Is(err, ErrAttestationMissingFields) {
		t.Fatalf("SignAttestation with empty Claim: err = %v, want ErrAttestationMissingFields", err)
	}
}

func TestSignAttestation_RejectsWrongSizedKey(t *testing.T) {
	if _, _, err := SignAttestation(ed25519.PrivateKey([]byte("too-short")), Attestation{XPub: "xpub-x", Claim: "self"}); err == nil {
		t.Fatal("SignAttestation with a malformed private key must error, got nil")
	}
}

func TestVerifyAttestation_ValidSignature_ReturnsRegisteredUser(t *testing.T) {
	pub, priv := newTestAttestationKeypair(t)
	xpub := "xpub-browser-self"
	store := newTestAttestationStore(t, xpub, "Browser Self", peers.Standard, pub)

	att, sig, err := SignAttestation(priv, Attestation{XPub: xpub, Claim: "self"})
	if err != nil {
		t.Fatalf("SignAttestation: %v", err)
	}

	user, err := VerifyAttestation(store, att, sig)
	if err != nil {
		t.Fatalf("VerifyAttestation: %v", err)
	}
	if user == nil {
		t.Fatal("VerifyAttestation returned nil user for a valid attestation")
	}
	if user.XPub != xpub {
		t.Fatalf("verified user.XPub = %q, want %q", user.XPub, xpub)
	}

	// Method form (UserStore).VerifyAttestation must agree with the free
	// function.
	user2, err := store.VerifyAttestation(att, sig)
	if err != nil {
		t.Fatalf("store.VerifyAttestation: %v", err)
	}
	if user2 == nil || user2.XPub != xpub {
		t.Fatalf("store.VerifyAttestation returned %+v, want xpub %q", user2, xpub)
	}
}

func TestVerifyAttestation_WrongKey_Fails(t *testing.T) {
	pub, _ := newTestAttestationKeypair(t)
	_, otherPriv := newTestAttestationKeypair(t)
	xpub := "xpub-wrong-key"
	store := newTestAttestationStore(t, xpub, "User", peers.Standard, pub)

	// Signed with a DIFFERENT key than the one bound to xpub in the store.
	att, sig, err := SignAttestation(otherPriv, Attestation{XPub: xpub, Claim: "self"})
	if err != nil {
		t.Fatalf("SignAttestation: %v", err)
	}

	if _, err := VerifyAttestation(store, att, sig); !errors.Is(err, ErrAttestationBadSignature) {
		t.Fatalf("VerifyAttestation with wrong key: err = %v, want ErrAttestationBadSignature", err)
	}
}

func TestVerifyAttestation_TamperedFields_Fail(t *testing.T) {
	pub, priv := newTestAttestationKeypair(t)
	xpub := "xpub-tamper"
	store := newTestAttestationStore(t, xpub, "User", peers.Standard, pub)

	// A second, distinct registered user with its own key, so the
	// "xpub changed" tamper case below targets a real user's record
	// (and so correctly fails on signature mismatch, not merely because
	// the substituted xpub happens to be unregistered).
	otherPub, _ := newTestAttestationKeypair(t)
	if err := store.AddUser("xpub-someone-else", "Someone Else", peers.Standard, hex.EncodeToString(otherPub)); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	baseline, sig, err := SignAttestation(priv, Attestation{XPub: xpub, Claim: "self"})
	if err != nil {
		t.Fatalf("SignAttestation: %v", err)
	}

	// Sanity: the untampered attestation verifies.
	if _, err := VerifyAttestation(store, baseline, sig); err != nil {
		t.Fatalf("untampered attestation must verify, got err = %v", err)
	}

	cases := []struct {
		name   string
		tamper func(Attestation) Attestation
	}{
		{
			name: "claim changed",
			tamper: func(a Attestation) Attestation {
				a.Claim = "not-self"
				return a
			},
		},
		{
			name: "xpub changed to another registered user (signature swap attempt)",
			tamper: func(a Attestation) Attestation {
				a.XPub = "xpub-someone-else"
				return a
			},
		},
		{
			name: "issued_at changed",
			tamper: func(a Attestation) Attestation {
				a.IssuedAt = a.IssuedAt.Add(time.Hour)
				return a
			},
		},
		{
			name: "nonce changed",
			tamper: func(a Attestation) Attestation {
				tampered := append([]byte(nil), a.Nonce...)
				if len(tampered) == 0 {
					tampered = []byte{0x01}
				} else {
					tampered[0] ^= 0xff
				}
				a.Nonce = tampered
				return a
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tampered := tc.tamper(baseline)
			if _, err := VerifyAttestation(store, tampered, sig); !errors.Is(err, ErrAttestationBadSignature) {
				t.Fatalf("VerifyAttestation(%s): err = %v, want ErrAttestationBadSignature", tc.name, err)
			}
		})
	}
}

func TestVerifyAttestation_UnknownXPub_FailsCleanly(t *testing.T) {
	pub, priv := newTestAttestationKeypair(t)
	// Store has a registered user, but NOT the xpub used below.
	store := newTestAttestationStore(t, "xpub-registered", "User", peers.Standard, pub)

	att, sig, err := SignAttestation(priv, Attestation{XPub: "xpub-does-not-exist", Claim: "self"})
	if err != nil {
		t.Fatalf("SignAttestation: %v", err)
	}

	user, err := VerifyAttestation(store, att, sig)
	if !errors.Is(err, ErrAttestationUnknownUser) {
		t.Fatalf("VerifyAttestation for unknown xpub: err = %v, want ErrAttestationUnknownUser", err)
	}
	if user != nil {
		t.Fatalf("VerifyAttestation for unknown xpub must return a nil user, got %+v", user)
	}
}

func TestVerifyAttestation_NoSigningKeyBoundYet_FailsCleanly(t *testing.T) {
	dir := t.TempDir()
	store, err := NewUserStore(filepath.Join(dir, "users.db"), nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	xpub := "xpub-tofu-pending"
	if err := store.AddUser(xpub, "Pending User", peers.Standard, ""); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	_, priv := newTestAttestationKeypair(t)
	att, sig, err := SignAttestation(priv, Attestation{XPub: xpub, Claim: "self"})
	if err != nil {
		t.Fatalf("SignAttestation: %v", err)
	}

	if _, err := VerifyAttestation(store, att, sig); !errors.Is(err, ErrAttestationNoSigningKey) {
		t.Fatalf("VerifyAttestation for a user with no bound signing key: err = %v, want ErrAttestationNoSigningKey", err)
	}
}

func TestVerifyAttestation_MissingFields_FailsCleanly(t *testing.T) {
	pub, _ := newTestAttestationKeypair(t)
	store := newTestAttestationStore(t, "xpub-present", "User", peers.Standard, pub)

	if _, err := VerifyAttestation(store, Attestation{Claim: "self"}, []byte("sig")); !errors.Is(err, ErrAttestationMissingFields) {
		t.Fatalf("VerifyAttestation with empty XPub: err = %v, want ErrAttestationMissingFields", err)
	}
	if _, err := VerifyAttestation(store, Attestation{XPub: "xpub-present"}, []byte("sig")); !errors.Is(err, ErrAttestationMissingFields) {
		t.Fatalf("VerifyAttestation with empty Claim: err = %v, want ErrAttestationMissingFields", err)
	}
}

func TestVerifyAttestation_NilStore_ErrorsInsteadOfPanicking(t *testing.T) {
	if _, err := VerifyAttestation(nil, Attestation{XPub: "xpub-x", Claim: "self"}, []byte("sig")); err == nil {
		t.Fatal("VerifyAttestation with a nil store must return an error, not panic or succeed")
	}
}

// End-to-end: this is exactly the reconciliation described in
// attestation.go's package doc — the browser-self identity (which is
// Ultimate purely by self-recognition, see
// desktop/src/static-http-server.js's applyWalletNodeIdentity) signs an
// Attestation with the same wallet key whose public half is bound to its
// xpub in the UserStore; the daemon verifies it here and reads back the
// same user record — one identity, not two.
func TestVerifyAttestation_ReconcilesBrowserSelfWithStoredXPub(t *testing.T) {
	pub, priv := newTestAttestationKeypair(t)
	xpub := "xpub-reconciled-self"
	store := newTestAttestationStore(t, xpub, "Node Operator", peers.Admin, pub)

	att, sig, err := SignAttestation(priv, Attestation{XPub: xpub, Claim: "self"})
	if err != nil {
		t.Fatalf("SignAttestation: %v", err)
	}

	user, err := VerifyAttestation(store, att, sig)
	if err != nil {
		t.Fatalf("VerifyAttestation: %v", err)
	}
	if user.XPub != xpub {
		t.Fatalf("verified user.XPub = %q, want %q (same identity, both sides of the reconciliation)", user.XPub, xpub)
	}
}
