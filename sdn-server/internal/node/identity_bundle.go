package node

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// shortHash returns the first 8 characters of a hex hash for concise logging.
func shortHash(h string) string {
	if len(h) <= 8 {
		return h
	}
	return h[:8]
}

// IdentityBundle is the canonical mnemonic-backed node identity surface.
type IdentityBundle struct {
	Mnemonic          string
	Identity          *wasm.DerivedIdentity
	PeerID            peer.ID
	XPub              string
	BitcoinAddress    string
	BitcoinKeyPath    string
	IdentityKeyPath   string
	SigningKeyPath    string
	EncryptionKeyPath string

	// keyPassword is the same at-rest password used to encrypt the mnemonic
	// (see resolveKeyPassword). It is reused to encrypt any exported copies
	// of the derived libp2p identity private key (e.g. the managed Kubo repo
	// config written by EnsureManagedIPFSRepoIdentity) so those copies never
	// hit disk in plaintext either. Unexported: package-internal only.
	keyPassword string
}

func (n *Node) loadOrCreateIdentityBundle() (*IdentityBundle, error) {
	if n == nil || n.hdwallet == nil || n.config == nil {
		return nil, errors.New("hd wallet not available")
	}

	keyDir := filepath.Join(filepath.Dir(n.config.Storage.Path), "keys")
	mnemonicPath := filepath.Join(keyDir, "mnemonic")

	mnemonic, err := n.loadOrCreateMnemonic(mnemonicPath, keyDir)
	if err != nil {
		return nil, err
	}

	identity, err := n.hdwallet.IdentityFromMnemonic(n.ctx, mnemonic, "", 0)
	if err != nil {
		return nil, fmt.Errorf("derive identity from mnemonic: %w", err)
	}

	bundle := &IdentityBundle{
		Mnemonic:          mnemonic,
		Identity:          identity,
		PeerID:            identity.PeerID,
		IdentityKeyPath:   identity.IdentityKeyPath,
		SigningKeyPath:    identity.SigningKeyPath,
		EncryptionKeyPath: identity.EncryptionKeyPath,
		keyPassword:       n.resolveKeyPassword(),
	}
	if identity.Addresses != nil && identity.Addresses.Bitcoin != nil {
		bundle.BitcoinAddress = identity.Addresses.Bitcoin.Address
		bundle.BitcoinKeyPath = identity.Addresses.Bitcoin.Path
	}

	xpub, err := n.deriveIdentityBundleXPub(mnemonic)
	if err != nil {
		return nil, fmt.Errorf("derive identity bundle xpub: %w", err)
	}
	bundle.XPub = xpub

	if identity.IdentityPrivKey != nil {
		keyData, err := identity.MarshalPrivateKey()
		if err == nil {
			keyPath := filepath.Join(keyDir, "node.key")
			// The identity key must never touch disk unencrypted; a plaintext
			// write here would persist even though loadOrCreateKey overwrites
			// the file later (crash window + non-secure overwrite on COW/SSD).
			_ = n.writeEncryptedNodeKey(keyPath, keyData)
		}
	}

	return bundle, nil
}

func (n *Node) deriveIdentityBundleXPub(mnemonic string) (string, error) {
	if n == nil || n.hdwallet == nil {
		return "", errors.New("hd wallet not available")
	}
	seed, err := n.hdwallet.MnemonicToSeed(n.ctx, mnemonic, "")
	if err != nil {
		return "", err
	}
	return n.hdwallet.DeriveXPub(n.ctx, seed, 0)
}

func (n *Node) loadOrCreateMnemonic(mnemonicPath, keyDir string) (string, error) {
	keyPassword := n.resolveKeyPassword()

	// Surface a hostname change (canary only — not part of the key).
	if canary, cerr := keys.CheckAndUpdateHostnameCanary(keyDir); cerr != nil {
		log.Warnf("Unable to update hostname canary in %s: %v", keyDir, cerr)
	} else if canary.Changed {
		log.Warnf("SECURITY: machine hostname changed since last start (canary %s… -> %s…); "+
			"the at-rest key is hardware-derived so this does not affect decryption, but a rename "+
			"can indicate the disk was cloned or re-provisioned",
			shortHash(canary.PreviousHash), shortHash(canary.CurrentHash))
	}

	if data, err := os.ReadFile(mnemonicPath); err == nil {
		if keys.IsMnemonicEncrypted(data) {
			mnemonic, err := keys.DecryptMnemonic(data, keyPassword)
			if err != nil {
				// Migration path: a mnemonic encrypted under the pre-v2
				// hostname-based key. Decrypt with the legacy password and
				// re-encrypt with the hardware-derived key so the node keeps
				// its identity. Only attempt when no explicit password is set
				// (explicit password takes precedence and must match).
				if n.usingDerivedKeyPassword() {
					if legacy, lerr := keys.DecryptMnemonic(data, keys.DeriveLegacyPassword()); lerr == nil {
						log.Warnf("Migrating mnemonic at %s from legacy hostname-derived key to hardware-derived key", mnemonicPath)
						if reenc, eerr := keys.EncryptMnemonic(strings.TrimSpace(legacy), keyPassword); eerr == nil {
							if werr := os.WriteFile(mnemonicPath, reenc, 0o600); werr != nil {
								log.Warnf("Mnemonic migration re-encrypt write failed (continuing with decrypted value): %v", werr)
							} else {
								log.Infof("Mnemonic re-encrypted with hardware-derived key at %s", mnemonicPath)
							}
						}
						return strings.TrimSpace(legacy), nil
					}
				}
				return "", fmt.Errorf("failed to decrypt mnemonic from %s: %w", mnemonicPath, err)
			}
			log.Infof("Loaded encrypted mnemonic from %s", mnemonicPath)
			return strings.TrimSpace(mnemonic), nil
		}

		mnemonic := strings.TrimSpace(string(data))
		if mnemonic == "" {
			return "", fmt.Errorf("mnemonic file %s is empty", mnemonicPath)
		}
		log.Warnf("Found plaintext mnemonic at %s - migrating to encrypted storage", mnemonicPath)
		encrypted, err := keys.EncryptMnemonic(mnemonic, keyPassword)
		if err != nil {
			return "", fmt.Errorf("failed to encrypt mnemonic during migration: %w", err)
		}
		if err := os.WriteFile(mnemonicPath, encrypted, 0o600); err != nil {
			return "", fmt.Errorf("failed to write encrypted mnemonic: %w", err)
		}
		log.Infof("Mnemonic migrated to encrypted storage at %s", mnemonicPath)
		return mnemonic, nil
	}

	newMnemonic, _, err := n.hdwallet.GenerateNewIdentity(n.ctx, 24)
	if err != nil {
		return "", fmt.Errorf("generate mnemonic: %w", err)
	}

	encrypted, err := keys.EncryptMnemonic(newMnemonic, keyPassword)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt mnemonic: %w", err)
	}
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create key directory: %w", err)
	}
	if err := os.WriteFile(mnemonicPath, encrypted, 0o600); err != nil {
		return "", fmt.Errorf("failed to save encrypted mnemonic: %w", err)
	}
	log.Infof("Generated and saved encrypted mnemonic to %s", mnemonicPath)
	return strings.TrimSpace(newMnemonic), nil
}

func (b *IdentityBundle) privateKeyBytes() ([]byte, error) {
	if b == nil || b.Identity == nil || b.Identity.IdentityPrivKey == nil {
		return nil, errors.New("missing identity private key")
	}
	return crypto.MarshalPrivateKey(b.Identity.IdentityPrivKey)
}

// encryptedPrivateKeyForConfig returns the libp2p identity private key
// encrypted for storage in the managed Kubo repo config's Identity.PrivKey
// field (see ipfs_repo_identity.go). The returned string is safe to write to
// disk in plaintext form: it is an encryptedIdentityKeyMagic-prefixed,
// base64-encoded Argon2id + XChaCha20-Poly1305 envelope (internal/keys),
// never the raw key material.
func (b *IdentityBundle) encryptedPrivateKeyForConfig() (string, error) {
	raw, err := b.privateKeyBytes()
	if err != nil {
		return "", err
	}
	defer zeroBytes(raw)

	enc, err := keys.EncryptSecret(raw, b.keyPassword)
	if err != nil {
		return "", fmt.Errorf("encrypt identity private key: %w", err)
	}
	return encryptedIdentityKeyMagic + base64.StdEncoding.EncodeToString(enc), nil
}

// zeroBytes best-effort scrubs sensitive byte slices from memory once no
// longer needed. It is not a cryptographic guarantee (the Go runtime may have
// copied the backing array), but it costs nothing and narrows the window.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
