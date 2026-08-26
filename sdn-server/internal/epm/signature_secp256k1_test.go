package epm

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	flatbuffers "github.com/google/flatbuffers/go"
)

// buildTestEPM assembles a size-prefixed $EPM with one signing key (algorithm +
// compressed/hex pubkey), ENTITY_TYPE User, timestamp, and optional SIGNATURE hex.
// §21: the verifier dispatches on ALGORITHM, not ADDRESS_TYPE. The addrType
// parameter is retained for callers but is now set as ALGORITHM (the curve
// designator); ADDRESS_TYPE is an address-format tag, not a curve designator.
func buildTestEPM(addrType, pubHex, sigHex string, ts int64) []byte {
	b := flatbuffers.NewBuilder(256)
	pub := b.CreateString(pubHex)
	alg := b.CreateString(addrType)
	EPM.CryptoKeyStart(b)
	EPM.CryptoKeyAddPUBLIC_KEY(b, pub)
	EPM.CryptoKeyAddALGORITHM(b, alg)
	EPM.CryptoKeyAddKEY_TYPE(b, EPM.KeyTypeSigning)
	key := EPM.CryptoKeyEnd(b)

	EPM.EPMStartKEYSVector(b, 1)
	b.PrependUOffsetT(key)
	keys := b.EndVector(1)

	var sig flatbuffers.UOffsetT
	if sigHex != "" {
		sig = b.CreateString(sigHex)
	}
	EPM.EPMStart(b)
	EPM.EPMAddKEYS(b, keys)
	EPM.EPMAddENTITY_TYPE(b, EPM.EntityTypeUser)
	EPM.EPMAddSIGNATURE_TIMESTAMP(b, ts)
	if sigHex != "" {
		EPM.EPMAddSIGNATURE(b, sig)
	}
	epm := EPM.EPMEnd(b)
	b.FinishSizePrefixedWithFileIdentifier(epm, []byte("$EPM"))
	return b.FinishedBytes()
}

func TestVerifyEPMSignatureSecp256k1(t *testing.T) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pubHex := hex.EncodeToString(priv.PubKey().SerializeCompressed())
	const ts = int64(1782470000)

	// Canonical payload comes from the unsigned EPM (SIGNATURE excluded).
	unsigned := buildTestEPM("secp256k1", pubHex, "", ts)
	payload, err := EPMSigningPayload(unsigned)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}

	// secp256k1 EPM signature scheme: ECDSA-DER over sha256(payload).
	digest := sha256.Sum256(payload)
	sigHex := hex.EncodeToString(ecdsa.Sign(priv, digest[:]).Serialize())

	signed := buildTestEPM("secp256k1", pubHex, sigHex, ts)
	if err := VerifyEPMSignature(signed); err != nil {
		t.Fatalf("secp256k1-signed EPM should verify: %v", err)
	}

	// Tampering the (signed) timestamp changes the payload -> must fail.
	tampered := buildTestEPM("secp256k1", pubHex, sigHex, ts+1)
	if err := VerifyEPMSignature(tampered); err == nil {
		t.Fatal("tampered secp256k1 EPM should fail verification")
	}

	// A wrong key -> must fail.
	other, _ := secp256k1.GeneratePrivateKey()
	wrong := buildTestEPM("secp256k1", hex.EncodeToString(other.PubKey().SerializeCompressed()), sigHex, ts)
	if err := VerifyEPMSignature(wrong); err == nil {
		t.Fatal("secp256k1 EPM under a different key should fail")
	}
}
