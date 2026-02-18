package wasm

import (
	"bytes"
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// HD wallet derivation constants — standard BIP-44 Bitcoin paths.
const (
	// DefaultCoinType is the BIP-44 coin type used for identity derivation.
	DefaultCoinType = 0

	// IdentityKeyPath is the BIP-32 secp256k1 derivation path for the node identity key.
	// This is the account-level key whose public key is encoded in the xpub.
	// Format: m/44'/0'/account'
	IdentityKeyPath = "m/44'/0'/%d'"

	// SigningKeyPath is the derivation path for Ed25519 signing keys (auth).
	// Format: m/44'/0'/account'/0'/0'
	SigningKeyPath = "m/44'/0'/%d'/0'/0'"

	// EncryptionKeyPath is the derivation path for X25519 encryption keys.
	// Format: m/44'/0'/account'/1'/0'
	EncryptionKeyPath = "m/44'/0'/%d'/1'/0'"
)

// DerivedIdentity represents a libp2p identity derived from an HD seed.
type DerivedIdentity struct {
	// Account is the BIP-44 account index used for derivation
	Account uint32

	// IdentityPrivKey is the secp256k1 private key for libp2p identity (PeerID)
	IdentityPrivKey crypto.PrivKey

	// IdentityPubKey is the secp256k1 public key for libp2p identity
	IdentityPubKey crypto.PubKey

	// SigningPrivKey is the Ed25519 private key for auth challenge-response signing
	SigningPrivKey crypto.PrivKey

	// SigningPubKey is the Ed25519 public key for auth verification
	SigningPubKey crypto.PubKey

	// EncryptionKey is the X25519 private key for encryption (32 bytes)
	EncryptionKey []byte

	// EncryptionPub is the X25519 public key (32 bytes)
	EncryptionPub []byte

	// PeerID is the libp2p peer ID derived from the secp256k1 identity key
	PeerID peer.ID

	// IdentityKeyPath is the derivation path for the secp256k1 identity key
	IdentityKeyPath string

	// SigningKeyPath is the derivation path used for the Ed25519 signing key
	SigningKeyPath string

	// EncryptionKeyPath is the derivation path used for the encryption key
	EncryptionKeyPath string

	// Addresses holds derived standard blockchain addresses (BTC, ETH, SOL)
	Addresses *CoinAddresses
}

// DeriveIdentity derives a libp2p identity from an HD wallet seed.
// The seed must be 64 bytes (from BIP-39 mnemonic).
// Account allows deriving multiple independent identities from the same seed.
//
// The libp2p PeerID is derived from a secp256k1 key at m/44'/0'/account'
// (the BIP-44 account level — same key the xpub represents). This gives a
// 1:1 mapping between xpub and PeerID.
//
// Ed25519 signing keys (for auth) and X25519 encryption keys are derived
// separately via SLIP-10.
func (hw *HDWalletModule) DeriveIdentity(ctx context.Context, seed []byte, account uint32) (*DerivedIdentity, error) {
	if len(seed) != 64 {
		return nil, ErrHDWalletInvalidSeed
	}

	// Derive paths
	identityPath := fmt.Sprintf(IdentityKeyPath, account)
	signingPath := fmt.Sprintf(SigningKeyPath, account)
	encryptionPath := fmt.Sprintf(EncryptionKeyPath, account)

	// Derive secp256k1 identity key at m/44'/0'/account'
	identityDerived, err := hw.DeriveSecp256k1Key(ctx, seed, identityPath)
	if err != nil {
		return nil, fmt.Errorf("failed to derive identity key: %w", err)
	}

	// Create libp2p secp256k1 private key from raw 32-byte key
	identityPrivKey, err := crypto.UnmarshalSecp256k1PrivateKey(identityDerived.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create libp2p secp256k1 key: %w", err)
	}
	identityPubKey := identityPrivKey.GetPublic()

	// Get peer ID from secp256k1 public key
	peerID, err := peer.IDFromPublicKey(identityPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create peer ID: %w", err)
	}

	// Derive Ed25519 signing key at m/44'/0'/account'/0'/0' (for auth)
	signingDerived, err := hw.DeriveEd25519Key(ctx, seed, signingPath)
	if err != nil {
		return nil, fmt.Errorf("failed to derive signing key: %w", err)
	}

	// Convert Ed25519 seed to libp2p crypto.PrivKey
	signingPrivKey, signingPubKey, err := crypto.GenerateEd25519Key(bytes.NewReader(signingDerived.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create libp2p Ed25519 key: %w", err)
	}

	// Derive X25519 encryption key at m/44'/0'/account'/1'/0'
	encryptionDerived, err := hw.DeriveEd25519Key(ctx, seed, encryptionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to derive encryption key: %w", err)
	}

	// Derive X25519 public key from the encryption private key
	encryptionPub, err := hw.X25519PublicKey(ctx, encryptionDerived.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive encryption public key: %w", err)
	}

	// Derive standard blockchain addresses (non-fatal if unavailable)
	coinAddrs, _ := hw.DeriveCoinAddresses(ctx, seed)

	return &DerivedIdentity{
		Account:           account,
		IdentityPrivKey:   identityPrivKey,
		IdentityPubKey:    identityPubKey,
		SigningPrivKey:     signingPrivKey,
		SigningPubKey:      signingPubKey,
		EncryptionKey:      encryptionDerived.PrivateKey,
		EncryptionPub:      encryptionPub,
		PeerID:             peerID,
		IdentityKeyPath:    identityPath,
		SigningKeyPath:     signingPath,
		EncryptionKeyPath:  encryptionPath,
		Addresses:          coinAddrs,
	}, nil
}

// DeriveMultipleIdentities derives multiple identities from the same seed.
// Useful for creating multiple peer identities for different purposes.
func (hw *HDWalletModule) DeriveMultipleIdentities(ctx context.Context, seed []byte, count uint32) ([]*DerivedIdentity, error) {
	identities := make([]*DerivedIdentity, count)
	for i := uint32(0); i < count; i++ {
		identity, err := hw.DeriveIdentity(ctx, seed, i)
		if err != nil {
			return nil, fmt.Errorf("failed to derive identity %d: %w", i, err)
		}
		identities[i] = identity
	}
	return identities, nil
}

// IdentityFromMnemonic creates a libp2p identity from a mnemonic phrase.
// This is a convenience function that combines seed derivation and identity creation.
func (hw *HDWalletModule) IdentityFromMnemonic(ctx context.Context, mnemonic, passphrase string, account uint32) (*DerivedIdentity, error) {
	// Validate mnemonic first
	valid, err := hw.ValidateMnemonic(ctx, mnemonic)
	if err != nil {
		return nil, fmt.Errorf("failed to validate mnemonic: %w", err)
	}
	if !valid {
		return nil, fmt.Errorf("invalid mnemonic phrase")
	}

	// Convert mnemonic to seed
	seed, err := hw.MnemonicToSeed(ctx, mnemonic, passphrase)
	if err != nil {
		return nil, fmt.Errorf("failed to derive seed: %w", err)
	}

	// Derive identity from seed
	return hw.DeriveIdentity(ctx, seed, account)
}

// Sign signs a message using the identity's Ed25519 signing key.
func (id *DerivedIdentity) Sign(message []byte) ([]byte, error) {
	return id.SigningPrivKey.Sign(message)
}

// Verify verifies a signature using the identity's Ed25519 public key.
func (id *DerivedIdentity) Verify(message, signature []byte) (bool, error) {
	return id.SigningPubKey.Verify(message, signature)
}

// RawSigningKey returns the raw 32-byte Ed25519 seed.
// Use with caution - this is sensitive key material.
func (id *DerivedIdentity) RawSigningKey() ([]byte, error) {
	raw, err := id.SigningPrivKey.Raw()
	if err != nil {
		return nil, err
	}
	// libp2p returns 64 bytes (seed + public key), we want just the seed
	if len(raw) == 64 {
		return raw[:32], nil
	}
	return raw, nil
}

// MarshalPrivateKey serializes the identity's secp256k1 identity key for storage.
// The result can be used with crypto.UnmarshalPrivateKey to restore the key.
func (id *DerivedIdentity) MarshalPrivateKey() ([]byte, error) {
	return crypto.MarshalPrivateKey(id.IdentityPrivKey)
}

// IdentityInfo holds non-sensitive identity information for display.
type IdentityInfo struct {
	Account           uint32
	PeerID            string
	IdentityPubKeyHex string
	SigningPubKeyHex  string
	EncryptionPubHex  string
	IdentityKeyPath   string
	SigningKeyPath    string
	EncryptionKeyPath string
	Addresses         *CoinAddresses
}

// Info returns non-sensitive identity information.
func (id *DerivedIdentity) Info() IdentityInfo {
	identityPubBytes, _ := id.IdentityPubKey.Raw()
	signingPubBytes, _ := id.SigningPubKey.Raw()
	return IdentityInfo{
		Account:           id.Account,
		PeerID:            id.PeerID.String(),
		IdentityPubKeyHex: fmt.Sprintf("%x", identityPubBytes),
		SigningPubKeyHex:  fmt.Sprintf("%x", signingPubBytes),
		EncryptionPubHex:  fmt.Sprintf("%x", id.EncryptionPub),
		IdentityKeyPath:   id.IdentityKeyPath,
		SigningKeyPath:     id.SigningKeyPath,
		EncryptionKeyPath:  id.EncryptionKeyPath,
		Addresses:          id.Addresses,
	}
}

// GenerateNewIdentity generates a new mnemonic and derives an identity.
// This is useful for first-time setup.
// Returns the mnemonic (for backup) and the derived identity.
func (hw *HDWalletModule) GenerateNewIdentity(ctx context.Context, wordCount int) (mnemonic string, identity *DerivedIdentity, err error) {
	// Generate new mnemonic
	mnemonic, err = hw.GenerateMnemonic(ctx, wordCount)
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate mnemonic: %w", err)
	}

	// Derive identity from mnemonic (no passphrase, account 0)
	identity, err = hw.IdentityFromMnemonic(ctx, mnemonic, "", 0)
	if err != nil {
		return "", nil, fmt.Errorf("failed to derive identity: %w", err)
	}

	return mnemonic, identity, nil
}

// RecoverIdentity recovers an identity from an existing mnemonic.
func (hw *HDWalletModule) RecoverIdentity(ctx context.Context, mnemonic, passphrase string) (*DerivedIdentity, error) {
	return hw.IdentityFromMnemonic(ctx, mnemonic, passphrase, 0)
}
