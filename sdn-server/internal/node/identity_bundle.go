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
			_ = os.WriteFile(keyPath, keyData, 0o600)
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

	if data, err := os.ReadFile(mnemonicPath); err == nil {
		if keys.IsMnemonicEncrypted(data) {
			mnemonic, err := keys.DecryptMnemonic(data, keyPassword)
			if err != nil {
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

func (b *IdentityBundle) base64PrivateKey() (string, error) {
	raw, err := b.privateKeyBytes()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
