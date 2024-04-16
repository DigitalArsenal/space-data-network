package crypto_utils

import (
	"fmt"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
)

// encodePeerID encodes a peer ID to a specified multibase encoding
func encodePeerID(peerID peer.ID, encoding multibase.Encoding) (string, error) {
	peerMh, err := multihash.FromB58String(peerID.String())
	if err != nil {
		return "", fmt.Errorf("error converting Peer ID to multihash: %w", err)
	}

	peerCid := cid.NewCidV1(cid.Libp2pKey, peerMh)
	encoded, err := multibase.Encode(encoding, peerCid.Bytes())
	if err != nil {
		return "", fmt.Errorf("error encoding CID: %w", err)
	}

	return encoded, nil
}

// EncodePeerIDToBase32 encodes a peer ID to base32
func EncodePeerIDToBase32(peerID peer.ID) (string, error) {
	return encodePeerID(peerID, multibase.Base32)
}

// EncodePeerIDToBase36 encodes a peer ID to base36
func EncodePeerIDToBase36(peerID peer.ID) (string, error) {
	return encodePeerID(peerID, multibase.Base36)
}

// EncodePublicKeyToBase32 encodes a public key to base32 by first converting it to a peer ID
func EncodePublicKeyToBase32(pubKey crypto.PubKey) (string, error) {
	pid, err := peer.IDFromPublicKey(pubKey)
	if err != nil {
		return "", fmt.Errorf("error getting peer ID from public key: %w", err)
	}
	return EncodePeerIDToBase32(pid)
}

// EncodePublicKeyToBase36 encodes a public key to base36 by first converting it to a peer ID
func EncodePublicKeyToBase36(pubKey crypto.PubKey) (string, error) {
	pid, err := peer.IDFromPublicKey(pubKey)
	if err != nil {
		return "", fmt.Errorf("error getting peer ID from public key: %w", err)
	}
	return EncodePeerIDToBase36(pid)
}
