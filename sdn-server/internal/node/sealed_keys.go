package node

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
)

// SealedTransportKeys returns what the $RPC admin transport needs from this
// node's seed: the private half of the encryption key the node advertises on
// its card (secp256k1 at encryptionPath; the one encryption path, as
// show-identity prints it) and the Ed25519 key that signs every response.
func (n *Node) SealedTransportKeys(encryptionPath string) (encryptionPriv []byte, signer ed25519.PrivateKey, err error) {
	if n == nil || n.identityBundle == nil || n.hdwallet == nil {
		return nil, nil, errors.New("node identity is not loaded")
	}
	bundle := n.identityBundle
	if strings.TrimSpace(bundle.Mnemonic) == "" || bundle.Identity == nil || bundle.Identity.SigningPrivKey == nil {
		return nil, nil, errors.New("node identity has no seed or signing key")
	}
	raw, err := bundle.Identity.SigningPrivKey.Raw()
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, nil, fmt.Errorf("node signing key: %v", err)
	}
	seed, err := n.hdwallet.MnemonicToSeed(n.ctx, bundle.Mnemonic, "")
	if err != nil {
		return nil, nil, fmt.Errorf("derive seed: %w", err)
	}
	defer func() {
		for i := range seed {
			seed[i] = 0
		}
	}()
	key, err := n.hdwallet.DeriveSecp256k1Key(n.ctx, seed, encryptionPath)
	if err != nil {
		return nil, nil, fmt.Errorf("derive encryption key at %s: %w", encryptionPath, err)
	}
	return key.PrivateKey, ed25519.PrivateKey(append([]byte(nil), raw...)), nil
}
