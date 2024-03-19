package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	config "github.com/DigitalArsenal/space-data-network/configs"
	spacedatastandards_utils "github.com/DigitalArsenal/space-data-network/internal/node/spacedatastandards_utils"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
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

	pnmBytes, _ := spacedatastandards_utils.ReadDataFromSource(ctx, s)
	// Deserialize PNM from the stream.
	pnm, err := spacedatastandards_utils.DeserializePNM(ctx, pnmBytes)
	if err != nil {
		return fmt.Errorf("failed to deserialize PNM: %v", err)
	}

	// Access the PNM fields.
	cid := string(pnm.CID())
	ethSignature := string(pnm.SIGNATURE()[2:])
	fmt.Printf("Received PNM from %s\n", peerID)
	fmt.Printf("with CID: %s\n", cid)
	fmt.Printf("ETH Signature: %s\n", ethSignature)
	fmt.Println(config.Conf.Datastore.Directory)

	hash := crypto.Keccak256Hash(pnm.CID())
	fmt.Println(hash.Hex())

	ethSignatureBytes, _ := hex.DecodeString(ethSignature)

	sigPublicKey, err := crypto.Ecrecover(hash.Bytes(), ethSignatureBytes)
	if err != nil {
		fmt.Println("Error Signature", err)
	}

	pPubKey, _ := peerID.ExtractPublicKey()

	pPubKeyRaw, _ := pPubKey.Raw()

	x, y := secp256k1.DecompressPubkey(pPubKeyRaw)

	if bytes.Equal(append(x.Bytes(), y.Bytes()...), sigPublicKey[1:]) {
		fmt.Println("Public keys match")

		// Convert the public key (x, y) to hex with 0x prefix
		publicKeyHex := "0x" + hex.EncodeToString(append(x.Bytes(), y.Bytes()...))

		// Construct the directory path using the hex representation of the public key
		directoryPath := filepath.Join(config.Conf.Datastore.Directory, "data", publicKeyHex, "PNM")

		// Ensure the directory exists
		if err := os.MkdirAll(directoryPath, 0755); err != nil {
			return fmt.Errorf("failed to create directory: %v", err)
		}

		// Define the full path to the new file using the CID as the filename
		filePath := filepath.Join(directoryPath, cid)

		// Save the PNM content to the file
		// Assuming pnm.Content() gives you the content of the PNM
		if err := os.WriteFile(filePath, pnmBytes, 0644); err != nil {
			return fmt.Errorf("failed to write PNM to file: %v", err)
		}

		fmt.Printf("PNM saved successfully to %s\n", filePath)
	}

	return nil
}

/*
	TODO:
	- Add EPMs to data folder under EPM


*/
