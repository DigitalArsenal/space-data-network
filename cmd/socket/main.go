package socket

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	nodepkg "github.com/DigitalArsenal/space-data-network/internal/node"
	sds_utils "github.com/DigitalArsenal/space-data-network/internal/node/sds_utils"
	config "github.com/DigitalArsenal/space-data-network/serverconfig"
	files "github.com/ipfs/boxo/files"
	boxoPath "github.com/ipfs/boxo/path"
	"github.com/ipfs/kubo/core/coreapi"
)

type Node = nodepkg.Node

var daemonNode *Node

type CommandHandler func(net.Conn, []byte)

var CommandRegistry = map[string]CommandHandler{
	"ADD_PEER":          handleAddPeer,
	"REMOVE_PEER":       handleRemovePeer,
	"LIST_PEERS":        handleListPeers,
	"PUBLIC_KEY":        handlePublicKey,
	"CREATE_SERVER_EPM": handleServerEPM,
	// Add more commands and their handlers here
}

func handleServerEPM(conn net.Conn, args []byte) {
	_ = nodepkg.CreateServerEPM(context.Background(), args, daemonNode)
}

func handlePublicKey(conn net.Conn, args []byte) {
	fmt.Println(daemonNode.Host.ID())
	// Add your implementation here
}

func handleAddPeer(conn net.Conn, args []byte) {
	// Implement the logic to add a peer and its associated file IDs
	// The args slice will contain the command arguments, e.g., peer ID and file IDs
	fmt.Fprintln(conn, "Add peer command received")
	// Add your implementation here
}

func handleRemovePeer(conn net.Conn, args []byte) {
	// Implement the logic to remove a peer and its associated file IDs
	fmt.Fprintln(conn, "Remove peer command received")
	// Add your implementation here
}

func handleListPeers(conn net.Conn, args []byte) {
	var output strings.Builder

	index := 1 // Start an index for numbering the peers

	// Assume PeerEPM maps PeerID to CID
	for peerID, CID := range config.Conf.IPFS.PeerEPM {
		// Create a new path object using the full IPFS path
		path, err := boxoPath.NewPath(CID)
		if err != nil {
			fmt.Println("failed to parse IPFS path: %v", err)
		}

		// Initialize the CoreAPI instance
		api, err := coreapi.NewCoreAPI(daemonNode.IPFS)
		if err != nil {
			fmt.Println("failed to create IPFS CoreAPI instance: %v", err)
		}

		// Use the CoreAPI to get the content from the specified path
		rootNode, err := api.Unixfs().Get(daemonNode.Ctx, path)
		if err != nil {
			fmt.Println("failed to fetch content from IPFS: %v", err)
		}

		file, ok := rootNode.(files.File)
		if !ok {
			fmt.Println("fetched IPFS node is not a file")
		}

		content, err := io.ReadAll(file)
		if err != nil {
			fmt.Println("failed to read content from IPFS file: %v", err)
		}

		peerEPM, _ := sds_utils.DeserializeEPM(daemonNode.Ctx, content)

		if len(peerEPM.EMAIL()) == 0 {
			//TODO error out or check another field
			fmt.Println("No Email")

		}
		// Format the PeerID to show only part of it for readability, assuming it's long enough
		displayPeerID := peerID
		/*if len(peerID) > 10 {
			displayPeerID = peerID[:5] + "..." + peerID[len(peerID)-5:]
		}*/

		fmt.Fprintf(&output, "Index:    %d\n", index)
		fmt.Fprintf(&output, "Email:    %s\n", string(peerEPM.EMAIL()))
		fmt.Fprintf(&output, "PeerID:   (%s)\n", displayPeerID)
		fmt.Fprintf(&output, "CID:      %s\n\n", CID)
		index++
	}
	sendResponse(conn, output.String())
}

func SendCommandToSocket(commandKey string, data []byte) []byte {
	conn, err := net.Dial("unix", config.Conf.SocketServer.Path)
	if err != nil {
		fmt.Printf("Error connecting to socket: %v\n", err)
		return []byte("")
	}
	defer conn.Close()

	// Ensure the command key is exactly 32 bytes long
	commandKey = fmt.Sprintf("%-32s", commandKey)
	if len(commandKey) > 32 {
		commandKey = commandKey[:32]
	}

	// Prepare the message: length + command key + data + EOT
	messageLength := 8 + 32 + len(data) + 1 // Length bytes + command bytes + data bytes + EOT
	message := make([]byte, 0, messageLength)
	message = append(message, []byte(fmt.Sprintf("%08d", messageLength))...) // Message length padded to 8 bytes
	message = append(message, []byte(commandKey)...)                         // Command key
	message = append(message, data...)                                       // Actual data
	message = append(message, '\x04')                                        // EOT

	// Send the constructed message
	if _, err := conn.Write(message); err != nil {
		fmt.Printf("Failed to send message: %v\n", err)
		return []byte("")
	}

	// Wait for a response
	responseReader := bufio.NewReader(conn)
	response, err := responseReader.ReadString('\x04') // Read until EOT character
	if err != nil {
		fmt.Printf("Failed to read response: %v\n", err)
		return []byte("")
	}

	// Print the response to the console, trimming the EOT character
	return []byte(strings.TrimSuffix(response, "\x04"))
}

func sendResponse(conn net.Conn, data string) {
	// Append the EOT character to the data
	message := data + "\x04"
	// Write the message with EOT to the connection
	if _, err := conn.Write([]byte(message)); err != nil {
		fmt.Printf("Failed to send response: %v\n", err)
	}
	conn.Close()
}

func HandleSocketConnection(conn net.Conn) {
	time.AfterFunc(10*time.Second, func() {
		conn.Close()
	})

	reader := bufio.NewReader(conn)

	// Read the message length (first 8 bytes)
	lengthBytes := make([]byte, 8)
	if _, err := io.ReadFull(reader, lengthBytes); err != nil {
		fmt.Printf("Error reading message length: %v\n", err)
		return
	}
	messageLength, err := strconv.Atoi(string(lengthBytes))
	if err != nil {
		fmt.Printf("Invalid message length: %v\n", err)
		return
	}

	// Read the command (next 32 bytes)
	commandBytes := make([]byte, 32)
	if _, err := io.ReadFull(reader, commandBytes); err != nil {
		fmt.Printf("Error reading command: %v\n", err)
		return
	}
	command := strings.TrimSpace(string(commandBytes))

	// Read the data (length - 41 bytes to account for the EOT)
	dataLength := messageLength - 41 // total length - 8 (length bytes) - 32 (command bytes) - 1 (EOT)
	data := make([]byte, dataLength)
	if _, err := io.ReadFull(reader, data); err != nil {
		fmt.Printf("Error reading data: %v\n", err)
		return
	}

	// Check for EOT character
	eot := make([]byte, 1)
	if _, err := io.ReadFull(reader, eot); err != nil || eot[0] != '\x04' {
		fmt.Printf("Error reading EOT or incorrect EOT: %v\n", err)
		return
	}

	// Handle the command
	if handler, ok := CommandRegistry[command]; ok {
		handler(conn, data)
	} else {
		fmt.Fprintf(conn, "Unknown command")
	}
}

func StartSocketServer(socketPath string, node *Node) {

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		fmt.Printf("Failed to listen on socket: %v\n", err)
		return
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("Failed to accept connection: %v\n", err)
			continue
		}
		daemonNode = node
		go HandleSocketConnection(conn)
	}
}
