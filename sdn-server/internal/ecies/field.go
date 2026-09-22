package ecies

// Whole-field sealing: the same ECIES pipeline Wrap uses for a content key,
// applied to an arbitrary encrypted field of any SDS record. The caller writes
// the returned Header into the record's $ENC table and the ciphertext into the
// field; the derivation is keyed by that field's id, exactly as the
// flatbuffers EncryptVector path keys it, so every runtime opens it the same
// way.
//
//	Z          = ECDH(ephemeralPriv, recipientPub)
//	K          = HKDF-SHA256(Z, salt=∅, info=Context)
//	fieldKey   = HKDF-SHA256(K, info="flatbuffers-field"+be16(fieldID)+be32(0))
//	fieldIV    = HKDF-SHA256(K, info="flatbuffers-iv"+be16(fieldID)+be32(0))
//	ciphertext = AES-256-CTR(fieldKey, fieldIV) XOR plaintext
//
// AES-CTR carries no integrity: a record sealed this way must be signed over
// its ciphertext and header (encrypt-then-sign), as $RPC is.

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"

	enc "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ENC"
)

// Header is the content of an $ENC table for one sealed field.
type Header struct {
	KeyExchange    KeyExchange
	EphemeralPub   []byte
	NonceStart     []byte
	Context        string
	RecipientKeyID []byte
}

// HeaderFromENC reads a Header out of a decoded $ENC table.
func HeaderFromENC(e *enc.ENC) Header {
	return Header{
		KeyExchange:    e.KEY_EXCHANGE(),
		EphemeralPub:   append([]byte(nil), e.EPHEMERAL_PUBLIC_KEYBytes()...),
		NonceStart:     append([]byte(nil), e.NONCE_STARTBytes()...),
		Context:        string(e.CONTEXT()),
		RecipientKeyID: append([]byte(nil), e.RECIPIENT_KEY_IDBytes()...),
	}
}

// SealField encrypts plaintext for recipientPub as field fieldID (record 0).
// opts.Context must be set: it domain-separates what the field is for.
func SealField(recipientPub, plaintext []byte, fieldID uint16, opts WrapOptions) (Header, []byte, error) {
	if opts.Context == "" {
		return Header{}, nil, errors.New("ecies: SealField needs an explicit context")
	}
	ephPriv, ephPub, err := ephemeralKeypair(opts.KeyExchange, opts)
	if err != nil {
		return Header{}, nil, err
	}
	shared, err := ecdh(opts.KeyExchange, ephPriv, recipientPub)
	zero(ephPriv)
	if err != nil {
		return Header{}, nil, err
	}
	master := deriveMasterKey(shared, opts.Context)
	zero(shared)
	defer zero(master)

	ciphertext := append([]byte(nil), plaintext...)
	if err := fieldXOR(master, fieldID, ciphertext); err != nil {
		return Header{}, nil, err
	}
	nonceStart := make([]byte, 12)
	if _, err := io.ReadFull(opts.reader(), nonceStart); err != nil {
		return Header{}, nil, err
	}
	return Header{
		KeyExchange:    opts.KeyExchange,
		EphemeralPub:   ephPub,
		NonceStart:     nonceStart,
		Context:        opts.Context,
		RecipientKeyID: append([]byte(nil), opts.RecipientKeyID...),
	}, ciphertext, nil
}

// OpenField decrypts a field sealed by SealField. wantContext, when set, must
// equal the header's context, so a field sealed for one purpose is never
// opened as another.
func OpenField(recipientPriv []byte, h Header, ciphertext []byte, fieldID uint16, wantContext string) ([]byte, error) {
	if wantContext != "" && h.Context != wantContext {
		return nil, fmt.Errorf("ecies: sealed for %q, not %q", h.Context, wantContext)
	}
	if h.Context == "" {
		return nil, errors.New("ecies: sealed field has no context")
	}
	if len(h.EphemeralPub) == 0 {
		return nil, errors.New("ecies: sealed field has no ephemeral public key")
	}
	shared, err := ecdh(h.KeyExchange, recipientPriv, h.EphemeralPub)
	if err != nil {
		return nil, err
	}
	master := deriveMasterKey(shared, h.Context)
	zero(shared)
	defer zero(master)
	plaintext := append([]byte(nil), ciphertext...)
	if err := fieldXOR(master, fieldID, plaintext); err != nil {
		return nil, err
	}
	return plaintext, nil
}

func fieldXOR(master []byte, fieldID uint16, data []byte) error {
	fieldKey := deriveField(master, "flatbuffers-field", fieldID, 0, aesKeyBytes)
	fieldIV := deriveField(master, "flatbuffers-iv", fieldID, 0, ctrIVBytes)
	defer zero(fieldKey)
	block, err := aes.NewCipher(fieldKey)
	if err != nil {
		return fmt.Errorf("ecies: aes cipher: %w", err)
	}
	cipher.NewCTR(block, fieldIV).XORKeyStream(data, data)
	return nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
