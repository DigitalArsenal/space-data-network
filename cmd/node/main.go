package main

import (
	"fmt"
	"log"

	"github.com/ethereum/go-ethereum/crypto"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	bip32 "github.com/tyler-smith/go-bip32"
	"github.com/tyler-smith/go-bip39"
)

func main() {
	// Generate a mnemonic for a new wallet (or use an existing one)
	entropy, err := bip39.NewEntropy(128)
	if err != nil {
		log.Fatalf("Error generating entropy: %v", err)
	}
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		log.Fatalf("Error creating mnemonic: %v", err)
	}
	fmt.Println("Mnemonic:", mnemonic)

	// Generate a seed from the mnemonic
	seed := bip39.NewSeed(mnemonic, "")

	// Generate a master key from the seed
	masterKey, err := bip32.NewMasterKey(seed)
	if err != nil {
		log.Fatalf("Error generating master key: %v", err)
	}

	// Derive the path: m/44'/60'/0'/0/0 for Ethereum
	derivationPath := []uint32{0x8000002C, 0x8000003C, 0x80000000, 0, 0}

	var derivedKey *bip32.Key
	for _, index := range derivationPath {
		if derivedKey == nil {
			derivedKey, err = masterKey.NewChildKey(index)
		} else {
			derivedKey, err = derivedKey.NewChildKey(index)
		}
		if err != nil {
			log.Fatalf("Error deriving key at index %d: %v", index, err)
		}
	}

	fmt.Println(derivedKey.Key)

	// Convert the derived key to an ECDSA private key
	privateKey, err := crypto.ToECDSA(derivedKey.Key)
	if err != nil {
		log.Fatalf("Error converting to ECDSA: %v", err)
	}

	// Generate the Ethereum address from the private key
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	fmt.Printf("Address: %s\n", address.Hex())

	// Convert the ECDSA private key to libp2p's crypto.PrivateKey format
	libp2pPrivKey, err := libp2pcrypto.UnmarshalSecp256k1PrivateKey(crypto.FromECDSA(privateKey))
	if err != nil {
		log.Fatalf("Error converting to libp2p private key: %v", err)
	}

	fmt.Println(libp2pPrivKey.Raw())
	// Now you can use libp2pPrivKey within the libp2p ecosystem
	fmt.Println("Successfully converted to libp2p private key")
}
