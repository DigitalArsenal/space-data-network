package node

import (
	"bufio"
	"context"
	"fmt"

	spacedatastandards_utils "github.com/DigitalArsenal/space-data-network/internal/node/spacedatastandards_utils"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const IDExchangeProtocol = protocol.ID("/space-data-network/id-exchange/1.0.0")

func SetupPNMExchange(n *Node) {
	n.Host.SetStreamHandler(IDExchangeProtocol, n.handlePNMExchange)
}

func (n *Node) handlePNMExchange(s network.Stream) {
	peerID := s.Conn().RemotePeer()
	fmt.Println("handlePNMExchange with peer:", peerID)

	pnmData, _ := n.KeyStore.LoadPNM()

	// Create a buffered writer for the stream
	rw := bufio.NewWriter(s)

	// Write the generated PNM data to the stream
	_, err := rw.Write(pnmData)
	if err != nil {
		fmt.Printf("Error writing PNM to peer %s: %s\n", peerID, err)
		s.Reset()
		return
	}

	// Flush the data to ensure it's sent
	err = rw.Flush()
	if err != nil {
		fmt.Printf("Error flushing PNM to peer %s: %s\n", peerID, err)
		s.Reset()
		return
	}

	fmt.Printf("PNM sent to peer %s\n", peerID)
	s.Close()
}

func RequestPNM(ctx context.Context, h host.Host, peerID peer.ID) error {
	fmt.Printf("Requesting PNM from %s\n", peerID)
	s, err := h.NewStream(ctx, peerID, IDExchangeProtocol)
	if err != nil {
		return fmt.Errorf("failed to open stream to peer %s: %v", peerID, err)
	}
	defer s.Close()

	// Deserialize PNM from the stream.
	pnm, err := spacedatastandards_utils.DeserializePNM(ctx, s)
	if err != nil {
		return fmt.Errorf("failed to deserialize PNM: %v", err)
	}

	// Access the PNM fields.
	cid := string(pnm.CID())
	ethSignature := string(pnm.ETH_DIGITAL_SIGNATURE())
	fmt.Printf("Received PNM from %s\n", peerID)
	fmt.Printf("with CID: %s\n", cid)
	fmt.Printf("ETH Signature: %s\n", ethSignature)

	return nil
}
