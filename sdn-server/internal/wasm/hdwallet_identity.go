package wasm

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
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

	// LicensingGrantKeyPath is the derivation path for the Ed25519 key that signs
	// LICENSING GRANTS (module-delivery). Format: m/44'/0'/account'/2'/0'.
	//
	// It is the FIRST consumer of the general purpose-key contract in
	// hdwallet_purpose.go and must stay equal to
	// PurposeKeyPath(PurposeLicensingGrant, account) — locked by test. New lanes
	// should use DeriveChildForPurpose rather than adding a constant here.
	//
	// OWNER RULING 2026-08-07, verbatim: "derive a grant-signing child from the
	// node identity, keep the update root isolated"
	// (graph/tasks/sdn-grant-verifier-key-domain-separation.md).
	//
	// WHY IT IS ITS OWN PATH. Until this constant existed the grant lane signed
	// with the key at SigningKeyPath — the SAME key that is the fleet update trust
	// root (fleet-trust-roots.json key_id d4a971a7e534) and the module publisher of
	// record. Those two duties have opposite risk profiles: the update root is
	// fleet-wide CODE AUTHORITY, exercised rarely, maximum blast radius; a grant
	// signature is issued to every anonymous browser, constantly, and is worth one
	// module download. Sharing a key made any compromise or forced rotation of the
	// high-volume path a compromise of fleet code authority.
	//
	// WHY THIS SHAPE. It is the next purpose index in the grammar this node already
	// uses at the change level: 0' = signing/auth (SigningKeyPath), 1' = encryption
	// (EncryptionKeyPath), 2' = licensing grants. Fully hardened, so it is a valid
	// SLIP-0010 Ed25519 path (SLIP-0010 has no non-hardened Ed25519 derivation) and
	// so publishing the child's PUBLIC key — which the grant lane MUST do, it is the
	// verifier key every client checks against — discloses nothing about the parent
	// or any sibling. That property is what makes the ruling safe to ship.
	//
	// The private half is never persisted: it is derived at identity-derivation time
	// from the seed already in hand and lives only in memory, exactly like
	// SigningPrivKey and EncryptionKey (machine-bound key-at-rest law).
	LicensingGrantKeyPath = "m/44'/0'/%d'/2'/0'"

	// LegacyAuthKeyPath is the derivation path hd-wallet-ui's LEGACY identity
	// schemes (sdn-bip39-auth-v1-legacy, sdn-fast-password-auth-v1-legacy) use
	// for their sdn-authentication key. Format: m/44'/0'/account'/0/0 — note
	// the last two components are NON-hardened, unlike SigningKeyPath.
	//
	// This is NOT a path this node derives for its own use. It exists solely
	// so the node can RECOGNISE its own root account when an operator signs in
	// through hd-wallet-ui: those legacy schemes are the only ones that can
	// produce the raw-32 Ed25519 signature the admit point verifies today (see
	// §11.2 of graph/tasks/nst-node-admin-contract.md), and they derive their
	// auth key here rather than at SigningKeyPath.
	LegacyAuthKeyPath = "m/44'/0'/%d'/0/0"
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

	// GrantSigningPrivKey is the Ed25519 private key that signs LICENSING GRANTS.
	// It is a hardened child of the same seed at LicensingGrantKeyPath and is
	// deliberately NOT SigningPrivKey — see LicensingGrantKeyPath's doc and
	// graph/tasks/sdn-grant-verifier-key-domain-separation.md.
	GrantSigningPrivKey crypto.PrivKey

	// GrantSigningPubKey is the Ed25519 public key clients verify grants against.
	// Its bytes are what the host publishes in KRF.PUBLIC_KEY and what the key
	// server stamps into every grant as GRANT_VERIFIER_PUBKEY.
	GrantSigningPubKey crypto.PubKey

	// PeerID is the libp2p peer ID derived from the secp256k1 identity key
	PeerID peer.ID

	// IdentityKeyPath is the derivation path for the secp256k1 identity key
	IdentityKeyPath string

	// SigningKeyPath is the derivation path used for the Ed25519 signing key
	SigningKeyPath string

	// EncryptionKeyPath is the derivation path used for the encryption key
	EncryptionKeyPath string

	// GrantSigningKeyPath is the derivation path used for the licensing grant
	// signing key (LicensingGrantKeyPath rendered for this account).
	GrantSigningKeyPath string

	// BitcoinKeyPath is the derivation path used for the Bitcoin signing key
	BitcoinKeyPath string

	// BitcoinPrivateKey is the secp256k1 private key for Bitcoin signing (32 bytes)
	BitcoinPrivateKey []byte

	// EthereumKeyPath is the derivation path used for the Ethereum signing key
	EthereumKeyPath string

	// EthereumPrivateKey is the secp256k1 private key for Ethereum signing (32 bytes)
	EthereumPrivateKey []byte

	// SolanaKeyPath is the derivation path used for the Solana signing key
	SolanaKeyPath string

	// SolanaPrivateKey is the ed25519 private key for Solana signing (32 bytes)
	SolanaPrivateKey []byte

	// Addresses holds derived standard blockchain addresses (BTC, ETH, SOL)
	Addresses *CoinAddresses
}

// DeriveLegacyAuthPublicKey derives the Ed25519 public key that hd-wallet-ui's
// LEGACY identity schemes present as their sdn-authentication key for this
// seed and account.
//
// The scheme, verified empirically against hd-wallet-wasm 2.0.28 (probe run
// 2026-07-27, both account 0 and account 1): derive the secp256k1 BIP-32 node
// at LegacyAuthKeyPath, then use the resulting 32-byte private SCALAR directly
// as an Ed25519 seed. hd-wallet-wasm labels this derivation
// "bip32-scalar-as-ed25519-seed". It is deliberately different from
// DeriveIdentity's SigningKeyPath key, which is SLIP-10 over a fully hardened
// path — for the same mnemonic the two keys DIFFER.
//
// Only the PUBLIC key is returned. The node needs this to recognise its own
// root account at sign-in; it never signs with this key.
func (hw *HDWalletModule) DeriveLegacyAuthPublicKey(ctx context.Context, seed []byte, account uint32) (ed25519.PublicKey, error) {
	if len(seed) != 64 {
		return nil, ErrHDWalletInvalidSeed
	}
	derived, err := hw.DeriveSecp256k1Key(ctx, seed, fmt.Sprintf(LegacyAuthKeyPath, account))
	if err != nil {
		return nil, fmt.Errorf("derive legacy auth key: %w", err)
	}
	if len(derived.PrivateKey) != ed25519.SeedSize {
		return nil, fmt.Errorf("derive legacy auth key: scalar is %d bytes, want %d", len(derived.PrivateKey), ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(derived.PrivateKey)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("derive legacy auth key: unexpected public key type")
	}
	return pub, nil
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
	grantSigningPath := PurposeKeyPath(PurposeLicensingGrant, account)

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

	// Derive the licensing GRANT signing key at m/44'/0'/account'/2'/0'.
	//
	// This is a hard failure, not a best-effort one. A node that cannot derive its
	// grant key must not silently fall back to signing grants with the identity
	// signing key — that fallback IS the defect this derivation exists to remove
	// (graph/tasks/sdn-grant-verifier-key-domain-separation.md). The licensing lane
	// fails closed downstream when the slot is absent.
	grantSigningPrivKey, grantSigningPubKey, err := hw.DeriveChildForPurpose(ctx, seed, account, PurposeLicensingGrant)
	if err != nil {
		return nil, fmt.Errorf("failed to derive licensing grant signing key: %w", err)
	}

	// Derive X25519 public key from the encryption private key
	encryptionPub, err := hw.X25519PublicKey(ctx, encryptionDerived.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive encryption public key: %w", err)
	}

	// Derive standard blockchain addresses (non-fatal if unavailable)
	coinAddrs, _ := hw.DeriveCoinAddresses(ctx, seed)

	// Derive chain-specific keys for address proofs (non-fatal if unavailable).
	bitcoinDerived, _ := hw.DeriveSecp256k1Key(ctx, seed, BitcoinDerivePath)
	ethereumDerived, _ := hw.DeriveSecp256k1Key(ctx, seed, EthereumDerivePath)
	solanaDerived, _ := hw.DeriveEd25519Key(ctx, seed, SolanaDerivePath)

	var bitcoinPriv, ethereumPriv, solanaPriv []byte
	if bitcoinDerived != nil {
		bitcoinPriv = bitcoinDerived.PrivateKey
	}
	if ethereumDerived != nil {
		ethereumPriv = ethereumDerived.PrivateKey
	}
	if solanaDerived != nil {
		solanaPriv = solanaDerived.PrivateKey
	}

	return &DerivedIdentity{
		Account:             account,
		IdentityPrivKey:     identityPrivKey,
		IdentityPubKey:      identityPubKey,
		SigningPrivKey:      signingPrivKey,
		SigningPubKey:       signingPubKey,
		EncryptionKey:       encryptionDerived.PrivateKey,
		EncryptionPub:       encryptionPub,
		GrantSigningPrivKey: grantSigningPrivKey,
		GrantSigningPubKey:  grantSigningPubKey,
		GrantSigningKeyPath: grantSigningPath,

		PeerID:             peerID,
		IdentityKeyPath:    identityPath,
		SigningKeyPath:     signingPath,
		EncryptionKeyPath:  encryptionPath,
		BitcoinKeyPath:     BitcoinDerivePath,
		BitcoinPrivateKey:  bitcoinPriv,
		EthereumKeyPath:    EthereumDerivePath,
		EthereumPrivateKey: ethereumPriv,
		SolanaKeyPath:      SolanaDerivePath,
		SolanaPrivateKey:   solanaPriv,
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
//
// THIS IS THE UPDATE / PUBLISHER ROOT. It signs SDN-UPDATE-MANIFEST-V1 and
// SDN-MODULE-BUNDLE statements — fleet code authority. It must NOT be used for
// licensing grants; use RawGrantSigningKey for that (owner ruling 2026-08-07).
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

// RawGrantSigningKey returns the raw 32-byte Ed25519 seed for the LICENSING GRANT
// signing key (LicensingGrantKeyPath). This is the seed the host loads into the
// "provider-signing" key slot, and the only key the licensing runtime may sign
// grants with.
//
// It is a sibling of the update root, never the update root itself. Callers that
// want the publisher-of-record key want RawSigningKey.
func (id *DerivedIdentity) RawGrantSigningKey() ([]byte, error) {
	if id == nil || id.GrantSigningPrivKey == nil {
		return nil, errors.New("identity has no licensing grant signing key")
	}
	raw, err := id.GrantSigningPrivKey.Raw()
	if err != nil {
		return nil, err
	}
	if len(raw) == 64 {
		return raw[:32], nil
	}
	return raw, nil
}

// GrantSigningPublicKey returns the 32-byte Ed25519 verification key clients use
// to verify licensing grants. Public material by construction.
func (id *DerivedIdentity) GrantSigningPublicKey() (ed25519.PublicKey, error) {
	if id == nil || id.GrantSigningPubKey == nil {
		return nil, errors.New("identity has no licensing grant signing key")
	}
	raw, err := id.GrantSigningPubKey.Raw()
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("grant signing public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
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
	// GrantSigningPubKeyHex / GrantSigningKeyPath describe the licensing grant
	// verifier key. Both are PUBLIC by construction — the pubkey is what every
	// client verifies grants against, and the path is a hardened SLIP-0010 path
	// whose disclosure enables no derivation. Surfacing them here is what makes
	// the update-root/grant-key separation checkable from a boot log.
	GrantSigningPubKeyHex string
	GrantSigningKeyPath   string
	Addresses             *CoinAddresses
}

// Info returns non-sensitive identity information.
func (id *DerivedIdentity) Info() IdentityInfo {
	identityPubBytes, _ := id.IdentityPubKey.Raw()
	signingPubBytes, _ := id.SigningPubKey.Raw()
	var grantPubHex string
	if id.GrantSigningPubKey != nil {
		grantPubBytes, _ := id.GrantSigningPubKey.Raw()
		grantPubHex = fmt.Sprintf("%x", grantPubBytes)
	}
	return IdentityInfo{
		Account:               id.Account,
		PeerID:                id.PeerID.String(),
		IdentityPubKeyHex:     fmt.Sprintf("%x", identityPubBytes),
		SigningPubKeyHex:      fmt.Sprintf("%x", signingPubBytes),
		EncryptionPubHex:      fmt.Sprintf("%x", id.EncryptionPub),
		IdentityKeyPath:       id.IdentityKeyPath,
		SigningKeyPath:        id.SigningKeyPath,
		EncryptionKeyPath:     id.EncryptionKeyPath,
		GrantSigningPubKeyHex: grantPubHex,
		GrantSigningKeyPath:   id.GrantSigningKeyPath,
		Addresses:             id.Addresses,
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
