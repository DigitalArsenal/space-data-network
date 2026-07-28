package escrow

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
)

// recoveryKeypair stands in for the node's advertised encryption key at
// m/44'/0'/account'/1/0 — the xpub-derivable secp256k1 key whose private half
// only the root mnemonic can produce.
func recoveryKeypair(t *testing.T) (priv, pub []byte) {
	t.Helper()
	sk, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recovery key: %v", err)
	}
	return sk.Serialize(), sk.PubKey().SerializeCompressed()
}

// ed25519PeerKey builds a libp2p Ed25519 identity of the kind `ipfs init`
// creates — random, NOT re-derivable, which is precisely why it needs escrow.
func ed25519PeerKey(t *testing.T) (marshaled []byte, id peer.ID) {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate peer key: %v", err)
	}
	marshaled, err = crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal peer key: %v", err)
	}
	id, err = peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("peer id: %v", err)
	}
	return marshaled, id
}

// TestDestroyAndRecoverPeerIdentity is THE test this whole package exists for:
// seal an identity, DESTROY the local copy, recover from escrow alone, and
// prove the PeerID is byte-identical. This is the vm-orbit-det-01 scenario
// with the outcome it should have had.
func TestDestroyAndRecoverPeerIdentity(t *testing.T) {
	recPriv, recPub := recoveryKeypair(t)
	keyMaterial, originalID := ed25519PeerKey(t)

	blob, err := Seal(keyMaterial, Subject{
		Role:        "kubo-od-producer",
		MachineName: "vm-orbit-det-01",
	}, Recipient{KeyPath: "m/44'/0'/0'/1/0"}, recPub, ecies.Secp256k1)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	escrowFile := filepath.Join(t.TempDir(), "identity.escrow.json")
	if err := blob.WriteFile(escrowFile); err != nil {
		t.Fatalf("write escrow: %v", err)
	}

	// DESTROY the only local copy of the key material.
	for i := range keyMaterial {
		keyMaterial[i] = 0
	}
	keyMaterial = nil

	// Recover from the escrow file alone.
	loaded, err := ReadFile(escrowFile)
	if err != nil {
		t.Fatalf("read escrow: %v", err)
	}
	recovered, err := loaded.Open(recPriv)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	priv, err := crypto.UnmarshalPrivateKey(recovered)
	if err != nil {
		t.Fatalf("unmarshal recovered: %v", err)
	}
	recoveredID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("recovered peer id: %v", err)
	}

	if recoveredID != originalID {
		t.Fatalf("PEER IDENTITY NOT RECOVERED: %s != %s", recoveredID, originalID)
	}
	if loaded.Subject.PeerID != originalID.String() {
		t.Fatalf("blob records the wrong PeerID: %s", loaded.Subject.PeerID)
	}
	if loaded.Subject.KeyType != KeyTypeLibp2pEd25519 {
		t.Fatalf("expected an Ed25519 key type, got %q", loaded.Subject.KeyType)
	}
	t.Logf("recovered PeerID %s (identical)", recoveredID)
}

// TestBlobCarriesNoPlaintextKeyMaterial is the property that makes an escrow
// blob safe to replicate to peers or hand to an operator.
func TestBlobCarriesNoPlaintextKeyMaterial(t *testing.T) {
	_, recPub := recoveryKeypair(t)
	keyMaterial, _ := ed25519PeerKey(t)

	blob, err := Seal(keyMaterial, Subject{Role: "test"}, Recipient{KeyPath: "m/44'/0'/0'/1/0"}, recPub, ecies.Secp256k1)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	raw, err := blob.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The raw key bytes must not appear anywhere in the manifest, in any of the
	// encodings the manifest uses.
	for name, needle := range map[string]string{
		"base64": base64.StdEncoding.EncodeToString(keyMaterial),
		"raw":    string(keyMaterial),
	} {
		if strings.Contains(string(raw), needle) {
			t.Fatalf("escrow blob leaks key material (%s encoding)", name)
		}
	}
	// Sanity: it really is sealed, not merely absent.
	if blob.Payload == "" || blob.ENC == "" || blob.KMF == "" {
		t.Fatal("blob must carry a sealed payload plus its $ENC/$KMF envelope")
	}
}

// TestWrongRecoveryKeyFailsClosed proves only the intended wallet recovers.
func TestWrongRecoveryKeyFailsClosed(t *testing.T) {
	_, recPub := recoveryKeypair(t)
	otherPriv, _ := recoveryKeypair(t)
	keyMaterial, _ := ed25519PeerKey(t)

	blob, err := Seal(keyMaterial, Subject{Role: "test"}, Recipient{KeyPath: "m/44'/0'/0'/1/0"}, recPub, ecies.Secp256k1)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := blob.Open(otherPriv); err == nil {
		t.Fatal("a different wallet must NOT open the escrow — fail closed")
	}
}

// TestMislabeledBlobRefused proves a blob cannot claim an identity it does not
// hold, in either direction: sealing checks, and opening re-verifies.
func TestMislabeledBlobRefused(t *testing.T) {
	recPriv, recPub := recoveryKeypair(t)
	keyMaterial, realID := ed25519PeerKey(t)
	_, otherID := ed25519PeerKey(t)

	// Sealing with a mismatched claim is refused up front.
	if _, err := Seal(keyMaterial, Subject{PeerID: otherID.String()}, Recipient{KeyPath: "m/44'/0'/0'/1/0"}, recPub, ecies.Secp256k1); err == nil {
		t.Fatal("sealing a mislabeled blob must be refused")
	}

	// A blob tampered AFTER sealing is caught on open.
	blob, err := Seal(keyMaterial, Subject{}, Recipient{KeyPath: "m/44'/0'/0'/1/0"}, recPub, ecies.Secp256k1)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if blob.Subject.PeerID != realID.String() {
		t.Fatalf("unexpected sealed PeerID %s", blob.Subject.PeerID)
	}
	blob.Subject.PeerID = otherID.String()
	if _, err := blob.Open(recPriv); err == nil {
		t.Fatal("opening a blob whose claimed PeerID does not match the material must fail closed")
	}
}

// TestDerivableRecordNeedsNoSealedPayload pins the preferred path: an identity
// re-creatable from the root seed is escrowed by RECORDING ITS PATH, with no
// key material sealed anywhere.
func TestDerivableRecordNeedsNoSealedPayload(t *testing.T) {
	blob, err := NewDerivable(Subject{
		PeerID:         "12D3KooWExampleDerivablePeerIdentityForTest",
		KeyType:        KeyTypeLibp2pSecp256k1,
		DerivationPath: "m/44'/0'/0'/0'/0'",
		MachineName:    "sidecar-host-01",
	})
	if err != nil {
		t.Fatalf("new derivable: %v", err)
	}
	if !blob.Derivable() {
		t.Fatal("record must report itself as derivation-only")
	}
	if blob.Payload != "" || blob.ENC != "" || blob.KMF != "" {
		t.Fatal("a derivable record must seal nothing")
	}
	// Opening it must direct the operator to the mnemonic, not fail obscurely.
	_, err = blob.Open([]byte("irrelevant"))
	if err == nil || !strings.Contains(err.Error(), "derivation-only") {
		t.Fatalf("expected a derivation-only explanation, got %v", err)
	}
	if _, err := NewDerivable(Subject{PeerID: "x"}); err == nil {
		t.Fatal("a derivable record without a path must be refused")
	}
}

// TestParseRejectsForeignFiles keeps the recover command from acting on
// arbitrary JSON an operator points it at.
func TestParseRejectsForeignFiles(t *testing.T) {
	if _, err := Parse([]byte(`{"Magic":"something-else","Version":1}`)); err == nil {
		t.Fatal("a foreign manifest must be rejected")
	}
	if _, err := Parse([]byte(`not json at all`)); err == nil {
		t.Fatal("non-JSON must be rejected")
	}
	bad, _ := json.Marshal(Blob{Magic: Magic, Version: 99})
	if _, err := Parse(bad); err == nil {
		t.Fatal("an unsupported version must be rejected")
	}
}

// TestEscrowFileMode keeps the manifest at 0600.
func TestEscrowFileMode(t *testing.T) {
	_, recPub := recoveryKeypair(t)
	keyMaterial, _ := ed25519PeerKey(t)
	blob, err := Seal(keyMaterial, Subject{}, Recipient{KeyPath: "m/44'/0'/0'/1/0"}, recPub, ecies.Secp256k1)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "e.json")
	if err := blob.WriteFile(path); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("escrow file mode = %v, want 0600", info.Mode().Perm())
	}
}
