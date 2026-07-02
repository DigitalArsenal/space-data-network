// Package ecies implements the SDN unified cross-runtime ECIES envelope
// (docs/UNIFIED_ECIES.md): an SDS $ENC header + $KMF payload whose KEY_BYTES
// field carries the wrapped content key. The key-exchange step is
// parameterized by ENC.KEY_EXCHANGE (X25519 or Secp256k1); everything after
// the ECDH — HKDF-SHA256 key derivation and AES-256-CTR field encryption — is
// curve-agnostic and byte-for-byte identical to the C++/WASM path
// (flatbuffers/encryption.h DeriveSymmetricKey + EncryptVector, and
// licensing/core wrap_content_key_for_requester). This is the Go reference the
// other runtimes are validated against.
//
// Crypto pipeline (matches the deployed C++ constants):
//
//	Z = ECDH(ephemeralPriv, recipientPub)          // X25519 raw / secp256k1 raw-X (RFC 5903)
//	K = HKDF-SHA256(ikm=Z, salt=∅, info=CONTEXT)    // DeriveSymmetricKey, 32 bytes
//	fieldKey = HKDF-SHA256(ikm=K, salt=∅, info="flatbuffers-field"+be16(fid)+be32(rec))  // 32 bytes
//	fieldIV  = HKDF-SHA256(ikm=K, salt=∅, info="flatbuffers-iv"  +be16(fid)+be32(rec))   // 16 bytes
//	wrapped  = AES-256-CTR(fieldKey, fieldIV) XOR contentKey                              // field-level
//
// The KMF KEY_BYTES field id is 4 and its record index is 0 (the single
// key material record), matching kKmfKeyBytesFieldId in the C++ grant builder.
package ecies

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"crypto/sha256"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	enc "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ENC"
	kmf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	flatbuffers "github.com/google/flatbuffers/go"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// DefaultGrantContext is the module-delivery grant domain separator — the
// CONTEXT the C++ licensing/core wrap uses (kGrantPayloadContext). Callers for
// other purposes (channel chat, storefront) pass their own domain string.
const DefaultGrantContext = "space-data-network/module-delivery/grant/v1"

const (
	aesKeyBytes         = 32
	ctrIVBytes          = 16
	kmfKeyBytesFieldID  = 4
	kmfKeyBytesRecIndex = 0
	contentKeyBytes     = 32
)

// KeyExchange selects the ECDH curve; values match ENC.KeyExchange.
type KeyExchange = enc.KeyExchange

const (
	X25519    = enc.KeyExchangeX25519
	Secp256k1 = enc.KeyExchangeSecp256k1
)

// WrapOptions configures a wrap. Context defaults to DefaultGrantContext.
type WrapOptions struct {
	KeyExchange KeyExchange
	Context     string
	RecipientKeyID []byte
	// rand is injectable for deterministic conformance vectors; nil = crypto/rand.
	rand io.Reader
	// ephemeralPrivOverride pins the ephemeral private key for conformance
	// vectors; nil = fresh random. 32 bytes.
	ephemeralPrivOverride []byte
}

func (o WrapOptions) context() string {
	if o.Context == "" {
		return DefaultGrantContext
	}
	return o.Context
}

func (o WrapOptions) reader() io.Reader {
	if o.rand != nil {
		return o.rand
	}
	return rand.Reader
}

// deriveMasterKey = HKDF-SHA256(ikm=shared, salt=∅, info=context) → 32 bytes.
func deriveMasterKey(shared []byte, context string) []byte {
	out := make([]byte, aesKeyBytes)
	r := hkdf.New(sha256.New, shared, nil, []byte(context))
	_, _ = io.ReadFull(r, out)
	return out
}

// deriveField reproduces the flatbuffers EncryptVector field derivation:
// HKDF-SHA256(ikm=master, salt=∅, info=label+be16(fieldID)+be32(recordIndex)).
func deriveField(master []byte, label string, fieldID uint16, recordIndex uint32, outLen int) []byte {
	info := make([]byte, 0, len(label)+6)
	info = append(info, []byte(label)...)
	var fid [2]byte
	binary.BigEndian.PutUint16(fid[:], fieldID)
	info = append(info, fid[:]...)
	var rec [4]byte
	binary.BigEndian.PutUint32(rec[:], recordIndex)
	info = append(info, rec[:]...)
	out := make([]byte, outLen)
	r := hkdf.New(sha256.New, master, nil, info)
	_, _ = io.ReadFull(r, out)
	return out
}

// fieldCipherXOR applies AES-256-CTR(fieldKey, fieldIV) over data in place —
// the field-level symmetric transform (its own inverse).
func fieldCipherXOR(master []byte, data []byte) error {
	fieldKey := deriveField(master, "flatbuffers-field", kmfKeyBytesFieldID, kmfKeyBytesRecIndex, aesKeyBytes)
	fieldIV := deriveField(master, "flatbuffers-iv", kmfKeyBytesFieldID, kmfKeyBytesRecIndex, ctrIVBytes)
	block, err := aes.NewCipher(fieldKey)
	if err != nil {
		return fmt.Errorf("ecies: aes cipher: %w", err)
	}
	cipher.NewCTR(block, fieldIV).XORKeyStream(data, data)
	return nil
}

// ecdh computes the raw shared secret for the given curve.
//
//	X25519: RFC 7748 raw 32-byte shared secret.
//	secp256k1: the raw X coordinate (RFC 5903 §9), NOT hashed — decred
//	           GenerateSharedSecret already returns exactly this, matching
//	           CryptoPP ECDH<ECP>.Agree and WebCrypto deriveBits.
func ecdh(kx KeyExchange, priv, pub []byte) ([]byte, error) {
	switch kx {
	case X25519:
		if len(priv) != 32 || len(pub) != 32 {
			return nil, errors.New("ecies: x25519 keys must be 32 bytes")
		}
		return curve25519.X25519(priv, pub)
	case Secp256k1:
		if len(priv) != 32 {
			return nil, errors.New("ecies: secp256k1 private key must be 32 bytes")
		}
		sk := secp256k1.PrivKeyFromBytes(priv)
		pk, err := secp256k1.ParsePubKey(pub)
		if err != nil {
			return nil, fmt.Errorf("ecies: parse secp256k1 pub: %w", err)
		}
		shared := secp256k1.GenerateSharedSecret(sk, pk) // raw X, per RFC 5903 §9
		// Left-pad to 32 bytes (X can be shorter if it has leading zero bytes).
		if len(shared) < 32 {
			padded := make([]byte, 32)
			copy(padded[32-len(shared):], shared)
			shared = padded
		}
		return shared, nil
	default:
		return nil, fmt.Errorf("ecies: unsupported key exchange %v", kx)
	}
}

func ephemeralKeypair(kx KeyExchange, opts WrapOptions) (priv, pub []byte, err error) {
	switch kx {
	case X25519:
		priv = make([]byte, 32)
		if opts.ephemeralPrivOverride != nil {
			if len(opts.ephemeralPrivOverride) != 32 {
				return nil, nil, errors.New("ecies: x25519 ephemeral override must be 32 bytes")
			}
			copy(priv, opts.ephemeralPrivOverride)
		} else if _, err = io.ReadFull(opts.reader(), priv); err != nil {
			return nil, nil, err
		}
		pub, err = curve25519.X25519(priv, curve25519.Basepoint)
		return priv, pub, err
	case Secp256k1:
		var sk *secp256k1.PrivateKey
		if opts.ephemeralPrivOverride != nil {
			if len(opts.ephemeralPrivOverride) != 32 {
				return nil, nil, errors.New("ecies: secp256k1 ephemeral override must be 32 bytes")
			}
			sk = secp256k1.PrivKeyFromBytes(opts.ephemeralPrivOverride)
		} else {
			sk, err = secp256k1.GeneratePrivateKeyFromRand(opts.reader())
			if err != nil {
				return nil, nil, err
			}
		}
		return sk.Serialize(), sk.PubKey().SerializeCompressed(), nil
	default:
		return nil, nil, fmt.Errorf("ecies: unsupported key exchange %v", kx)
	}
}

// Wrap seals a 32-byte content key for the recipient, returning the standalone
// $ENC header bytes and the $KMF payload bytes (KEY_BYTES field-encrypted).
func Wrap(recipientPub, contentKey []byte, opts WrapOptions) (encBytes, kmfBytes []byte, err error) {
	if len(contentKey) != contentKeyBytes {
		return nil, nil, fmt.Errorf("ecies: content key must be %d bytes, got %d", contentKeyBytes, len(contentKey))
	}
	ephPriv, ephPub, err := ephemeralKeypair(opts.KeyExchange, opts)
	if err != nil {
		return nil, nil, err
	}
	shared, err := ecdh(opts.KeyExchange, ephPriv, recipientPub)
	if err != nil {
		return nil, nil, err
	}
	master := deriveMasterKey(shared, opts.context())

	wrappedKey := make([]byte, contentKeyBytes)
	copy(wrappedKey, contentKey)
	if err := fieldCipherXOR(master, wrappedKey); err != nil {
		return nil, nil, err
	}

	nonceStart := make([]byte, 12)
	if _, err := io.ReadFull(opts.reader(), nonceStart); err != nil {
		return nil, nil, err
	}
	return buildENC(opts, ephPub, nonceStart), buildKMF(wrappedKey), nil
}

// Unwrap recovers the content key from an $ENC header + $KMF payload using the
// recipient's private key.
func Unwrap(recipientPriv, encBytes, kmfBytes []byte, context string) ([]byte, error) {
	if !enc.ENCBufferHasIdentifier(encBytes) {
		return nil, errors.New("ecies: not an $ENC buffer")
	}
	if !kmf.KMFBufferHasIdentifier(kmfBytes) {
		return nil, errors.New("ecies: not a $KMF buffer")
	}
	header := enc.GetRootAsENC(encBytes, 0)
	kx := header.KEY_EXCHANGE()
	ephPub := header.EPHEMERAL_PUBLIC_KEYBytes()
	if len(ephPub) == 0 {
		return nil, errors.New("ecies: ENC missing ephemeral public key")
	}
	shared, err := ecdh(kx, recipientPriv, ephPub)
	if err != nil {
		return nil, err
	}
	ctx := context
	if ctx == "" {
		if c := header.CONTEXT(); len(c) > 0 {
			ctx = string(c)
		} else {
			ctx = DefaultGrantContext
		}
	}
	master := deriveMasterKey(shared, ctx)

	km := kmf.GetRootAsKMF(kmfBytes, 0)
	wrapped := km.KeyBytesBytes()
	if len(wrapped) != contentKeyBytes {
		return nil, fmt.Errorf("ecies: KMF KEY_BYTES must be %d bytes, got %d", contentKeyBytes, len(wrapped))
	}
	out := make([]byte, contentKeyBytes)
	copy(out, wrapped)
	if err := fieldCipherXOR(master, out); err != nil {
		return nil, err
	}
	return out, nil
}

func buildENC(opts WrapOptions, ephPub, nonceStart []byte) []byte {
	b := flatbuffers.NewBuilder(128)
	ephOff := b.CreateByteVector(ephPub)
	nonceOff := b.CreateByteVector(nonceStart)
	var ridOff flatbuffers.UOffsetT
	if len(opts.RecipientKeyID) > 0 {
		ridOff = b.CreateByteVector(opts.RecipientKeyID)
	}
	ctxOff := b.CreateString(opts.context())
	enc.ENCStart(b)
	enc.ENCAddVERSION(b, 1)
	enc.ENCAddKEY_EXCHANGE(b, opts.KeyExchange)
	enc.ENCAddSYMMETRIC(b, enc.SymmetricAlgoAES_256_CTR)
	enc.ENCAddKEY_DERIVATION(b, enc.KDFHKDF_SHA256)
	enc.ENCAddEPHEMERAL_PUBLIC_KEY(b, ephOff)
	enc.ENCAddNONCE_START(b, nonceOff)
	if ridOff != 0 {
		enc.ENCAddRECIPIENT_KEY_ID(b, ridOff)
	}
	enc.ENCAddCONTEXT(b, ctxOff)
	root := enc.ENCEnd(b)
	enc.FinishENCBuffer(b, root)
	return append([]byte(nil), b.FinishedBytes()...)
}

func buildKMF(wrappedKey []byte) []byte {
	b := flatbuffers.NewBuilder(128)
	keyOff := b.CreateByteVector(wrappedKey)
	kmf.KMFStart(b)
	kmf.KMFAddKEY_BYTES(b, keyOff)
	root := kmf.KMFEnd(b)
	kmf.FinishKMFBuffer(b, root)
	return append([]byte(nil), b.FinishedBytes()...)
}
