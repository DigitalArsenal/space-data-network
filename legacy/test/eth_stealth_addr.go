package main

import (
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/sha3"
)

// Address represents an Ethereum address.
type Address = common.Address

// StealthMetaAddress represents a stealth meta-address, consisting of spending and viewing public keys.
type StealthMetaAddress struct {
	SpendingPublicKey []byte
	ViewingPublicKey  []byte
}

// GenerateStealthAddress generates a stealth address, an ephemeral public key, and a view tag.
func GenerateStealthAddress(meta StealthMetaAddress) (Address, []byte, byte) {
	// Generate random scalar r
	r, err := crypto.GenerateKey()
	if err != nil {
		log.Fatalf("Failed to generate random scalar: %v", err)
	}

	// Decode meta-address to obtain spending and viewing public keys
	spendingPubKey, err := crypto.DecompressPubkey(meta.SpendingPublicKey)
	if err != nil {
		log.Fatalf("Failed to decompress spending public key: %v", err)
	}
	viewingPubKey, err := crypto.DecompressPubkey(meta.ViewingPublicKey)
	if err != nil {
		log.Fatalf("Failed to decompress viewing public key: %v", err)
	}

	// Derive shared secret using ECDH and viewing public key
	sharedSecret, viewTag := getSharedSecret(viewingPubKey, r)

	// Calculate stealth public key using spending public key and shared secret
	stealthPubKeyX, stealthPubKeyY := spendingPubKey.ScalarBaseMult(sharedSecret.Bytes())

	// Calculate stealth address
	stealthAddress := crypto.PubkeyToAddress(ecdsa.PublicKey{Curve: crypto.S256(), X: stealthPubKeyX, Y: stealthPubKeyY})

	// Use compressed format for the ephemeral public key
	ephemeralPubKey := crypto.CompressPubkey(&r.PublicKey)

	return stealthAddress, ephemeralPubKey, viewTag
}

// getSharedSecret derives the shared secret and view tag using the sender's ephemeral private key and the recipient's public key.
func getSharedSecret(pubKey *ecdsa.PublicKey, privKey *ecdsa.PrivateKey) (*big.Int, byte) {
	sharedSecretX, _ := pubKey.Curve.ScalarMult(pubKey.X, pubKey.Y, privKey.D.Bytes())

	// Hash the X-coordinate of the shared secret to derive the view tag
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(sharedSecretX.Bytes())
	hash := hasher.Sum(nil)

	return sharedSecretX, hash[0]
}

// checkStealthAddress verifies whether a given stealth address can be derived from the provided keys.
func checkStealthAddress(stealthAddress Address, ephemeralPubKey []byte, viewingKey *ecdsa.PrivateKey, spendingPubKey *ecdsa.PublicKey) bool {
	ephemeralPublicKeyECDSA, err := crypto.DecompressPubkey(ephemeralPubKey)
	if err != nil {
		log.Fatalf("Failed to decompress ephemeral public key: %v", err)
	}

	sharedSecret, _ := getSharedSecret(ephemeralPublicKeyECDSA, viewingKey)

	// Calculate the stealth public key using the shared secret and the spending public key
	stealthPubKeyX, stealthPubKeyY := spendingPubKey.Curve.ScalarBaseMult(sharedSecret.Bytes())
	calculatedStealthAddress := crypto.PubkeyToAddress(ecdsa.PublicKey{Curve: crypto.S256(), X: stealthPubKeyX, Y: stealthPubKeyY})

	return stealthAddress == calculatedStealthAddress
}

func main() {
	// Generate stealth meta-address
	spendPrivKey, err := crypto.GenerateKey()
	if err != nil {
		log.Fatalf("Failed to generate spending key: %v", err)
	}
	viewPrivKey, err := crypto.GenerateKey()
	if err != nil {
		log.Fatalf("Failed to generate viewing key: %v", err)
	}
	meta := StealthMetaAddress{
		SpendingPublicKey: crypto.CompressPubkey(&spendPrivKey.PublicKey),
		ViewingPublicKey:  crypto.CompressPubkey(&viewPrivKey.PublicKey),
	}

	// Generate stealth address
	stealthAddr, ephemeralPubKey, _ := GenerateStealthAddress(meta)

	// Check stealth address with correct parameters
	if success := checkStealthAddress(stealthAddr, ephemeralPubKey, viewPrivKey, &spendPrivKey.PublicKey); success {
		fmt.Println("Success: Stealth address is valid")
		fmt.Printf("Stealth Address: %s\n", stealthAddr.Hex())
	} else {
		fmt.Println("Failure: Stealth address is invalid")
	}

	// Change one character in the stealth address to simulate a failure
	changedAddr := []byte(stealthAddr.Hex())
	// For simplicity, let's just change the first character
	if changedAddr[0] == '0' {
		changedAddr[0] = '1'
	} else {
		changedAddr[0] = '0'
	}
	invalidStealthAddr := common.BytesToAddress(changedAddr)

	// Check stealth address with incorrect parameters
	if success := checkStealthAddress(invalidStealthAddr, ephemeralPubKey, viewPrivKey, &spendPrivKey.PublicKey); !success {
		fmt.Println("Success: Stealth address is invalid")
		fmt.Printf("Stealth Address: %s\n", invalidStealthAddr.Hex())
	} else {
		fmt.Println("Failure: Stealth address is valid")
	}
}
