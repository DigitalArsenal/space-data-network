package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/DigitalArsenal/space-data-network/internal/node" // Replace 'your_project_path/node' with the actual import path of your node package
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize the new node
	n, err := node.NewNode(ctx)
	if err != nil {
		fmt.Printf("Error creating node: %s\n", err)
		os.Exit(1)
	}

	// Placeholder for node's Start function if you plan to add more functionality there
	n.Start(ctx)

	// Wait for interrupt signal to gracefully shut down the node
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	fmt.Println("\nReceived shutdown signal, stopping node...")
	n.Stop()
	fmt.Println("Node stopped gracefully")
}
