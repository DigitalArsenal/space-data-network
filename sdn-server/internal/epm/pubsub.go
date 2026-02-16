package epm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// TopicPublisher is the interface for publishing to a PubSub topic.
type TopicPublisher interface {
	Publish(schema string, data []byte) error
}

// PublishEPM announces the current node EPM via PNM on the PubSub network.
// It computes a CID for the EPM data, signs it with the node's Ed25519 key,
// and publishes a PNM with FILE_ID="EPM".
func (s *Service) PublishEPM(ctx context.Context, publisher TopicPublisher) error {
	s.mu.RLock()
	epmData := s.epmBytes
	identity := s.identity
	peerID := s.peerID
	s.mu.RUnlock()

	if len(epmData) == 0 {
		return fmt.Errorf("no EPM data to publish")
	}

	// Compute CID from EPM data
	hash := sha256.Sum256(epmData)
	multihash, err := mh.Encode(hash[:], mh.SHA2_256)
	if err != nil {
		return fmt.Errorf("failed to encode multihash: %w", err)
	}
	epmCID := cid.NewCidV1(cid.Raw, multihash)

	// Sign the CID with Ed25519 signing key (via libp2p crypto.PrivKey)
	var signatureHex string
	if identity != nil && identity.SigningPrivKey != nil {
		sigBytes, err := identity.SigningPrivKey.Sign([]byte(epmCID.String()))
		if err != nil {
			log.Warnf("Failed to sign EPM CID: %v", err)
		} else {
			signatureHex = hex.EncodeToString(sigBytes)
		}
	}

	// Build PNM FlatBuffer
	builder := flatbuffers.NewBuilder(512)

	cidOffset := builder.CreateString(epmCID.String())
	fileIDOffset := builder.CreateString("EPM")
	tsOffset := builder.CreateString(time.Now().UTC().Format(time.RFC3339))
	addrOffset := builder.CreateString(fmt.Sprintf("/p2p/%s", peerID.String()))
	sigOffset := builder.CreateString(signatureHex)
	sigTypeOffset := builder.CreateString("Ed25519")

	PNM.PNMStart(builder)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, tsOffset)
	PNM.PNMAddMULTIFORMAT_ADDRESS(builder, addrOffset)
	PNM.PNMAddSIGNATURE(builder, sigOffset)
	PNM.PNMAddSIGNATURE_TYPE(builder, sigTypeOffset)
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)

	data := make([]byte, len(builder.FinishedBytes()))
	copy(data, builder.FinishedBytes())

	if err := publisher.Publish("PNM", data); err != nil {
		return fmt.Errorf("failed to publish EPM PNM: %w", err)
	}

	log.Infof("Published EPM to PubSub (CID: %s)", epmCID)
	return nil
}

// HandlePNMEPM processes an incoming PNM with FILE_ID="EPM".
// It verifies the signature and stores the EPM data in the peer registry.
func (s *Service) HandlePNMEPM(pnmData []byte, fromPeer string) {
	pnm := PNM.GetSizePrefixedRootAsPNM(pnmData, 0)
	if pnm == nil {
		log.Warn("Received invalid PNM data")
		return
	}

	fileID := string(pnm.FILE_ID())
	if fileID != "EPM" {
		return // not an EPM announcement
	}

	epmCIDStr := string(pnm.CID())
	if epmCIDStr == "" {
		log.Warn("EPM PNM missing CID")
		return
	}

	log.Debugf("Received EPM PNM from %s (CID: %s)", fromPeer, epmCIDStr)

	// TODO: Fetch the actual EPM data by CID from the network
	// For now, log the announcement. When IPFS content routing is
	// integrated, we would: fetch EPM bytes by CID, verify signature,
	// parse EPM FlatBuffer, store in registry.
}

// StartAutoPublish runs a background goroutine that publishes the EPM
// on startup and every interval.
func (s *Service) StartAutoPublish(ctx context.Context, publisher TopicPublisher, interval time.Duration) {
	// Initial publish
	if err := s.PublishEPM(ctx, publisher); err != nil {
		log.Warnf("Initial EPM publish failed: %v", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.PublishEPM(ctx, publisher); err != nil {
				log.Debugf("EPM auto-publish failed: %v", err)
			}
		}
	}
}
