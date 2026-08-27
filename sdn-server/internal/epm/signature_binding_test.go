package epm

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"
)

// buildMultiSigningEPM assembles a size-prefixed $EPM whose KEYS vector holds
// signing keys in the given order, each with its ALGORITHM. sigHex is embedded
// as SIGNATURE (or omitted when empty). This reproduces the §21 wire shape for a
// record that carries multiple signing keys.
func buildMultiSigningEPM(pubKeys, algs []string, sigHex string, ts int64) []byte {
	b := flatbuffers.NewBuilder(2048)

	pubOffs := make([]flatbuffers.UOffsetT, len(pubKeys))
	algoOffs := make([]flatbuffers.UOffsetT, len(pubKeys))
	for i := range pubKeys {
		pubOffs[i] = b.CreateString(pubKeys[i])
		algoOffs[i] = b.CreateString(algs[i])
	}

	keyOffs := make([]flatbuffers.UOffsetT, len(pubKeys))
	for i := range pubKeys {
		EPM.CryptoKeyStart(b)
		EPM.CryptoKeyAddPUBLIC_KEY(b, pubOffs[i])
		EPM.CryptoKeyAddALGORITHM(b, algoOffs[i])
		EPM.CryptoKeyAddKEY_TYPE(b, EPM.KeyTypeSigning)
		keyOffs[i] = EPM.CryptoKeyEnd(b)
	}
	EPM.EPMStartKEYSVector(b, len(keyOffs))
	for i := len(keyOffs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(keyOffs[i])
	}
	keysVec := b.EndVector(len(keyOffs))

	var sig flatbuffers.UOffsetT
	if sigHex != "" {
		sig = b.CreateString(sigHex)
	}
	EPM.EPMStart(b)
	EPM.EPMAddKEYS(b, keysVec)
	EPM.EPMAddENTITY_TYPE(b, EPM.EntityTypeUser)
	EPM.EPMAddSIGNATURE_TIMESTAMP(b, ts)
	if sigHex != "" {
		EPM.EPMAddSIGNATURE(b, sig)
	}
	epm := EPM.EPMEnd(b)
	b.FinishSizePrefixedWithFileIdentifier(epm, []byte("$EPM"))
	return b.FinishedBytes()
}

// TestVerifyEPMSignatureBindingKeyBindsToSignAlias pins the §21 chain
// tightening: the record's SIGNATURE must verify against the card's sign-alias
// key, which appleIdentityEntriesFromEPM emits as the FIRST signing key's
// PUBLIC_KEY. A signature that verifies against the second signing key but NOT
// the first must be rejected by the binding verify path.
func TestVerifyEPMSignatureBindingKeyBindsToSignAlias(t *testing.T) {
	t.Parallel()

	_, priv1, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	_, priv2, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pub1 := priv1.Public().(ed25519.PublicKey)
	pub2 := priv2.Public().(ed25519.PublicKey)
	pub1Hex := hex.EncodeToString(pub1)
	pub2Hex := hex.EncodeToString(pub2)
	const ts = int64(1782500000)

	// One unsigned wire shape, signed under each key in turn. The signing
	// payload covers BOTH KEYS entries (canonicalSigningContentFromEPM), so the
	// key-2 signature is a genuine forgery attempt against the same record, not
	// a payload mismatch.
	unsigned := buildMultiSigningEPM([]string{pub1Hex, pub2Hex}, []string{"ed25519", "ed25519"}, "", ts)
	payload, err := EPMSigningPayload(unsigned)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}

	// Signed by key1 — the card's sign alias (first signing key) — must verify.
	sig1 := ed25519.Sign(priv1, payload)
	signedByKey1 := buildMultiSigningEPM([]string{pub1Hex, pub2Hex}, []string{"ed25519", "ed25519"}, hex.EncodeToString(sig1), ts)
	if err := VerifyEPMSignatureBindingKey(signedByKey1, []byte(pub1)); err != nil {
		t.Fatalf("signature by the sign-alias key (first signing key) must verify: %v", err)
	}

	// Signed by key2 — verifies on its own key but NOT against the sign alias —
	// must be rejected by the binding.
	sig2 := ed25519.Sign(priv2, payload)
	signedByKey2 := buildMultiSigningEPM([]string{pub1Hex, pub2Hex}, []string{"ed25519", "ed25519"}, hex.EncodeToString(sig2), ts)
	if err := VerifyEPMSignatureBindingKey(signedByKey2, []byte(pub1)); err == nil {
		t.Fatal("signature by the SECOND signing key must be rejected by the sign-alias binding")
	} else if !errors.Is(err, ErrInvalidEPMSignature) {
		t.Fatalf("want ErrInvalidEPMSignature, got %v", err)
	}
	// The loose WIRE verifier still accepts it — this is exactly the loophole
	// VerifyEPMSignatureBindingKey closes (any KEY_TYPE=Signing entry would do).
	if err := VerifyEPMSignature(signedByKey2); err != nil {
		t.Fatalf("precondition: loose verifier accepts second-key signature, got %v", err)
	}

	// Handing the binding the SECOND key as "the card's sign alias" must also
	// fail: the record's first signing key (the emitted alias) is key1, so a
	// card advertising key2 disagrees with its own record.
	if err := VerifyEPMSignatureBindingKey(signedByKey1, []byte(pub2)); err == nil {
		t.Fatal("a card advertising a non-first signing key must not verify")
	}
}

// TestVerifyEPMSignatureBindingKeySignerMustBeInRecord pins that the binding
// does not widen into "any key that verifies": a signature from a key that is
// NOT in the record's KEYS vector is rejected even though it is a valid ed25519
// signature over the payload.
func TestVerifyEPMSignatureBindingKeySignerMustBeInRecord(t *testing.T) {
	t.Parallel()

	_, priv1, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	_, rogue, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pub1 := priv1.Public().(ed25519.PublicKey)
	roguePub := rogue.Public().(ed25519.PublicKey)
	const ts = int64(1782500000)

	unsigned := buildMultiSigningEPM(
		[]string{hex.EncodeToString(pub1), hex.EncodeToString(roguePub)},
		[]string{"ed25519", "ed25519"}, "", ts)
	payload, err := EPMSigningPayload(unsigned)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}

	sig1 := ed25519.Sign(priv1, payload)
	signed := buildMultiSigningEPM(
		[]string{hex.EncodeToString(pub1), hex.EncodeToString(roguePub)},
		[]string{"ed25519", "ed25519"}, hex.EncodeToString(sig1), ts)
	if err := VerifyEPMSignatureBindingKey(signed, []byte(pub1)); err != nil {
		t.Fatalf("signature by the sign-alias key must verify: %v", err)
	}
	if err := VerifyEPMSignature(signed); err != nil {
		t.Fatalf("wire verifier accepts: %v", err)
	}

	if err := VerifyEPMSignatureBindingKey(signed, []byte(roguePub)); err == nil {
		t.Fatal("rogue key not advertised as the first signing key must not bind")
	}
}
