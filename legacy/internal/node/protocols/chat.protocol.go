package protocols

import (
	"bufio"
	"context"
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const ChatProtocolID = protocol.ID("/space-data-network/chat/1.0.0")

// StartChatSession starts a chat session with a specified peer.
func StartChatSession(ctx context.Context, h host.Host, peerID peer.ID) error {
	fmt.Printf("Starting chat with %s...\n", peerID)

	// Create a new stream to the specified peer using the chat protocol.
	s, err := h.NewStream(ctx, peerID, ChatProtocolID)
	if err != nil {
		return fmt.Errorf("failed to open stream to peer %s: %v", peerID, err)
	}
	defer s.Close()

	// Start a goroutine to handle incoming messages.
	go handleIncomingMessages(s)

	// Capture input from the command line.
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		msg := scanner.Text()
		if _, err := s.Write([]byte(msg + "\n")); err != nil {
			return fmt.Errorf("failed to send message: %v", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading from stdin: %v", err)
	}

	return nil
}

// handleIncomingMessages reads messages from the stream and prints them to the console.
func handleIncomingMessages(s network.Stream) {
	reader := bufio.NewReader(s)
	for {
		msg, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("Error reading from stream:", err)
			return
		}
		fmt.Print("Received: " + msg)
	}
}

// HandleChatProtocol handles incoming chat streams.
func HandleChatProtocol(s network.Stream) {
	fmt.Printf("New chat initiated from peer %s\n", s.Conn().RemotePeer())
	handleIncomingMessages(s)
}
