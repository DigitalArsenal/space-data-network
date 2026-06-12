package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

const flatBuffersEncryptedStreamFieldID = uint16(0)

type FlatBuffersEncryptedNativeStreamDecryptor struct {
	recipientPrivateKey []byte
}

func NewFlatBuffersEncryptedNativeStreamDecryptor(recipientPrivateKey []byte) *FlatBuffersEncryptedNativeStreamDecryptor {
	return &FlatBuffersEncryptedNativeStreamDecryptor{
		recipientPrivateKey: append([]byte(nil), recipientPrivateKey...),
	}
}

func (d *FlatBuffersEncryptedNativeStreamDecryptor) DecryptNativeStream(req EncryptedNativeStreamDecryptRequest) ([]byte, error) {
	if d == nil || len(d.recipientPrivateKey) != 32 {
		return nil, fmt.Errorf("recipient X25519 private key is unavailable")
	}
	if !strings.EqualFold(strings.TrimSpace(req.Header.Algorithm), "x25519") {
		return nil, fmt.Errorf("unsupported encrypted stream algorithm %q", req.Header.Algorithm)
	}
	if strings.TrimSpace(req.Header.Context) == "" {
		return nil, fmt.Errorf("encrypted stream context is required")
	}
	senderPublicKey, err := hex.DecodeString(strings.TrimSpace(req.Header.SenderPublicKey))
	if err != nil || len(senderPublicKey) != 32 {
		return nil, fmt.Errorf("decode sender X25519 public key: %w", err)
	}
	nonceStart, err := hex.DecodeString(strings.TrimSpace(req.Header.NonceStart))
	if err != nil || len(nonceStart) != 12 {
		return nil, fmt.Errorf("decode nonce start: %w", err)
	}
	sharedSecret, err := curve25519.X25519(d.recipientPrivateKey, senderPublicKey)
	if err != nil {
		return nil, fmt.Errorf("X25519 shared secret: %w", err)
	}
	defer zeroSensitiveBytes(sharedSecret)

	sessionKey, err := hkdfSHA256(sharedSecret, nil, []byte(req.Header.Context), 32)
	if err != nil {
		return nil, fmt.Errorf("derive encrypted stream session key: %w", err)
	}
	defer zeroSensitiveBytes(sessionKey)

	fieldKey, err := flatBuffersFieldKey(sessionKey, flatBuffersEncryptedStreamFieldID, uint32(req.RecordIndex))
	if err != nil {
		return nil, err
	}
	defer zeroSensitiveBytes(fieldKey)
	fieldIV, err := flatBuffersFieldIV(sessionKey, flatBuffersEncryptedStreamFieldID, uint32(req.RecordIndex))
	if err != nil {
		return nil, err
	}
	defer zeroSensitiveBytes(fieldIV)

	block, err := aes.NewCipher(fieldKey)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	plaintext := append([]byte(nil), req.Ciphertext...)
	cipher.NewCTR(block, fieldIV).XORKeyStream(plaintext, plaintext)
	return plaintext, nil
}

func flatBuffersFieldKey(sessionKey []byte, fieldID uint16, recordIndex uint32) ([]byte, error) {
	info := make([]byte, 23)
	copy(info, []byte("flatbuffers-field"))
	info[17] = byte(fieldID >> 8)
	info[18] = byte(fieldID)
	info[19] = byte(recordIndex >> 24)
	info[20] = byte(recordIndex >> 16)
	info[21] = byte(recordIndex >> 8)
	info[22] = byte(recordIndex)
	return hkdfSHA256(sessionKey, nil, info, 32)
}

func flatBuffersFieldIV(sessionKey []byte, fieldID uint16, recordIndex uint32) ([]byte, error) {
	info := make([]byte, 20)
	copy(info, []byte("flatbuffers-iv"))
	info[14] = byte(fieldID >> 8)
	info[15] = byte(fieldID)
	info[16] = byte(recordIndex >> 24)
	info[17] = byte(recordIndex >> 16)
	info[18] = byte(recordIndex >> 8)
	info[19] = byte(recordIndex)
	return hkdfSHA256(sessionKey, nil, info, aes.BlockSize)
}

func hkdfSHA256(secret []byte, salt []byte, info []byte, length int) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("invalid HKDF output length %d", length)
	}
	out := make([]byte, length)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, salt, info), out); err != nil {
		return nil, err
	}
	return out, nil
}

func zeroSensitiveBytes(bytes []byte) {
	for i := range bytes {
		bytes[i] = 0
	}
}
