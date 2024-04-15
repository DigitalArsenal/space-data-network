package pubsub

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

type ServerEPM struct{}

func (p *ServerEPM) Test(msg *pubsub.Message, pnm *PNM.PNM) bool {
	ethSignature := string(pnm.SIGNATURE()[2:])
	hash := crypto.Keccak256Hash(pnm.CID()) // Assuming CID() returns []byte of the CID
	ethSignatureBytes, _ := hex.DecodeString(ethSignature)

	sigPublicKey, err := crypto.Ecrecover(hash.Bytes(), ethSignatureBytes)
	if err != nil {
		fmt.Printf("Signature verification failed: %v\n", err)
		return false
	}

	pPubKey, err := msg.ReceivedFrom.ExtractPublicKey()
	if err != nil {
		fmt.Printf("Error extracting public key from peer ID: %v\n", err)
		return false
	}

	pPubKeyRaw, err := pPubKey.Raw()
	if err != nil {
		fmt.Printf("Error getting raw public key: %v\n", err)
		return false
	}

	x, y := secp256k1.DecompressPubkey(pPubKeyRaw)
	if !bytes.Equal(append(x.Bytes(), y.Bytes()...), sigPublicKey[1:]) {
		fmt.Println("Public keys do not match")
		return false
	}

	fmt.Println("Public keys match, valid signature")
	return true
}

func (p *ServerEPM) Main(msg *pubsub.Message, pnm *PNM.PNM) {
	fmt.Printf("Handling server EPM with CID: %s\n", string(pnm.CID()))
}
