package trustanchors

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/libp2p/go-libp2p/core/peer"
)

// The canonical file lives outside this module; go:embed needs a copy here.
// sync-anchors.mjs writes it, and this test fails the build if they drift.
func TestEmbeddedAnchorsMatchTheCanonicalFile(t *testing.T) {
	canonical, err := os.ReadFile("../../../deployment/identity/anchors.json")
	if err != nil {
		t.Fatalf("read the canonical anchors: %v", err)
	}
	if !bytes.Equal(canonical, anchorsJSON) {
		t.Fatal("sdn-server/internal/trustanchors/anchors.json differs from deployment/identity/anchors.json: run `node deployment/identity/sync-anchors.mjs`")
	}
}

// SEC-001 regression guard: these are the PUBLIC values of the identity whose
// seed was committed to the public repository. They must stay denylisted for
// as long as anything might still present them.
func TestTheLeakedIdentityStaysDenylisted(t *testing.T) {
	a := Load()
	for _, value := range []string{
		"16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45",                                                           // PeerID
		"0257d9a39fac79d4c36e017b3b6913f60684586605ebb9370cf417ef44bf0f7cd2",                                              // identity key
		"038664c404be42123ce709e53da2b63fc24c091a968dd2200c443d7470f73fb1e6",                                              // EPM signing key
		"0213dc855b71c36b4a7e47b034e5f0bcce8b5fdbdae95a04b3441bc8ac2db3cb41",                                              // sealed-transport encryption key
		"0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c",                                                // Ed25519 publisher / old update root
		"8e2f6db0048925ce413512df5b4261cee5ad7e04e65588d1a9af26cd4254fa2b",                                                // grant verifier
		"xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa", // account xpub
		"d4a971a7e534", // update-root key id
		"bafkreiceqr2v4fvjqddussy5wydruetwmupxzvlett4ezl3zywg4ndl2di", // discovery CID
	} {
		if !a.IsDenied(value) {
			t.Errorf("%s is no longer denylisted", value)
		}
		if !a.IsDenied(strings.ToUpper(value)) && !strings.HasPrefix(value, "16Uiu2") && !strings.HasPrefix(value, "xpub") && !strings.HasPrefix(value, "bafk") {
			t.Errorf("%s is not matched case-insensitively", value)
		}
	}
	p, err := peer.Decode("16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPeer(p, "peer"); !errors.Is(err, ErrDenied) {
		t.Fatalf("CheckPeer = %v, want ErrDenied", err)
	}
	if a.IsDenied("16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U") {
		t.Fatal("celestrak.eth's live identity is not part of SEC-001")
	}
}

func TestBootstrapAddressesSkipPlaceholdersAndDeniedPeers(t *testing.T) {
	a := Load()
	for _, addr := range a.BootstrapAddresses() {
		if strings.Contains(addr, PlaceholderPrefix) {
			t.Fatalf("a placeholder became a bootstrap address: %s", addr)
		}
		for _, denied := range a.Denylist.PeerIDs {
			if strings.Contains(addr, denied) {
				t.Fatalf("a denylisted peer became a bootstrap address: %s", addr)
			}
		}
	}

	// A provider that is (mistakenly) set to a denylisted identity contributes nothing.
	poisoned, err := Parse(anchorsJSON)
	if err != nil {
		t.Fatal(err)
	}
	poisoned.Provider.PeerID = poisoned.Denylist.PeerIDs[0]
	for _, addr := range poisoned.BootstrapAddresses() {
		if strings.Contains(addr, poisoned.Denylist.PeerIDs[0]) {
			t.Fatalf("the denylisted provider was dialled: %s", addr)
		}
	}
	if err := poisoned.ReleaseReady(); err == nil || !strings.Contains(err.Error(), "denylisted") {
		t.Fatalf("ReleaseReady with a denylisted provider = %v", err)
	}
}

func TestPlaceholdersBlockARelease(t *testing.T) {
	a, err := Parse(anchorsJSON)
	if err != nil {
		t.Fatal(err)
	}
	a.Status = "released"
	a.Provider.PeerID = PlaceholderPrefix + "TEST"
	if err := a.ReleaseReady(); err == nil || !strings.Contains(err.Error(), "provider.peerId") {
		t.Fatalf("ReleaseReady with a placeholder PeerID = %v", err)
	}
}

// The release gate on the committed file. A release build sets
// SDN_IDENTITY_RELEASE=1; with any placeholder left this test FAILS it.
// Without the variable it reports the state, so ordinary CI stays green while
// the owner's new anchors are pending.
func TestCommittedAnchorsAreReleaseReady(t *testing.T) {
	err := Load().ReleaseReady()
	if os.Getenv("SDN_IDENTITY_RELEASE") == "1" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil {
		t.Logf("not release-ready yet (expected until the owner's anchors land): %v", err)
	}
}

func TestParseRefusesAnEmptyDenylist(t *testing.T) {
	stripped := bytes.Replace(anchorsJSON, []byte(`"peerIds": [`), []byte(`"peerIdsX": [`), 1)
	if _, err := Parse(stripped); err == nil {
		t.Fatal("anchors without a SEC-001 peer denylist parsed")
	}
}

// Verifier S6: the retired identity's X25519 wrapping key, once the owner
// records it, is denied like every other public value of that identity.
func TestX25519DenylistEntriesAreDenied(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal(anchorsJSON, &raw); err != nil {
		t.Fatal(err)
	}
	wrapping := strings.Repeat("ab", 32)
	raw["denylist"].(map[string]any)["x25519PublicKeys"] = []string{strings.ToUpper(wrapping)}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !a.IsDenied(wrapping) || !a.IsDenied(strings.ToUpper(wrapping)) {
		t.Fatal("a recorded X25519 wrapping key is not denied")
	}
	if a.IsDenied(strings.Repeat("cd", 32)) {
		t.Fatal("an unrelated key is denied")
	}
}

// The denylisted secp256k1 keys are denied uncompressed too (04 || X || Y).
func TestUncompressedSecp256k1KeysAreDenied(t *testing.T) {
	keys := Load().Denylist.Secp256k1PublicKeys
	if len(keys) != 5 {
		t.Fatalf("want the 5 denylisted secp256k1 keys, got %d", len(keys))
	}
	for _, k := range keys {
		compressed, err := hex.DecodeString(k)
		if err != nil {
			t.Fatal(err)
		}
		pub, err := secp256k1.ParsePubKey(compressed)
		if err != nil {
			t.Fatal(err)
		}
		if !IsDeniedKey(pub.SerializeUncompressed()) {
			t.Fatalf("uncompressed form of %s… is not denied", k[:12])
		}
	}
}
