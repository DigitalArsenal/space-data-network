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
	h.SetStreamHandler(IDExchangeProtocol, handlePNMExchange)
}

func handlePNMExchange(s network.Stream) {
	fmt.Println("Received a PNM exchange request")
	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))

	// Read the message from the stream
	message, err := rw.ReadString('\n')
	if err != nil {
		fmt.Println("Error reading from buffer")
		return
	}

	fmt.Printf("Received message: %s", message)

	// Send a response back
	_, err = rw.WriteString("Received your PNM\n")
	if err != nil {
		fmt.Println("Error writing to buffer")
		return
	}
	err = rw.Flush()
	if err != nil {
		fmt.Println("Error flushing buffer")
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
	_, err = rw.WriteString("PNM\n")
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

	fmt.Printf("Received response: %s", response)

	return nil
}
