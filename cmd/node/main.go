package main

import (
	"context"
	"fmt"

	"github.com/DigitalArsenal/space-data-network/internal/node" // Replace 'your_project_path/node' with the actual import path of your node package
)

func main() {
	ctx := context.Background()

	// Create a new libp2p Host
	Node, err := node.NewNode(ctx)
	if err != nil {
		panic(err)
	}

	// Get the public key in hex format
	pubKeyHex, err := Node.PublicKey()
	if err != nil {
		fmt.Printf("Failed to get public key: %v\n", err)
		return
	}

	fmt.Printf("Public Key in hex: %s\n", pubKeyHex)
}
