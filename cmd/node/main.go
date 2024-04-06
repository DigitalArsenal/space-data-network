package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	web "github.com/DigitalArsenal/space-data-network/internal/web"

	nodepkg "github.com/DigitalArsenal/space-data-network/internal/node"
	cryptoUtils "github.com/DigitalArsenal/space-data-network/internal/node/crypto_utils"
	config "github.com/DigitalArsenal/space-data-network/serverconfig"
	"github.com/ipfs/kubo/repo/fsrepo"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
)

func validateEthPrivateKey(key string) string {
	// Check if key is a mnemonic phrase: typically 12, 15, 18, 21, or 24 words
	wordCount := len(strings.Fields(key))
	if wordCount >= 12 && wordCount <= 24 && wordCount%3 == 0 {
		return "mnemonic"
	}

	// Check if key is a hex string: starts with '0x' and is 64 characters long
	if strings.HasPrefix(key, "0x") {
		hexLength := len(key) - 2                                   // Subtracting 2 to account for the '0x' prefix
		if hexLength >= 32 && hexLength <= 64 && hexLength%2 == 0 { // Length should be even, each byte is represented by two hex characters
			_, err := hex.DecodeString(key[2:])
			if err == nil {
				return "hex"
			}
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
	importPrivateKeyMnemonicPath := flag.String("import-private-key-mnemonic", "", "Path to file containing a mnemonic phrase for the Ethereum private key")
	importPrivateKeyHexPath := flag.String("import-private-key-hex", "", "Path to file containing a hex string (with '0x' prefix) for the Ethereum private key")
	exportPrivateKeyMnemonic := flag.String("export-private-key-mnemonic", "", "Path to file where the private as a mnemonic will be exported")
	exportPrivateKeyHex := flag.String("export-private-key-hex", "", "Path to file where the private key as a hex string will be exported")

	flag.Parse()
	config.Init() // Make sure configuration is initialized

	// Help flag
	if *helpFlag {
		flag.Usage()
		return
	}
	var mnemonic string

	if *importPrivateKeyMnemonicPath != "" && *importPrivateKeyHexPath != "" {
		fmt.Println("Please specify only one import flag, either -import-private-key-mnemonic or -import-private-key-hex.")
		os.Exit(1)
	}

	var privateKeyContent string

	if *importPrivateKeyMnemonicPath != "" {
		content, err := os.ReadFile(*importPrivateKeyMnemonicPath)
		if err != nil {
			fmt.Printf("Failed to read file: %v\n", err)
			os.Exit(1)
		}
		privateKeyContent = strings.TrimSpace(string(content))
		mnemonic = privateKeyContent
		if validateEthPrivateKey(privateKeyContent) != "mnemonic" {
			fmt.Println("Invalid mnemonic phrase in file. Please ensure it contains a valid mnemonic phrase.")
			os.Exit(1)
		}
	}

	if *importPrivateKeyHexPath != "" {
		content, err := os.ReadFile(*importPrivateKeyHexPath)
		if err != nil {
			fmt.Printf("Failed to read file: %v\n", err)
			os.Exit(1)
		}
		privateKeyContent = strings.TrimSpace(string(content))
		entropy, err := hex.DecodeString(privateKeyContent[2:])
		if err != nil {
			fmt.Printf("Failed to decode hex string: %v\n", err)
			os.Exit(1)
		}
		mnemonic, err = hdwallet.NewMnemonicFromEntropy(entropy)
		if err != nil {
			fmt.Printf("Failed to decode hex string: %v\n", err)
			os.Exit(1)
		}
		if validateEthPrivateKey(privateKeyContent) != "hex" {
			fmt.Println("Invalid hex string in file. Please ensure it contains a valid hex string with '0x' prefix.")
			os.Exit(1)
		}
	}

	if *exportPrivateKeyMnemonic != "" || *exportPrivateKeyHex != "" {
		var ipfsConfigDir = filepath.Join(config.Conf.Datastore.Directory, "ipfs")
		repo, err := fsrepo.Open(ipfsConfigDir)
		if err != nil {
			fmt.Printf("Failed to open IPFS repo: %v\n", err)
			os.Exit(1)
		}

		cfg, err := repo.Config()
		if err != nil {
			fmt.Printf("Failed to get config from repo: %v\n", err)
			os.Exit(1)
		}

		pkBytes, err := base64.StdEncoding.DecodeString(cfg.Identity.PrivKey)
		if err != nil {
			fmt.Printf("Failed to decode base64 private key: %v\n", err)
			os.Exit(1)
		}

		unencryptedPrivateKey := cryptoUtils.DecryptPrivateKey(pkBytes, config.Conf.Datastore.Password)

		// Determine the output content and file path
		var outputContent string
		var outputFilePath string
		if *exportPrivateKeyMnemonic != "" {
			mnemonic, err := hdwallet.NewMnemonicFromEntropy(unencryptedPrivateKey)
			if err != nil {
				fmt.Printf("Failed to generate mnemonic from entropy: %v\n", err)
				os.Exit(1)
			}
			outputContent = mnemonic
			outputFilePath = *exportPrivateKeyMnemonic
		} else {
			outputContent = fmt.Sprintf("0x%s", hex.EncodeToString(unencryptedPrivateKey))
			outputFilePath = *exportPrivateKeyHex
		}

		// Check if the directory of the output file exists, create it if not
		outputFileDir := filepath.Dir(outputFilePath)
		if _, err := os.Stat(outputFileDir); os.IsNotExist(err) {
			if err := os.MkdirAll(outputFileDir, 0755); err != nil {
				fmt.Printf("Failed to create directory for output file: %v\n", err)
				os.Exit(1)
			}
		}

		// Write the output content to the file
		if err := os.WriteFile(outputFilePath, []byte(outputContent), 0644); err != nil {
			fmt.Printf("Failed to write to output file: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Data exported successfully to %s\n", outputFilePath)
		os.Exit(0)
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

	if len(mnemonic) == 0 {
		// Start the Node operations
		if err := node.Start(ctx); err != nil {
			fmt.Printf("Error starting node: %v\n", err)
			os.Exit(1)
		}

		server := web.NewAPI(node)
		server.Start()

		// Handle system interrupts for graceful shutdown
		setupGracefulShutdown(ctx, node, cancel)

		// Wait here until the context is cancelled
		<-ctx.Done()
		fmt.Println("Node shutdown completed")
	}
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
