// Envelope (G2) adds a per-peer encryption layer on top of the existing
// cleartext update carrier (carrier.go). It does not change the carrier
// format itself: BuildCarrier/ExtractBundleFromCarrier are reused unchanged,
// so an EncryptedBundle's Carrier field is just an ordinary carrier whose
// custom-section payload happens to be ciphertext instead of a plaintext
// bundle. That keeps the cleartext receive path (Stage ->
// ExtractBundleFromCarrier) working for unencrypted carriers without any
// branching on the caller's part.
package update

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spacedatanetwork/sdn-server/internal/deliveryclient"
	"github.com/spacedatanetwork/sdn-server/internal/ecies"
)

// EnvelopeSchemaV1 identifies the G2 per-peer encrypted-bundle envelope
// format.
const EnvelopeSchemaV1 = "org.spacedatanetwork.update.envelope.v1"

// envelopeContentKeyBytes is the AES-256 content-key length shared by
// ecies.WrapForRecipients and deliveryclient's AES-GCM bundle format.
const envelopeContentKeyBytes = 32

// envelopeIVBytes matches deliveryclient's module-delivery bundle format
// ([12-byte iv][ciphertext||16-byte gcm tag]).
const envelopeIVBytes = 12

// ErrEnvelopeNotForRecipient is returned when none of an EncryptedBundle's
// SealedEnvelope rows match the requested recipient key id. This is the
// "clean error, not garbage" outcome for a non-recipient: no ECIES unwrap or
// AES-GCM decrypt is even attempted.
var ErrEnvelopeNotForRecipient = errors.New("update: no envelope in this bundle matches the requested recipient key id")

// EnvelopeRecipient is one wrap target for EncryptCarrierForRecipients: a
// recipient's ECIES encryption public key (see internal/ecies.Recipient)
// plus an opaque KeyID the recipient can recognize as its own when
// selecting its envelope out of a multi-recipient set (e.g. a hash or
// fingerprint of the recipient's own encryption pubkey — callers choose the
// scheme; this package only requires it be stable and unique per recipient).
type EnvelopeRecipient struct {
	KeyID       []byte
	PublicKey   []byte
	KeyExchange ecies.KeyExchange
}

// SealedEnvelope is one recipient's wrapped-content-key row, carried
// alongside the (now ciphertext) update carrier. ENC/KMF are the standard
// SDN unified-ECIES $ENC header + $KMF payload (internal/ecies); Unwrap(
// recipientPriv, ENC, KMF, ctx) recovers the shared content key.
type SealedEnvelope struct {
	KeyID []byte `json:"key_id,omitempty"`
	ENC   []byte `json:"enc"`
	KMF   []byte `json:"kmf"`
}

// EncryptedBundle is the G2 per-peer encryption envelope: an update
// carrier's bundle payload encrypted once under a random content key
// (AES-256-GCM, the same [iv][ciphertext||tag] format
// deliveryclient.AESGCMBundleDecryptor already decodes), plus one
// SealedEnvelope per recipient wrapping that content key
// (ecies.WrapForRecipients). Carrier is an ordinary BuildCarrier module
// whose custom-section payload is the ciphertext.
//
// Recipient-set scoping (seam, not solved here): the update feed/pubsub
// announcement path is one-to-many broadcast, but sealing here always
// targets a concrete, enumerated Recipients list. The natural default is
// "every node subscribed to the update topic at seal time" (the caller
// supplies that roster), but a narrower cohort (a channel's members, a
// staged rollout percentage, a single targeted peer) is equally expressible
// — it is just a different EnvelopeRecipient slice passed in by the caller,
// nothing in this type constrains it either way. What this package does NOT
// solve is fleet rekey: if the recipient set changes after a bundle is
// sealed (a peer joins or is revoked), the bundle must be resealed with a
// fresh content key and a fresh recipient list — there is no in-place
// add/revoke-a-recipient operation, and revocation of an already-delivered
// content key is not tracked at all. That is a follow-up design, not a gap
// this primitive attempts to close.
type EncryptedBundle struct {
	Schema    string           `json:"schema"`
	Context   string           `json:"context,omitempty"`
	Envelopes []SealedEnvelope `json:"envelopes"`
	Carrier   []byte           `json:"carrier"`
}

// EncryptCarrierForRecipients is the G2 encrypt-once/wrap-per-recipient
// primitive. It AES-256-GCM-encrypts bundleBytes under a fresh random
// 32-byte content key (deliveryclient.EncryptBundleAESGCM — the same
// on-disk format the module-delivery lane already uses, so this
// interoperates with any AES-GCM-aware consumer by construction), wraps
// that content key independently for each recipient via
// ecies.WrapForRecipients (each recipient gets its own fresh ephemeral key;
// recipients may mix X25519/secp256k1), and returns an EncryptedBundle
// whose Carrier is an ordinary BuildCarrier wasm module carrying the
// ciphertext.
func EncryptCarrierForRecipients(bundleBytes []byte, recipients []EnvelopeRecipient, ctx string) (*EncryptedBundle, error) {
	if len(bundleBytes) == 0 {
		return nil, errors.New("update: bundle bytes are required")
	}
	if len(recipients) == 0 {
		return nil, errors.New("update: at least one recipient is required")
	}

	contentKey := make([]byte, envelopeContentKeyBytes)
	if _, err := io.ReadFull(rand.Reader, contentKey); err != nil {
		return nil, fmt.Errorf("update: generate content key: %w", err)
	}
	iv := make([]byte, envelopeIVBytes)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, fmt.Errorf("update: generate iv: %w", err)
	}
	ciphertext, err := deliveryclient.EncryptBundleAESGCM(contentKey, iv, bundleBytes, nil)
	if err != nil {
		return nil, fmt.Errorf("update: encrypt bundle: %w", err)
	}

	eciesRecipients := make([]ecies.Recipient, len(recipients))
	for i, r := range recipients {
		if len(r.PublicKey) == 0 {
			return nil, fmt.Errorf("update: recipient %d has no public key", i)
		}
		eciesRecipients[i] = ecies.Recipient{
			PublicKey:   r.PublicKey,
			KeyExchange: r.KeyExchange,
			KeyID:       r.KeyID,
		}
	}
	wrapped, err := ecies.WrapForRecipients(contentKey, eciesRecipients, ctx)
	if err != nil {
		return nil, fmt.Errorf("update: wrap content key for recipients: %w", err)
	}
	envelopes := make([]SealedEnvelope, len(wrapped))
	for i, w := range wrapped {
		envelopes[i] = SealedEnvelope{KeyID: w.KeyID, ENC: w.ENC, KMF: w.KMF}
	}

	return &EncryptedBundle{
		Schema:    EnvelopeSchemaV1,
		Context:   ctx,
		Envelopes: envelopes,
		Carrier:   BuildCarrier(ciphertext),
	}, nil
}

// Marshal serializes the envelope for transport/storage (e.g. as the
// delivery payload alongside the signed manifest).
func (e *EncryptedBundle) Marshal() ([]byte, error) {
	return json.Marshal(e)
}

// ParseEncryptedBundle parses and shape-checks a serialized EncryptedBundle.
func ParseEncryptedBundle(raw []byte) (*EncryptedBundle, error) {
	var env EncryptedBundle
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("update: parse encrypted bundle envelope: %w", err)
	}
	if env.Schema != EnvelopeSchemaV1 {
		return nil, fmt.Errorf("update: unsupported encrypted bundle envelope schema: %s", env.Schema)
	}
	if len(env.Envelopes) == 0 {
		return nil, errors.New("update: encrypted bundle envelope has no recipients")
	}
	if len(env.Carrier) == 0 {
		return nil, errors.New("update: encrypted bundle envelope has no carrier payload")
	}
	return &env, nil
}

// selectEnvelope finds this recipient's SealedEnvelope by KeyID.
func (e *EncryptedBundle) selectEnvelope(keyID []byte) (*SealedEnvelope, error) {
	for i := range e.Envelopes {
		if bytes.Equal(e.Envelopes[i].KeyID, keyID) {
			return &e.Envelopes[i], nil
		}
	}
	return nil, ErrEnvelopeNotForRecipient
}

// DecryptCarrierForRecipient is the receive-side G2 counterpart: it selects
// this node's SealedEnvelope (matched by keyID; ErrEnvelopeNotForRecipient
// if none matches — no unwrap/decrypt is attempted for a non-recipient),
// ecies.Unwraps the shared content key with recipientPriv, extracts the
// carrier's ciphertext bundle payload (ExtractBundleFromCarrier, unchanged),
// AES-256-GCM-decrypts it, and returns a fresh plaintext carrier
// (BuildCarrier of the decrypted bundle bytes) — a drop-in input for the
// existing cleartext receive path (Stage ultimately calls
// ExtractBundleFromCarrier on whatever carrier bytes it is given).
func DecryptCarrierForRecipient(env *EncryptedBundle, keyID, recipientPriv []byte) ([]byte, error) {
	if env == nil {
		return nil, errors.New("update: nil encrypted bundle envelope")
	}
	sealed, err := env.selectEnvelope(keyID)
	if err != nil {
		return nil, err
	}
	contentKey, err := ecies.Unwrap(recipientPriv, sealed.ENC, sealed.KMF, env.Context)
	if err != nil {
		return nil, fmt.Errorf("update: unwrap content key: %w", err)
	}
	ciphertext, err := ExtractBundleFromCarrier(env.Carrier)
	if err != nil {
		return nil, fmt.Errorf("update: extract encrypted carrier payload: %w", err)
	}
	decryptor := deliveryclient.AESGCMBundleDecryptor{}
	plaintext, err := decryptor.Decrypt(ciphertext, contentKey)
	if err != nil {
		return nil, fmt.Errorf("update: decrypt bundle: %w", err)
	}
	return BuildCarrier(plaintext), nil
}
