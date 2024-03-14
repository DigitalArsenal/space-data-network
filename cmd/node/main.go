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
	envDocs := flag.Bool("env-docs", false, "Display environment variable docs")
	createEPMFlag := flag.Bool("create-server-epm", false, "Create server EPM")
	outputEPMFlag := flag.Bool("output-server-epm", false, "Output server EPM")

	flag.Parse()

	// Help flag
	if *helpFlag {
		flag.Usage()
		return
	}

	if *envDocs {
		fmt.Print(`Environment Variables

		- SPACE_DATA_NETWORK_DATASTORE_PASSWORD: Used to access the application's datastore. This is a critical security parameter, and it's recommended to set this in production environments.
		- SPACE_DATA_NETWORK_DATASTORE_DIRECTORY: Specifies the filesystem path for the secure LevelDB storage. If not set, the application defaults to a directory named .spacedatanetwork in the user's home directory.
		- SPACE_DATA_NETWORK_WEBSERVER_PORT: Port for the webserver to listen on.
		- SPACE_DATA_NETWORK_CPUS: Number of CPUs to give to the webserver.
		- SPACE_DATA_NETWORK_ETHEREUM_DERIVATION_PATH: BIP32 / BIP44 path to use for account. Defaults to: m/44'/60'/0'/0'/0. It's important to set this in environments that interact with the Ethereum blockchain.
		
		For more information, see https://spacedatanetwork.com
			`)
	}

	// EPM related operations should be checked first and then exit if they are called
	if *createEPMFlag {
		cli_epm.CreateServerEPM()
		return
	}

	if *outputEPMFlag {
		cli_epm.ReadServerEPM()
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
	`
	fmt.Println(usageText)
}

func init() {
	flag.Usage = flagUsage
}
