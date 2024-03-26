package crypto_utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

func Encrypt(data interface{}, key interface{}) ([]byte, error) {
	dataBytes, ok := data.([]byte)
	if !ok {
		dataStr, ok := data.(string)
		if !ok {
			return nil, fmt.Errorf("data must be a byte array or a string")
		}
		dataBytes = []byte(dataStr)
	}

	keyBytes, ok := key.([]byte)
	if !ok {
		keyStr, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("key must be a byte array or a string")
		}
		keyBytes = []byte(keyStr)
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, dataBytes, nil), nil
}

func Decrypt(data interface{}, key interface{}) ([]byte, error) {
	dataBytes, ok := data.([]byte)
	if !ok {
		dataStr, ok := data.(string)
		if !ok {
			return nil, fmt.Errorf("data must be a byte array or a string")
		}
		dataBytes = []byte(dataStr)
	}

	keyBytes, ok := key.([]byte)
	if !ok {
		keyStr, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("key must be a byte array or a string")
		}
		keyBytes = []byte(keyStr)
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	if len(dataBytes) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := dataBytes[:gcm.NonceSize()], dataBytes[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
