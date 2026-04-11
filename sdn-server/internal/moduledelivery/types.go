package moduledelivery

import (
	"crypto/sha256"
	"fmt"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	// ProtocolID is the public libp2p module-delivery stream.
	ProtocolID = "/space-data-network/module-delivery/1.0.0"

	// DiscoveryNamespace is the provider-identity DHT namespace used by sdn-js.
	DiscoveryNamespace = "space-data-network/module-delivery/provider-pubkey"

	schemaVersion = uint32(1)
	wrapAlgorithm = "X25519+HKDF-SHA256+AES-256-GCM"
)

var wrapInfo = []byte("space-data-network/module-delivery/wrap/v1")

// ComputeDiscoveryCID matches the requester-side discovery CID derivation in sdn-js.
func ComputeDiscoveryCID(providerPublicKey []byte) (cid.Cid, error) {
	if err := validateProviderPublicKey(providerPublicKey); err != nil {
		return cid.Cid{}, err
	}
	input := make([]byte, 0, len(DiscoveryNamespace)+len(providerPublicKey))
	input = append(input, []byte(DiscoveryNamespace)...)
	input = append(input, providerPublicKey...)

	sum := sha256.Sum256(input)
	multihash, err := mh.Encode(sum[:], mh.SHA2_256)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("encode discovery multihash: %w", err)
	}
	return cid.NewCidV1(cid.Raw, multihash), nil
}

func validateProviderPublicKey(providerPublicKey []byte) error {
	if len(providerPublicKey) != 33 {
		return fmt.Errorf("provider public key must be 33-byte compressed secp256k1, got %d bytes", len(providerPublicKey))
	}
	if providerPublicKey[0] != 0x02 && providerPublicKey[0] != 0x03 {
		return fmt.Errorf("provider public key must use compressed secp256k1 prefix 0x02/0x03")
	}
	return nil
}
