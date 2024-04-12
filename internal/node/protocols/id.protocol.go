package protocols

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"

	sds_utils "github.com/DigitalArsenal/space-data-network/internal/node/sds_utils"
	"github.com/DigitalArsenal/space-data-network/internal/node/server_info"
	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	"github.com/DigitalArsenal/space-data-network/serverconfig"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
	files "github.com/ipfs/boxo/files"
	boxoPath "github.com/ipfs/boxo/path"
	"github.com/ipfs/kubo/core"
	"github.com/ipfs/kubo/core/coreapi"
	"github.com/ipfs/kubo/core/coreiface/options"
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
	var cid, ethSignature string
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
		ethSignature = string(pnm.SIGNATURE()[2:])
	}()
	if panicErr != nil {
		return panicErr // Return the captured panic error
	}
	if err != nil {
		return err // Return deserialization error if it occurred
	}

	//fmt.Printf("Received PNM from %s\n", peerID)
	//fmt.Printf("with CID: %s\n", cid)
	//fmt.Printf("ETH Signature: %s\n", ethSignature)

	hash := crypto.Keccak256Hash(pnm.CID())
	//fmt.Println(hash.Hex())

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

	if cid == "" {
		fmt.Println("Public keys match")
		fmt.Println(cid)
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

	oldCID, _ := serverconfig.Conf.UpdateEpmCidForPeer(peerID, cid)

	if oldCID != "" && oldCID != cid {
		// Unpin the old CID
		oldPath, err := boxoPath.NewPath(oldCID)
		if err != nil {
			log.Printf("Failed to parse old CID path: %v", err)
			// Handle error appropriately
			return nil
		}

		err = api.Pin().Rm(ctx, oldPath, options.Pin.RmRecursive(true))
		if err != nil {
			log.Printf("Failed to unpin old content in IPFS: %v", err)
			// Handle error appropriately
		} else {
			log.Println("Successfully unpinned old CID:", oldCID)
		}
	}

	// Attempt to pin the rootNode
	err = api.Pin().Add(ctx, path, options.Pin.Recursive(true))

	if err != nil {
		return fmt.Errorf("failed to pin content in IPFS: %v", err)
	}

	return nil
}

func formatEmail(keyHex, domain string) string {
	var formattedKey string
	if len(keyHex) > 10 {
		// Extract the first 5 characters, concatenate with an ellipsis, and the last 5 characters
		formattedKey = fmt.Sprintf("%s...%s", keyHex[:5], keyHex[len(keyHex)-5:])
	} else {
		// If keyHex is not long enough, use it as is
		formattedKey = keyHex
	}
	return fmt.Sprintf("%s@%s", formattedKey, domain)
}
