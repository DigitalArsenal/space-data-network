package peers

// Peer identification from raw key material, for the operator "add a peer by
// public key" surface (POST /api/peers with public_key instead of id).
//
// The registry is keyed by libp2p peer ID, and a peer ID is a deterministic
// function of a public key — so an operator handed a KEY rather than an ID
// must not have to run a derivation tool by hand. This file is the connector:
// it parses key material the node can already interpret and hands back the
// canonical peer ID. It performs no trust decision of its own; the caller
// still applies the trust level, and every write path stays behind the admin
// gate.
//
// Deliberately NOT handled here: BIP-32 extended public keys (xpub). An xpub
// identifies a WALLET ACCOUNT, not a libp2p host, and the xpub-keyed identity
// registry is auth.UserStore (/api/auth/users, /api/auth/attest) — a different
// registry with a different key space. Clients that hold only an xpub derive
// the peer ID themselves (hd-wallet-wasm libp2p.peerIdFromXpub, which mirrors
// internal/wasm.DeriveIdentity: the secp256k1 account key at m/44'/0'/N' IS
// the key inside the xpub) and send the resulting id. Doing that derivation
// here would duplicate HD-wallet math into the peer registry for no gain.

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	ed25519RawPublicKeySize   = 32
	secp256k1CompressedKeSize = 33
)

// PeerIDFromPublicKey derives the libp2p peer ID for a public key supplied as
// text. Accepted encodings, tried in this order:
//
//  1. A protobuf-marshalled libp2p public key (crypto.MarshalPublicKey output)
//     in standard base64, raw-URL base64, or hex. This is the canonical wire
//     form and carries its own key type.
//  2. A raw 32-byte Ed25519 public key in hex or base64.
//  3. A raw 33-byte compressed secp256k1 public key (0x02/0x03 prefix) in hex
//     or base64 — the curve SDN node identities use (internal/wasm.DeriveIdentity).
//
// Anything else is refused with an error rather than guessed at: a peer ID
// derived from misparsed bytes would silently create a trust entry for an
// identity nobody controls.
func PeerIDFromPublicKey(encoded string) (peer.ID, error) {
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" {
		return "", fmt.Errorf("public_key is empty")
	}

	raws := decodeKeyCandidates(trimmed)
	if len(raws) == 0 {
		return "", fmt.Errorf("public_key must be hex or base64")
	}

	// 1. Marshalled libp2p public key (self-describing).
	for _, raw := range raws {
		if pub, err := libp2pcrypto.UnmarshalPublicKey(raw); err == nil {
			return peer.IDFromPublicKey(pub)
		}
	}

	// 2/3. Raw curve points, disambiguated by length (and, for secp256k1, the
	// SEC1 compressed-point prefix).
	for _, raw := range raws {
		switch {
		case len(raw) == ed25519RawPublicKeySize:
			pub, err := libp2pcrypto.UnmarshalEd25519PublicKey(raw)
			if err != nil {
				continue
			}
			return peer.IDFromPublicKey(pub)
		case len(raw) == secp256k1CompressedKeSize && (raw[0] == 0x02 || raw[0] == 0x03):
			pub, err := libp2pcrypto.UnmarshalSecp256k1PublicKey(raw)
			if err != nil {
				continue
			}
			return peer.IDFromPublicKey(pub)
		}
	}

	return "", fmt.Errorf("public_key is not a libp2p public key, a raw 32-byte ed25519 key, or a raw 33-byte compressed secp256k1 key")
}

// decodeKeyCandidates returns every plausible byte decoding of s. Hex and the
// two base64 alphabets overlap for some inputs, so callers try each candidate
// against the structural checks above rather than guessing an encoding first.
func decodeKeyCandidates(s string) [][]byte {
	var out [][]byte
	appendUnique := func(raw []byte) {
		if len(raw) == 0 {
			return
		}
		for _, existing := range out {
			if string(existing) == string(raw) {
				return
			}
		}
		out = append(out, raw)
	}

	if raw, err := hex.DecodeString(strings.TrimPrefix(s, "0x")); err == nil {
		appendUnique(raw)
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if raw, err := enc.DecodeString(s); err == nil {
			appendUnique(raw)
		}
	}
	return out
}

// ResolvePeerIdentifier resolves the peer ID a write request targets from the
// identifier fields it supplied: an explicit id/peer_id, a public key, or both.
// When both are present they MUST agree — a mismatch is an error, never a
// silent preference for one field, because the two would otherwise name
// different identities in the same request.
func ResolvePeerIdentifier(id, peerID, publicKey string) (peer.ID, error) {
	idText := strings.TrimSpace(id)
	if idText == "" {
		idText = strings.TrimSpace(peerID)
	}
	keyText := strings.TrimSpace(publicKey)

	if idText == "" && keyText == "" {
		return "", fmt.Errorf("one of id, peer_id or public_key is required")
	}

	var fromID peer.ID
	if idText != "" {
		decoded, err := peer.Decode(idText)
		if err != nil {
			return "", fmt.Errorf("invalid peer ID: %w", err)
		}
		fromID = decoded
	}
	if keyText == "" {
		return fromID, nil
	}

	fromKey, err := PeerIDFromPublicKey(keyText)
	if err != nil {
		return "", err
	}
	if fromID != "" && fromID != fromKey {
		return "", fmt.Errorf("public_key derives peer ID %s, which does not match the supplied id %s", fromKey, fromID)
	}
	return fromKey, nil
}
