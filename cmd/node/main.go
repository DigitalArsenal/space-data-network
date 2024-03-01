package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	nodepkg "github.com/DigitalArsenal/space-data-network/internal/node" // Adjust the import path to your actual node package
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure context cancellation is called on function exit

	// Initialize the Node
	node, err := nodepkg.NewNode(ctx) // Assuming NewNode now doesn't require a context
	if err != nil {
		fmt.Printf("Error initializing node: %v\n", err)
		os.Exit(1)
	}

	// Start the Node operations
	if err := node.Start(ctx); err != nil {
		fmt.Printf("Error starting node: %v\n", err)
		os.Exit(1)
	}

	// Handle system interrupts for graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		fmt.Println("\nReceived shutdown signal, shutting down...")
		node.Stop() // Trigger node shutdown
		cancel()    // Cancel the context to stop any ongoing operations
	}()

	// Example usage of Node after it has been started
	pubKeyHex, err := node.PublicKey()
	if err != nil {
		fmt.Printf("Failed to get public key: %v\n", err)
		return
	}
	fmt.Printf("Public Key in hex: %s\n", pubKeyHex)

	// Wait here until the context is cancelled (i.e., until node.Stop() is called and completes)
	<-ctx.Done()
	fmt.Println("Node shutdown completed")
}
