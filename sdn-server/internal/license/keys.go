package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

func loadOrCreateEd25519Key(path string) (ed25519.PrivateKey, error) {
	if data, err := os.ReadFile(path); err == nil {
		switch len(data) {
		case ed25519.SeedSize:
			return ed25519.NewKeyFromSeed(data), nil
		case ed25519.PrivateKeySize:
			return ed25519.PrivateKey(data), nil
		default:
			return nil, fmt.Errorf("invalid key length %d at %s", len(data), path)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}

	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("generate key seed: %w", err)
	}
	if err := os.WriteFile(path, seed, 0600); err != nil {
		return nil, fmt.Errorf("write key seed: %w", err)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
