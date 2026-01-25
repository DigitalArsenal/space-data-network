// Package ecies provides ECIES (Elliptic Curve Integrated Encryption Scheme)
// implementation for encrypted communication between SDN nodes.
//
// Supports multiple curve types:
// - X25519 (Curve25519) - Primary, fast
// - secp256k1 - Ethereum/Bitcoin compatible
// - P-256 (NIST) - HSM compatible
package ecies

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// CurveType represents the elliptic curve type for ECIES
type CurveType int

const (
	// CurveX25519 is the Curve25519 curve (default, fastest)
	CurveX25519 CurveType = iota
	// CurveSecp256k1 is the secp256k1 curve (Ethereum/Bitcoin compatible)
	CurveSecp256k1
	// CurveP256 is the NIST P-256 curve (HSM compatible)
	CurveP256
)

// EncryptedMessage represents an ECIES encrypted message
type EncryptedMessage struct {
	// EphemeralPublicKey is the ephemeral public key used for key exchange
	EphemeralPublicKey []byte
	// Ciphertext is the AES-GCM encrypted data
	Ciphertext []byte
	// Nonce is the AES-GCM nonce (12 bytes)
	Nonce []byte
	// MAC is the HMAC-SHA256 of the ciphertext
	MAC []byte
	// CurveType indicates which curve was used
	CurveType CurveType
}

// KeyPair represents an ECIES key pair
type KeyPair struct {
	PrivateKey []byte
	PublicKey  []byte
	CurveType  CurveType
}

// GenerateKeyPair generates a new ECIES key pair for the specified curve
func GenerateKeyPair(curveType CurveType) (*KeyPair, error) {
	switch curveType {
	case CurveX25519:
		return generateX25519KeyPair()
	case CurveSecp256k1:
		return generateSecp256k1KeyPair()
	case CurveP256:
		return generateP256KeyPair()
	default:
		return nil, errors.New("unsupported curve type")
	}
}

func generateX25519KeyPair() (*KeyPair, error) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate X25519 key: %w", err)
	}

	return &KeyPair{
		PrivateKey: privateKey.Bytes(),
		PublicKey:  privateKey.PublicKey().Bytes(),
		CurveType:  CurveX25519,
	}, nil
}

func generateSecp256k1KeyPair() (*KeyPair, error) {
	// secp256k1 uses the same structure as P-256 but with secp256k1 curve
	// For now, we'll use P-256 as a placeholder since Go doesn't have native secp256k1
	// In production, use github.com/decred/dcrd/dcrec/secp256k1/v4
	curve := elliptic.P256() // Placeholder - replace with secp256k1
	privateKey, x, y, err := elliptic.GenerateKey(curve, rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate secp256k1 key: %w", err)
	}

	publicKey := elliptic.MarshalCompressed(curve, x, y)

	return &KeyPair{
		PrivateKey: privateKey,
		PublicKey:  publicKey,
		CurveType:  CurveSecp256k1,
	}, nil
}

func generateP256KeyPair() (*KeyPair, error) {
	privateKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate P-256 key: %w", err)
	}

	return &KeyPair{
		PrivateKey: privateKey.Bytes(),
		PublicKey:  privateKey.PublicKey().Bytes(),
		CurveType:  CurveP256,
	}, nil
}

// Encrypt encrypts plaintext using ECIES with the recipient's public key
func Encrypt(recipientPublicKey []byte, plaintext []byte, curveType CurveType) (*EncryptedMessage, error) {
	switch curveType {
	case CurveX25519:
		return encryptX25519(recipientPublicKey, plaintext)
	case CurveSecp256k1:
		return encryptSecp256k1(recipientPublicKey, plaintext)
	case CurveP256:
		return encryptP256(recipientPublicKey, plaintext)
	default:
		return nil, errors.New("unsupported curve type")
	}
}

func encryptX25519(recipientPublicKey []byte, plaintext []byte) (*EncryptedMessage, error) {
	// Generate ephemeral key pair
	ephemeralPrivate, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ephemeral key: %w", err)
	}

	// Parse recipient public key
	recipientPubKey, err := ecdh.X25519().NewPublicKey(recipientPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid recipient public key: %w", err)
	}

	// Perform ECDH to get shared secret
	sharedSecret, err := ephemeralPrivate.ECDH(recipientPubKey)
	if err != nil {
		return nil, fmt.Errorf("ECDH failed: %w", err)
	}

	// Derive encryption and MAC keys using HKDF
	encKey, macKey, err := deriveKeys(sharedSecret, ephemeralPrivate.PublicKey().Bytes())
	if err != nil {
		return nil, fmt.Errorf("key derivation failed: %w", err)
	}

	// Encrypt with AES-GCM
	ciphertext, nonce, err := encryptAESGCM(encKey, plaintext)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM encryption failed: %w", err)
	}

	// Compute HMAC
	mac := computeHMAC(macKey, ciphertext)

	return &EncryptedMessage{
		EphemeralPublicKey: ephemeralPrivate.PublicKey().Bytes(),
		Ciphertext:         ciphertext,
		Nonce:              nonce,
		MAC:                mac,
		CurveType:          CurveX25519,
	}, nil
}

func encryptSecp256k1(recipientPublicKey []byte, plaintext []byte) (*EncryptedMessage, error) {
	curve := elliptic.P256() // Placeholder - replace with secp256k1

	// Parse recipient public key
	x, y := elliptic.UnmarshalCompressed(curve, recipientPublicKey)
	if x == nil {
		return nil, errors.New("invalid recipient public key")
	}

	// Generate ephemeral key pair
	ephemeralPrivate, ephX, ephY, err := elliptic.GenerateKey(curve, rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ephemeral key: %w", err)
	}
	ephemeralPublic := elliptic.MarshalCompressed(curve, ephX, ephY)

	// Perform ECDH to get shared secret
	sharedX, _ := curve.ScalarMult(x, y, ephemeralPrivate)
	sharedSecret := sharedX.Bytes()

	// Derive encryption and MAC keys
	encKey, macKey, err := deriveKeys(sharedSecret, ephemeralPublic)
	if err != nil {
		return nil, fmt.Errorf("key derivation failed: %w", err)
	}

	// Encrypt with AES-GCM
	ciphertext, nonce, err := encryptAESGCM(encKey, plaintext)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM encryption failed: %w", err)
	}

	// Compute HMAC
	mac := computeHMAC(macKey, ciphertext)

	return &EncryptedMessage{
		EphemeralPublicKey: ephemeralPublic,
		Ciphertext:         ciphertext,
		Nonce:              nonce,
		MAC:                mac,
		CurveType:          CurveSecp256k1,
	}, nil
}

func encryptP256(recipientPublicKey []byte, plaintext []byte) (*EncryptedMessage, error) {
	// Generate ephemeral key pair
	ephemeralPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ephemeral key: %w", err)
	}

	// Parse recipient public key
	recipientPubKey, err := ecdh.P256().NewPublicKey(recipientPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid recipient public key: %w", err)
	}

	// Perform ECDH to get shared secret
	sharedSecret, err := ephemeralPrivate.ECDH(recipientPubKey)
	if err != nil {
		return nil, fmt.Errorf("ECDH failed: %w", err)
	}

	// Derive encryption and MAC keys
	encKey, macKey, err := deriveKeys(sharedSecret, ephemeralPrivate.PublicKey().Bytes())
	if err != nil {
		return nil, fmt.Errorf("key derivation failed: %w", err)
	}

	// Encrypt with AES-GCM
	ciphertext, nonce, err := encryptAESGCM(encKey, plaintext)
	if err != nil {
		return nil, fmt.Errorf("AES-GCM encryption failed: %w", err)
	}

	// Compute HMAC
	mac := computeHMAC(macKey, ciphertext)

	return &EncryptedMessage{
		EphemeralPublicKey: ephemeralPrivate.PublicKey().Bytes(),
		Ciphertext:         ciphertext,
		Nonce:              nonce,
		MAC:                mac,
		CurveType:          CurveP256,
	}, nil
}

// Decrypt decrypts an ECIES encrypted message using the recipient's private key
func Decrypt(privateKey []byte, msg *EncryptedMessage) ([]byte, error) {
	switch msg.CurveType {
	case CurveX25519:
		return decryptX25519(privateKey, msg)
	case CurveSecp256k1:
		return decryptSecp256k1(privateKey, msg)
	case CurveP256:
		return decryptP256(privateKey, msg)
	default:
		return nil, errors.New("unsupported curve type")
	}
}

func decryptX25519(privateKeyBytes []byte, msg *EncryptedMessage) ([]byte, error) {
	// Parse private key
	privateKey, err := ecdh.X25519().NewPrivateKey(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	// Parse ephemeral public key
	ephemeralPubKey, err := ecdh.X25519().NewPublicKey(msg.EphemeralPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid ephemeral public key: %w", err)
	}

	// Perform ECDH to get shared secret
	sharedSecret, err := privateKey.ECDH(ephemeralPubKey)
	if err != nil {
		return nil, fmt.Errorf("ECDH failed: %w", err)
	}

	// Derive encryption and MAC keys
	encKey, macKey, err := deriveKeys(sharedSecret, msg.EphemeralPublicKey)
	if err != nil {
		return nil, fmt.Errorf("key derivation failed: %w", err)
	}

	// Verify HMAC
	expectedMAC := computeHMAC(macKey, msg.Ciphertext)
	if !hmac.Equal(msg.MAC, expectedMAC) {
		return nil, errors.New("MAC verification failed")
	}

	// Decrypt with AES-GCM
	return decryptAESGCM(encKey, msg.Ciphertext, msg.Nonce)
}

func decryptSecp256k1(privateKeyBytes []byte, msg *EncryptedMessage) ([]byte, error) {
	curve := elliptic.P256() // Placeholder - replace with secp256k1

	// Parse ephemeral public key
	x, y := elliptic.UnmarshalCompressed(curve, msg.EphemeralPublicKey)
	if x == nil {
		return nil, errors.New("invalid ephemeral public key")
	}

	// Perform ECDH to get shared secret
	sharedX, _ := curve.ScalarMult(x, y, privateKeyBytes)
	sharedSecret := sharedX.Bytes()

	// Derive encryption and MAC keys
	encKey, macKey, err := deriveKeys(sharedSecret, msg.EphemeralPublicKey)
	if err != nil {
		return nil, fmt.Errorf("key derivation failed: %w", err)
	}

	// Verify HMAC
	expectedMAC := computeHMAC(macKey, msg.Ciphertext)
	if !hmac.Equal(msg.MAC, expectedMAC) {
		return nil, errors.New("MAC verification failed")
	}

	// Decrypt with AES-GCM
	return decryptAESGCM(encKey, msg.Ciphertext, msg.Nonce)
}

func decryptP256(privateKeyBytes []byte, msg *EncryptedMessage) ([]byte, error) {
	// Parse private key
	privateKey, err := ecdh.P256().NewPrivateKey(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	// Parse ephemeral public key
	ephemeralPubKey, err := ecdh.P256().NewPublicKey(msg.EphemeralPublicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid ephemeral public key: %w", err)
	}

	// Perform ECDH to get shared secret
	sharedSecret, err := privateKey.ECDH(ephemeralPubKey)
	if err != nil {
		return nil, fmt.Errorf("ECDH failed: %w", err)
	}

	// Derive encryption and MAC keys
	encKey, macKey, err := deriveKeys(sharedSecret, msg.EphemeralPublicKey)
	if err != nil {
		return nil, fmt.Errorf("key derivation failed: %w", err)
	}

	// Verify HMAC
	expectedMAC := computeHMAC(macKey, msg.Ciphertext)
	if !hmac.Equal(msg.MAC, expectedMAC) {
		return nil, errors.New("MAC verification failed")
	}

	// Decrypt with AES-GCM
	return decryptAESGCM(encKey, msg.Ciphertext, msg.Nonce)
}

// deriveKeys derives encryption and MAC keys from shared secret using HKDF
func deriveKeys(sharedSecret, ephemeralPublicKey []byte) (encKey, macKey []byte, err error) {
	// Use HKDF with SHA-512 to derive keys
	// Info includes the ephemeral public key for domain separation
	info := append([]byte("ECIES-AES256-GCM-HMAC-SHA256"), ephemeralPublicKey...)

	hkdfReader := hkdf.New(sha512.New, sharedSecret, nil, info)

	// Derive 32-byte encryption key + 32-byte MAC key
	keys := make([]byte, 64)
	if _, err := io.ReadFull(hkdfReader, keys); err != nil {
		return nil, nil, fmt.Errorf("HKDF failed: %w", err)
	}

	return keys[:32], keys[32:], nil
}

// encryptAESGCM encrypts data using AES-256-GCM
func encryptAESGCM(key, plaintext []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext = aead.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// decryptAESGCM decrypts data using AES-256-GCM
func decryptAESGCM(key, ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return plaintext, nil
}

// computeHMAC computes HMAC-SHA256 of data
func computeHMAC(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// Serialize serializes an EncryptedMessage to bytes
func (m *EncryptedMessage) Serialize() []byte {
	// Format: [curve_type(1)] [epk_len(2)] [epk] [nonce_len(1)] [nonce] [mac_len(1)] [mac] [ciphertext]
	epkLen := len(m.EphemeralPublicKey)
	nonceLen := len(m.Nonce)
	macLen := len(m.MAC)
	ctLen := len(m.Ciphertext)

	result := make([]byte, 1+2+epkLen+1+nonceLen+1+macLen+ctLen)
	offset := 0

	// Curve type
	result[offset] = byte(m.CurveType)
	offset++

	// Ephemeral public key
	binary.BigEndian.PutUint16(result[offset:], uint16(epkLen))
	offset += 2
	copy(result[offset:], m.EphemeralPublicKey)
	offset += epkLen

	// Nonce
	result[offset] = byte(nonceLen)
	offset++
	copy(result[offset:], m.Nonce)
	offset += nonceLen

	// MAC
	result[offset] = byte(macLen)
	offset++
	copy(result[offset:], m.MAC)
	offset += macLen

	// Ciphertext (remaining bytes)
	copy(result[offset:], m.Ciphertext)

	return result
}

// DeserializeEncryptedMessage deserializes bytes to an EncryptedMessage
func DeserializeEncryptedMessage(data []byte) (*EncryptedMessage, error) {
	if len(data) < 6 {
		return nil, errors.New("data too short")
	}

	offset := 0

	// Curve type
	curveType := CurveType(data[offset])
	offset++

	// Ephemeral public key
	epkLen := int(binary.BigEndian.Uint16(data[offset:]))
	offset += 2
	if offset+epkLen > len(data) {
		return nil, errors.New("invalid ephemeral public key length")
	}
	epk := make([]byte, epkLen)
	copy(epk, data[offset:offset+epkLen])
	offset += epkLen

	// Nonce
	if offset >= len(data) {
		return nil, errors.New("missing nonce length")
	}
	nonceLen := int(data[offset])
	offset++
	if offset+nonceLen > len(data) {
		return nil, errors.New("invalid nonce length")
	}
	nonce := make([]byte, nonceLen)
	copy(nonce, data[offset:offset+nonceLen])
	offset += nonceLen

	// MAC
	if offset >= len(data) {
		return nil, errors.New("missing MAC length")
	}
	macLen := int(data[offset])
	offset++
	if offset+macLen > len(data) {
		return nil, errors.New("invalid MAC length")
	}
	mac := make([]byte, macLen)
	copy(mac, data[offset:offset+macLen])
	offset += macLen

	// Ciphertext (remaining bytes)
	ciphertext := make([]byte, len(data)-offset)
	copy(ciphertext, data[offset:])

	return &EncryptedMessage{
		EphemeralPublicKey: epk,
		Ciphertext:         ciphertext,
		Nonce:              nonce,
		MAC:                mac,
		CurveType:          curveType,
	}, nil
}

// PublicKeyFromPrivate derives the public key from a private key
func PublicKeyFromPrivate(privateKey []byte, curveType CurveType) ([]byte, error) {
	switch curveType {
	case CurveX25519:
		privKey, err := ecdh.X25519().NewPrivateKey(privateKey)
		if err != nil {
			return nil, err
		}
		return privKey.PublicKey().Bytes(), nil
	case CurveP256:
		privKey, err := ecdh.P256().NewPrivateKey(privateKey)
		if err != nil {
			return nil, err
		}
		return privKey.PublicKey().Bytes(), nil
	case CurveSecp256k1:
		curve := elliptic.P256() // Placeholder
		x, y := curve.ScalarBaseMult(privateKey)
		return elliptic.MarshalCompressed(curve, x, y), nil
	default:
		return nil, errors.New("unsupported curve type")
	}
}

// DeriveKeyFromWallet derives an encryption key from a Web3 wallet signature
// This allows wallet-based key derivation for encryption
func DeriveKeyFromWallet(signature []byte, curveType CurveType) (*KeyPair, error) {
	// Use HKDF to derive a key from the signature
	info := []byte("SDN-ECIES-KEY-DERIVATION")
	hkdfReader := hkdf.New(sha256.New, signature, nil, info)

	// Derive enough bytes for a private key
	var keyLen int
	switch curveType {
	case CurveX25519:
		keyLen = 32
	case CurveSecp256k1:
		keyLen = 32
	case CurveP256:
		keyLen = 32
	default:
		return nil, errors.New("unsupported curve type")
	}

	privateKey := make([]byte, keyLen)
	if _, err := io.ReadFull(hkdfReader, privateKey); err != nil {
		return nil, fmt.Errorf("key derivation failed: %w", err)
	}

	// Get public key
	publicKey, err := PublicKeyFromPrivate(privateKey, curveType)
	if err != nil {
		return nil, fmt.Errorf("failed to derive public key: %w", err)
	}

	return &KeyPair{
		PrivateKey: privateKey,
		PublicKey:  publicKey,
		CurveType:  curveType,
	}, nil
}

// SignMessage signs a message with ECDSA (for secp256k1 and P-256)
func SignMessage(privateKey []byte, message []byte, curveType CurveType) ([]byte, error) {
	if curveType == CurveX25519 {
		return nil, errors.New("X25519 does not support signing, use Ed25519 instead")
	}

	var curve elliptic.Curve
	switch curveType {
	case CurveSecp256k1:
		curve = elliptic.P256() // Placeholder
	case CurveP256:
		curve = elliptic.P256()
	default:
		return nil, errors.New("unsupported curve type for signing")
	}

	// Create ECDSA private key
	privKey := new(ecdsa.PrivateKey)
	privKey.Curve = curve
	privKey.D.SetBytes(privateKey)
	privKey.PublicKey.X, privKey.PublicKey.Y = curve.ScalarBaseMult(privateKey)

	// Hash the message
	hash := sha256.Sum256(message)

	// Sign
	r, s, err := ecdsa.Sign(rand.Reader, privKey, hash[:])
	if err != nil {
		return nil, fmt.Errorf("signing failed: %w", err)
	}

	// Serialize signature (r || s)
	signature := make([]byte, 64)
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(signature[32-len(rBytes):32], rBytes)
	copy(signature[64-len(sBytes):], sBytes)

	return signature, nil
}
