package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
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

func startSocketServer(socketPath string, n *nodepkg.Node) {
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		fmt.Printf("Failed to listen on socket: %s, error: %v\n", socketPath, err)
		return
	}
	defer listener.Close()

	fmt.Printf("Socket server listening at %s\n", socketPath)

	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("Failed to accept connection on socket: %v\n", err)
			continue
		}
		go handleSocketConnection(conn, n)
	}
}

func handleSocketConnection(conn net.Conn, n *nodepkg.Node) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	// Read the initial command from the client
	command, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("Error reading command from connection: %v\n", err)
		return
	}
	command = strings.TrimSpace(command)

	// Echo back the received command for verification
	fmt.Fprintf(conn, "Received command: %s\n", command)

	// Process the command
	if command == "GET_PEER_ID" {
		// Read the next line, which should be the Peer ID or public key hex
		clientInput, err := reader.ReadString('\n')
		if err != nil {
			fmt.Printf("Error reading from connection: %v\n", err)
			return
		}
		clientInput = strings.TrimSpace(clientInput)

		// Echo back what was received for verification
		fmt.Fprintf(conn, "Client sent: %s\n", clientInput)

		// Here you can use `clientInput` to perform the required operation,
		// for now, we just print it to the server's console and send back a dummy response
		fmt.Println("Received from client:", clientInput)
		peerID := n.Host.ID()
		fmt.Println("Node PeerID:", peerID.String())
		fmt.Fprintln(conn, peerID.String()) // Respond with the Node's PeerID or the relevant info
	} else {
		fmt.Fprintln(conn, "Unknown command")
	}
}

// handlePublicKeyHex connects to the socket server, sends the public key, and prints the response
func GetPeerID(pubKeyHex string) {
	fmt.Printf("Public Key: %s\n", pubKeyHex)

	socketPath := filepath.Join(config.Conf.Datastore.Directory, "app.sock")
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Printf("Daemon not running: %v\n", err)
		return
	}
	defer conn.Close()

	// Send the GET_PEER_ID command to the socket server
	command := "GET_PEER_ID"
	fmt.Fprintf(conn, "%s\n", command)

	// Optionally send the public key if needed by the server for this operation
	fmt.Fprintf(conn, "%s\n", pubKeyHex)

	// Read and print the response from the socket server
	scanner := bufio.NewScanner(conn)
	if scanner.Scan() {
		fmt.Println("Received from server:", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		fmt.Printf("Error reading from socket server: %v\n", err)
	}
}

func main() {
	var (
		addPeerID                    = flag.String("add-peerid", "", "PeerID to add along with fileID(s)")
		addFileIDs                   = flag.String("add-fileids", "", "Comma-separated FileIDs to add for the specified PeerID")
		removePeerID                 = flag.String("remove-peerid", "", "PeerID to remove along with fileID(s)")
		removeFileIDs                = flag.String("remove-fileids", "", "Comma-separated FileIDs to remove for the specified PeerID")
		listPeersFlag                = flag.Bool("list-peers", false, "List all peers and their associated file IDs")
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
		publicKeyHex                 = flag.String("pubkey", "", "The public key in hexadecimal format")
	)

	flag.Parse()

	config.Init()

	if *publicKeyHex != "" {
		GetPeerID(*publicKeyHex)
		return // Exit after handling public key
	}

	if *helpFlag {
		flag.Usage()
		return
	}

	if *listPeersFlag {
		listPeersAndFileIDs()
		return
	}

	if *addPeerID != "" || *removePeerID != "" {

		managePeerFileIDs(*addPeerID, *addFileIDs, *removePeerID, *removeFileIDs)
		saveConfigAndSendSIGHUP()
	}

	if *envDocs {
		displayEnvironmentVariableDocs()
		return
	}

	if *versionFlag {
		fmt.Println("Version:", config.Conf.Info.Version)
		return
	}

	if *createEPMFlag {
		nodepkg.CreateServerEPM()
		saveConfigAndSendSIGHUP()
		return
	}

	if *outputEPMFlag {
		nodepkg.ReadServerEPM(*outputQRFlag)
		return
	}

	processPrivateKeyFlags(importPrivateKeyMnemonicPath, importPrivateKeyHexPath, exportPrivateKeyMnemonic, exportPrivateKeyHex)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node, err := nodepkg.NewSDNNode(ctx, "")
	if err != nil {
		handleNodeInitializationError(err)
		return
	}

	if err := node.Start(ctx); err != nil {
		fmt.Printf("Error starting node: %v\n", err)
		os.Exit(1)
	}

	// Determine the socket path from the configuration's root folder
	socketPath := filepath.Join(config.Conf.Datastore.Directory, "app.sock")
	os.Remove(socketPath) // Remove the existing socket file if present

	// Start the socket server in a goroutine
	go startSocketServer(socketPath, node)

	server := web.NewAPI(node)
	server.Start()

	setupGracefulShutdown(ctx, node, cancel)

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

func listPeersAndFileIDs() {
	for _, peerPin := range config.Conf.IPFS.PeerPins {
		fmt.Printf("PeerID: %s, FileIDs: %v\n", peerPin.PeerID, peerPin.FileIDs)
	}
}

func saveConfigAndSendSIGHUP() {
	err := config.Conf.SaveConfigToFile()
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
	for _, standard := range config.Conf.Info.Standards {
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
	flag.VisitAll(func(f *flag.Flag) {
		fmt.Fprintf(os.Stderr, "  -%s\t%s\n", f.Name, f.Usage)
	})
}

func init() {
	flag.Usage = flagUsage
}
