package wasm

// SEC-001: the owner-run identity generator (deployment/identity/
// new-node-identity.mjs) must derive EXACTLY the identity this node derives
// from the same seed, or the anchors it prints would pin a peer the node never
// becomes. This test derives the public identity of the PUBLIC BIP-39 test
// vector (all-"abandon" + "about", assembled at runtime so no mnemonic-shaped
// run is ever committed) with the node's own code and the WASI module the
// binary embeds, and compares it field for field with
// deployment/identity/testdata/derivation-vector.json. The generator's
// --self-test compares its own derivation of the same phrase against the same
// file, so the two meet in one committed artifact of public values.
//
// Regenerate the file (only when the node's derivation deliberately changes):
//
//	SDN_WRITE_ROTATION_VECTOR=1 ./scripts/go-with-wasmedge.sh test ./internal/wasm -run TestRotationDerivationVector

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const rotationVectorPath = "../../../deployment/identity/testdata/derivation-vector.json"

// rotationVector is the public identity set the generator prints, keyed by the
// same names it uses in anchors.json's provider section.
type rotationVector struct {
	Note                   string            `json:"note"`
	Mnemonic               string            `json:"mnemonic"`
	PeerID                 string            `json:"peerId"`
	IdentityPublicKey      string            `json:"identityPublicKey"`
	XPub                   string            `json:"xpub"`
	SigningPublicKey       string            `json:"signingPublicKey"`
	EncryptionPublicKey    string            `json:"encryptionPublicKey"`
	PublisherPublicKey     string            `json:"publisherPublicKey"`
	X25519PublicKey        string            `json:"x25519PublicKey"`
	GrantVerifierPublicKey string            `json:"grantVerifierPublicKey"`
	RootAdminPublicKey     string            `json:"rootAdminPublicKey"`
	DiscoveryCID           string            `json:"discoveryCid"`
	ChainAddresses         map[string]string `json:"chainAddresses"`
	DistributionPublicKey  string            `json:"distributionPublicKey"`
}

func publicTestVectorMnemonic() string {
	return strings.TrimSpace(strings.Repeat("abandon ", 11) + "about")
}

func rotationModule(t *testing.T) *HDWalletModule {
	t.Helper()
	wasmBytes := EmbeddedHDWalletWasm()
	if wasmBytes == nil {
		t.Fatal("embedded hd-wallet-wasi.wasm does not match its pinned sha256")
	}
	ctx := context.Background()
	hw, err := NewHDWalletModuleFromBytes(ctx, wasmBytes)
	if err != nil {
		t.Skipf("WASI runtime unavailable (run through scripts/go-with-wasmedge.sh): %v", err)
	}
	entropy := make([]byte, 64)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatal(err)
	}
	if err := hw.InjectEntropy(ctx, entropy); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hw.Close(context.Background()) })
	return hw
}

func deriveRotationVector(t *testing.T, hw *HDWalletModule, mnemonic string) rotationVector {
	t.Helper()
	ctx := context.Background()
	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	identityPub, err := id.IdentityPubKey.Raw()
	if err != nil {
		t.Fatal(err)
	}
	xpub, err := hw.DeriveXPub(ctx, seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The EPM's signing/encryption keys are the xpub's /0/0 and /1/0 children
	// (internal/epm derivePublicIdentityKeysFromXPub): the same points as the
	// private derivation at these paths.
	signing, err := hw.Secp256k1PublicKey(ctx, seed, "m/44'/0'/0'/0/0")
	if err != nil {
		t.Fatal(err)
	}
	encryption, err := hw.Secp256k1PublicKey(ctx, seed, "m/44'/0'/0'/1/0")
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := id.SigningPubKey.Raw()
	if err != nil {
		t.Fatal(err)
	}
	grant, err := id.GrantSigningPubKey.Raw()
	if err != nil {
		t.Fatal(err)
	}
	rootAdmin, err := hw.DeriveLegacyAuthPublicKey(ctx, seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Purpose 3 is the fleet distribution key (never derived by a node; the
	// updater module derives it from a SEPARATE phrase). The vector carries it
	// so the generator's --distribution mode is pinned to the same path.
	distribution, err := hw.DeriveEd25519Key(ctx, seed, "m/44'/0'/0'/3'/0'")
	if err != nil {
		t.Fatal(err)
	}
	distributionPub, err := hw.Ed25519PublicKeyFromSeed(ctx, distribution.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}

	// Discovery CID exactly as internal/node computeModuleDeliveryDiscoveryCID.
	sum := sha256.Sum256(append([]byte("space-data-network/module-delivery/provider-pubkey"), identityPub...))
	digest, err := mh.Encode(sum[:], mh.SHA2_256)
	if err != nil {
		t.Fatal(err)
	}

	chain := map[string]string{}
	if id.Addresses != nil {
		if id.Addresses.Bitcoin != nil {
			chain["bitcoin"] = id.Addresses.Bitcoin.Address
		}
		if id.Addresses.Ethereum != nil {
			chain["ethereum"] = id.Addresses.Ethereum.Address
		}
		if id.Addresses.Solana != nil {
			chain["solana"] = id.Addresses.Solana.Address
		}
	}

	return rotationVector{
		Note:                   "Public values derived by the node (sdn-server/internal/wasm, embedded hd-wallet-wasi.wasm) from the PUBLIC BIP-39 test vector. Contains no secret.",
		Mnemonic:               "bip39-test-vector:abandon*11+about",
		PeerID:                 id.PeerID.String(),
		IdentityPublicKey:      hex.EncodeToString(identityPub),
		XPub:                   xpub,
		SigningPublicKey:       hex.EncodeToString(signing),
		EncryptionPublicKey:    hex.EncodeToString(encryption),
		PublisherPublicKey:     hex.EncodeToString(publisher),
		X25519PublicKey:        hex.EncodeToString(id.EncryptionPub),
		GrantVerifierPublicKey: hex.EncodeToString(grant),
		RootAdminPublicKey:     hex.EncodeToString(rootAdmin),
		DiscoveryCID:           cid.NewCidV1(cid.Raw, digest).String(),
		ChainAddresses:         chain,
		DistributionPublicKey:  hex.EncodeToString(distributionPub),
	}
}

func TestRotationDerivationVector(t *testing.T) {
	hw := rotationModule(t)
	got := deriveRotationVector(t, hw, publicTestVectorMnemonic())

	if os.Getenv("SDN_WRITE_ROTATION_VECTOR") == "1" {
		data, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(rotationVectorPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rotationVectorPath, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", rotationVectorPath)
		return
	}

	data, err := os.ReadFile(rotationVectorPath)
	if err != nil {
		t.Fatalf("read %s: %v", rotationVectorPath, err)
	}
	var want rotationVector
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("the node's derivation drifted from the committed rotation vector:\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}
