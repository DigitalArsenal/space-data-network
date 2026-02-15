package wasm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Test mnemonic (DO NOT USE IN PRODUCTION)
const testMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// testHDWalletModule returns a test HD wallet module, skipping if WASM not available.
// NOTE: The HD wallet WASM must be built with WASI (not Emscripten) for Go integration.
// The Emscripten build (for JavaScript) won't work with wazero.
// To build the hardened WASI version:
//   emcmake cmake -B build-wasi -S . -DHD_WALLET_BUILD_WASM=ON -DCMAKE_BUILD_TYPE=Release
//   cmake --build build-wasi --target hd_wallet_wasm_wasi
// Or set HD_WALLET_WASM_PATH to a WASI build.
func testHDWalletModule(t *testing.T) *HDWalletModule {
	t.Helper()

	// Look for hd-wallet WASM binary. Prefer hardened Emscripten WASI build.
	wasmPaths := []string{
		os.Getenv("HD_WALLET_WASM_PATH"),
		"../../../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"../../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"../../../../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm",
		"../../../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm",
	}

	var wasmPath string
	for _, p := range wasmPaths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			wasmPath = p
			break
		}
	}

	if wasmPath == "" {
		t.Skip("HD wallet WASM not found - set HD_WALLET_WASM_PATH or build with emcmake")
	}

	ctx := context.Background()
	hw, err := NewHDWalletModule(ctx, wasmPath)
	if err != nil {
		t.Fatalf("failed to load HD wallet WASM: %v", err)
	}

	// Inject entropy for WASI environment
	entropy := make([]byte, 64)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatalf("failed to generate entropy: %v", err)
	}
	if err := hw.InjectEntropy(ctx, entropy); err != nil {
		t.Fatalf("failed to inject entropy: %v", err)
	}

	t.Cleanup(func() {
		hw.Close(context.Background())
	})

	return hw
}

func TestHDWalletModule_GenerateMnemonic(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	tests := []struct {
		name      string
		wordCount int
		wantErr   bool
	}{
		{"12 words", 12, false},
		{"15 words", 15, false},
		{"18 words", 18, false},
		{"21 words", 21, false},
		{"24 words", 24, false},
		{"invalid word count", 13, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mnemonic, err := hw.GenerateMnemonic(ctx, tt.wordCount)
			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateMnemonic() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				// Verify word count
				words := bytes.Count([]byte(mnemonic), []byte(" ")) + 1
				if words != tt.wordCount {
					t.Errorf("GenerateMnemonic() returned %d words, want %d", words, tt.wordCount)
				}
			}
		})
	}
}

func TestHDWalletModule_ValidateMnemonic(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		mnemonic string
		want     bool
	}{
		{"valid test mnemonic", testMnemonic, true},
		{"invalid mnemonic", "abandon abandon abandon", false},
		{"empty mnemonic", "", false},
		{"invalid word", "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon invalid", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, err := hw.ValidateMnemonic(ctx, tt.mnemonic)
			if err != nil {
				t.Errorf("ValidateMnemonic() error = %v", err)
				return
			}
			if valid != tt.want {
				t.Errorf("ValidateMnemonic() = %v, want %v", valid, tt.want)
			}
		})
	}
}

func TestHDWalletModule_MnemonicToSeed(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	// Test with known test vector
	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	if len(seed) != 64 {
		t.Errorf("MnemonicToSeed() seed length = %d, want 64", len(seed))
	}

	// Test with passphrase produces different seed
	seedWithPass, err := hw.MnemonicToSeed(ctx, testMnemonic, "mypassphrase")
	if err != nil {
		t.Fatalf("MnemonicToSeed() with passphrase error = %v", err)
	}

	if bytes.Equal(seed, seedWithPass) {
		t.Error("MnemonicToSeed() seed with passphrase should be different from without")
	}

	// Test determinism - same inputs produce same output
	seed2, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	if !bytes.Equal(seed, seed2) {
		t.Error("MnemonicToSeed() should be deterministic")
	}
}

func TestHDWalletModule_DeriveEd25519Key(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"BIP-44 signing key", "m/44'/0'/0'/0'/0'", false},
		{"BIP-44 encryption key", "m/44'/0'/0'/1'/0'", false},
		{"Solana path", "m/44'/501'/0'/0'", false},
		{"invalid path", "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := hw.DeriveEd25519Key(ctx, seed, tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("DeriveEd25519Key() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(key.PrivateKey) != 32 {
					t.Errorf("DeriveEd25519Key() private key length = %d, want 32", len(key.PrivateKey))
				}
				if len(key.ChainCode) != 32 {
					t.Errorf("DeriveEd25519Key() chain code length = %d, want 32", len(key.ChainCode))
				}
			}
		})
	}

	// Test determinism
	key1, _ := hw.DeriveEd25519Key(ctx, seed, "m/44'/0'/0'/0'/0'")
	key2, _ := hw.DeriveEd25519Key(ctx, seed, "m/44'/0'/0'/0'/0'")
	if !bytes.Equal(key1.PrivateKey, key2.PrivateKey) {
		t.Error("DeriveEd25519Key() should be deterministic")
	}

	// Different paths produce different keys
	key3, _ := hw.DeriveEd25519Key(ctx, seed, "m/44'/0'/1'/0'/0'")
	if bytes.Equal(key1.PrivateKey, key3.PrivateKey) {
		t.Error("DeriveEd25519Key() different paths should produce different keys")
	}
}

func TestHDWalletModule_Ed25519SignVerify(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	// Derive a key
	derivedKey, err := hw.DeriveEd25519Key(ctx, seed, "m/44'/0'/0'/0'/0'")
	if err != nil {
		t.Fatalf("DeriveEd25519Key() error = %v", err)
	}

	// Get public key
	pubKey, err := hw.Ed25519PublicKeyFromSeed(ctx, derivedKey.PrivateKey)
	if err != nil {
		t.Fatalf("Ed25519PublicKeyFromSeed() error = %v", err)
	}

	// Sign a message
	message := []byte("Hello, Space Data Network!")
	signature, err := hw.Ed25519Sign(ctx, derivedKey.PrivateKey, message)
	if err != nil {
		t.Fatalf("Ed25519Sign() error = %v", err)
	}

	if len(signature) != 64 {
		t.Errorf("Ed25519Sign() signature length = %d, want 64", len(signature))
	}

	// Verify signature
	valid, err := hw.Ed25519Verify(ctx, pubKey, message, signature)
	if err != nil {
		t.Fatalf("Ed25519Verify() error = %v", err)
	}
	if !valid {
		t.Error("Ed25519Verify() = false, want true for valid signature")
	}

	// Verify with wrong message
	wrongMessage := []byte("Wrong message")
	valid, err = hw.Ed25519Verify(ctx, pubKey, wrongMessage, signature)
	if err != nil {
		t.Fatalf("Ed25519Verify() error = %v", err)
	}
	if valid {
		t.Error("Ed25519Verify() = true, want false for wrong message")
	}

	// Verify with wrong public key
	wrongKey := make([]byte, 32)
	rand.Read(wrongKey)
	valid, err = hw.Ed25519Verify(ctx, wrongKey, message, signature)
	if err != nil {
		t.Fatalf("Ed25519Verify() error = %v", err)
	}
	if valid {
		t.Error("Ed25519Verify() = true, want false for wrong public key")
	}
}

func TestHDWalletModule_X25519(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	// Derive two X25519 key pairs
	aliceKey, err := hw.DeriveEd25519Key(ctx, seed, "m/44'/0'/0'/1'/0'")
	if err != nil {
		t.Fatalf("DeriveEd25519Key() error = %v", err)
	}

	bobKey, err := hw.DeriveEd25519Key(ctx, seed, "m/44'/0'/1'/1'/0'")
	if err != nil {
		t.Fatalf("DeriveEd25519Key() error = %v", err)
	}

	// Get public keys
	alicePub, err := hw.X25519PublicKey(ctx, aliceKey.PrivateKey)
	if err != nil {
		t.Fatalf("X25519PublicKey() error = %v", err)
	}

	bobPub, err := hw.X25519PublicKey(ctx, bobKey.PrivateKey)
	if err != nil {
		t.Fatalf("X25519PublicKey() error = %v", err)
	}

	// Perform ECDH
	aliceShared, err := hw.X25519ECDH(ctx, aliceKey.PrivateKey, bobPub)
	if err != nil {
		t.Fatalf("X25519ECDH() error = %v", err)
	}

	bobShared, err := hw.X25519ECDH(ctx, bobKey.PrivateKey, alicePub)
	if err != nil {
		t.Fatalf("X25519ECDH() error = %v", err)
	}

	// Shared secrets should match
	if !bytes.Equal(aliceShared, bobShared) {
		t.Error("X25519ECDH() shared secrets should match")
	}

	if len(aliceShared) != 32 {
		t.Errorf("X25519ECDH() shared secret length = %d, want 32", len(aliceShared))
	}
}

func TestHDWalletModule_DeriveIdentity(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	// Derive identity
	identity, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		t.Fatalf("DeriveIdentity() error = %v", err)
	}

	// Verify identity components
	if identity.Account != 0 {
		t.Errorf("DeriveIdentity() account = %d, want 0", identity.Account)
	}

	if identity.SigningPrivKey == nil {
		t.Error("DeriveIdentity() SigningPrivKey is nil")
	}

	if identity.SigningPubKey == nil {
		t.Error("DeriveIdentity() SigningPubKey is nil")
	}

	if len(identity.EncryptionKey) != 32 {
		t.Errorf("DeriveIdentity() EncryptionKey length = %d, want 32", len(identity.EncryptionKey))
	}

	if len(identity.EncryptionPub) != 32 {
		t.Errorf("DeriveIdentity() EncryptionPub length = %d, want 32", len(identity.EncryptionPub))
	}

	if identity.PeerID == "" {
		t.Error("DeriveIdentity() PeerID is empty")
	}

	// Verify PeerID is valid
	_, err = peer.Decode(identity.PeerID.String())
	if err != nil {
		t.Errorf("DeriveIdentity() invalid peer ID: %v", err)
	}

	// Verify determinism - same seed + account produces same identity
	identity2, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		t.Fatalf("DeriveIdentity() error = %v", err)
	}

	if identity.PeerID != identity2.PeerID {
		t.Error("DeriveIdentity() should be deterministic")
	}

	// Different accounts produce different identities
	identity3, err := hw.DeriveIdentity(ctx, seed, 1)
	if err != nil {
		t.Fatalf("DeriveIdentity() error = %v", err)
	}

	if identity.PeerID == identity3.PeerID {
		t.Error("DeriveIdentity() different accounts should produce different identities")
	}
}

func TestDerivedIdentity_SignVerify(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	identity, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		t.Fatalf("DeriveIdentity() error = %v", err)
	}

	// Sign using identity method
	message := []byte("Test message for signing")
	signature, err := identity.Sign(message)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	// Verify signature
	valid, err := identity.Verify(message, signature)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !valid {
		t.Error("Verify() = false, want true for valid signature")
	}

	// Verify with wrong message
	valid, err = identity.Verify([]byte("wrong message"), signature)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if valid {
		t.Error("Verify() = true, want false for wrong message")
	}
}

func TestHDWalletModule_IdentityFromMnemonic(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	// Test with valid mnemonic
	identity, err := hw.IdentityFromMnemonic(ctx, testMnemonic, "", 0)
	if err != nil {
		t.Fatalf("IdentityFromMnemonic() error = %v", err)
	}

	if identity.PeerID == "" {
		t.Error("IdentityFromMnemonic() PeerID is empty")
	}

	// Test with passphrase produces different identity
	identityWithPass, err := hw.IdentityFromMnemonic(ctx, testMnemonic, "password", 0)
	if err != nil {
		t.Fatalf("IdentityFromMnemonic() with passphrase error = %v", err)
	}

	if identity.PeerID == identityWithPass.PeerID {
		t.Error("IdentityFromMnemonic() different passphrases should produce different identities")
	}

	// Test with invalid mnemonic
	_, err = hw.IdentityFromMnemonic(ctx, "invalid mnemonic words", "", 0)
	if err == nil {
		t.Error("IdentityFromMnemonic() should fail with invalid mnemonic")
	}
}

func TestHDWalletModule_DeriveMultipleIdentities(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	identities, err := hw.DeriveMultipleIdentities(ctx, seed, 5)
	if err != nil {
		t.Fatalf("DeriveMultipleIdentities() error = %v", err)
	}

	if len(identities) != 5 {
		t.Errorf("DeriveMultipleIdentities() returned %d identities, want 5", len(identities))
	}

	// All identities should be unique
	seen := make(map[peer.ID]bool)
	for i, id := range identities {
		if seen[id.PeerID] {
			t.Errorf("DeriveMultipleIdentities() identity %d has duplicate peer ID", i)
		}
		seen[id.PeerID] = true

		if id.Account != uint32(i) {
			t.Errorf("DeriveMultipleIdentities() identity %d has wrong account %d", i, id.Account)
		}
	}
}

func TestDerivedIdentity_Info(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	identity, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		t.Fatalf("DeriveIdentity() error = %v", err)
	}

	info := identity.Info()

	if info.Account != 0 {
		t.Errorf("Info() Account = %d, want 0", info.Account)
	}

	if info.PeerID == "" {
		t.Error("Info() PeerID is empty")
	}

	// Verify hex strings are valid
	if _, err := hex.DecodeString(info.SigningPubKeyHex); err != nil {
		t.Errorf("Info() invalid SigningPubKeyHex: %v", err)
	}

	if _, err := hex.DecodeString(info.EncryptionPubHex); err != nil {
		t.Errorf("Info() invalid EncryptionPubHex: %v", err)
	}

	if info.SigningKeyPath == "" {
		t.Error("Info() SigningKeyPath is empty")
	}

	if info.EncryptionKeyPath == "" {
		t.Error("Info() EncryptionKeyPath is empty")
	}
}

func TestDerivedIdentity_MarshalPrivateKey(t *testing.T) {
	hw := testHDWalletModule(t)
	ctx := context.Background()

	seed, err := hw.MnemonicToSeed(ctx, testMnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed() error = %v", err)
	}

	identity, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		t.Fatalf("DeriveIdentity() error = %v", err)
	}

	// Marshal the key
	keyBytes, err := identity.MarshalPrivateKey()
	if err != nil {
		t.Fatalf("MarshalPrivateKey() error = %v", err)
	}

	if len(keyBytes) == 0 {
		t.Error("MarshalPrivateKey() returned empty bytes")
	}
}
