package node

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const IDExchangeProtocol = protocol.ID("/space-data-network/id-exchange/1.0.0")

func SetupPNMExchange(n *Node) {
	n.Host.SetStreamHandler(IDExchangeProtocol, handlePNMExchange)
}

// generatePNM simulates generating a PNMCOLLECTION
// It creates a new PNM, adds a dummy CID string and a dummy ETH signature,
// and returns the collection as a binary FlatBuffer
func generatePNM() []byte {
	builder := flatbuffers.NewBuilder(0)

	// Create a PNM with dummy values
	pnm := GeneratePNM(
		builder,
		"/ip4/127.0.0.1/tcp/4001", // Dummy multiformat address
		"QmTmVtboD4DBn5nXAyH6GkSbjTsG47jxjsXz6KXLzKdW9X", // Dummy CID
		"0x123456789abcdef", // Dummy Ethereum digital signature
	)

	// Finish the FlatBuffer with the PNMCOLLECTION
	builder.FinishSizePrefixedWithFileIdentifier(pnm, []byte(PNMFID))

	// Return the byte slice containing the encoded PNMCOLLECTION
	return builder.FinishedBytes()
}

func handlePNMExchange(s network.Stream) {
	peerID := s.Conn().RemotePeer()
	fmt.Println("handlePNMExchange with peer:", peerID)

	// Generate PNM
	pnmData := generatePNM()
	fmt.Println(pnmData)
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

	// Assuming the first 4 bytes represent the total size including the file identifier
	totalSizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(s, totalSizeBuf); err != nil {
		return fmt.Errorf("failed to read total size prefix: %v", err)
	}
	totalSize := binary.LittleEndian.Uint32(totalSizeBuf)

	// Read the FlatBuffer data including the file identifier
	data := make([]byte, totalSize)
	if _, err := io.ReadFull(s, data); err != nil {
		return fmt.Errorf("failed to read PNM data: %v", err)
	}

	// Use GetRootAsPNM to deserialize the data, skipping the size prefix and file identifier
	pnm := PNM.GetRootAsPNM(data, 0) // Start at the beginning of the actual FlatBuffer content

	// Access the PNM fields
	cid := string(pnm.CID())
	ethSignature := string(pnm.ETH_DIGITAL_SIGNATURE())
	fmt.Printf("Received PNM from %s\n", peerID)
	fmt.Printf("with CID: %s\n", cid)
	fmt.Printf("ETH Signature: %s\n", ethSignature)

	return nil
}
