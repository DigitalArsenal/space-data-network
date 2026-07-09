package caps

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"golang.org/x/crypto/ed25519"
	"golang.org/x/crypto/hkdf"
)

// keyslotUnwrapHKDFInfo domain-separates the HKDF step of keyslot.unwrap from
// every other HKDF use in this codebase (e.g. crypto.kdf). Changing this
// string is a breaking change for any previously wrapped payload.
const keyslotUnwrapHKDFInfo = "sdn-server/keyslot.unwrap/v1"

// NewKeyslotCapFactory returns a CapFactory for the manifest-level "wallet_sign"
// capability. The current plugin manifest schema has no dedicated keyslot
// capability, so keyslot host operations are gated behind wallet_sign until
// the manifest schema is standardized upstream.
//
// This capability is a host-side crypto oracle: it never returns the slot's
// private key material to the guest. Guests get the *outputs* of private-key
// operations (a signature, or the plaintext that was wrapped to a slot's
// public key) — never the key itself. See keyslot.sign and keyslot.unwrap.
func NewKeyslotCapFactory() modulert.CapFactory {
	return func(mod *modulert.Module) modulert.CapHandler {
		if mod == nil {
			return newKeyslotCapHandler(nil)
		}
		return newKeyslotCapHandler(mod.NodeContext())
	}
}

func newKeyslotCapHandler(nodeCtx *modulert.NodeContext) modulert.CapHandler {
	return func(operation string, payload []byte) ([]byte, error) {
		switch operation {
		case "keyslot.sign":
			return handleKeyslotSign(nodeCtx, payload), nil
		case "keyslot.unwrap":
			return handleKeyslotUnwrap(nodeCtx, payload), nil
		default:
			// Deliberately includes the removed "keyslot.get" raw-export
			// operation: it must resolve here, as an unknown operation, not
			// as a distinct (deprecated-but-working) branch. Never add a
			// case that returns slot key bytes to the caller.
			return errCapJSON(fmt.Sprintf("unknown keyslot operation: %s", operation)), nil
		}
	}
}

// resolveKeySlot looks up a slot's private key material. The returned slice
// aliases the host-side slot map — callers MUST NOT return it, or any
// encoding of it, to the guest. Only derived outputs (signatures, decrypted
// plaintext of a wrapped payload) may cross the host/guest boundary.
func resolveKeySlot(nodeCtx *modulert.NodeContext, slotID string) ([]byte, error) {
	if nodeCtx == nil {
		return nil, fmt.Errorf("keyslot context is not available")
	}
	slotID = strings.TrimSpace(slotID)
	if slotID == "" {
		return nil, fmt.Errorf("missing slotId")
	}
	keySlots := nodeCtx.KeySlots
	if len(keySlots) == 0 {
		return nil, fmt.Errorf("keyslot map is not configured")
	}
	raw, ok := keySlots[slotID]
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("keyslot not found")
	}
	return raw, nil
}

// handleKeyslotSign performs a host-side signature over a guest-supplied
// payload using the named slot's private key. Request:
//
//	{"slotId":"...", "payload":"base64", "algorithm":"ed25519|secp256k1"}
//
// algorithm defaults to "ed25519" (the node identity / provider signing slot
// algorithm). Response: {"signature":"base64","algorithm":"..."}. The slot's
// key material never appears in the response.
func handleKeyslotSign(nodeCtx *modulert.NodeContext, payload []byte) []byte {
	var request struct {
		SlotID    string `json:"slotId"`
		Payload   string `json:"payload"`
		Algorithm string `json:"algorithm"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		return errCapJSON("invalid keyslot.sign request payload: " + err.Error())
	}

	slotKey, err := resolveKeySlot(nodeCtx, request.SlotID)
	if err != nil {
		return errCapJSON(err.Error())
	}

	data := decodeBase64Cap(request.Payload)

	algo := strings.TrimSpace(request.Algorithm)
	if algo == "" {
		algo = "ed25519"
	}

	switch algo {
	case "ed25519":
		if len(slotKey) != ed25519.SeedSize {
			return errCapJSON(fmt.Sprintf("keyslot %q is not a %d-byte ed25519 seed", request.SlotID, ed25519.SeedSize))
		}
		signature := ed25519.Sign(ed25519.NewKeyFromSeed(slotKey), data)
		return okCapJSON(map[string]interface{}{
			"signature": encodeBase64Cap(signature),
			"algorithm": "ed25519",
		})

	case "secp256k1":
		if len(slotKey) != secp256k1PrivKeySize {
			return errCapJSON(fmt.Sprintf("keyslot %q is not a %d-byte secp256k1 private key", request.SlotID, secp256k1PrivKeySize))
		}
		digest := sha256.Sum256(data)
		sig := ecdsa.Sign(secp256k1.PrivKeyFromBytes(slotKey), digest[:])
		return okCapJSON(map[string]interface{}{
			"signature": encodeBase64Cap(sig.Serialize()),
			"algorithm": "secp256k1",
		})

	default:
		return errCapJSON(fmt.Sprintf("unsupported keyslot.sign algorithm: %s", algo))
	}
}

// handleKeyslotUnwrap decrypts a payload that was sealed to a slot's X25519
// public key, and returns only the decrypted plaintext (e.g. a licensing
// content key) — never the slot's private key. The wrap scheme is
// ephemeral-sender ECIES: X25519(slotPrivateKey, ephemeralPublicKey) -> HKDF-SHA256
// -> AES-256-GCM. Request:
//
//	{"slotId":"...", "ephemeralPublicKey":"base64", "nonce":"base64", "ciphertext":"base64"}
//
// Response: {"plaintext":"base64"}.
func handleKeyslotUnwrap(nodeCtx *modulert.NodeContext, payload []byte) []byte {
	var request struct {
		SlotID             string `json:"slotId"`
		EphemeralPublicKey string `json:"ephemeralPublicKey"`
		Nonce              string `json:"nonce"`
		Ciphertext         string `json:"ciphertext"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		return errCapJSON("invalid keyslot.unwrap request payload: " + err.Error())
	}

	slotKey, err := resolveKeySlot(nodeCtx, request.SlotID)
	if err != nil {
		return errCapJSON(err.Error())
	}
	if len(slotKey) != 32 {
		return errCapJSON(fmt.Sprintf("keyslot %q is not a 32-byte x25519 private key", request.SlotID))
	}

	ephemeralPub := decodeBase64Cap(request.EphemeralPublicKey)
	nonce := decodeBase64Cap(request.Nonce)
	ciphertext := decodeBase64Cap(request.Ciphertext)
	if len(ephemeralPub) == 0 || len(ciphertext) == 0 {
		return errCapJSON("keyslot.unwrap requires ephemeralPublicKey and ciphertext")
	}

	curve := ecdh.X25519()
	priv, err := curve.NewPrivateKey(slotKey)
	if err != nil {
		return errCapJSON("invalid keyslot x25519 key: " + err.Error())
	}
	pub, err := curve.NewPublicKey(ephemeralPub)
	if err != nil {
		return errCapJSON("invalid ephemeral public key: " + err.Error())
	}
	shared, err := priv.ECDH(pub)
	if err != nil {
		return errCapJSON("ECDH failed: " + err.Error())
	}

	aesKey := make([]byte, 32)
	kdf := hkdf.New(sha256.New, shared, nil, []byte(keyslotUnwrapHKDFInfo))
	if _, err := io.ReadFull(kdf, aesKey); err != nil {
		return errCapJSON("key derivation failed: " + err.Error())
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return errCapJSON("invalid derived key: " + err.Error())
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return errCapJSON("gcm init failed: " + err.Error())
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return errCapJSON("unwrap failed: " + err.Error())
	}
	return okCapJSON(map[string]interface{}{
		"plaintext": encodeBase64Cap(plaintext),
	})
}
