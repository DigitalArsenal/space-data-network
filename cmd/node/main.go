package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	cli_epm "github.com/DigitalArsenal/space-data-network/internal/cli"
	nodepkg "github.com/DigitalArsenal/space-data-network/internal/node"
)

func main() {
	// CLI flags
	helpFlag := flag.Bool("help", false, "Display help")
	createEPMFlag := flag.Bool("create-server-epm", false, "Create server EPM")
	outputEPMFlag := flag.Bool("output-server-epm", false, "Output server EPM")

	flag.Parse()

	// Help flag
	if *helpFlag {
		flag.Usage()
		return
	}

	// EPM related operations should be checked first and then exit if they are called
	if *createEPMFlag {
		cli_epm.CreateServerEPM()
		return
	}

	if *outputEPMFlag {

		return
	}

	// If no other action flag is passed, proceed with running the node by default
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize the Node
	node, err := nodepkg.NewNode(ctx)
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
	setupGracefulShutdown(ctx, node, cancel)

	// Wait here until the context is cancelled
	<-ctx.Done()
	fmt.Println("Node shutdown completed")
}

func createServerEPM() {
	// Logic to create a server EPM
	fmt.Println("Creating server EPM...")
	// Implement the function that creates the server EPM
}

func outputServerEPM() {
	// Logic to output a server EPM
	fmt.Println("Outputting server EPM...")
	// Implement the function that outputs the server EPM
}

func setupGracefulShutdown(_ context.Context, node *nodepkg.Node, cancel context.CancelFunc) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		fmt.Println("\nReceived shutdown signal, shutting down...")
		node.Stop()
		cancel()
	}()
}

func flagUsage() {
	usageText := `
Usage: main [options]

Options:
	-help                 Display this help message
	-run                  Run the server node
	-create-server-epm    Create server Entity Profile Message (EPM)
	-output-server-epm    Output server Entity Profile Message (EPM)

For more information, see https://spacedatanetwork.com
	`
	fmt.Println(usageText)
}

func init() {
	flag.Usage = flagUsage
}
