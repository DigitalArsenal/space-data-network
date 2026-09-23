// Package walletderive reproduces, on the server, the sign-in key a browser
// wallet derives, so an admin enrolled from the CLI signs in from the dashboard
// with the very same credentials.
//
// Two hd-wallet-ui profiles are accepted by the node's sign-in today, and both
// are reproduced here byte-for-byte:
//
//	password-fast-v1-legacy   (hd-wallet-wasm src/password_profile.cpp realLegacy)
//	  ikm    = SHA256(username || password)      raw UTF-8 concat, no separator
//	  master = HKDF-SHA256(ikm, salt=username, info="master-key", 32)
//	  seed   = HKDF-SHA256(master, salt=empty, info="hd-wallet-seed", 64)
//
//	bip39-mnemonic-v1-legacy  (hd-wallet-wasm src/sdn_identity_secrets.cpp)
//	  ASCII phrase, whitespace runs collapsed, trimmed, lowercased,
//	  checksum-validated, then the standard BIP-39 seed with no passphrase.
//
// Either seed then yields the sign-in key the same way: the secp256k1 BIP-32
// private scalar at m/44'/0'/0'/0/0 used directly as an Ed25519 seed. The
// BIP-32 and BIP-39 steps run in the HD wallet WASM module, as everywhere else
// on the node; only the small HKDF step of the password profile is Go.
package walletderive

import (
	"context"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// SignInKeyPath is the derivation path of the key that signs
// /api/auth/challenge for both legacy profiles (account 0).
const SignInKeyPath = "m/44'/0'/0'/0/0"

// maxSecretBytes mirrors the wallet's input bound for username and password.
const maxSecretBytes = 4096

// PubKeyRowPrefix keys an admin row enrolled from a bare public key. The users
// table is keyed by xpub; a pasted key has none, and this prefix can never be
// mistaken for one.
const PubKeyRowPrefix = "ed25519:"

// Wallet is the slice of the HD wallet WASM module the derivation needs.
// HDWallet adapts the WASM module to it.
type Wallet interface {
	ValidateMnemonic(ctx context.Context, mnemonic string) (bool, error)
	MnemonicToSeed(ctx context.Context, mnemonic, passphrase string) ([]byte, error)
	DeriveSecp256k1PrivateKey(ctx context.Context, seed []byte, path string) ([]byte, error)
	DeriveXPub(ctx context.Context, seed []byte, account uint32) (string, error)
}

// Source names where an enrollment's key came from.
type Source string

const (
	SourcePassword  Source = "password"
	SourceMnemonic  Source = "mnemonic"
	SourcePublicKey Source = "public-key"
)

// Enrollment is everything the node stores about an admin. It never holds a
// secret: the private material is used to derive the public key and dropped.
type Enrollment struct {
	// RowKey is the users-table key: the account xpub (m/44'/0'/0') for a
	// seed-derived identity, or PubKeyRowPrefix+hex for a pasted key.
	RowKey string
	// SigningPubKeyHex is the Ed25519 key the dashboard signs in with.
	SigningPubKeyHex string
	Source           Source
}

// PasswordSeed derives the 64-byte seed of the password-fast-v1-legacy profile.
func PasswordSeed(username, password []byte) ([]byte, error) {
	if len(username) == 0 {
		return nil, errors.New("username is empty")
	}
	if len(password) == 0 {
		return nil, errors.New("password is empty")
	}
	if len(username) > maxSecretBytes || len(password) > maxSecretBytes {
		return nil, fmt.Errorf("username and password are limited to %d bytes each", maxSecretBytes)
	}
	if !utf8.Valid(username) || !utf8.Valid(password) {
		return nil, errors.New("username and password must be valid UTF-8")
	}

	ikmInput := make([]byte, 0, len(username)+len(password))
	ikmInput = append(ikmInput, username...)
	ikmInput = append(ikmInput, password...)
	ikm := sha256.Sum256(ikmInput)
	zero(ikmInput)
	defer zero(ikm[:])

	master, err := hkdf.Key(sha256.New, ikm[:], username, "master-key", 32)
	if err != nil {
		return nil, err
	}
	defer zero(master)
	return hkdf.Key(sha256.New, master, nil, "hd-wallet-seed", 64)
}

// NormalizeMnemonic applies the wallet's phrase normalization: ASCII only,
// whitespace runs collapsed to one space, trimmed, lowercased.
func NormalizeMnemonic(phrase string) (string, error) {
	for i := 0; i < len(phrase); i++ {
		if phrase[i] >= 0x80 {
			return "", errors.New("recovery phrase must be ASCII")
		}
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(phrase), " "))
	switch len(strings.Fields(normalized)) {
	case 12, 15, 18, 21, 24:
		return normalized, nil
	default:
		return "", errors.New("recovery phrase must have 12, 15, 18, 21 or 24 words")
	}
}

// MnemonicSeed validates a recovery phrase and derives its BIP-39 seed.
func MnemonicSeed(ctx context.Context, w Wallet, phrase string) ([]byte, error) {
	normalized, err := NormalizeMnemonic(phrase)
	if err != nil {
		return nil, err
	}
	ok, err := w.ValidateMnemonic(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("validate recovery phrase: %w", err)
	}
	if !ok {
		return nil, errors.New("recovery phrase checksum is invalid")
	}
	return w.MnemonicToSeed(ctx, normalized, "")
}

// SignInPublicKey derives the Ed25519 sign-in public key from a wallet seed.
func SignInPublicKey(ctx context.Context, w Wallet, seed []byte) (ed25519.PublicKey, error) {
	if len(seed) != 64 {
		return nil, fmt.Errorf("seed is %d bytes, want 64", len(seed))
	}
	scalar, err := w.DeriveSecp256k1PrivateKey(ctx, seed, SignInKeyPath)
	if err != nil {
		return nil, fmt.Errorf("derive %s: %w", SignInKeyPath, err)
	}
	defer zero(scalar)
	if len(scalar) != ed25519.SeedSize {
		return nil, fmt.Errorf("derived scalar is %d bytes, want %d", len(scalar), ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(scalar)
	defer zero(priv)
	return append(ed25519.PublicKey(nil), priv.Public().(ed25519.PublicKey)...), nil
}

// SignInPrivateKey derives the Ed25519 sign-in private key from a wallet
// seed, for a CLI that signs as the admin itself. The caller zeroes it.
func SignInPrivateKey(ctx context.Context, w Wallet, seed []byte) (ed25519.PrivateKey, error) {
	if len(seed) != 64 {
		return nil, fmt.Errorf("seed is %d bytes, want 64", len(seed))
	}
	scalar, err := w.DeriveSecp256k1PrivateKey(ctx, seed, SignInKeyPath)
	if err != nil {
		return nil, fmt.Errorf("derive %s: %w", SignInKeyPath, err)
	}
	defer zero(scalar)
	if len(scalar) != ed25519.SeedSize {
		return nil, fmt.Errorf("derived scalar is %d bytes, want %d", len(scalar), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(scalar), nil
}

// FromSeed builds the enrollment for a seed-derived identity.
func FromSeed(ctx context.Context, w Wallet, seed []byte, source Source) (Enrollment, error) {
	defer zero(seed)
	pub, err := SignInPublicKey(ctx, w, seed)
	if err != nil {
		return Enrollment{}, err
	}
	xpub, err := w.DeriveXPub(ctx, seed, 0)
	if err != nil {
		return Enrollment{}, fmt.Errorf("derive account xpub: %w", err)
	}
	return Enrollment{RowKey: xpub, SigningPubKeyHex: hex.EncodeToString(pub), Source: source}, nil
}

// FromPassword enrolls a username + password identity.
func FromPassword(ctx context.Context, w Wallet, username, password []byte) (Enrollment, error) {
	seed, err := PasswordSeed(username, password)
	if err != nil {
		return Enrollment{}, err
	}
	return FromSeed(ctx, w, seed, SourcePassword)
}

// FromMnemonic enrolls a recovery-phrase identity.
func FromMnemonic(ctx context.Context, w Wallet, phrase string) (Enrollment, error) {
	seed, err := MnemonicSeed(ctx, w, phrase)
	if err != nil {
		return Enrollment{}, err
	}
	return FromSeed(ctx, w, seed, SourceMnemonic)
}

// FromPublicKey enrolls a pasted 64-hex Ed25519 sign-in public key.
func FromPublicKey(pubHex string) (Enrollment, error) {
	trimmed := strings.ToLower(strings.TrimSpace(pubHex))
	raw, err := hex.DecodeString(trimmed)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return Enrollment{}, errors.New("public key must be 64 hex characters (a 32-byte Ed25519 key)")
	}
	return Enrollment{RowKey: PubKeyRowPrefix + trimmed, SigningPubKeyHex: trimmed, Source: SourcePublicKey}, nil
}

// Fingerprint is the short form of a server key an admin compares by eye: the
// first 8 bytes of its SHA-256, as four colon-separated hex groups.
func Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:8])
	return h[0:4] + ":" + h[4:8] + ":" + h[8:12] + ":" + h[12:16]
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// HDWallet adapts the HD wallet WASM module to Wallet.
type HDWallet struct{ *wasm.HDWalletModule }

// DeriveSecp256k1PrivateKey returns the 32-byte private scalar at path.
func (h HDWallet) DeriveSecp256k1PrivateKey(ctx context.Context, seed []byte, path string) ([]byte, error) {
	key, err := h.DeriveSecp256k1Key(ctx, seed, path)
	if err != nil {
		return nil, err
	}
	zero(key.ChainCode)
	return key.PrivateKey, nil
}
