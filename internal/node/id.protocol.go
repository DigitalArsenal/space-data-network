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
	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"

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

	// Variables to hold deserialized data and values outside the closure
	var pnm *PNM.PNM
	var cid, ethSignature, publicKeyHex, filePath string
	var panicErr error

	// Use a deferred function to encapsulate panic recovery
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("Recovered from panic while deserializing PNM or accessing fields: %v\n", r)
				panicErr = fmt.Errorf("panic occurred: %v", r)
			}
		}()

		// Deserialize PNM from the stream.
		pnm, err = spacedatastandards_utils.DeserializePNM(ctx, pnmBytes)
		if err != nil {
			panic(fmt.Errorf("failed to deserialize PNM: %v", err)) // Convert error to panic for recovery
		}

		// Access the PNM fields within the same deferred function.
		cid = string(pnm.CID())
		ethSignature = string(pnm.SIGNATURE()[2:])
	}()
	if panicErr != nil {
		return panicErr // Return the captured panic error
	}
	if err != nil {
		return err // Return deserialization error if it occurred
	}

	fmt.Printf("Received PNM from %s\n", peerID)
	fmt.Printf("with CID: %s\n", cid)
	fmt.Printf("ETH Signature: %s\n", ethSignature)

	hash := crypto.Keccak256Hash(pnm.CID())
	fmt.Println(hash.Hex())

	ethSignatureBytes, _ := hex.DecodeString(ethSignature)

	sigPublicKey, err := crypto.Ecrecover(hash.Bytes(), ethSignatureBytes)
	if err != nil {
		return fmt.Errorf("error Signature: %v", err)
	}

	pPubKey, err := peerID.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("error extracting public key: %v", err)
	}

	pPubKeyRaw, err := pPubKey.Raw()
	if err != nil {
		return fmt.Errorf("error getting raw public key: %v", err)
	}

	x, y := secp256k1.DecompressPubkey(pPubKeyRaw)
	if !bytes.Equal(append(x.Bytes(), y.Bytes()...), sigPublicKey[1:]) {
		return fmt.Errorf("public keys do not match")
	}

	fmt.Println("Public keys match")

	publicKeyHex = "0x" + hex.EncodeToString(append(x.Bytes(), y.Bytes()...))
	directoryPath := filepath.Join(config.Conf.Datastore.Directory, "data", publicKeyHex, "PNM")
	if err := os.MkdirAll(directoryPath, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	filePath = filepath.Join(directoryPath, cid)
	if err := os.WriteFile(filePath, pnmBytes, 0644); err != nil {
		return fmt.Errorf("failed to write PNM to file: %v", err)
	}

	fmt.Printf("PNM saved successfully to %s\n", filePath)

	return nil
}

/*
	TODO:
	- Add EPMs to data folder under EPM


*/
