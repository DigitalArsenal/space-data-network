// Package peers provides trusted peer registry and management for the Space Data Network.
//
// This package implements Phase 12 of the SDN tasks: Trusted Peer Registry.
// It provides peer trust management that leverages IPFS Peering.Peers config
// while adding SDN-specific trust metadata and access controls.
//
// # Trust Levels
//
// TrustLevel is aligned with the PGP/GPG ownertrust scale (Phase C1 of the
// alignment plan): Never < Unknown < Marginal < Standard < Full < Admin <
// Ultimate. Ultimate (5) is the numeric maximum, reserved exclusively for
// the node's own identity (Phase F). The legacy names below remain valid
// identifiers (and legacy-persisted 0-4 values keep their original
// meaning); see the TrustLevel doc comment in trust.go for the full
// legacy<->PGP mapping and rationale.
//
//   - Untrusted / Unknown: No connection allowed / no trust assertion made.
//   - Limited / Marginal: Read-only, rate-limited access. Partial WoT confidence.
//   - Standard: Normal peer with standard access. Default for unknown peers in non-strict mode.
//   - Trusted / Full: Full access with priority routing. Full WoT confidence.
//   - Admin: Can manage other peers. Operational super-user, not a WoT signing level.
//   - Ultimate: This key IS the node's own identity (Phase F only).
//   - Never: Explicit, deliberate distrust — a hard veto that computed
//     web-of-trust validity can never override.
//
// # Web of Trust (Phase C2)
//
// Registry.SetTrustGraph wires an internal/trust.Graph of trust
// assertions between identities. Once wired, EffectiveTrustLevel (and the
// IsAllowed/IsTrusted/IsAdmin accessors built on it) augments a peer's
// direct assignment with computed PGP web-of-trust validity: a peer with
// >=3 marginal trusters, or >=1 full/ultimate truster, is computed VALID
// and floored at Marginal even absent (or below) a direct assignment.
// Direct assignments at or above Marginal always win, and a direct
// assignment of Never is a hard veto no graph can override. No graph wired
// (the default) is exactly the pre-C2, direct-assignment-only behavior.
// See Registry.IsFullyTrusted for the (separate, stronger) Phase D
// auto-subscribe/auto-pin hook.
//
// # Rooted validity (Phase C6)
//
// The live path (EffectiveTrustLevel/ComputedValidity) does not count
// every truster in the graph — only trusters that are themselves
// trust-anchored to this node's own identity (Registry.SetRootIdentity):
// the root itself, or a peer the root directly trusts at >=Marginal (a
// depth-1 "trusted introducer"). This mirrors real PGP web-of-trust, where
// validity is always computed relative to the user's own ultimate key —
// otherwise any set of self-minted identities could vouch for each other
// and manufacture computed validity out of nothing. See
// ComputeValidityRooted for the rule and ComputeValidity for the
// unrooted primitive it is built on (kept for tests, not the live path).
// No root identity wired (the default) fail-safes to "no bonus", exactly
// like no graph wired.
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
//	// Create persistence provider over the node FlatSQL store
//	persistence, _ := peers.NewFlatSQLPersistence(flatStore)
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
