package node

import (
	"encoding/hex"
	"fmt"

	"github.com/libp2p/go-libp2p/core/crypto"
)

func (n *Node) PublicKey(marshaled ...bool) (string, error) {

	useMarshaled := false
	if len(marshaled) > 0 {
		useMarshaled = marshaled[0]
	}

	if n.Host == nil {
		return "", fmt.Errorf("host is not initialized")
	}

	pubKey, err := n.Host.ID().ExtractPublicKey()
	if err != nil {
		return "", fmt.Errorf("failed to extract public key: %w", err)
	}
	if pubKey == nil {
		return "", fmt.Errorf("public key is nil")
	}

	if useMarshaled {

		pubKeyBytes, err := crypto.MarshalPublicKey(pubKey)
		if err != nil {
			return "", fmt.Errorf("failed to marshal public key: %w", err)
		}

		return hex.EncodeToString(pubKeyBytes), nil
	}

	rawBytes, err := pubKey.Raw()
	if err != nil {
		return "", fmt.Errorf("failed to extract raw public key: %w", err)
	}

	return hex.EncodeToString(rawBytes), nil
}

func (n *Node) PrivateKey() (*crypto.Secp256k1PrivateKey, error) {
	if n.Host == nil || n.Host.Peerstore() == nil {
		return nil, fmt.Errorf("host or peerstore not initialized")
	}

	privKey := n.Host.Peerstore().PrivKey(n.Host.ID())
	if privKey == nil {
		return nil, fmt.Errorf("private key not found in peerstore")
	}

	secp256k1PrivKey, ok := privKey.(*crypto.Secp256k1PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not of type *crypto.Secp256k1PrivateKey")
	}

	return secp256k1PrivKey, nil
}
