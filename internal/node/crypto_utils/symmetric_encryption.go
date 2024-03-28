package crypto_utils

import (
	"crypto/rand"
	"fmt"
	"log"

	config "github.com/DigitalArsenal/space-data-network/configs"
	"golang.org/x/crypto/chacha20poly1305"
)

func EncryptPrivateKey(key []byte) []byte {
	aead, err := chacha20poly1305.NewX([]byte(config.Conf.Datastore.Password))
	if err != nil {
		log.Fatalf("Failed to create encryption cipher: %v", err)
	}

	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		log.Fatalf("Failed to generate nonce: %v", err)
	}

	return aead.Seal(nonce, nonce, key, nil)
}

func DecryptPrivateKey(key []byte) []byte {
	aead, err := chacha20poly1305.NewX([]byte(config.Conf.Datastore.Password))
	if err != nil {
		log.Fatalf("Failed to create decryption cipher: %v", err)
	}
	nonce, ciphertext := key[:chacha20poly1305.NonceSizeX], key[chacha20poly1305.NonceSizeX:]
	decrypted, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		log.Fatalf("Failed to decrypt private key: %v", err)
	}
	fmt.Printf("Decrypted: %s\n", decrypted)
	return decrypted
}
