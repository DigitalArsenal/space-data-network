package caps

import (
	"bytes"
	"encoding/json"
	"testing"

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
