package peers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// newTestKeyPair returns a fresh key of the requested libp2p type plus the peer
// ID it derives. Keys are generated, never hard-coded: this file must never
// carry key material that resembles a real identity.
func newTestKeyPair(t *testing.T, typ int) (libp2pcrypto.PubKey, peer.ID) {
	t.Helper()

	_, pub, err := libp2pcrypto.GenerateKeyPairWithReader(typ, 256, rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKeyPair(%d): %v", typ, err)
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey: %v", err)
	}
	return pub, id
}

// TestPeerIDFromPublicKeyAcceptsEveryDocumentedEncoding locks the three
// accepted forms (marshalled libp2p key, raw ed25519, raw compressed
// secp256k1) across hex and both base64 alphabets, and locks that each one
// derives the SAME peer ID libp2p itself would.
func TestPeerIDFromPublicKeyAcceptsEveryDocumentedEncoding(t *testing.T) {
	t.Parallel()

	edPub, edID := newTestKeyPair(t, libp2pcrypto.Ed25519)
	secpPub, secpID := newTestKeyPair(t, libp2pcrypto.Secp256k1)

	edMarshalled, err := libp2pcrypto.MarshalPublicKey(edPub)
	if err != nil {
		t.Fatalf("MarshalPublicKey(ed25519): %v", err)
	}
	secpMarshalled, err := libp2pcrypto.MarshalPublicKey(secpPub)
	if err != nil {
		t.Fatalf("MarshalPublicKey(secp256k1): %v", err)
	}
	edRaw, err := edPub.Raw()
	if err != nil {
		t.Fatalf("ed25519 Raw: %v", err)
	}
	secpRaw, err := secpPub.Raw()
	if err != nil {
		t.Fatalf("secp256k1 Raw: %v", err)
	}
	if len(edRaw) != ed25519RawPublicKeySize {
		t.Fatalf("ed25519 raw len = %d, want %d", len(edRaw), ed25519RawPublicKeySize)
	}
	if len(secpRaw) != secp256k1CompressedKeSize {
		t.Fatalf("secp256k1 raw len = %d, want %d", len(secpRaw), secp256k1CompressedKeSize)
	}

	cases := []struct {
		name string
		raw  []byte
		want peer.ID
	}{
		{"marshalled ed25519", edMarshalled, edID},
		{"marshalled secp256k1", secpMarshalled, secpID},
		{"raw ed25519", edRaw, edID},
		{"raw compressed secp256k1", secpRaw, secpID},
	}

	for _, tc := range cases {
		tc := tc
		encodings := map[string]string{
			"hex":             hex.EncodeToString(tc.raw),
			"hex 0x-prefixed": "0x" + hex.EncodeToString(tc.raw),
			"base64 std":      base64.StdEncoding.EncodeToString(tc.raw),
			"base64 raw-url":  base64.RawURLEncoding.EncodeToString(tc.raw),
		}
		for encName, encoded := range encodings {
			encName, encoded := encName, encoded
			t.Run(tc.name+"/"+encName, func(t *testing.T) {
				t.Parallel()
				got, err := PeerIDFromPublicKey(encoded)
				if err != nil {
					t.Fatalf("PeerIDFromPublicKey: %v", err)
				}
				if got != tc.want {
					t.Fatalf("peer ID = %s, want %s", got, tc.want)
				}
			})
		}
	}
}

// TestPeerIDFromPublicKeyRefusesUnparseableMaterial locks that unrecognised
// input is an error rather than a guess. A peer ID derived from misparsed bytes
// would create a trust entry for an identity nobody controls.
func TestPeerIDFromPublicKeyRefusesUnparseableMaterial(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":             "",
		"whitespace":        "   ",
		"not hex or base64": "not a key!!",
		"short hex":         hex.EncodeToString([]byte{1, 2, 3}),
		// 33 bytes but not a SEC1 compressed point (bad prefix byte).
		"bad secp prefix": hex.EncodeToString(append([]byte{0x07}, make([]byte, 32)...)),
		// The right length for a raw ed25519 key but not a valid point set is
		// accepted by ed25519 (any 32 bytes are a public key), so use a length
		// that matches nothing instead.
		"unrecognised length": hex.EncodeToString(make([]byte, 40)),
		"an xpub":             "xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa",
	}

	for name, input := range cases {
		name, input := name, input
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got, err := PeerIDFromPublicKey(input); err == nil {
				t.Fatalf("PeerIDFromPublicKey(%q) = %s, want error", input, got)
			}
		})
	}
}

// TestResolvePeerIdentifierRequiresAgreementBetweenIDAndKey locks the
// fail-closed rule for requests that carry both identifier forms: they must
// name the same peer, or the request is refused.
func TestResolvePeerIdentifierRequiresAgreementBetweenIDAndKey(t *testing.T) {
	t.Parallel()

	pubA, idA := newTestKeyPair(t, libp2pcrypto.Ed25519)
	_, idB := newTestKeyPair(t, libp2pcrypto.Ed25519)
	rawA, err := pubA.Raw()
	if err != nil {
		t.Fatalf("Raw: %v", err)
	}
	keyA := hex.EncodeToString(rawA)

	t.Run("id only", func(t *testing.T) {
		got, err := ResolvePeerIdentifier(idA.String(), "", "")
		if err != nil || got != idA {
			t.Fatalf("ResolvePeerIdentifier(id) = %s, %v; want %s, nil", got, err, idA)
		}
	})

	t.Run("peer_id alias only", func(t *testing.T) {
		got, err := ResolvePeerIdentifier("", idA.String(), "")
		if err != nil || got != idA {
			t.Fatalf("ResolvePeerIdentifier(peer_id) = %s, %v; want %s, nil", got, err, idA)
		}
	})

	t.Run("public_key only", func(t *testing.T) {
		got, err := ResolvePeerIdentifier("", "", keyA)
		if err != nil || got != idA {
			t.Fatalf("ResolvePeerIdentifier(public_key) = %s, %v; want %s, nil", got, err, idA)
		}
	})

	t.Run("agreeing id and public_key", func(t *testing.T) {
		got, err := ResolvePeerIdentifier(idA.String(), "", keyA)
		if err != nil || got != idA {
			t.Fatalf("ResolvePeerIdentifier(id, public_key) = %s, %v; want %s, nil", got, err, idA)
		}
	})

	t.Run("disagreeing id and public_key", func(t *testing.T) {
		got, err := ResolvePeerIdentifier(idB.String(), "", keyA)
		if err == nil {
			t.Fatalf("ResolvePeerIdentifier(mismatched) = %s, want error", got)
		}
		if !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("error = %v, want a mismatch explanation", err)
		}
	})

	t.Run("nothing supplied", func(t *testing.T) {
		if got, err := ResolvePeerIdentifier("", "", ""); err == nil {
			t.Fatalf("ResolvePeerIdentifier(empty) = %s, want error", got)
		}
	})

	t.Run("malformed id", func(t *testing.T) {
		if got, err := ResolvePeerIdentifier("definitely-not-a-peer-id", "", ""); err == nil {
			t.Fatalf("ResolvePeerIdentifier(bad id) = %s, want error", got)
		}
	})
}
