package wasiplugin

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/ipfs/go-cid"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	mh "github.com/multiformats/go-multihash"
)

const (
	// KeyBrokerProtocolID is the libp2p protocol for OrbPro key exchange.
	// The key exchange happens entirely over encrypted libp2p streams,
	// following a Widevine/Signal-style model where the WASM CDM handles
	// all crypto internally.
	KeyBrokerProtocolID = protocol.ID("/orbpro/key-broker/1.0.0")

	// PublicKeyProtocolID is the libp2p protocol for retrieving the
	// server's P-256 public key. Clients fetch this before initiating
	// the key exchange.
	PublicKeyProtocolID = protocol.ID("/orbpro/public-key/1.0.0")

	// streamReadDeadline is the maximum time to wait for a complete
	// request packet from the client.
	streamReadDeadline = 15 * time.Second

	// streamWriteDeadline is the maximum time to send the response.
	streamWriteDeadline = 10 * time.Second

	// maxStreamPacketSize is the maximum size of a single binary packet
	// over the stream (16 KB — same as HTTP bridge limit).
	maxStreamPacketSize = 16 * 1024

	// publicKeyCIDNamespace is the content namespace for DHT provider
	// records so clients can discover the key broker by CID.
	publicKeyCIDNamespace = "orbpro-key-broker-pubkey"
)

// StreamBridge adapts the WASI plugin Runtime to libp2p stream handlers.
// It handles two protocols:
//   - /orbpro/public-key/1.0.0 — serves the server's P-256 public key
//   - /orbpro/key-broker/1.0.0 — handles binary key exchange packets
type StreamBridge struct {
	runtime *Runtime
}

// NewStreamBridge creates a stream bridge backed by the given WASI runtime.
func NewStreamBridge(rt *Runtime) *StreamBridge {
	return &StreamBridge{runtime: rt}
}

// HandlePublicKeyStream handles requests for the server's P-256 public key
// over a libp2p stream. The client opens a stream, and the server responds
// with the raw uncompressed P-256 public key bytes (65 bytes: 0x04 + x + y).
//
// Wire format (response):
//
//	pubKeyLen(4 LE) + pubKeyBytes(N)
func (sb *StreamBridge) HandlePublicKeyStream(stream network.Stream) {
	defer stream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), streamWriteDeadline)
	defer cancel()

	pubKey, err := sb.runtime.GetPublicKey(ctx)
	if err != nil {
		log.Errorf("stream public-key: GetPublicKey failed: %v", err)
		return
	}

	// Write length-prefixed public key
	header := make([]byte, 4)
	binary.LittleEndian.PutUint32(header, uint32(len(pubKey)))

	_ = stream.SetWriteDeadline(time.Now().Add(streamWriteDeadline))
	if _, err := stream.Write(header); err != nil {
		log.Debugf("stream public-key: write header failed: %v", err)
		return
	}
	if _, err := stream.Write(pubKey); err != nil {
		log.Debugf("stream public-key: write key failed: %v", err)
		return
	}

	log.Debugf("stream public-key: served %d-byte key to %s",
		len(pubKey), stream.Conn().RemotePeer().ShortString())
}

// HandleKeyBrokerStream handles binary key exchange packets over a libp2p
// stream. This is the core of the Widevine/Signal-style key exchange:
//
//  1. Client opens stream to /orbpro/key-broker/1.0.0
//  2. Client sends: packetLen(4 LE) + packet(N)
//  3. Server processes via WASM HandleRequest
//  4. Server responds: statusCode(4 LE) + responseLen(4 LE) + response(N)
//  5. Stream is closed
//
// The packet contents are opaque to the server — the WASM CDM defines the
// internal format including ephemeral ECDH keys and encrypted payloads.
func (sb *StreamBridge) HandleKeyBrokerStream(stream network.Stream) {
	defer stream.Close()

	remotePeer := stream.Conn().RemotePeer().ShortString()

	// Read request packet (length-prefixed binary)
	_ = stream.SetReadDeadline(time.Now().Add(streamReadDeadline))

	var packetLen uint32
	if err := binary.Read(stream, binary.LittleEndian, &packetLen); err != nil {
		log.Debugf("stream key-broker: read header from %s failed: %v", remotePeer, err)
		return
	}

	if packetLen == 0 || packetLen > maxStreamPacketSize {
		log.Warnf("stream key-broker: invalid packet size %d from %s", packetLen, remotePeer)
		return
	}

	packet := make([]byte, packetLen)
	if _, err := io.ReadFull(stream, packet); err != nil {
		log.Debugf("stream key-broker: read packet from %s failed: %v", remotePeer, err)
		return
	}

	// Process through WASM runtime
	// The host header is empty for libp2p streams — domain validation
	// is handled differently over p2p (the stream is already authenticated
	// by the libp2p connection).
	ctx, cancel := context.WithTimeout(context.Background(), streamWriteDeadline)
	defer cancel()

	response, status, err := sb.runtime.HandleRequest(ctx, packet, "")
	if err != nil {
		log.Errorf("stream key-broker: HandleRequest failed for %s: %v", remotePeer, err)
		// Send error status
		_ = stream.SetWriteDeadline(time.Now().Add(streamWriteDeadline))
		errResp := make([]byte, 8)
		binary.LittleEndian.PutUint32(errResp[0:4], 0xFFFFFFFF) // -1 as uint32
		binary.LittleEndian.PutUint32(errResp[4:8], 0)
		_, _ = stream.Write(errResp)
		return
	}

	// Write response: statusCode(4 LE) + responseLen(4 LE) + response(N)
	_ = stream.SetWriteDeadline(time.Now().Add(streamWriteDeadline))

	respHeader := make([]byte, 8)
	binary.LittleEndian.PutUint32(respHeader[0:4], uint32(status))
	binary.LittleEndian.PutUint32(respHeader[4:8], uint32(len(response)))

	if _, err := stream.Write(respHeader); err != nil {
		log.Debugf("stream key-broker: write header to %s failed: %v", remotePeer, err)
		return
	}
	if len(response) > 0 {
		if _, err := stream.Write(response); err != nil {
			log.Debugf("stream key-broker: write response to %s failed: %v", remotePeer, err)
			return
		}
	}

	log.Debugf("stream key-broker: exchange with %s completed (status=%d, %d bytes)",
		remotePeer, status, len(response))
}

// PublicKeyCID computes the CID for the server's P-256 public key.
// Clients use this CID to discover the key broker via DHT.FindProviders.
func (sb *StreamBridge) PublicKeyCID(ctx context.Context) (cid.Cid, error) {
	pubKey, err := sb.runtime.GetPublicKey(ctx)
	if err != nil {
		return cid.Undef, fmt.Errorf("failed to get public key: %w", err)
	}

	// Hash: SHA-256(namespace + pubkey)
	h := sha256.New()
	h.Write([]byte(publicKeyCIDNamespace))
	h.Write(pubKey)

	multihash, err := mh.Encode(h.Sum(nil), mh.SHA2_256)
	if err != nil {
		return cid.Undef, fmt.Errorf("failed to encode multihash: %w", err)
	}

	return cid.NewCidV1(cid.Raw, multihash), nil
}

// AnnouncePublicKey publishes the server's P-256 public key CID to the DHT
// so clients can discover this key broker node via FindProviders.
func (sb *StreamBridge) AnnouncePublicKey(ctx context.Context, d *dht.IpfsDHT) error {
	keyCID, err := sb.PublicKeyCID(ctx)
	if err != nil {
		return err
	}

	announceCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := d.Provide(announceCtx, keyCID, true); err != nil {
		return fmt.Errorf("DHT provide failed for public key CID %s: %w", keyCID, err)
	}

	log.Infof("Published key broker public key to DHT (CID: %s)", keyCID)
	return nil
}
