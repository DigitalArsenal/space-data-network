package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"

	"github.com/spacedatanetwork/sdn-server/internal/protocol"
)

func TestNodeSourceInstallsFlatSQLSyncResourceManager(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile(filepath.Join(".", "node.go"))
	if err != nil {
		t.Fatalf("os.ReadFile(node.go) failed: %v", err)
	}
	if !strings.Contains(string(source), "libp2p.ResourceManager(resourceManager)") {
		t.Fatalf("node host must install the FlatSQL sync resource manager")
	}
}

func TestFlatSQLSyncResourceManagerAllowsBulkInboundRangeStreams(t *testing.T) {
	t.Parallel()

	manager, err := newFlatSQLSyncResourceManager()
	if err != nil {
		t.Fatalf("newFlatSQLSyncResourceManager failed: %v", err)
	}
	defer manager.Close()

	remotePeer, err := peer.Decode("16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4")
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}

	scopes := make([]network.StreamManagementScope, 0, flatSQLSyncBulkStreamLimit)
	defer func() {
		for _, scope := range scopes {
			scope.Done()
		}
	}()

	for i := 0; i < flatSQLSyncBulkStreamLimit; i++ {
		scope, err := manager.OpenStream(remotePeer, network.DirInbound)
		if err != nil {
			t.Fatalf("OpenStream #%d failed: %v", i+1, err)
		}
		if err := scope.SetProtocol(protocol.FlatSQLSyncProtocolID); err != nil {
			scope.Done()
			t.Fatalf("SetProtocol #%d failed: %v", i+1, err)
		}
		scopes = append(scopes, scope)
	}
}

// host-01, 2026-09-25: ~2,200 connections (mostly the public IPFS swarm)
// against go-libp2p's default ~2,048 node-wide inbound streams; 425,350
// inbound streams were refused in 45 hours, Sandcastle module grants among
// them. Under kubo's policy the node-wide scopes never cap streams, so a
// swarm that size is admitted and only each peer is bounded.
func TestNodeWideStreamsAreNotCappedLikeKubo(t *testing.T) {
	manager, err := newNodeResourceManager(nil, 872)
	if err != nil {
		t.Fatalf("newNodeResourceManager: %v", err)
	}
	defer manager.Close()

	const peersCount = 3000
	scopes := make([]network.StreamManagementScope, 0, peersCount)
	for i := 0; i < peersCount; i++ {
		id, err := test.RandPeerID()
		if err != nil {
			t.Fatalf("peer id: %v", err)
		}
		scope, err := manager.OpenStream(id, network.DirInbound)
		if err != nil {
			t.Fatalf("inbound stream %d of %d refused: %v — the node-wide stream budget is capping the swarm again", i+1, peersCount, err)
		}
		scopes = append(scopes, scope)
	}
	for _, scope := range scopes {
		scope.Done()
	}
}

func TestKuboLimitsCapConnectionsNotStreams(t *testing.T) {
	limits := kuboResourceLimits(4<<30, 32768, 872).ToPartialLimitConfig()
	if limits.System.StreamsInbound != rcmgr.Unlimited || limits.Transient.StreamsInbound != rcmgr.Unlimited {
		t.Fatalf("node-wide inbound streams = %v / %v, want unlimited (kubo)", limits.System.StreamsInbound, limits.Transient.StreamsInbound)
	}
	if got := int(limits.System.ConnsInbound); got < 4096 || got < inboundAdmissionSystemConns {
		t.Fatalf("System.ConnsInbound = %d, want kubo's one per MB of 4 GiB (4096) or the measured floor, whichever is higher", got)
	}
	if got := int(limits.Transient.ConnsInbound); got < inboundAdmissionTransientConns {
		t.Fatalf("Transient.ConnsInbound = %d, below the measured wedge floor %d", got, inboundAdmissionTransientConns)
	}
	if got := int(limits.PeerDefault.StreamsInbound); got <= 0 {
		t.Fatalf("PeerDefault.StreamsInbound = %d, want a finite per-peer bound", got)
	}
	// A 2 GB host (host-02) keeps the measured floors that kubo's quarter
	// rule would undercut.
	small := kuboResourceLimits(1<<30, 32768, 48).ToPartialLimitConfig()
	if int(small.Transient.ConnsInbound) < inboundAdmissionTransientConns || int(small.System.ConnsInbound) < kuboMinInboundConns {
		t.Fatalf("2 GB host: System %v / Transient %v inbound connections, below kubo's 800 or the measured floor", small.System.ConnsInbound, small.Transient.ConnsInbound)
	}
}

func TestResourceAllowlistCoversLoopbackAndTheFleet(t *testing.T) {
	got := map[string]bool{}
	for _, addr := range resourceAllowlist(
		[]string{"/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"},
		[]string{"/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45", "not a multiaddr"},
	) {
		got[addr.String()] = true
	}
	for _, want := range []string{
		"/ip4/127.0.0.0/ipcidr/8",
		"/ip6/::1/ipcidr/128",
		"/ip4/167.172.219.213/p2p/16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U",
	} {
		if !got[want] {
			t.Errorf("allowlist is missing %s (have %v)", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("allowlist = %v, want loopback + the one IP-addressed fleet peer (DNS names cannot be matched)", got)
	}
}
