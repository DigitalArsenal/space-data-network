package epm

import (
	"encoding/json"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// ConnectionType mirrors the PGR.fbs enum.
type ConnectionType string

const (
	ConnectionDirect    ConnectionType = "Direct"
	ConnectionRelay     ConnectionType = "Relay"
	ConnectionBootstrap ConnectionType = "Bootstrap"
	ConnectionDHT       ConnectionType = "DHT"
)

// PeerRole mirrors the PGR.fbs enum.
type PeerRole string

const (
	RoleStandard  PeerRole = "Standard"
	RoleBootstrap PeerRole = "Bootstrap"
	RoleRelay     PeerRole = "Relay"
	RoleGateway   PeerRole = "Gateway"
	RoleSeed      PeerRole = "Seed"
)

// PeerNode is a node in the peer graph.
type PeerNode struct {
	PeerID             string   `json:"peer_id"`
	DN                 string   `json:"dn,omitempty"`
	Organization       string   `json:"organization,omitempty"`
	TrustLevel         string   `json:"trust_level,omitempty"`
	Role               PeerRole `json:"role"`
	MultiformatAddress []string `json:"multiformat_address,omitempty"`
	LastSeen           string   `json:"last_seen,omitempty"`
	IsOnline           bool     `json:"is_online"`
}

// PeerEdge is an edge in the peer graph.
type PeerEdge struct {
	SourcePeerID   string         `json:"source_peer_id"`
	TargetPeerID   string         `json:"target_peer_id"`
	ConnectionType ConnectionType `json:"connection_type"`
	LatencyMs      uint32         `json:"latency_ms,omitempty"`
	BandwidthBps   uint64         `json:"bandwidth_bps,omitempty"`
	EstablishedAt  string         `json:"established_at,omitempty"`
	Protocols      []string       `json:"protocols,omitempty"`
}

// PeerGraphSnapshot is a point-in-time snapshot of the network graph.
type PeerGraphSnapshot struct {
	Timestamp   string      `json:"timestamp"`
	LocalPeerID string      `json:"local_peer_id"`
	Nodes       []PeerNode  `json:"nodes"`
	Edges       []PeerEdge  `json:"edges"`
	Metadata    string      `json:"metadata,omitempty"`
}

// BuildGraphSnapshot creates a PeerGraphSnapshot from the current network state.
func BuildGraphSnapshot(h host.Host, registry *peers.Registry) *PeerGraphSnapshot {
	localID := h.ID()
	now := time.Now().UTC()
	snapshot := &PeerGraphSnapshot{
		Timestamp:   now.Format(time.RFC3339),
		LocalPeerID: localID.String(),
	}

	// Connected peers
	connectedPeers := make(map[peer.ID]bool)
	for _, conn := range h.Network().Conns() {
		connectedPeers[conn.RemotePeer()] = true
	}

	// Build the local node entry
	localNode := PeerNode{
		PeerID:   localID.String(),
		Role:     RoleStandard,
		IsOnline: true,
	}
	localAddrs := h.Addrs()
	for _, a := range localAddrs {
		localNode.MultiformatAddress = append(localNode.MultiformatAddress, a.String())
	}
	snapshot.Nodes = append(snapshot.Nodes, localNode)

	// Add all trusted/known peers
	seen := make(map[peer.ID]bool)
	seen[localID] = true

	if registry != nil {
		for _, tp := range registry.ListPeers() {
			seen[tp.ID] = true
			node := PeerNode{
				PeerID:     tp.ID.String(),
				DN:         tp.Name,
				Organization: tp.Organization,
				TrustLevel: tp.TrustLevel.String(),
				Role:       RoleStandard,
				IsOnline:   connectedPeers[tp.ID],
			}
			if !tp.LastSeen.IsZero() {
				node.LastSeen = tp.LastSeen.Format(time.RFC3339)
			}
			for _, a := range tp.AddrsStrings {
				node.MultiformatAddress = append(node.MultiformatAddress, a)
			}
			snapshot.Nodes = append(snapshot.Nodes, node)

			// If connected, add an edge
			if connectedPeers[tp.ID] {
				edge := PeerEdge{
					SourcePeerID:   localID.String(),
					TargetPeerID:   tp.ID.String(),
					ConnectionType: classifyConnection(h, tp.ID),
				}
				// Try to get protocols from the connection
				if protos := getConnectionProtocols(h, tp.ID); len(protos) > 0 {
					edge.Protocols = protos
				}
				snapshot.Edges = append(snapshot.Edges, edge)
			}
		}
	}

	// Add connected peers not in the registry
	for pid := range connectedPeers {
		if seen[pid] {
			continue
		}
		node := PeerNode{
			PeerID:   pid.String(),
			Role:     RoleStandard,
			IsOnline: true,
		}
		snapshot.Nodes = append(snapshot.Nodes, node)

		edge := PeerEdge{
			SourcePeerID:   localID.String(),
			TargetPeerID:   pid.String(),
			ConnectionType: classifyConnection(h, pid),
		}
		if protos := getConnectionProtocols(h, pid); len(protos) > 0 {
			edge.Protocols = protos
		}
		snapshot.Edges = append(snapshot.Edges, edge)
	}

	return snapshot
}

// GraphSnapshotJSON returns the snapshot as JSON bytes.
func GraphSnapshotJSON(h host.Host, registry *peers.Registry) ([]byte, error) {
	snap := BuildGraphSnapshot(h, registry)
	return json.Marshal(snap)
}

// classifyConnection determines the connection type for a peer.
func classifyConnection(h host.Host, pid peer.ID) ConnectionType {
	conns := h.Network().ConnsToPeer(pid)
	for _, c := range conns {
		// Check if the connection is through a relay
		stat := c.Stat()
		if stat.Limited {
			return ConnectionRelay
		}
		if stat.Direction == network.DirInbound {
			return ConnectionDirect
		}
	}
	return ConnectionDirect
}

// getConnectionProtocols returns the protocols negotiated on streams to a peer.
func getConnectionProtocols(h host.Host, pid peer.ID) []string {
	protocols := make(map[string]bool)
	conns := h.Network().ConnsToPeer(pid)
	for _, c := range conns {
		for _, s := range c.GetStreams() {
			p := string(s.Protocol())
			if p != "" {
				protocols[p] = true
			}
		}
	}
	out := make([]string, 0, len(protocols))
	for p := range protocols {
		out = append(out, p)
	}
	return out
}

// PGRSchema is the FlatBuffers schema definition for Peer Graph Records.
const PGRSchema = `// Peer Graph Record (PGR) FlatBuffers Schema
// Version: 1.0.0

enum ConnectionType : byte { Direct, Relay, Bootstrap, DHT }
enum PeerRole : byte { Standard, Bootstrap, Relay, Gateway, Seed }

table PeerNode {
  PEER_ID: string;
  DN: string;
  ORGANIZATION: string;
  TRUST_LEVEL: string;
  ROLE: PeerRole;
  MULTIFORMAT_ADDRESS: [string];
  LAST_SEEN: string;
  IS_ONLINE: bool;
}

table PeerEdge {
  SOURCE_PEER_ID: string;
  TARGET_PEER_ID: string;
  CONNECTION_TYPE: ConnectionType;
  LATENCY_MS: uint32;
  BANDWIDTH_BPS: uint64;
  ESTABLISHED_AT: string;
  PROTOCOLS: [string];
}

table PeerGraphSnapshot {
  TIMESTAMP: string;
  LOCAL_PEER_ID: string;
  NODES: [PeerNode];
  EDGES: [PeerEdge];
  METADATA: string;
}

root_type PeerGraphSnapshot;
file_identifier "$PGR";
`
