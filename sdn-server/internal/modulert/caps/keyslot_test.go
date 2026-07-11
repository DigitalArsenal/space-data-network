package caps

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"golang.org/x/crypto/ed25519"
	"golang.org/x/crypto/hkdf"
)

// throwawayEd25519Seed is a fixed, obviously-fake 32-byte Ed25519 seed used
// only inside this test file. It is never a real node/wallet key.
func throwawayEd25519Seed() []byte {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return seed
}

// throwawayX25519PrivateKey is a fixed, obviously-fake 32-byte X25519
// private key used only inside this test file.
func throwawayX25519PrivateKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(0xA0 + i)
	}
	// Clamp per X25519 convention so it's accepted as a valid scalar.
	key[0] &= 248
	key[31] &= 127
	key[31] |= 64
	return key
}

// assertResponseNeverContainsKeyMaterial scans a raw capability-response
// frame (what the guest would read out of WASM memory) for the private key
// bytes in both raw and base64-encoded form. This is the fail-closed
// assertion: even if some future edit reintroduces a leak, this test must
// fail loudly rather than merely checking the decoded JSON shape.
func assertResponseNeverContainsKeyMaterial(t *testing.T, response []byte, keyMaterial []byte) {
	t.Helper()

	if bytes.Contains(response, keyMaterial) {
		t.Fatalf("response frame contains raw key material: %x", response)
	}

	encoded := base64.StdEncoding.EncodeToString(keyMaterial)
	if bytes.Contains(response, []byte(encoded)) {
		t.Fatalf("response frame contains base64-encoded key material: %s", encoded)
	}

	rawEncoded := base64.RawStdEncoding.EncodeToString(keyMaterial)
	if bytes.Contains(response, []byte(rawEncoded)) {
		t.Fatalf("response frame contains unpadded base64-encoded key material: %s", rawEncoded)
	}
}

// TestKeyslotGetOperationIsGone asserts the raw-bytes export operation no
// longer exists as a distinct, working binding — a guest invoking
// "keyslot.get" must land in the generic unknown-operation branch, not any
// case that resolves a slot and returns its bytes.
func TestKeyslotGetOperationIsGone(t *testing.T) {
	seed := throwawayEd25519Seed()
	nodeCtx := &modulert.NodeContext{
		KeySlots: map[string][]byte{
			"provider-signing": seed,
		},
		KeySlotAlgorithms: map[string]string{
			"provider-signing": modulert.KeySlotAlgorithmEd25519,
		},
	}

	handler := newKeyslotCapHandler(nodeCtx)
	responseEnvelope, err := handler("keyslot.get", []byte(`{"slotId":"provider-signing"}`))
	if err != nil {
		t.Fatalf("handler returned Go error (want JSON error envelope): %v", err)
	}

	var response struct {
		Ok    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseEnvelope, &response); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	if response.Ok {
		t.Fatalf("keyslot.get must fail (unknown operation), got ok response: %s", responseEnvelope)
	}
	if response.Error.Message == "" || !bytes.Contains([]byte(response.Error.Message), []byte("unknown keyslot operation")) {
		t.Fatalf("expected an unknown-operation error, got: %q", response.Error.Message)
	}

	assertResponseNeverContainsKeyMaterial(t, responseEnvelope, seed)
}

// TestKeyslotSignReturnsSignatureNotKey exercises keyslot.sign with a
// throwaway Ed25519 seed and asserts (a) the returned signature verifies
// against the slot's public key and (b) the seed never appears anywhere in
// the response frame, raw or base64.
func TestKeyslotSignReturnsSignatureNotKey(t *testing.T) {
	seed := throwawayEd25519Seed()
	nodeCtx := &modulert.NodeContext{
		KeySlots: map[string][]byte{
			"provider-signing": seed,
		},
		KeySlotAlgorithms: map[string]string{
			"provider-signing": modulert.KeySlotAlgorithmEd25519,
		},
	}
	publicKey := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)

	handler := newKeyslotCapHandler(nodeCtx)
	payload := []byte("pnm-provenance-payload-to-sign")
	request, _ := json.Marshal(map[string]string{
		"slotId":  "provider-signing",
		"payload": base64.StdEncoding.EncodeToString(payload),
	})

	responseEnvelope, err := handler("keyslot.sign", request)
	if err != nil {
		t.Fatalf("keyslot.sign returned Go error: %v", err)
	}

	var response struct {
		Ok     bool `json:"ok"`
		Result struct {
			Signature string `json:"signature"`
			Algorithm string `json:"algorithm"`
		} `json:"result"`
	}
	if err := json.Unmarshal(responseEnvelope, &response); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	if !response.Ok {
		t.Fatalf("expected ok response, got: %s", responseEnvelope)
	}
	if response.Result.Algorithm != "ed25519" {
		t.Fatalf("algorithm = %q, want ed25519", response.Result.Algorithm)
	}

	signature, err := base64.StdEncoding.DecodeString(response.Result.Signature)
	if err != nil {
		t.Fatalf("decode signature base64: %v", err)
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		t.Fatalf("signature does not verify against the slot's public key")
	}
	if bytes.Equal(signature, seed) {
		t.Fatalf("signature must not equal the key material")
	}

	assertResponseNeverContainsKeyMaterial(t, responseEnvelope, seed)
}

// TestKeyslotSignSecp256k1ReturnsSignatureNotKey exercises the secp256k1
// branch of keyslot.sign with a throwaway private key.
func TestKeyslotSignSecp256k1ReturnsSignatureNotKey(t *testing.T) {
	priv := make([]byte, secp256k1PrivKeySize)
	for i := range priv {
		priv[i] = byte(200 - i)
	}
	nodeCtx := &modulert.NodeContext{
		KeySlots: map[string][]byte{
			"provider-secp": priv,
		},
		KeySlotAlgorithms: map[string]string{
			"provider-secp": modulert.KeySlotAlgorithmSecp256k1,
		},
	}
	pub := secp256k1.PrivKeyFromBytes(priv).PubKey()

	handler := newKeyslotCapHandler(nodeCtx)
	payload := []byte("secp256k1-signed-payload")
	request, _ := json.Marshal(map[string]string{
		"slotId":    "provider-secp",
		"payload":   base64.StdEncoding.EncodeToString(payload),
		"algorithm": "secp256k1",
	})

	responseEnvelope, err := handler("keyslot.sign", request)
	if err != nil {
		t.Fatalf("keyslot.sign returned Go error: %v", err)
	}

	var response struct {
		Ok     bool `json:"ok"`
		Result struct {
			Signature string `json:"signature"`
		} `json:"result"`
	}
	if err := json.Unmarshal(responseEnvelope, &response); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	if !response.Ok {
		t.Fatalf("expected ok response, got: %s", responseEnvelope)
	}

	sigBytes, err := base64.StdEncoding.DecodeString(response.Result.Signature)
	if err != nil {
		t.Fatalf("decode signature base64: %v", err)
	}
	sig, err := ecdsa.ParseDERSignature(sigBytes)
	if err != nil {
		t.Fatalf("parse DER signature: %v", err)
	}
	digest := sha256.Sum256(payload)
	if !sig.Verify(digest[:], pub) {
		t.Fatalf("signature does not verify against the slot's public key")
	}

	assertResponseNeverContainsKeyMaterial(t, responseEnvelope, priv)
}

// TestKeyslotUnwrapRoundTrips builds a wrapped payload the way a sender
// would (ephemeral X25519 keypair -> ECDH with the slot's public key ->
// HKDF-SHA256 -> AES-256-GCM seal), then asserts keyslot.unwrap recovers the
// plaintext and the slot's private key never appears in the response frame.
func TestKeyslotUnwrapRoundTrips(t *testing.T) {
	slotPriv := throwawayX25519PrivateKey()
	nodeCtx := &modulert.NodeContext{
		KeySlots: map[string][]byte{
			"provider-wrapping": slotPriv,
		},
		KeySlotAlgorithms: map[string]string{
			"provider-wrapping": modulert.KeySlotAlgorithmX25519,
		},
	}

	curve := ecdh.X25519()
	slotPrivKey, err := curve.NewPrivateKey(slotPriv)
	if err != nil {
		t.Fatalf("NewPrivateKey(slotPriv): %v", err)
	}
	slotPubKey := slotPrivKey.PublicKey()

	ephemeralPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(ephemeral): %v", err)
	}
	shared, err := ephemeralPriv.ECDH(slotPubKey)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}

	aesKey := make([]byte, 32)
	kdf := hkdf.New(sha256.New, shared, nil, []byte(keyslotUnwrapHKDFInfo))
	if _, err := io.ReadFull(kdf, aesKey); err != nil {
		t.Fatalf("hkdf: %v", err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatalf("nonce: %v", err)
	}

	plaintext := []byte("licensing-content-key-material-that-is-legitimately-guest-visible")
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	request, _ := json.Marshal(map[string]string{
		"slotId":             "provider-wrapping",
		"ephemeralPublicKey": base64.StdEncoding.EncodeToString(ephemeralPriv.PublicKey().Bytes()),
		"nonce":              base64.StdEncoding.EncodeToString(nonce),
		"ciphertext":         base64.StdEncoding.EncodeToString(ciphertext),
	})

	handler := newKeyslotCapHandler(nodeCtx)
	responseEnvelope, err := handler("keyslot.unwrap", request)
	if err != nil {
		t.Fatalf("keyslot.unwrap returned Go error: %v", err)
	}

	var response struct {
		Ok     bool `json:"ok"`
		Result struct {
			Plaintext string `json:"plaintext"`
		} `json:"result"`
	}
	if err := json.Unmarshal(responseEnvelope, &response); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	if !response.Ok {
		t.Fatalf("expected ok response, got: %s", responseEnvelope)
	}

	decoded, err := base64.StdEncoding.DecodeString(response.Result.Plaintext)
	if err != nil {
		t.Fatalf("decode plaintext base64: %v", err)
	}
	if !bytes.Equal(decoded, plaintext) {
		t.Fatalf("unwrap round-trip mismatch: got %q, want %q", decoded, plaintext)
	}

	assertResponseNeverContainsKeyMaterial(t, responseEnvelope, slotPriv)
}

// TestKeyslotUnwrapWrongSlotFailsClosed asserts a payload wrapped to one
// slot's public key cannot be unwrapped by referencing a different slot —
// GCM authentication must reject it, and no key material of either slot
// leaks in the (error) response.
func TestKeyslotUnwrapWrongSlotFailsClosed(t *testing.T) {
	slotPriv := throwawayX25519PrivateKey()
	otherSlotPriv := make([]byte, 32)
	for i := range otherSlotPriv {
		otherSlotPriv[i] = byte(0x50 + i)
	}
	otherSlotPriv[0] &= 248
	otherSlotPriv[31] &= 127
	otherSlotPriv[31] |= 64

	nodeCtx := &modulert.NodeContext{
		KeySlots: map[string][]byte{
			"provider-wrapping": slotPriv,
			"other-slot":        otherSlotPriv,
		},
		KeySlotAlgorithms: map[string]string{
			"provider-wrapping": modulert.KeySlotAlgorithmX25519,
			"other-slot":        modulert.KeySlotAlgorithmX25519,
		},
	}

	curve := ecdh.X25519()
	slotPrivKey, _ := curve.NewPrivateKey(slotPriv)
	ephemeralPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(ephemeral): %v", err)
	}
	shared, err := ephemeralPriv.ECDH(slotPrivKey.PublicKey())
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	aesKey := make([]byte, 32)
	kdf := hkdf.New(sha256.New, shared, nil, []byte(keyslotUnwrapHKDFInfo))
	io.ReadFull(kdf, aesKey)
	block, _ := aes.NewCipher(aesKey)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	io.ReadFull(rand.Reader, nonce)
	ciphertext := gcm.Seal(nil, nonce, []byte("secret"), nil)

	request, _ := json.Marshal(map[string]string{
		"slotId":             "other-slot", // wrong slot on purpose
		"ephemeralPublicKey": base64.StdEncoding.EncodeToString(ephemeralPriv.PublicKey().Bytes()),
		"nonce":              base64.StdEncoding.EncodeToString(nonce),
		"ciphertext":         base64.StdEncoding.EncodeToString(ciphertext),
	})

	handler := newKeyslotCapHandler(nodeCtx)
	responseEnvelope, err := handler("keyslot.unwrap", request)
	if err != nil {
		t.Fatalf("keyslot.unwrap returned Go error: %v", err)
	}

	var response struct {
		Ok bool `json:"ok"`
	}
	if err := json.Unmarshal(responseEnvelope, &response); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	if response.Ok {
		t.Fatalf("unwrap with the wrong slot must fail closed, got ok response: %s", responseEnvelope)
	}

	assertResponseNeverContainsKeyMaterial(t, responseEnvelope, slotPriv)
	assertResponseNeverContainsKeyMaterial(t, responseEnvelope, otherSlotPriv)
}

// decodeCapErrorEnvelope decodes a capability response envelope and asserts
// it is an error, returning the error message for content checks.
func decodeCapErrorEnvelope(t *testing.T, responseEnvelope []byte) string {
	t.Helper()
	var response struct {
		Ok    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseEnvelope, &response); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	if response.Ok {
		t.Fatalf("expected error envelope, got ok response: %s", responseEnvelope)
	}
	return response.Error.Message
}

// TestKeyslotCrossProtocolUseIsDenied is the loop B9.5 acceptance test: a
// 32-byte slot is bit-compatible with both an Ed25519 seed and an X25519
// scalar, so the oracle must refuse to drive a slot through any algorithm
// other than the one it was declared for. Each denial must happen before any
// key operation runs and must leak no key material.
func TestKeyslotCrossProtocolUseIsDenied(t *testing.T) {
	signingSeed := throwawayEd25519Seed()
	wrappingKey := throwawayX25519PrivateKey()
	nodeCtx := &modulert.NodeContext{
		KeySlots: map[string][]byte{
			"provider-signing":  signingSeed,
			"provider-wrapping": wrappingKey,
		},
		KeySlotAlgorithms: map[string]string{
			"provider-signing":  modulert.KeySlotAlgorithmEd25519,
			"provider-wrapping": modulert.KeySlotAlgorithmX25519,
		},
	}
	handler := newKeyslotCapHandler(nodeCtx)

	// keyslot.unwrap against the Ed25519 signing slot: the exact B9.5
	// scenario (signing seed used as an X25519 scalar). The request is
	// otherwise well-formed so only the algorithm gate can reject it.
	curve := ecdh.X25519()
	ephemeralPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(ephemeral): %v", err)
	}
	unwrapRequest, _ := json.Marshal(map[string]string{
		"slotId":             "provider-signing",
		"ephemeralPublicKey": base64.StdEncoding.EncodeToString(ephemeralPriv.PublicKey().Bytes()),
		"nonce":              base64.StdEncoding.EncodeToString(make([]byte, 12)),
		"ciphertext":         base64.StdEncoding.EncodeToString([]byte("opaque-ciphertext")),
	})
	responseEnvelope, err := handler("keyslot.unwrap", unwrapRequest)
	if err != nil {
		t.Fatalf("keyslot.unwrap returned Go error: %v", err)
	}
	message := decodeCapErrorEnvelope(t, responseEnvelope)
	if !strings.Contains(message, "not declared for algorithm") {
		t.Fatalf("unwrap of an ed25519-declared slot must be an algorithm denial, got: %q", message)
	}
	assertResponseNeverContainsKeyMaterial(t, responseEnvelope, signingSeed)

	// keyslot.sign against the X25519 wrapping slot (both algorithms).
	for _, algo := range []string{"", "secp256k1"} {
		signRequest, _ := json.Marshal(map[string]string{
			"slotId":    "provider-wrapping",
			"payload":   base64.StdEncoding.EncodeToString([]byte("data")),
			"algorithm": algo,
		})
		responseEnvelope, err := handler("keyslot.sign", signRequest)
		if err != nil {
			t.Fatalf("keyslot.sign returned Go error: %v", err)
		}
		message := decodeCapErrorEnvelope(t, responseEnvelope)
		if !strings.Contains(message, "not declared for algorithm") {
			t.Fatalf("sign with an x25519-declared slot (algo %q) must be an algorithm denial, got: %q", algo, message)
		}
		assertResponseNeverContainsKeyMaterial(t, responseEnvelope, wrappingKey)
	}

	// keyslot.sign with the signing slot but the wrong sign algorithm: the
	// declaration binds one algorithm, not the operation family.
	signRequest, _ := json.Marshal(map[string]string{
		"slotId":    "provider-signing",
		"payload":   base64.StdEncoding.EncodeToString([]byte("data")),
		"algorithm": "secp256k1",
	})
	responseEnvelope, err = handler("keyslot.sign", signRequest)
	if err != nil {
		t.Fatalf("keyslot.sign returned Go error: %v", err)
	}
	message = decodeCapErrorEnvelope(t, responseEnvelope)
	if !strings.Contains(message, "not declared for algorithm") {
		t.Fatalf("secp256k1 sign with an ed25519-declared slot must be an algorithm denial, got: %q", message)
	}
	assertResponseNeverContainsKeyMaterial(t, responseEnvelope, signingSeed)
}

// TestKeyslotUndeclaredSlotFailsClosed asserts a slot present in KeySlots but
// absent from KeySlotAlgorithms is unusable by every operation — a provider
// that forgets the declaration gets a dead slot, never an unrestricted one.
func TestKeyslotUndeclaredSlotFailsClosed(t *testing.T) {
	seed := throwawayEd25519Seed()
	nodeCtx := &modulert.NodeContext{
		KeySlots: map[string][]byte{
			"undeclared-slot": seed,
		},
	}
	handler := newKeyslotCapHandler(nodeCtx)

	signRequest, _ := json.Marshal(map[string]string{
		"slotId":  "undeclared-slot",
		"payload": base64.StdEncoding.EncodeToString([]byte("data")),
	})
	responseEnvelope, err := handler("keyslot.sign", signRequest)
	if err != nil {
		t.Fatalf("keyslot.sign returned Go error: %v", err)
	}
	message := decodeCapErrorEnvelope(t, responseEnvelope)
	if !strings.Contains(message, "no declared algorithm") {
		t.Fatalf("sign with an undeclared slot must fail closed on the declaration, got: %q", message)
	}
	assertResponseNeverContainsKeyMaterial(t, responseEnvelope, seed)

	curve := ecdh.X25519()
	ephemeralPriv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(ephemeral): %v", err)
	}
	unwrapRequest, _ := json.Marshal(map[string]string{
		"slotId":             "undeclared-slot",
		"ephemeralPublicKey": base64.StdEncoding.EncodeToString(ephemeralPriv.PublicKey().Bytes()),
		"nonce":              base64.StdEncoding.EncodeToString(make([]byte, 12)),
		"ciphertext":         base64.StdEncoding.EncodeToString([]byte("opaque-ciphertext")),
	})
	responseEnvelope, err = handler("keyslot.unwrap", unwrapRequest)
	if err != nil {
		t.Fatalf("keyslot.unwrap returned Go error: %v", err)
	}
	message = decodeCapErrorEnvelope(t, responseEnvelope)
	if !strings.Contains(message, "no declared algorithm") {
		t.Fatalf("unwrap with an undeclared slot must fail closed on the declaration, got: %q", message)
	}
	assertResponseNeverContainsKeyMaterial(t, responseEnvelope, seed)
}

// TestKeyslotSignMissingSlotFailsClosed asserts an unknown slot id is a
// clean error, not a panic or a zero-value key operation.
func TestKeyslotSignMissingSlotFailsClosed(t *testing.T) {
	nodeCtx := &modulert.NodeContext{KeySlots: map[string][]byte{}}
	handler := newKeyslotCapHandler(nodeCtx)

	request, _ := json.Marshal(map[string]string{
		"slotId":  "does-not-exist",
		"payload": base64.StdEncoding.EncodeToString([]byte("data")),
	})
	responseEnvelope, err := handler("keyslot.sign", request)
	if err != nil {
		t.Fatalf("keyslot.sign returned Go error: %v", err)
	}
	var response struct {
		Ok bool `json:"ok"`
	}
	if err := json.Unmarshal(responseEnvelope, &response); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	if response.Ok {
		t.Fatalf("expected failure for missing slot, got ok response: %s", responseEnvelope)
	}
}

// TestKeyslotNilNodeContextFailsClosed asserts a nil node context (module
// invoked outside a node, e.g. a unit test harness) never panics and never
// leaks anything.
func TestKeyslotNilNodeContextFailsClosed(t *testing.T) {
	handler := newKeyslotCapHandler(nil)

	for _, op := range []string{"keyslot.get", "keyslot.sign", "keyslot.unwrap"} {
		responseEnvelope, err := handler(op, []byte(`{"slotId":"provider-signing"}`))
		if err != nil {
			t.Fatalf("%s returned Go error: %v", op, err)
		}
		var response struct {
			Ok bool `json:"ok"`
		}
		if jsonErr := json.Unmarshal(responseEnvelope, &response); jsonErr != nil {
			t.Fatalf("decode response envelope: %v", jsonErr)
		}
		if response.Ok {
			t.Fatalf("%s with nil node context must fail closed, got ok response: %s", op, responseEnvelope)
		}
	}
}
