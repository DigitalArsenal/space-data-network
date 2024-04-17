package socket

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	nodepkg "github.com/DigitalArsenal/space-data-network/internal/node"
	sds_utils "github.com/DigitalArsenal/space-data-network/internal/node/sds_utils"
	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/EPM"
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
	"GET_EPM":           handleGetEPM,
	// Add more commands and their handlers here
}

type EPMData struct {
	PeerID   string
	Email    string
	EPM      *EPM.EPM
	EPMBytes []byte
	Valid    bool
	Error    error
}

func fetchEPMDataByCID(peerID, cid string) EPMData {
	path, err := boxoPath.NewPath(cid)
	if err != nil {
		return EPMData{PeerID: peerID, Valid: false, Error: fmt.Errorf("error parsing IPFS path: %v", err)}
	}

	api, err := coreapi.NewCoreAPI(daemonNode.IPFS)
	if err != nil {
		return EPMData{PeerID: peerID, Valid: false, Error: fmt.Errorf("error creating IPFS CoreAPI instance: %v", err)}
	}

	rootNode, err := api.Unixfs().Get(daemonNode.Ctx, path)
	if err != nil {
		return EPMData{PeerID: peerID, Valid: false, Error: fmt.Errorf("error fetching content from IPFS: %v", err)}
	}

	file, ok := rootNode.(files.File)
	if !ok {
		return EPMData{PeerID: peerID, Valid: false, Error: fmt.Errorf("fetched IPFS node is not a file")}
	}

	content, err := io.ReadAll(file)
	if err != nil {
		return EPMData{PeerID: peerID, Valid: false, Error: fmt.Errorf("failed to read content from IPFS file: %v", err)}
	}

	peerEPM, _ := sds_utils.DeserializeEPM(daemonNode.Ctx, content)
	return EPMData{
		PeerID:   peerID,
		Email:    string(peerEPM.EMAIL()),
		EPM:      peerEPM,
		EPMBytes: content,
		Valid:    true,
	}
}

func handleServerEPM(conn net.Conn, args []byte) {
	epmBytes := nodepkg.CreateServerEPM(context.Background(), args, daemonNode)
	epm, _ := sds_utils.DeserializeEPM(daemonNode.Ctx, epmBytes)
	daemonNode.EPM = epm
	sendResponse(conn, "Server EPM Created with length: "+string(len(epmBytes)))
}

func handleGetEPM(conn net.Conn, args []byte) {
	identifier := string(args)
	var found bool

	for peerID, CID := range config.Conf.IPFS.PeerEPM {
		epmData := fetchEPMDataByCID(peerID, CID)
		if epmData.Valid && (epmData.PeerID == identifier || strings.EqualFold(epmData.Email, identifier)) {
			sendResponse(conn, hex.EncodeToString(epmData.EPMBytes))
			found = true
			break
		}
	}

	if !found {
		fmt.Fprintf(conn, "No EPM found for the given identifier\x04")
	}
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

	// Assuming EMAIL and MULTIFORMAT_ADDRESS are accessible through currentEPM

	fmt.Fprintf(&output, "(Current Node)\n")
	fmt.Fprintf(&output, "Email:    %s\n", string(daemonNode.EPM.EMAIL()))
	fmt.Fprintf(&output, "PeerID:   (%s)\n", daemonNode.Host.ID())
	for i := 0; i < daemonNode.EPM.MULTIFORMAT_ADDRESSLength(); i++ {
		fmt.Fprintf(&output, "IPNS:     %s\n", string(daemonNode.EPM.MULTIFORMAT_ADDRESS(i)))
	}
	fmt.Fprint(&output, "\n")
	for peerID, CID := range config.Conf.IPFS.PeerEPM {
		peerEPMData := fetchEPMDataByCID(peerID, CID)

		peerEPM := peerEPMData.EPM

		if len(peerEPM.EMAIL()) == 0 {
			fmt.Println("No Email")

		} else {
			fmt.Fprintf(&output, "Email:    %s\n", string(peerEPM.EMAIL()))
		}
		displayPeerID := peerID
		fmt.Fprintf(&output, "PeerID:   (%s)\n", displayPeerID)
		fmt.Fprintf(&output, "CID:      %s\n", CID)

		for i := 0; i < peerEPM.MULTIFORMAT_ADDRESSLength(); i++ {
			fmt.Fprintf(&output, "IPNS:      %s\n", string(peerEPM.MULTIFORMAT_ADDRESS(i)))
		}
		fmt.Fprint(&output, "\n")
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

	// Setup a channel to read response and a timer for timeout
	responseChan := make(chan []byte, 1)
	go func() {
		responseReader := bufio.NewReader(conn)
		response, err := responseReader.ReadString('\x04') // Read until EOT character
		if err != nil {
			fmt.Printf("Failed to read response: %v\n", err)
			responseChan <- []byte("")
			return
		}
		// Send response to channel, trimming the EOT character
		responseChan <- []byte(strings.TrimSuffix(response, "\x04"))
	}()

	// Use select to wait for response or timeout
	select {
	case response := <-responseChan:
		return response
	case <-time.After(10 * time.Second):
		fmt.Println("Response timeout: no response from server within 10 seconds.")
		return []byte("")
	}
}

func sendResponse(conn net.Conn, data interface{}) {
	var message []byte

	// Check the type of data and process accordingly
	switch v := data.(type) {
	case string:
		// If data is a string, convert it to []byte and append the EOT character
		message = append([]byte(v), '\x04')
	case []byte:
		// If data is already []byte, simply append the EOT character
		message = append(v, '\x04')
	default:
		// If data is neither []byte nor string, log an error or handle it appropriately
		fmt.Println("sendResponse received data of unsupported type")
		return
	}

	// Write the message with EOT to the connection
	if _, err := conn.Write(message); err != nil {
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
