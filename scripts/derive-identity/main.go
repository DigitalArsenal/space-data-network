package main

import (
	"bufio"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/lmars/go-slip10"
	"github.com/tyler-smith/go-bip39"
)

func main() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Enter your BIP-39 mnemonic phrase: ")
	mnemonic, _ := reader.ReadString('
')
	mnemonic = strings.TrimSpace(mnemonic)

	if mnemonic == "" {
		fmt.Println("Error: Mnemonic cannot be empty.")
		os.Exit(1)
	}

	// 1. Mnemonic to Seed
	seed := bip39.NewSeed(mnemonic, "") // No passphrase used in SDN default

	// 2. Derive Ed25519 Key using SLIP-10
	// Path: m/44'/1957'/0'/0'/0'
	// 44' = 0x8000002C
	// 1957' = 0x800007A5
	// 0' = 0x80000000
	// 0' = 0x80000000
	// 0' = 0x80000000
	path := []uint32{
		0x8000002C,
		0x800007A5,
		0x80000000,
		0x80000000,
		0x80000000,
	}

	key, err := slip10.NewNode(seed, slip10.Ed25519)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create master node: %v
", err)
		os.Exit(1)
	}

	for _, index := range path {
		key, err = key.Derive(index)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to derive child: %v
", err)
			os.Exit(1)
		}
	}

	// 3. Generate Public Key
	// The private key from SLIP-10 is the seed for Ed25519
	privKey := ed25519.NewKeyFromSeed(key.Key)
	pubKey := privKey.Public().(ed25519.PublicKey)
	pubKeyHex := hex.EncodeToString(pubKey)

	// 4. Output
	fmt.Println("
--- SDN Identity Config ---")
	fmt.Println("Add this to your config.yaml under 'users':")
	fmt.Println("users:")
	fmt.Println("  - xpub: "YOUR_XPUB_HERE"")
	fmt.Printf("    signing_pubkey_hex: "%s"
", pubKeyHex)
	fmt.Println("    trust_level: "admin"")
	fmt.Println("    name: "Operator"")
}
