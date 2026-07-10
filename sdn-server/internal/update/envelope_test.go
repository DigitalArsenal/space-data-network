package update

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	"golang.org/x/crypto/curve25519"
)

// envelopeTestRecipient is a synthetic X25519 recipient keypair generated
// with crypto/rand for tests — never real seed/key material.
type envelopeTestRecipient struct {
	keyID []byte
	priv  []byte
	pub   []byte
}

func newEnvelopeTestRecipient(t *testing.T, keyID string) envelopeTestRecipient {
	t.Helper()
	priv := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, priv); err != nil {
		t.Fatal(err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return envelopeTestRecipient{keyID: []byte(keyID), priv: priv, pub: pub}
}

func (r envelopeTestRecipient) asEnvelopeRecipient() EnvelopeRecipient {
	return EnvelopeRecipient{KeyID: r.keyID, PublicKey: r.pub, KeyExchange: ecies.X25519}
}

func TestEncryptCarrierForRecipientsRoundTripsForEveryRecipient(t *testing.T) {
	plaintextBundle := []byte("plaintext update bundle payload for three recipients")
	recipients := []envelopeTestRecipient{
		newEnvelopeTestRecipient(t, "node-a"),
		newEnvelopeTestRecipient(t, "node-b"),
		newEnvelopeTestRecipient(t, "node-c"),
	}
	var envRecipients []EnvelopeRecipient
	for _, r := range recipients {
		envRecipients = append(envRecipients, r.asEnvelopeRecipient())
	}

	env, err := EncryptCarrierForRecipients(plaintextBundle, envRecipients, "test-update-envelope-context")
	if err != nil {
		t.Fatalf("EncryptCarrierForRecipients returned error: %v", err)
	}
	if len(env.Envelopes) != len(recipients) {
		t.Fatalf("got %d sealed envelopes, want %d", len(env.Envelopes), len(recipients))
	}

	for _, r := range recipients {
		plainCarrier, err := DecryptCarrierForRecipient(env, r.keyID, r.priv)
		if err != nil {
			t.Fatalf("recipient %s: DecryptCarrierForRecipient returned error: %v", r.keyID, err)
		}
		got, err := ExtractBundleFromCarrier(plainCarrier)
		if err != nil {
			t.Fatalf("recipient %s: ExtractBundleFromCarrier returned error: %v", r.keyID, err)
		}
		if !bytes.Equal(got, plaintextBundle) {
			t.Fatalf("recipient %s: decrypted bundle = %q, want %q", r.keyID, got, plaintextBundle)
		}
	}
}

func TestDecryptCarrierForRecipientRejectsNonRecipientCleanly(t *testing.T) {
	plaintextBundle := []byte("only for node-a and node-b")
	a := newEnvelopeTestRecipient(t, "node-a")
	b := newEnvelopeTestRecipient(t, "node-b")
	outsider := newEnvelopeTestRecipient(t, "node-outsider")

	env, err := EncryptCarrierForRecipients(plaintextBundle, []EnvelopeRecipient{
		a.asEnvelopeRecipient(), b.asEnvelopeRecipient(),
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// A key id with no matching envelope: no unwrap/decrypt is attempted.
	if _, err := DecryptCarrierForRecipient(env, outsider.keyID, outsider.priv); err != ErrEnvelopeNotForRecipient {
		t.Fatalf("error = %v, want ErrEnvelopeNotForRecipient", err)
	}

	// A spoofed key id that does match an envelope, but the wrong private
	// key: ECIES/AEAD authentication must fail cleanly, never returning
	// garbage plaintext.
	if _, err := DecryptCarrierForRecipient(env, a.keyID, outsider.priv); err == nil {
		t.Fatal("expected a clean decrypt error for a mismatched private key, got nil")
	}
}

func TestEncryptedCarrierAtRestDoesNotContainPlaintextBundle(t *testing.T) {
	plaintextBundle := bytes.Repeat([]byte("super-secret-update-bundle-bytes"), 8)
	recipient := newEnvelopeTestRecipient(t, "node-a")

	env, err := EncryptCarrierForRecipients(plaintextBundle, []EnvelopeRecipient{recipient.asEnvelopeRecipient()}, "")
	if err != nil {
		t.Fatal(err)
	}
	marshaled, err := env.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(marshaled, plaintextBundle) {
		t.Fatal("serialized encrypted envelope contains the plaintext bundle bytes")
	}
	if bytes.Contains(env.Carrier, plaintextBundle) {
		t.Fatal("encrypted carrier bytes contain the plaintext bundle bytes")
	}

	roundTripped, err := ParseEncryptedBundle(marshaled)
	if err != nil {
		t.Fatalf("ParseEncryptedBundle returned error: %v", err)
	}
	plainCarrier, err := DecryptCarrierForRecipient(roundTripped, recipient.keyID, recipient.priv)
	if err != nil {
		t.Fatalf("DecryptCarrierForRecipient returned error: %v", err)
	}
	got, err := ExtractBundleFromCarrier(plainCarrier)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintextBundle) {
		t.Fatal("round trip through marshal/parse did not recover the original bundle bytes")
	}
}

// TestCleartextCarrierStillStagesWithoutEnvelope is the explicit G2
// back-compat check: a carrier built the original (unencrypted) way, with
// no EncryptedBundle wrapper at all, must still stage successfully through
// the existing receive path.
func TestCleartextCarrierStillStagesWithoutEnvelope(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)
	_ = root

	bundleBytes := makeBundleTarGz(t, "9.9.9", map[string]string{
		"bin/spacedatanetwork": "new-binary",
	})
	wasmBytes := BuildCarrier(bundleBytes) // cleartext carrier, no envelope
	manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
		doc["version"] = "9.9.9"
		doc["bundle"].(map[string]any)["hash"] = sha256Hex(bundleBytes)
		doc["bundle"].(map[string]any)["size"] = int64(len(bundleBytes))
		doc["wasm"].(map[string]any)["hash"] = sha256Hex(wasmBytes)
	}, bundleBytes, wasmBytes)

	staged, err := Stage(paths, manifestBytes, wasmBytes, HostVerifyOptions(signer.roots(t), 0, time.Now()))
	if err != nil {
		t.Fatalf("Stage returned error for a cleartext carrier: %v", err)
	}
	if staged.UpdateID != "cli-beta-0001" {
		t.Fatalf("unexpected staged update id: %s", staged.UpdateID)
	}
}
