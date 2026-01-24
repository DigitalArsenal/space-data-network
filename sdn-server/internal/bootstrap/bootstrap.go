// Package bootstrap provides utilities for connecting to bootstrap peers
// with peer ID verification (peer ID pinning).
package bootstrap

import (
	"context"
	"fmt"
	"strings"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

var log = logging.Logger("sdn-bootstrap")

// PeerInfo represents a bootstrap peer with its address and expected peer ID.
type PeerInfo struct {
	// AddrInfo contains the peer ID and addresses for connection
	AddrInfo peer.AddrInfo

	// HasPinnedID indicates whether the address included a peer ID
	// If false, the connection is less secure as we cannot verify
	// we're connecting to the expected peer
	HasPinnedID bool

	// RawAddress is the original multiaddr string for logging
	RawAddress string
}

// ParseBootstrapAddresses parses a list of bootstrap multiaddresses and validates
// that they contain peer IDs for secure peer ID pinning.
//
// Addresses should be in the format: /ip4/x.x.x.x/tcp/port/p2p/PEER_ID
// or /dnsaddr/hostname/p2p/PEER_ID
//
// Addresses without peer IDs will be logged as warnings and marked as unpinned.
func ParseBootstrapAddresses(addresses []string) ([]PeerInfo, error) {
	peers := make([]PeerInfo, 0, len(addresses))

	for _, addr := range addresses {
		peerInfo, err := ParseBootstrapAddress(addr)
		if err != nil {
			log.Warnf("Invalid bootstrap address %s: %v", addr, err)
			continue
		}

		if !peerInfo.HasPinnedID {
			log.Warnf("SECURITY WARNING: Bootstrap address %s does not include a peer ID. "+
				"This connection cannot be verified and may be susceptible to MITM attacks. "+
				"Please update to use format: %s/p2p/<PEER_ID>", addr, addr)
		}

		peers = append(peers, peerInfo)
	}

	return peers, nil
}

// ParseBootstrapAddress parses a single bootstrap multiaddress.
// It extracts the peer ID if present and validates the address format.
func ParseBootstrapAddress(addr string) (PeerInfo, error) {
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return PeerInfo{}, fmt.Errorf("invalid multiaddr: %w", err)
	}

	// Check if the address contains a p2p component (peer ID)
	hasPeerID := containsP2PComponent(addr)

	if hasPeerID {
		// Parse using the standard libp2p function which extracts peer ID
		addrInfo, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			return PeerInfo{}, fmt.Errorf("failed to parse peer info: %w", err)
		}

		return PeerInfo{
			AddrInfo:    *addrInfo,
			HasPinnedID: true,
			RawAddress:  addr,
		}, nil
	}

	// Address doesn't have a peer ID - create a partial AddrInfo
	// Note: This will require connecting without ID verification
	return PeerInfo{
		AddrInfo: peer.AddrInfo{
			Addrs: []multiaddr.Multiaddr{ma},
		},
		HasPinnedID: false,
		RawAddress:  addr,
	}, nil
}

// containsP2PComponent checks if a multiaddr string contains a /p2p/ component.
func containsP2PComponent(addr string) bool {
	return strings.Contains(addr, "/p2p/") || strings.Contains(addr, "/ipfs/")
}

// ConnectResult represents the result of a bootstrap connection attempt.
type ConnectResult struct {
	PeerID      peer.ID
	Address     string
	Success     bool
	Error       error
	WasPinned   bool
	ActualPeer  peer.ID // The peer ID we actually connected to
}

// ConnectToBootstrapPeers connects to a list of parsed bootstrap peers.
// It returns results for each connection attempt.
//
// For peers with pinned IDs (HasPinnedID=true), the libp2p host will
// automatically verify that the connected peer's ID matches the expected ID.
// This is because peer.AddrInfo includes the expected peer ID, and libp2p
// verifies this during the security handshake.
//
// For peers without pinned IDs, a warning is logged but the connection
// proceeds (less secure).
func ConnectToBootstrapPeers(ctx context.Context, h host.Host, peers []PeerInfo) []ConnectResult {
	results := make([]ConnectResult, len(peers))

	for i, p := range peers {
		results[i] = connectToBootstrapPeer(ctx, h, p)
	}

	return results
}

// connectToBootstrapPeer attempts to connect to a single bootstrap peer.
func connectToBootstrapPeer(ctx context.Context, h host.Host, p PeerInfo) ConnectResult {
	result := ConnectResult{
		PeerID:    p.AddrInfo.ID,
		Address:   p.RawAddress,
		WasPinned: p.HasPinnedID,
	}

	if !p.HasPinnedID {
		// Cannot securely connect without a peer ID
		// Log warning and skip this peer
		log.Warnf("Skipping bootstrap peer %s: no peer ID for verification (peer ID pinning required)", p.RawAddress)
		result.Success = false
		result.Error = fmt.Errorf("bootstrap address missing peer ID: peer ID pinning required for security")
		return result
	}

	// Connect to the peer
	// libp2p will verify the peer ID matches during the security handshake
	// (TLS or Noise protocol verifies the peer's public key matches their ID)
	err := h.Connect(ctx, p.AddrInfo)
	if err != nil {
		result.Success = false
		result.Error = err
		log.Warnf("Failed to connect to bootstrap peer %s (%s): %v", p.AddrInfo.ID, p.RawAddress, err)
		return result
	}

	result.Success = true
	result.ActualPeer = p.AddrInfo.ID
	log.Infof("Connected to bootstrap peer %s (peer ID verified)", p.AddrInfo.ID)

	return result
}

// ConnectToBootstrapPeersAsync connects to bootstrap peers asynchronously.
// It returns a channel that will receive results as connections complete.
func ConnectToBootstrapPeersAsync(ctx context.Context, h host.Host, peers []PeerInfo) <-chan ConnectResult {
	results := make(chan ConnectResult, len(peers))

	go func() {
		defer close(results)

		for _, p := range peers {
			select {
			case <-ctx.Done():
				return
			default:
				result := connectToBootstrapPeer(ctx, h, p)
				results <- result
			}
		}
	}()

	return results
}

// ValidateBootstrapConfig checks a list of bootstrap addresses and returns
// warnings about any security issues.
func ValidateBootstrapConfig(addresses []string) []string {
	var warnings []string

	for _, addr := range addresses {
		if !containsP2PComponent(addr) {
			warnings = append(warnings, fmt.Sprintf(
				"Bootstrap address %q lacks peer ID - update to format: %s/p2p/<PEER_ID>",
				addr, addr))
		}
	}

	return warnings
}

// RequirePinnedPeerIDs returns only the bootstrap peers that have pinned peer IDs.
// Use this when strict security is required.
func RequirePinnedPeerIDs(peers []PeerInfo) []PeerInfo {
	pinned := make([]PeerInfo, 0, len(peers))
	for _, p := range peers {
		if p.HasPinnedID {
			pinned = append(pinned, p)
		}
	}
	return pinned
}
