package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	config "github.com/DigitalArsenal/space-data-network/configs"
	nodepkg "github.com/DigitalArsenal/space-data-network/internal/node"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
)

func validateEthPrivateKey(key string) string {
	// Check if key is a mnemonic phrase: typically 12, 15, 18, 21, or 24 words
	wordCount := len(strings.Fields(key))
	if wordCount >= 12 && wordCount <= 24 && wordCount%3 == 0 {
		return "mnemonic"
	}

	// Check if key is a hex string: starts with '0x' and is 64 characters long
	if strings.HasPrefix(key, "0x") && len(key) == 66 { // 2 characters for '0x' + 64 hex characters
		_, err := hex.DecodeString(key[2:])
		if err == nil {
			return "hex"
		}
	}

	return "invalid"
}

func main() {
	// CLI flags
	helpFlag := flag.Bool("help", false, "Display help")
	envDocs := flag.Bool("env-docs", false, "Display environment variable docs")
	createEPMFlag := flag.Bool("create-server-epm", false, "Create server EPM")
	outputEPMFlag := flag.Bool("output-server-epm", false, "Output server EPM")
	outputQRFlag := flag.Bool("qr", false, "Output server EPM as QR code")
	versionFlag := flag.Bool("version", false, "Display the version")
	privateKeyFilePath := flag.String("private-key-file", "", "Path to file with private key (mnemonic phrase or hex key '0x' prefix)")

	flag.Parse()

	// Help flag
	if *helpFlag {
		flag.Usage()
		return
	}
	var mnemonic string

	if *privateKeyFilePath != "" {
		privateKey, err := os.ReadFile(*privateKeyFilePath)
		if err != nil {
			fmt.Printf("Failed to read Ethereum private key file: %v\n", err)
			os.Exit(1)
		}
		ethPrivateKey := strings.TrimSpace(string(privateKey))
		keyType := validateEthPrivateKey(ethPrivateKey)
		if keyType == "invalid" {
			fmt.Println("Invalid private key in file. Please ensure it contains a valid mnemonic phrase or hex key.")
			os.Exit(1)
		}
		if keyType == "mnemonic" {
			mnemonic = ethPrivateKey
		}
		if keyType == "hex" {

			entropy, err := hex.DecodeString(ethPrivateKey[2:])
			if err != nil {
				fmt.Printf("Failed to decode hex string: %v\n", err)
				os.Exit(1)
			}

			mnemonic, err = hdwallet.NewMnemonicFromEntropy(entropy)
			if err != nil {
				fmt.Printf("Failed to decode hex string: %v\n", err)
				os.Exit(1)
			}
		}
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

	// Version flag
	if *versionFlag {
		config.Init() // Make sure configuration is initialized and version is loaded
		fmt.Println("Version:", config.Conf.Info.Version)
		return
	}

	// EPM related operations should be checked first and then exit if they are called
	if *createEPMFlag {
		nodepkg.CreateServerEPM()
		return
	}

	if *outputEPMFlag {
		nodepkg.ReadServerEPM(*outputQRFlag)
		return
	}

	// If no other action flag is passed, proceed with running the node by default
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize the Node
	node, err := nodepkg.NewSDNNode(ctx, mnemonic)
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
	// Find the longest flag name
	longestFlagNameLen := 0
	flag.VisitAll(func(f *flag.Flag) {
		if len(f.Name) > longestFlagNameLen {
			longestFlagNameLen = len(f.Name)
		}
	})

	// Create the usage output with aligned descriptions
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	flag.VisitAll(func(f *flag.Flag) {
		padding := strings.Repeat(" ", longestFlagNameLen-len(f.Name))    // Calculate padding
		fmt.Fprintf(os.Stderr, "  -%s%s\t%s\n", f.Name, padding, f.Usage) // Print flag name with padding and its usage description
	})
}
func init() {
	flag.Usage = flagUsage
}
