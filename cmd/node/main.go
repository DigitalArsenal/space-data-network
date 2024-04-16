package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	socketserver "github.com/DigitalArsenal/space-data-network/cmd/socket"
	nodepkg "github.com/DigitalArsenal/space-data-network/internal/node"
	cryptoUtils "github.com/DigitalArsenal/space-data-network/internal/node/crypto_utils"
	protocols "github.com/DigitalArsenal/space-data-network/internal/node/protocols"
	"github.com/DigitalArsenal/space-data-network/internal/node/sds_utils"
	"github.com/DigitalArsenal/space-data-network/serverconfig"
	"github.com/ipfs/kubo/repo/fsrepo"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
	"github.com/rs/zerolog"
	"github.com/skip2/go-qrcode"
)

func RegisterPlugins(node *nodepkg.Node) {
	node.Host.SetStreamHandler(protocols.IDExchangeProtocol, protocols.HandlePNMExchange)

}
func setupLogging(level string) {
	// Map string levels to zerolog levels
	levelMap := map[string]zerolog.Level{
		"debug": zerolog.DebugLevel,
		"info":  zerolog.InfoLevel,
		"warn":  zerolog.WarnLevel,
		"error": zerolog.ErrorLevel,
		"fatal": zerolog.FatalLevel,
		"panic": zerolog.PanicLevel,
	}

	// Set global log level from flag
	if chosenLevel, ok := levelMap[level]; ok {
		zerolog.SetGlobalLevel(chosenLevel)
	} else {
		fmt.Printf("Invalid log level: %s. Defaulting to error.\n", level)
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	}
}

func main() {
	var (
		// addPeerID     = flag.String("add-peerid", "", "PeerID to add along with fileID(s)")
		// addFileIDs    = flag.String("add-fileids", "", "Comma-separated FileIDs to add for the specified PeerID")
		// removePeerID  = flag.String("remove-peerid", "", "PeerID to remove along with fileID(s)")
		// removeFileIDs = flag.String("remove-fileids", "", "Comma-separated FileIDs to remove for the specified PeerID")
		// Flags for listing peers
		listPeers = flag.Bool("list-peers", false, "List peers with details")

		// Flags for fetching and outputting EPM
		getEPM       = flag.String("get-epm", "", "Get EPM by PeerID, index, or email")
		outputPath   = flag.String("output-path", "", "File path to output the EPM data")
		outputFormat = flag.String("output-format", "vcard", "Format to output the EPM (flatbuffer, vcard, qrcode)")

		helpFlag                     = flag.Bool("help", false, "Display help")
		envDocs                      = flag.Bool("env-docs", false, "Display environment variable docs")
		createEPMFlag                = flag.Bool("create-server-epm", false, "Create server EPM")
		outputEPMFlag                = flag.Bool("output-server-epm", false, "Output server EPM")
		outputQRFlag                 = flag.Bool("qr", false, "Output server EPM as QR code")
		versionFlag                  = flag.Bool("version", false, "Display the version")
		importPrivateKeyMnemonicPath = flag.String("import-private-key-mnemonic", "", "Path to file containing a mnemonic phrase for the Ethereum private key")
		importPrivateKeyHexPath      = flag.String("import-private-key-hex", "", "Path to file containing a hex string (with '0x' prefix) for the Ethereum private key")
		exportPrivateKeyMnemonic     = flag.String("export-private-key-mnemonic", "", "Path to file where the private as a mnemonic will be exported")
		exportPrivateKeyHex          = flag.String("export-private-key-hex", "", "Path to file where the private key as a hex string will be exported")
		publicKeyHex                 = flag.Bool("pubkey", false, "The public key in hexadecimal format")
		errorLevel                   = flag.String("error-level", "error", "Set the logging level (debug, info, warn, error, fatal, panic)")
	)

	flag.Parse()
	setupLogging(*errorLevel)

	serverconfig.Init()

	if *publicKeyHex {
		socketserver.SendCommandToSocket("PUBLIC_KEY", []byte(""))
		return
	}

	if *helpFlag {
		flag.Usage()
		return
	}

	if *listPeers {
		response := socketserver.SendCommandToSocket("LIST_PEERS", []byte(""))
		fmt.Println(string(response))
		return
	}

	if *getEPM != "" {
		response := socketserver.SendCommandToSocket("GET_EPM", []byte(*getEPM))
		processEPMResponse(response, *outputPath, *outputFormat)
		return
	}

	/*
				if *addPeerID != "" || *removePeerID != "" {
					managePeerFileIDs(*addPeerID, *addFileIDs, *removePeerID, *removeFileIDs)
					saveConfigAndSendSIGHUP()
				}

		func managePeerFileIDs(addPeerID, addFileIDs, removePeerID, removeFileIDs string) {
			if addPeerID != "" && addFileIDs != "" {
				fileIDs := strings.Split(addFileIDs, ",")
				if validateFileIDs(fileIDs) {
					addPeerFileIDPair(addPeerID, fileIDs)
				} else {
					fmt.Println("Invalid FileID(s). Check the 'Standards' in the configuration.")
					os.Exit(1)
				}
			}

			if removePeerID != "" && removeFileIDs != "" {
				fileIDs := strings.Split(removeFileIDs, ",")
				removePeerFileIDPair(removePeerID, fileIDs)
			}
		}

		func addPeerFileIDPair(peerID string, fileIDs []string) {
			for _, configPeer := range config.Conf.IPFS.PeerPins {
				if configPeer.PeerID == peerID {
					configPeer.FileIDs = appendUnique(configPeer.FileIDs, fileIDs)
					return
				}
			}
			config.Conf.IPFS.PeerPins = append(config.Conf.IPFS.PeerPins, config.IpfsPeerPinConfig{
				PeerID:  peerID,
				FileIDs: fileIDs,
			})
		}

		func removePeerFileIDPair(peerID string, fileIDs []string) {
			for i, configPeer := range config.Conf.IPFS.PeerPins {
				if configPeer.PeerID == peerID {
					configPeer.FileIDs = removeItems(configPeer.FileIDs, fileIDs)
					if len(configPeer.FileIDs) == 0 {
						config.Conf.IPFS.PeerPins = append(config.Conf.IPFS.PeerPins[:i], config.Conf.IPFS.PeerPins[i+1:]...)
					}
					return
				}
			}
		}
	*/

	if *envDocs {
		displayEnvironmentVariableDocs()
		return
	}

	if *versionFlag {
		fmt.Println("Version:", serverconfig.Conf.Info.Version)
		return
	}

	if *createEPMFlag {
		epmBytes := nodepkg.CreateServerEPM(context.Background(), nil, nil)

		socketserver.SendCommandToSocket("CREATE_SERVER_EPM", epmBytes)
		return
	}

	if *outputEPMFlag {
		nodepkg.ReadServerEPM(*outputQRFlag)
		return
	}

	processPrivateKeyFlags(importPrivateKeyMnemonicPath, importPrivateKeyHexPath, exportPrivateKeyMnemonic, exportPrivateKeyHex)

	ctx, cancel := context.WithCancel(context.Background())

	node, err := nodepkg.NewSDNNode(ctx, cancel, "")
	if err != nil {
		handleNodeInitializationError(err)
		return
	}

	os.Remove(serverconfig.Conf.SocketServer.Path) // Remove the existing socket file if present

	// Start the socket server in a goroutine
	go socketserver.StartSocketServer(serverconfig.Conf.SocketServer.Path, node)

	// Setup Plugins
	RegisterPlugins(node)

	setupGracefulShutdown(ctx, node, cancel)

	if err := node.Start(ctx); err != nil {
		fmt.Printf("Error starting node: %v\n", err)
		os.Exit(1)
	}

	//important to block context
	<-ctx.Done()
	fmt.Println("Node shutdown completed")
}

func displayEnvironmentVariableDocs() {
	fmt.Println(`Environment Variables:

	- SPACE_DATA_NETWORK_DATASTORE_PASSWORD: Password for accessing the application's datastore. Essential for security in production environments.
	- SPACE_DATA_NETWORK_DATASTORE_DIRECTORY: Filesystem path for the LevelDB storage. Defaults to a directory named .spacedatanetwork in the user's home directory if unset.
	- SPACE_DATA_NETWORK_WEBSERVER_PORT: Webserver listening port.
	- SPACE_DATA_NETWORK_CPUS: Number of CPUs allocated to the webserver.
	- SPACE_DATA_NETWORK_ETHEREUM_DERIVATION_PATH: BIP32 / BIP44 derivation path for the Ethereum account, defaulting to m/44'/60'/0'/0'/0.

For more information, visit https://spacedatanetwork.com`)
}

// Function to process the response of the GET_EPM command
func processEPMResponse(response []byte, outputPath, format string) {
	// Decode the response from a hex string to bytes.
	epmBytes, err := hex.DecodeString(string(response))
	if err != nil {
		fmt.Printf("Error decoding response: %v\n", err)
		return
	}

	var output string
	vCard := sds_utils.ConvertTovCard(epmBytes)
	switch format {
	case "vcard":
		// Convert to vCard format
		output = vCard
		if outputPath != "" {
			if err := os.WriteFile(outputPath, []byte(output), 0644); err != nil {
				fmt.Printf("Failed to save output to file: %v\n", err)
			} else {
				fmt.Printf("Output saved to %s\n", outputPath)
			}
		}

	case "qrcode":
		if outputPath != "" {
			// Save QR code to the specified file
			err := qrcode.WriteFile(vCard, qrcode.Medium, 256, outputPath)

			if err != nil {
				fmt.Printf("Failed to generate QR code: %v\n", err)
				return
			}
			fmt.Printf("QR code saved to %s\n", outputPath)
			return
		} else {
			// Display QR code in the terminal
			nodepkg.GenerateAndDisplayQRCode(vCard)
			return
		}
	default:
		if outputPath != "" {
			if err := os.WriteFile(outputPath, epmBytes, 0644); err != nil {
				fmt.Printf("Failed to save output to file: %v\n", err)
			} else {
				fmt.Printf("Output saved to %s\n", outputPath)
			}
		} else {
			fmt.Println("Output:", output)
		}
	}
}

func saveConfigAndSendSIGHUP() {
	err := serverconfig.Conf.SaveConfigToFile()
	if err != nil {
		fmt.Printf("Failed to save configuration: %v\n", err)
		return
	}

	pgrepCmd := exec.Command("pgrep", "-f", "spacedatanetwork")
	pid, err := pgrepCmd.Output()
	if err != nil {
		fmt.Printf("Failed to find daemon PID: %v\n", err)
		fmt.Println("If the daemon isn't running, changes will apply upon the next start.")
		return
	}

	killCmd := exec.Command("kill", "-HUP", strings.TrimSpace(string(pid)))
	if err = killCmd.Run(); err != nil {
		fmt.Printf("Failed to send SIGHUP to daemon: %v\n", err)
	} else {
		fmt.Println("Configuration saved and SIGHUP sent to the daemon.")
	}
}

func appendUnique(slice []string, items []string) []string {
	for _, item := range items {
		if !contains(slice, item) {
			slice = append(slice, item)
		}
	}
	return slice
}

func removeItems(slice []string, itemsToRemove []string) []string {
	var result []string
	for _, item := range slice {
		if !contains(itemsToRemove, item) {
			result = append(result, item)
		}
	}
	return result
}

func contains(slice []string, item string) bool {
	for _, sliceItem := range slice {
		if sliceItem == item {
			return true
		}
	}
	return false
}

func validateFileIDs(fileIDs []string) bool {
	for _, fileID := range fileIDs {
		if !isSupportedFileID(fileID) {
			return false
		}
	}
	return true
}

func isSupportedFileID(fileID string) bool {
	for _, standard := range serverconfig.Conf.Info.Standards {
		if fileID == standard {
			return true
		}
	}
	return false
}

func processPrivateKeyFlags(importPrivateKeyMnemonicPath, importPrivateKeyHexPath, exportPrivateKeyMnemonic, exportPrivateKeyHex *string) {
	if *importPrivateKeyMnemonicPath != "" && *importPrivateKeyHexPath != "" {
		fmt.Println("Specify only one import flag: -import-private-key-mnemonic or -import-private-key-hex.")
		os.Exit(1)
	}

	if *importPrivateKeyMnemonicPath != "" {
		importPrivateKeyMnemonic(importPrivateKeyMnemonicPath)
	}

	if *importPrivateKeyHexPath != "" {
		importPrivateKeyHex(importPrivateKeyHexPath)
	}

	if *exportPrivateKeyMnemonic != "" || *exportPrivateKeyHex != "" {
		exportPrivateKey(exportPrivateKeyMnemonic, exportPrivateKeyHex)
	}
}

func importPrivateKeyMnemonic(importPrivateKeyMnemonicPath *string) {
	content, err := os.ReadFile(*importPrivateKeyMnemonicPath)
	if err != nil {
		fmt.Printf("Failed to read file: %v\n", err)
		os.Exit(1)
	}
	privateKeyContent := strings.TrimSpace(string(content))
	if validateEthPrivateKey(privateKeyContent) != "mnemonic" {
		fmt.Println("Invalid mnemonic phrase in file.")
		os.Exit(1)
	}
	saveConfigAndSendSIGHUP()
}

func importPrivateKeyHex(importPrivateKeyHexPath *string) {
	content, err := os.ReadFile(*importPrivateKeyHexPath)
	if err != nil {
		fmt.Printf("Failed to read file: %v\n", err)
		os.Exit(1)
	}
	privateKeyContent := strings.TrimSpace(string(content))
	if validateEthPrivateKey(privateKeyContent) != "hex" {
		fmt.Println("Invalid hex string in file.")
		os.Exit(1)
	}
	saveConfigAndSendSIGHUP()
}

func exportPrivateKey(exportPrivateKeyMnemonic, exportPrivateKeyHex *string) {
	var ipfsConfigDir = filepath.Join(serverconfig.Conf.Datastore.Directory, "ipfs")
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

	unencryptedPrivateKey := cryptoUtils.DecryptPrivateKey(pkBytes, serverconfig.Conf.Datastore.Password)

	var outputContent, outputFilePath string
	if *exportPrivateKeyMnemonic != "" {
		outputContent, err = hdwallet.NewMnemonicFromEntropy(unencryptedPrivateKey)
		if err != nil {
			fmt.Printf("Failed to generate mnemonic from entropy: %v\n", err)
			os.Exit(1)
		}
		outputFilePath = *exportPrivateKeyMnemonic
	} else if *exportPrivateKeyHex != "" {
		outputContent = fmt.Sprintf("0x%s", hex.EncodeToString(unencryptedPrivateKey))
		outputFilePath = *exportPrivateKeyHex
	}

	writeToFile(outputFilePath, outputContent)
	fmt.Printf("Data exported successfully to %s\n", outputFilePath)
	os.Exit(0)
}

func writeToFile(filePath, content string) {
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		fmt.Printf("Failed to write to file: %v\n", err)
		os.Exit(1)
	}
}

func validateEthPrivateKey(key string) string {
	wordCount := len(strings.Fields(key))
	if wordCount >= 12 && wordCount <= 24 && wordCount%3 == 0 {
		return "mnemonic"
	}

	if strings.HasPrefix(key, "0x") {
		hexLength := len(key) - 2
		if hexLength == 64 {
			if _, err := hex.DecodeString(key[2:]); err == nil {
				return "hex"
			}
		}
	}

	return "invalid"
}

func setupGracefulShutdown(_ context.Context, node *nodepkg.Node, cancel context.CancelFunc) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		<-c
		fmt.Println("\nGracefully shutting down...")
		node.Stop()
		cancel()
	}()
}

func handleNodeInitializationError(err error) {
	if strings.Contains(err.Error(), "lock") && strings.Contains(err.Error(), "someone else has the lock") {
		fmt.Println("The spacedatanetwork daemon is already running.")
	} else {
		fmt.Printf("Failed to initialize node: %v\n", err)
	}
	os.Exit(1)
}

func flagUsage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])

	// Determine the longest flag name
	var longestNameLength int
	flag.VisitAll(func(f *flag.Flag) {
		if len(f.Name) > longestNameLength {
			longestNameLength = len(f.Name)
		}
	})

	// Print each flag with padding to align descriptions
	flag.VisitAll(func(f *flag.Flag) {
		fmt.Fprintf(os.Stderr, "  -%-"+strconv.Itoa(longestNameLength)+"s\t%s\n", f.Name, f.Usage)
	})
}

func init() {
	flag.Usage = flagUsage
}
