package protocols

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const IDExchangeProtocol = protocol.ID("/space-data-network/id-exchange/1.0.0")

func SetupPNMExchange(h host.Host) {
	fmt.Printf("Setting up PNM exchange for local peer %s\n", h.ID())
	h.SetStreamHandler(IDExchangeProtocol, handlePNMExchange)
}

func handlePNMExchange(s network.Stream) {
	peerID := s.Conn().RemotePeer()
	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))

	// Read the message from the stream
	message, err := rw.ReadString('\n')
	if err != nil {
		fmt.Println("Error reading from buffer")
		s.Close()
		return
	}

	if message != "ok\n" {
		fmt.Printf("Received message: %s from %s\n", message, peerID)
	}

	// Send a response back (either "ok" or a blank string)
	response := "ok\n" // or response := "\n" for a blank string
	_, err = rw.WriteString(response)
	if err != nil {
		s.Close()
		return
	}
	err = rw.Flush()
	if err != nil {
		s.Close()
		return
	}

	s.Close()
}

func RequestPNM(ctx context.Context, h host.Host, peerID peer.ID) error {
	s, err := h.NewStream(ctx, peerID, IDExchangeProtocol)
	if err != nil {
		return fmt.Errorf("failed to open stream to peer %s: %w", peerID, err)
	}
	defer s.Close()

	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))

	// Send "PNM" message
	message := fmt.Sprintf("PNM %s\n", h.ID())
	_, err = rw.WriteString(message)
	if err != nil {
		return fmt.Errorf("failed to write PNM to stream: %w", err)
	}
	err = rw.Flush()
	if err != nil {
		return fmt.Errorf("failed to flush PNM to stream: %w", err)
	}

	// Read the response
	response, err := rw.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("failed to read response from stream: %w", err)
	}

	// Avoid printing "ok" response to keep terminal output clean
	if response != "ok\n" {
		fmt.Printf("Received response: %s", response)
	}

	return nil
}
