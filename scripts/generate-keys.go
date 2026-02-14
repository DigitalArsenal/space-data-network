package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
)

func main() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate key: %v
", err)
		os.Exit(1)
	}

	pubHex := hex.EncodeToString(pub)
	privHex := hex.EncodeToString(priv)

	fmt.Println("--- Generated Ed25519 Identity ---")
	fmt.Printf("Signing PubKey Hex: %s
", pubHex)
	fmt.Printf("Private Key Hex:    %s
", privHex)
	fmt.Println("
Add this to your config.yaml under 'users':")
	fmt.Println("users:")
	fmt.Println("  - xpub: "YOUR_XPUB_HERE"")
	fmt.Printf("    signing_pubkey_hex: "%s"
", pubHex)
	fmt.Println("    trust_level: "admin"")
	fmt.Println("    name: "Operator"")
	fmt.Println("
(Store the private key safely if you need to use it for programmatic signing outside the wallet)")
}
