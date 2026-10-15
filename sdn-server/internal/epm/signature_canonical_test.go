package epm

// SEC-038 regressions: the Go EPM signing preimage follows the one canonical
// rule the wallet and the modules use (ASCII-only trim, raw U+2028/U+2029,
// invalid UTF-8 refused), checked against the shared conformance vectors.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"
)

type canonicalVectorFile struct {
	Schema  string `json:"schema"`
	Vectors []struct {
		Name               string `json:"name"`
		LegalNameHex       string `json:"legal_name_hex"`
		SignatureTimestamp int64  `json:"signature_timestamp"`
		PreimageHex        string `json:"preimage_hex"`
		Error              string `json:"error"`
	} `json:"vectors"`
}

// legalNameEPM builds a minimal $EPM carrying exactly these LEGAL_NAME bytes
// (CreateByteString keeps invalid UTF-8 as is).
func legalNameEPM(legalName []byte, timestamp int64, signatureHex string, signingKey ed25519.PublicKey) []byte {
	b := flatbuffers.NewBuilder(256)
	name := b.CreateByteString(legalName)
	var keys flatbuffers.UOffsetT
	if signingKey != nil {
		pub := b.CreateString(hex.EncodeToString(signingKey))
		EPM.CryptoKeyStart(b)
		EPM.CryptoKeyAddPUBLIC_KEY(b, pub)
		EPM.CryptoKeyAddKEY_TYPE(b, EPM.KeyTypeSigning)
		key := EPM.CryptoKeyEnd(b)
		EPM.EPMStartKEYSVector(b, 1)
		b.PrependUOffsetT(key)
		keys = b.EndVector(1)
	}
	var sig flatbuffers.UOffsetT
	if signatureHex != "" {
		sig = b.CreateString(signatureHex)
	}
	EPM.EPMStart(b)
	EPM.EPMAddLEGAL_NAME(b, name)
	if signingKey != nil {
		EPM.EPMAddKEYS(b, keys)
	}
	EPM.EPMAddSIGNATURE_TIMESTAMP(b, timestamp)
	if signatureHex != "" {
		EPM.EPMAddSIGNATURE(b, sig)
	}
	EPM.FinishSizePrefixedEPMBuffer(b, EPM.EPMEnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

func TestEPMSigningPayloadMatchesSharedConformanceVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "shared-test-vectors", "epm-canonical", "vectors.json"))
	if err != nil {
		t.Fatalf("read shared vectors: %v", err)
	}
	var file canonicalVectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode shared vectors: %v", err)
	}
	if file.Schema != "sdn-epm-canonical-preimage-vectors/1" || len(file.Vectors) < 20 {
		t.Fatalf("unexpected vector file: schema %q, %d vectors", file.Schema, len(file.Vectors))
	}
	for _, vector := range file.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			legalName, err := hex.DecodeString(vector.LegalNameHex)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := EPMSigningPayload(legalNameEPM(legalName, vector.SignatureTimestamp, "", nil))
			if vector.Error != "" {
				if !errors.Is(err, ErrInvalidEPMString) {
					t.Fatalf("payload=%q err=%v, want ErrInvalidEPMString", payload, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("EPMSigningPayload: %v", err)
			}
			if got := hex.EncodeToString(payload); got != vector.PreimageHex {
				t.Fatalf("preimage\n got  %q\n want %q", payload, mustHex(t, vector.PreimageHex))
			}
		})
	}
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	out, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestRelayCannotPadSignedStringsWithUnicodeWhitespace is the audit repro: a
// signature over "Alice" must not verify for "Alice" padded with Unicode
// whitespace the signature never covered.
func TestRelayCannotPadSignedStringsWithUnicodeWhitespace(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const ts = 1777000000
	payload, err := EPMSigningPayload(legalNameEPM([]byte("Alice"), ts, "", pub))
	if err != nil {
		t.Fatal(err)
	}
	signature := hex.EncodeToString(ed25519.Sign(priv, payload))
	if err := VerifyEPMSignature(legalNameEPM([]byte("Alice"), ts, signature, pub)); err != nil {
		t.Fatalf("original record: %v", err)
	}
	for _, padded := range []string{"Alice　", " Alice", "Alice\u0085", " Alice ", "Alice "} {
		if err := VerifyEPMSignature(legalNameEPM([]byte(padded), ts, signature, pub)); err == nil {
			t.Fatalf("signature over %q verified for %q", "Alice", padded)
		}
	}
	if err := VerifyEPMSignature(legalNameEPM([]byte("Al\xffice"), ts, signature, pub)); err == nil {
		t.Fatal("a record with invalid UTF-8 verified")
	}
}
