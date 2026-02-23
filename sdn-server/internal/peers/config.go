// Package peers provides trusted peer registry and management for the SDN.
package peers

import (
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// RegistryConfig holds configuration for initializing the peer registry.
type RegistryConfig struct {
	// StrictMode only allows connections to/from peers in the trusted registry.
	StrictMode bool

	// RegistryPath is the path to the peer registry database file.
	// If empty, an in-memory registry is used.
	RegistryPath string

	// TrustedPeers is a list of multiaddr strings for peers that should be
	// added to the registry with Trusted level on startup. This mirrors
	// the IPFS Peering.Peers configuration pattern.
	// Format: /ip4/1.2.3.4/tcp/4001/p2p/12D3KooW...
	TrustedPeers []string

	// TrustBasedRateLimiting enables trust-level-based rate limit adjustments.
	TrustBasedRateLimiting bool

	// BaseMPS is the base messages-per-second rate limit for Standard peers.
	BaseMPS float64

	// BaseMPM is the base messages-per-minute rate limit for Standard peers.
	BaseMPM int

	// BaseBurst is the base burst size for Standard peers.
	BaseBurst int
}

// InitializeFromConfig creates and configures a Registry from a RegistryConfig.
// It parses the TrustedPeers multiaddr strings, extracts peer IDs and addresses,
// and adds them to the registry with Trusted level. Peers that already exist
// in the registry (loaded from persistence) are not modified.
func InitializeFromConfig(cfg RegistryConfig) (*Registry, *TrustedConnectionGater, *TrustBasedRateLimiter, error) {
	// Set up persistence
	var persistence PersistenceProvider
	if cfg.RegistryPath != "" {
		if strings.HasSuffix(cfg.RegistryPath, ".db") {
			sp, err := NewSQLitePersistence(cfg.RegistryPath)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to create SQLite persistence: %w", err)
			}
			persistence = sp
		} else {
			persistence = NewJSONFilePersistence(cfg.RegistryPath)
		}
	}

	// Create registry
	registry := NewRegistry(cfg.StrictMode, persistence)

	// Add trusted peers from config
	for _, addrStr := range cfg.TrustedPeers {
		info, err := ParsePeerMultiaddr(addrStr)
		if err != nil {
			log.Warnf("Skipping invalid trusted peer address %q: %v", addrStr, err)
			continue
		}

		// Only add if not already present (don't overwrite persisted data)
		if _, err := registry.GetPeer(info.ID); err == ErrPeerNotFound {
			tp := &TrustedPeer{
				ID:         info.ID,
				Addrs:      info.Addrs,
				TrustLevel: Trusted,
				Name:       "config:" + info.ID.ShortString(),
			}
			if err := registry.AddPeer(tp); err != nil {
				log.Warnf("Failed to add trusted peer %s: %v", info.ID.ShortString(), err)
			} else {
				log.Infof("Added trusted peer from config: %s", info.ID.ShortString())
			}
		}
	}

	// Create connection gater
	gater := NewTrustedConnectionGater(registry)

	// Create rate limiter
	var limiter *TrustBasedRateLimiter
	if cfg.TrustBasedRateLimiting {
		baseMPS := cfg.BaseMPS
		if baseMPS == 0 {
			baseMPS = 100
		}
		baseMPM := cfg.BaseMPM
		if baseMPM == 0 {
			baseMPM = 1000
		}
		baseBurst := cfg.BaseBurst
		if baseBurst == 0 {
			baseBurst = 50
		}
		limiter = NewTrustBasedRateLimiter(registry, baseMPS, baseMPM, baseBurst)
	}

	return registry, gater, limiter, nil
}

// ParsePeerMultiaddr parses a multiaddr string containing a peer ID component
// (e.g., /ip4/1.2.3.4/tcp/4001/p2p/12D3KooW...) and returns the peer.AddrInfo.
// This follows the same format used by IPFS Peering.Peers configuration.
func ParsePeerMultiaddr(addrStr string) (*peer.AddrInfo, error) {
	ma, err := multiaddr.NewMultiaddr(addrStr)
	if err != nil {
		return nil, fmt.Errorf("invalid multiaddr: %w", err)
	}

	info, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return nil, fmt.Errorf("multiaddr does not contain peer ID: %w", err)
	}

	return info, nil
}

// ParsePeerAddrInfos parses multiple multiaddr strings into peer.AddrInfo slices,
// grouping addresses by peer ID. This is useful for building the IPFS Peering.Peers
// equivalent list from the registry.
func ParsePeerAddrInfos(addrStrs []string) []peer.AddrInfo {
	infoMap := make(map[peer.ID]*peer.AddrInfo)

	for _, addrStr := range addrStrs {
		info, err := ParsePeerMultiaddr(addrStr)
		if err != nil {
			continue
		}

		if existing, ok := infoMap[info.ID]; ok {
			existing.Addrs = append(existing.Addrs, info.Addrs...)
		} else {
			infoMap[info.ID] = info
		}
	}

	infos := make([]peer.AddrInfo, 0, len(infoMap))
	for _, info := range infoMap {
		infos = append(infos, *info)
	}
	return infos
}
