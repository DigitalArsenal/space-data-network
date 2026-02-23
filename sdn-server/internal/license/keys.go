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
		// Verify file permissions are not world-readable.
		if info, serr := os.Stat(path); serr == nil {
			if perm := info.Mode().Perm(); perm&0077 != 0 {
				return nil, fmt.Errorf("key file %s has insecure permissions %o (expected 0600)", path, perm)
			}
		}
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
	key := ed25519.NewKeyFromSeed(seed)
	// Zero the seed material after deriving the key.
	for i := range seed {
		seed[i] = 0
	}
	return key, nil
}
