package socket

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	nodepkg "github.com/DigitalArsenal/space-data-network/internal/node"
	config "github.com/DigitalArsenal/space-data-network/serverconfig"
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

func SendCommandToSocket(commandKey string, data []byte) {
	conn, err := net.Dial("unix", config.Conf.SocketServer.Path)
	if err != nil {
		fmt.Printf("Error connecting to socket: %v\n", err)
		return
	}
	defer conn.Close()

	// Prepare the data with length prefix
	dataLength := len(data)
	prefixedData := append([]byte(fmt.Sprintf("%d\n", dataLength)), data...)

	// Send the command and the length-prefixed data
	fmt.Fprintf(conn, "%s\n", commandKey) // Send the command key followed by a newline
	conn.Write(prefixedData)              // Send the length-prefixed data
}

func handlePublicKey(conn net.Conn, args []byte) {
	fmt.Println(daemonNode.Host.ID())
	// Add your implementation here
}

func listPins(conn net.Conn, args []byte) {

	/*pinnedFiles, _ := n.ListPinnedFiles(ctx)
	fmt.Println("pinnedFiles: ")
	fmt.Println(pinnedFiles)*/

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
	// Implement the logic to list all peers and their associated file IDs
	fmt.Fprintln(conn, "List peers command received")
	// Add your implementation here
}

func HandleSocketConnection(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)

	// Read the command key first
	commandKey, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(conn, "Error reading command key: %v\n", err)
		return
	}
	commandKey = strings.TrimSpace(commandKey)

	// Read the length of the data
	lengthStr, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(conn, "Error reading data length: %v\n", err)
		return
	}
	lengthStr = strings.TrimSpace(lengthStr)
	dataLength, err := strconv.Atoi(lengthStr)
	if err != nil {
		fmt.Fprintf(conn, "Invalid data length: %v\n", err)
		return
	}

	// Read the actual data
	data := make([]byte, dataLength)
	_, err = io.ReadFull(reader, data)
	if err != nil {
		fmt.Fprintf(conn, "Error reading data: %v\n", err)
		return
	}

	if handler, ok := CommandRegistry[commandKey]; ok {
		handler(conn, data)
	} else {
		fmt.Fprintln(conn, "Unknown command")
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
