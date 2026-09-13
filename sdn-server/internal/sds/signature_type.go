package sds

// One place that names the signature algorithm an SDS record was actually
// signed with.
//
// PNM and DPM both carry SIGNATURE_TYPE, but the publishers used to write the
// string "Ed25519" unconditionally while signing with whatever key the node
// held. On an HD node that key is secp256k1, so every record went out claiming
// an algorithm it was not signed with, and every verifier — which correctly
// trusted the field and called ed25519.Verify — rejected it as "invalid PNM
// signature". The publication pipeline on host-02 sat in exactly that state,
// materializing 0 updates per cycle while its IPNS name resolved to the empty
// directory.
//
// The field is the contract. Signers derive it from the key; verifiers
// dispatch on it. Neither side hardcodes an algorithm again.

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/crypto"
	cryptopb "github.com/libp2p/go-libp2p/core/crypto/pb"
)

// SDS signature-type labels. These are wire values: never change a spelling
// without a migration, because records already published carry them.
const (
	SignatureTypeEd25519   = "Ed25519"
	SignatureTypeSecp256k1 = "Secp256k1"
)

// SignatureTypeForKey names the algorithm of a libp2p key.
func SignatureTypeForKey(keyType cryptopb.KeyType) (string, error) {
	switch keyType {
	case cryptopb.KeyType_Ed25519:
		return SignatureTypeEd25519, nil
	case cryptopb.KeyType_Secp256k1:
		return SignatureTypeSecp256k1, nil
	default:
		return "", fmt.Errorf("unsupported SDS signing key type %s", keyType.String())
	}
}

// SignatureTypeForPrivKey names the algorithm a private key will produce.
func SignatureTypeForPrivKey(key crypto.PrivKey) (string, error) {
	if key == nil {
		return "", fmt.Errorf("signing key is required")
	}
	return SignatureTypeForKey(key.Type())
}

// SignatureTypeForPubKey names the algorithm a public key verifies.
func SignatureTypeForPubKey(key crypto.PubKey) (string, error) {
	if key == nil {
		return "", fmt.Errorf("public key is required")
	}
	return SignatureTypeForKey(key.Type())
}

// VerifySDSSignature checks sig over payload using the algorithm the record
// DECLARES, refusing when the declared type and the key disagree. A record that
// claims Ed25519 while carrying a secp256k1 key is not merely unverifiable, it
// is mislabelled, and saying so is more useful than a bare signature failure.
func VerifySDSSignature(declaredType string, key crypto.PubKey, payload, sig []byte) error {
	if key == nil {
		return fmt.Errorf("public key is required")
	}
	keyType, err := SignatureTypeForPubKey(key)
	if err != nil {
		return err
	}
	if declaredType != keyType {
		return fmt.Errorf(
			"SIGNATURE_TYPE %q does not match the provider's %s key",
			declaredType, keyType,
		)
	}
	ok, err := key.Verify(payload, sig)
	if err != nil {
		return fmt.Errorf("verify %s signature: %w", keyType, err)
	}
	if !ok {
		return fmt.Errorf("invalid %s signature", keyType)
	}
	return nil
}
