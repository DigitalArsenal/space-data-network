package epm

import (
	"context"
	"encoding/binary"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/vcard"
)

const (
	// EPMExchangeProtocolID is the libp2p protocol for EPM exchange.
	EPMExchangeProtocolID = protocol.ID("/spacedatanetwork/epm-exchange/1.0.0")

	// streamReadDeadline is the max time to wait for a request.
	streamReadDeadline = 15 * time.Second
	// streamWriteDeadline is the max time to send a response.
	streamWriteDeadline = 10 * time.Second
	// maxEPMSize is the max EPM binary size (64 KB).
	maxEPMSize = 64 * 1024

	// Status codes
	statusOK       uint32 = 0
	statusNotFound uint32 = 1
	statusError    uint32 = 2
)

// RegisterProtocol registers the EPM exchange stream handler on the host.
func (s *Service) RegisterProtocol(h host.Host) {
	h.SetStreamHandler(EPMExchangeProtocolID, s.handleStream)
	log.Infof("Registered EPM exchange protocol: %s", EPMExchangeProtocolID)
}

// handleStream handles incoming EPM exchange requests.
//
// Wire format:
//   - Request: requestLen(4 LE) + requestData(N)
//     If requestLen == 0: "send me your EPM"
//     If requestLen > 0: requestData is a peer ID string for "send me their EPM"
//   - Response: statusCode(4 LE) + epmLen(4 LE) + epmBytes(N)
func (s *Service) handleStream(stream network.Stream) {
	defer stream.Close()

	remotePeer := stream.Conn().RemotePeer().ShortString()

	// Read request
	_ = stream.SetReadDeadline(time.Now().Add(streamReadDeadline))

	var reqLen uint32
	if err := binary.Read(stream, binary.LittleEndian, &reqLen); err != nil {
		log.Debugf("epm-exchange: read header from %s failed: %v", remotePeer, err)
		return
	}

	var epmData []byte

	if reqLen == 0 {
		// Client wants our EPM
		epmData = s.GetNodeEPM()
		if epmData == nil {
			s.writeResponse(stream, statusNotFound, nil, remotePeer)
			return
		}
	} else if reqLen <= 128 {
		// Client wants a specific peer's EPM
		peerIDBytes := make([]byte, reqLen)
		if _, err := io.ReadFull(stream, peerIDBytes); err != nil {
			log.Debugf("epm-exchange: read peer ID from %s failed: %v", remotePeer, err)
			return
		}

		targetPeerID, err := peer.Decode(string(peerIDBytes))
		if err != nil {
			s.writeResponse(stream, statusError, nil, remotePeer)
			return
		}

		// Look up peer's EPM in registry
		tp, err := s.registry.GetPeer(targetPeerID)
		if err != nil || len(tp.EPMData) == 0 {
			s.writeResponse(stream, statusNotFound, nil, remotePeer)
			return
		}
		epmData = tp.EPMData
	} else {
		log.Warnf("epm-exchange: invalid request length %d from %s", reqLen, remotePeer)
		return
	}

	s.writeResponse(stream, statusOK, epmData, remotePeer)
}

// writeResponse sends a response back over the stream.
func (s *Service) writeResponse(stream network.Stream, status uint32, data []byte, remotePeer string) {
	_ = stream.SetWriteDeadline(time.Now().Add(streamWriteDeadline))

	header := make([]byte, 8)
	binary.LittleEndian.PutUint32(header[0:4], status)
	binary.LittleEndian.PutUint32(header[4:8], uint32(len(data)))

	if _, err := stream.Write(header); err != nil {
		log.Debugf("epm-exchange: write header to %s failed: %v", remotePeer, err)
		return
	}
	if len(data) > 0 {
		if _, err := stream.Write(data); err != nil {
			log.Debugf("epm-exchange: write data to %s failed: %v", remotePeer, err)
			return
		}
	}

	log.Debugf("epm-exchange: sent %d bytes (status=%d) to %s", len(data), status, remotePeer)
}

// RequestPeerEPM opens a stream to a remote peer and requests their EPM.
// On success, it stores the EPM in the peer registry.
func (s *Service) RequestPeerEPM(ctx context.Context, h host.Host, target peer.ID) error {
	streamCtx, cancel := context.WithTimeout(ctx, streamReadDeadline+streamWriteDeadline)
	defer cancel()

	stream, err := h.NewStream(streamCtx, target, EPMExchangeProtocolID)
	if err != nil {
		return err
	}
	defer stream.Close()

	// Send request (len=0 means "send me yours")
	_ = stream.SetWriteDeadline(time.Now().Add(streamWriteDeadline))
	header := make([]byte, 4)
	binary.LittleEndian.PutUint32(header, 0)
	if _, err := stream.Write(header); err != nil {
		return err
	}

	// Read response
	_ = stream.SetReadDeadline(time.Now().Add(streamReadDeadline))

	var respHeader [8]byte
	if _, err := io.ReadFull(stream, respHeader[:]); err != nil {
		return err
	}

	status := binary.LittleEndian.Uint32(respHeader[0:4])
	dataLen := binary.LittleEndian.Uint32(respHeader[4:8])

	if status != statusOK || dataLen == 0 || dataLen > maxEPMSize {
		return nil // peer doesn't have an EPM or returned error
	}

	epmData := make([]byte, dataLen)
	if _, err := io.ReadFull(stream, epmData); err != nil {
		return err
	}

	// Store in peer registry
	tp, err := s.registry.GetPeer(target)
	if err != nil {
		// Peer not in registry yet — auto-add with Standard trust
		tp = &peers.TrustedPeer{
			ID:         target,
			TrustLevel: peers.Standard,
		}
		if addErr := s.registry.AddPeer(tp); addErr != nil {
			log.Debugf("epm-exchange: failed to add peer %s: %v", target, addErr)
			return nil
		}
		tp, _ = s.registry.GetPeer(target)
	}

	if tp != nil {
		tp.EPMData = epmData
		// Generate vCard from EPM
		if vcardStr, err := vcard.EPMToVCard(epmData); err == nil {
			tp.VCardData = vcardStr
		}
		_ = s.registry.UpdatePeer(tp)
		log.Infof("epm-exchange: stored EPM for peer %s (%d bytes)", target.ShortString(), len(epmData))
	}

	return nil
}
