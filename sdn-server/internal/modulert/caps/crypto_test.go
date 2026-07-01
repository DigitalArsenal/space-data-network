package caps

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/ed25519"
)

func TestCryptoCapHandlerSupportsEd25519SdkOperations(t *testing.T) {
	t.Parallel()

	seed := []byte{
		1, 2, 3, 4, 5, 6, 7, 8,
		9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24,
		25, 26, 27, 28, 29, 30, 31, 32,
	}
	message := []byte("orbpro licensing sdk compatibility")
	expectedPublicKey := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)

	publicKeyResponse, err := cryptoCapHandle(
		"crypto.ed25519.publicKeyFromSeed",
		[]byte(`{"seed":"`+encodeBase64Cap(seed)+`"}`),
	)
	if err != nil {
		t.Fatalf("crypto.ed25519.publicKeyFromSeed returned error: %v", err)
	}

	var publicKeyEnvelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Base64 string `json:"base64"`
		} `json:"result"`
	}
	if err := json.Unmarshal(publicKeyResponse, &publicKeyEnvelope); err != nil {
		t.Fatalf("Unmarshal(publicKeyResponse) failed: %v", err)
	}
	if !publicKeyEnvelope.OK {
		t.Fatalf("public key response was not ok: %s", publicKeyResponse)
	}
	if got := decodeBase64Cap(publicKeyEnvelope.Result.Base64); !bytes.Equal(got, expectedPublicKey) {
		t.Fatalf("public key mismatch: got %x want %x", got, expectedPublicKey)
	}

	signResponse, err := cryptoCapHandle(
		"crypto.ed25519.sign",
		[]byte(`{"message":"`+encodeBase64Cap(message)+`","seed":"`+encodeBase64Cap(seed)+`"}`),
	)
	if err != nil {
		t.Fatalf("crypto.ed25519.sign returned error: %v", err)
	}

	var signEnvelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Base64 string `json:"base64"`
		} `json:"result"`
	}
	if err := json.Unmarshal(signResponse, &signEnvelope); err != nil {
		t.Fatalf("Unmarshal(signResponse) failed: %v", err)
	}
	if !signEnvelope.OK {
		t.Fatalf("sign response was not ok: %s", signResponse)
	}
	signature := decodeBase64Cap(signEnvelope.Result.Base64)
	if !ed25519.Verify(expectedPublicKey, message, signature) {
		t.Fatalf("signature verification failed for generated signature")
	}

	verifyResponse, err := cryptoCapHandle(
		"crypto.ed25519.verify",
		[]byte(`{"message":"`+encodeBase64Cap(message)+`","signature":"`+signEnvelope.Result.Base64+`","publicKey":"`+publicKeyEnvelope.Result.Base64+`"}`),
	)
	if err != nil {
		t.Fatalf("crypto.ed25519.verify returned error: %v", err)
	}

	var verifyEnvelope struct {
		OK     bool `json:"ok"`
		Result bool `json:"result"`
	}
	if err := json.Unmarshal(verifyResponse, &verifyEnvelope); err != nil {
		t.Fatalf("Unmarshal(verifyResponse) failed: %v", err)
	}
	if !verifyEnvelope.OK {
		t.Fatalf("verify response was not ok: %s", verifyResponse)
	}
	if !verifyEnvelope.Result {
		t.Fatalf("verify result = false, want true")
	}
}

func TestCryptoCapHandlerSupportsSecp256k1SdkOperations(t *testing.T) {
	t.Parallel()

	privateKey := []byte{
		1, 2, 3, 4, 5, 6, 7, 8,
		9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24,
		25, 26, 27, 28, 29, 30, 31, 32,
	}
	message := []byte("orbpro licensing secp256k1 epm signature")
	expectedPub := secp256k1.PrivKeyFromBytes(privateKey).PubKey().SerializeCompressed()

	// crypto.secp256k1.publicKeyFromPrivate → 33-byte compressed pubkey.
	pubResponse, err := cryptoCapHandle(
		"crypto.secp256k1.publicKeyFromPrivate",
		[]byte(`{"privateKey":"`+encodeBase64Cap(privateKey)+`"}`),
	)
	if err != nil {
		t.Fatalf("crypto.secp256k1.publicKeyFromPrivate returned error: %v", err)
	}
	var pubEnvelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Base64 string `json:"base64"`
		} `json:"result"`
	}
	if err := json.Unmarshal(pubResponse, &pubEnvelope); err != nil {
		t.Fatalf("Unmarshal(pubResponse) failed: %v", err)
	}
	if !pubEnvelope.OK {
		t.Fatalf("public key response was not ok: %s", pubResponse)
	}
	publicKey := decodeBase64Cap(pubEnvelope.Result.Base64)
	if len(publicKey) != 33 {
		t.Fatalf("compressed public key length = %d, want 33", len(publicKey))
	}
	if !bytes.Equal(publicKey, expectedPub) {
		t.Fatalf("public key mismatch: got %x want %x", publicKey, expectedPub)
	}

	// crypto.secp256k1.sign → DER ECDSA signature over sha256(message).
	signResponse, err := cryptoCapHandle(
		"crypto.secp256k1.sign",
		[]byte(`{"message":"`+encodeBase64Cap(message)+`","privateKey":"`+encodeBase64Cap(privateKey)+`"}`),
	)
	if err != nil {
		t.Fatalf("crypto.secp256k1.sign returned error: %v", err)
	}
	var signEnvelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Base64 string `json:"base64"`
		} `json:"result"`
	}
	if err := json.Unmarshal(signResponse, &signEnvelope); err != nil {
		t.Fatalf("Unmarshal(signResponse) failed: %v", err)
	}
	if !signEnvelope.OK {
		t.Fatalf("sign response was not ok: %s", signResponse)
	}
	signature := decodeBase64Cap(signEnvelope.Result.Base64)

	// Independently confirm the signature parses as DER and verifies against the pubkey.
	parsedSig, err := ecdsa.ParseDERSignature(signature)
	if err != nil {
		t.Fatalf("signature is not valid DER: %v", err)
	}
	parsedPub, err := secp256k1.ParsePubKey(publicKey)
	if err != nil {
		t.Fatalf("ParsePubKey failed: %v", err)
	}
	digest := sha256.Sum256(message)
	if !parsedSig.Verify(digest[:], parsedPub) {
		t.Fatalf("independent secp256k1 verification failed")
	}

	// crypto.secp256k1.verify → { result: true } for the valid signature.
	verifyResponse, err := cryptoCapHandle(
		"crypto.secp256k1.verify",
		[]byte(`{"message":"`+encodeBase64Cap(message)+`","signature":"`+signEnvelope.Result.Base64+`","publicKey":"`+pubEnvelope.Result.Base64+`"}`),
	)
	if err != nil {
		t.Fatalf("crypto.secp256k1.verify returned error: %v", err)
	}
	var verifyEnvelope struct {
		OK     bool `json:"ok"`
		Result bool `json:"result"`
	}
	if err := json.Unmarshal(verifyResponse, &verifyEnvelope); err != nil {
		t.Fatalf("Unmarshal(verifyResponse) failed: %v", err)
	}
	if !verifyEnvelope.OK {
		t.Fatalf("verify response was not ok: %s", verifyResponse)
	}
	if !verifyEnvelope.Result {
		t.Fatalf("verify result = false, want true")
	}

	// Negative: tampered message must not verify.
	tampered := append([]byte{}, message...)
	tampered[0] ^= 0xFF
	negResponse, err := cryptoCapHandle(
		"crypto.secp256k1.verify",
		[]byte(`{"message":"`+encodeBase64Cap(tampered)+`","signature":"`+signEnvelope.Result.Base64+`","publicKey":"`+pubEnvelope.Result.Base64+`"}`),
	)
	if err != nil {
		t.Fatalf("crypto.secp256k1.verify (negative) returned error: %v", err)
	}
	var negEnvelope struct {
		OK     bool `json:"ok"`
		Result bool `json:"result"`
	}
	if err := json.Unmarshal(negResponse, &negEnvelope); err != nil {
		t.Fatalf("Unmarshal(negResponse) failed: %v", err)
	}
	if !negEnvelope.OK {
		t.Fatalf("negative verify response was not ok: %s", negResponse)
	}
	if negEnvelope.Result {
		t.Fatalf("tampered message verified true, want false")
	}

	// Generic crypto.sign/crypto.verify with algorithm=secp256k1 round-trips.
	genSignResponse, err := cryptoCapHandle(
		"crypto.sign",
		[]byte(`{"algorithm":"secp256k1","key":"`+encodeBase64Cap(privateKey)+`","data":"`+encodeBase64Cap(message)+`"}`),
	)
	if err != nil {
		t.Fatalf("crypto.sign (secp256k1) returned error: %v", err)
	}
	var genSignEnvelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Signature string `json:"signature"`
			Algorithm string `json:"algorithm"`
		} `json:"result"`
	}
	if err := json.Unmarshal(genSignResponse, &genSignEnvelope); err != nil {
		t.Fatalf("Unmarshal(genSignResponse) failed: %v", err)
	}
	if !genSignEnvelope.OK || genSignEnvelope.Result.Algorithm != "secp256k1" {
		t.Fatalf("generic secp256k1 sign response unexpected: %s", genSignResponse)
	}

	genVerifyResponse, err := cryptoCapHandle(
		"crypto.verify",
		[]byte(`{"algorithm":"secp256k1","key":"`+pubEnvelope.Result.Base64+`","data":"`+encodeBase64Cap(message)+`","signature":"`+genSignEnvelope.Result.Signature+`"}`),
	)
	if err != nil {
		t.Fatalf("crypto.verify (secp256k1) returned error: %v", err)
	}
	var genVerifyEnvelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Valid bool `json:"valid"`
		} `json:"result"`
	}
	if err := json.Unmarshal(genVerifyResponse, &genVerifyEnvelope); err != nil {
		t.Fatalf("Unmarshal(genVerifyResponse) failed: %v", err)
	}
	if !genVerifyEnvelope.OK || !genVerifyEnvelope.Result.Valid {
		t.Fatalf("generic secp256k1 verify failed: %s", genVerifyResponse)
	}
}
