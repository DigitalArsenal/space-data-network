// Package peers provides trusted peer registry and management for the Space Data Network.
//
// This package implements Phase 12 of the SDN tasks: Trusted Peer Registry.
// It provides peer trust management that leverages IPFS Peering.Peers config
// while adding SDN-specific trust metadata and access controls.
//
// # Trust Levels
//
// The package defines five trust levels for peers:
//
//   - Untrusted: No connection allowed. Used for blocked peers.
//   - Limited: Read-only, rate-limited access. For untrusted but not blocked peers.
//   - Standard: Normal peer with standard access. Default for unknown peers in non-strict mode.
//   - Trusted: Full access with priority routing. For verified trusted peers.
//   - Admin: Can manage other peers. For administrative access.
//
// # Registry
//
// The Registry type manages the trusted peer registry with support for:
//   - Adding, updating, and removing peers
//   - Trust level management
//   - Peer groups for organization
//   - Connection statistics tracking
//   - Import/export functionality
//   - Strict mode (only allow connections to known peers)
//
// Example usage:
//
//	// Create persistence provider
//	persistence, _ := peers.NewSQLitePersistence("/path/to/peers.db")
//
//	// Create registry in strict mode
//	registry := peers.NewRegistry(true, persistence)
//
//	// Add a trusted peer
//	tp := &peers.TrustedPeer{
//	    ID:         peerID,
//	    TrustLevel: peers.Trusted,
//	    Name:       "My Peer",
//	}
//	registry.AddPeer(tp)
//
// # Connection Gater
//
// The TrustedConnectionGater implements libp2p's ConnectionGater interface
// to enforce trust-based connection policies:
//
//	gater := peers.NewTrustedConnectionGater(registry)
//
//	// Use with libp2p host
//	host, _ := libp2p.New(
//	    libp2p.ConnectionGater(gater),
//	)
//
// # Admin API
//
// The APIHandler provides HTTP endpoints for peer management:
//
//	GET    /api/peers              - List all peers
//	POST   /api/peers              - Add peer
//	GET    /api/peers/:id          - Get peer
//	PUT    /api/peers/:id          - Update peer
//	DELETE /api/peers/:id          - Remove peer
//	PUT    /api/peers/:id/trust    - Update trust level
//	GET    /api/peers/:id/stats    - Connection stats
//
//	GET    /api/groups             - List groups
//	POST   /api/groups             - Add group
//	GET    /api/groups/:name       - Get group
//	DELETE /api/groups/:name       - Remove group
//
//	GET    /api/blocklist          - List blocked peers
//	POST   /api/blocklist          - Block peer
//	DELETE /api/blocklist/:id      - Unblock peer
//
//	GET    /api/settings           - Get settings
//	PUT    /api/settings           - Update settings
//
//	GET    /api/export             - Export registry
//	POST   /api/import             - Import registry
//
// # Admin UI
//
// The AdminUI provides a web interface for peer management at /admin.
// Features include:
//   - Peer list with search and filtering
//   - Trust level management
//   - Peer group management
//   - Blocklist management
//   - Import/export functionality
//   - Visual trust indicators (color-coded badges)
package peers
