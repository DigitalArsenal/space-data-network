package pubsub

import (
	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	serverconfig "github.com/DigitalArsenal/space-data-network/serverconfig"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/rs/zerolog/log"
)

type ServerEPM struct{}

func (p *ServerEPM) Test(msg *pubsub.Message, pnm *PNM.PNM) bool {
	pPubKey, err := msg.ReceivedFrom.ExtractPublicKey()
	if err != nil {
		log.Info().Err(err).Msg("Error extracting public key from peer ID")
		return false
	}
	pPubKeyRaw, err := pPubKey.Raw()
	if err != nil {
		log.Info().Err(err).Msg("Error getting raw public key")
		return false
	}
	valid, err := serverconfig.VerifyPNMSignature(pnm, pPubKeyRaw)
	if err != nil {
		log.Info().Err(err).Msg("Error verifying PNM signature")
		return false
	}
	return valid
}

func (p *ServerEPM) Main(msg *pubsub.Message, pnm *PNM.PNM) {
	log.Printf("Handling server EPM with CID: %s\n", string(pnm.CID()))
	log.Printf("")
}
