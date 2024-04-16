package protocols

import (
	"bufio"
	"context"
	"fmt"
	"io"

	sds_utils "github.com/DigitalArsenal/space-data-network/internal/node/sds_utils"
	"github.com/DigitalArsenal/space-data-network/internal/node/server_info"
	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	"github.com/DigitalArsenal/space-data-network/serverconfig"
	files "github.com/ipfs/boxo/files"
	boxoPath "github.com/ipfs/boxo/path"
	"github.com/ipfs/kubo/core"
	"github.com/ipfs/kubo/core/coreapi"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const IDExchangeProtocol = protocol.ID("/space-data-network/id-exchange/1.0.0")

func HandlePNMExchange(s network.Stream) {
	_ = s.Conn().RemotePeer()

	// Create a buffered writer for the stream
	rw := bufio.NewWriter(s)
	pnmBytes, _ := server_info.LoadPNMFromFile()
	// Write the generated PNM data to the stream
	_, err := rw.Write(pnmBytes)
	if err != nil {
		//fmt.Printf("Error writing PNM to peer %s: %s\n", peerID, err)
		s.Reset()
		return
	}

	// Flush the data to ensure it's sent
	err = rw.Flush()
	if err != nil {
		//fmt.Printf("Error flushing PNM to peer %s: %s\n", peerID, err)
		s.Reset()
		return
	}

	//fmt.Printf("PNM sent to peer %s\n", peerID)
	s.Close()
}

func RequestPNM(ctx context.Context, h host.Host, i *core.IpfsNode, peerID peer.ID) error {

	//fmt.Printf("Requesting PNM from %s\n", peerID)
	s, err := h.NewStream(ctx, peerID, IDExchangeProtocol)
	if err != nil {
		return fmt.Errorf("failed to open stream to peer %s: %v", peerID, err)
	}
	defer s.Close()

	pnmBytes, _ := sds_utils.ReadDataFromSource(ctx, s)

	// Variables to hold deserialized data and values outside the closure
	var pnm *PNM.PNM
	var cid string
	//var publicKeyHex, filePath string
	var panicErr error

	// Use a deferred function to encapsulate panic recovery
	func() {
		defer func() {
			if r := recover(); r != nil {
				//fmt.Printf("Recovered from panic while deserializing PNM or accessing fields: %v\n", r)
				panicErr = fmt.Errorf("panic occurred: %v", r)
			}
		}()

		// Deserialize PNM from the stream.
		pnm, err = sds_utils.DeserializePNM(ctx, pnmBytes)
		if err != nil {
			panic(fmt.Errorf("failed to deserialize PNM: %v", err)) // Convert error to panic for recovery
		}

		// Access the PNM fields within the same deferred function.
		cid = string(pnm.CID())
		pPubKey, err := peerID.ExtractPublicKey()
		if err != nil {
			return
		}

		pPubKeyRaw, err := pPubKey.Raw()
		if err != nil {
			return
		}
		serverconfig.VerifyPNMSignature(pnm, pPubKeyRaw)
	}()

	if panicErr != nil {
		return panicErr // Return the captured panic error
	}
	if err != nil {
		return err // Return deserialization error if it occurred
	}

	// Create a new path object using the full IPFS path
	path, err := boxoPath.NewPath(cid)
	if err != nil {
		return fmt.Errorf("failed to parse IPFS path: %v", err)
	}

	// Initialize the CoreAPI instance
	api, err := coreapi.NewCoreAPI(i)
	if err != nil {
		return fmt.Errorf("failed to create IPFS CoreAPI instance: %v", err)
	}

	// Use the CoreAPI to get the content from the specified path
	rootNode, err := api.Unixfs().Get(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to fetch content from IPFS: %v", err)
	}

	file, ok := rootNode.(files.File)
	if !ok {
		return fmt.Errorf("fetched IPFS node is not a file")
	}

	content, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read content from IPFS file: %v", err)
	}

	peerEPM, _ := sds_utils.DeserializeEPM(ctx, content)

	if len(peerEPM.EMAIL()) == 0 {
		//TODO error out or check another field
		return nil
	}

	return serverconfig.Conf.UpdateEpmCidForPeer(ctx, api, peerID, cid)
}
