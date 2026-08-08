package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	coreprotocol "github.com/libp2p/go-libp2p/core/protocol"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// THE DEFECT THIS FILE PINS
//
// Owner, 2026-08-08, on the live RF gallery:
//
//	"there should be no reason for this error message, it should load quickly
//	 and efficiently on the first try if the server is up."
//
//	SDN module delivery failed for com.orbpro.rf-atmospheric-gaseous@0.1.0:
//	failed to dial /space-data-network/module-delivery/1.0.0 for
//	16Uiu2HAm1Lbv…Fm45: stream reset
//
// "stream reset" here is not a network fault. go-libp2p emits exactly one
// StreamErrorCode for a resource-manager denial — 0x1002 (4098),
// network.StreamResourceLimitExceeded — and the reported code was 4098. The
// node CHOSE to reset. These tests assert it can no longer make that choice for
// a burst any honest client will produce.

const deliveryBurstTestPeer = "16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4"

// TestDeliveryProtocolsSurviveAClientBurst is the regression test for the
// reported failure: one peer, many concurrent streams on the delivery
// protocols, every one of which must be admitted through BOTH gates a real
// inbound stream passes — OpenStream (peer/system scope) and SetProtocol
// (protocol and protocol-peer scope, the gate that was actually denying).
func TestDeliveryProtocolsSurviveAClientBurst(t *testing.T) {
	t.Parallel()

	remotePeer, err := peer.Decode(deliveryBurstTestPeer)
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}

	for _, pid := range deliveryBurstProtocols() {
		t.Run(string(pid), func(t *testing.T) {
			manager, err := newFlatSQLSyncResourceManager()
			if err != nil {
				t.Fatalf("newFlatSQLSyncResourceManager failed: %v", err)
			}
			defer manager.Close()

			scopes := make([]network.StreamManagementScope, 0, deliveryBurstStreamLimit)
			defer func() {
				for _, scope := range scopes {
					scope.Done()
				}
			}()

			for i := 0; i < deliveryBurstStreamLimit; i++ {
				scope, err := manager.OpenStream(remotePeer, network.DirInbound)
				if err != nil {
					t.Fatalf("OpenStream #%d on %s failed: %v "+
						"(a client would see this as \"stream reset\")", i+1, pid, err)
				}
				if err := scope.SetProtocol(pid); err != nil {
					scope.Done()
					t.Fatalf("SetProtocol #%d on %s failed: %v — this is the exact denial that "+
						"resets the stream with code 0x1002", i+1, pid, err)
				}
				scopes = append(scopes, scope)
			}
		})
	}
}

// TestDeliveryBurstBudgetExceedsUpstreamDefault guards the sizing itself. The
// delivery lane previously inherited rcmgr's generic ProtocolPeerBaseLimit
// (StreamsInbound: 64). If a future edit drops the explicit limit, the burst
// test above could still pass on a large host purely from AutoScale, so assert
// the DECLARED budget is deliberately above upstream's default.
func TestDeliveryBurstBudgetExceedsUpstreamDefault(t *testing.T) {
	t.Parallel()

	upstream := rcmgr.DefaultLimits.ProtocolPeerBaseLimit.StreamsInbound
	if deliveryBurstStreamLimit <= upstream {
		t.Fatalf("deliveryBurstStreamLimit=%d must exceed upstream ProtocolPeerBaseLimit.StreamsInbound=%d; "+
			"otherwise the delivery lane is back on the generic budget that reset live clients",
			deliveryBurstStreamLimit, upstream)
	}

	limits := rcmgr.DefaultLimits
	applyModuleDeliveryResourceLimits(&limits)
	for _, pid := range deliveryBurstProtocols() {
		got, ok := limits.ProtocolPeerLimits[pid]
		if !ok {
			t.Fatalf("no explicit protocol-peer limit declared for %s", pid)
		}
		if got.BaseLimit.StreamsInbound < deliveryBurstStreamLimit {
			t.Fatalf("%s: StreamsInbound=%d, want >= %d",
				pid, got.BaseLimit.StreamsInbound, deliveryBurstStreamLimit)
		}
	}
}

// TestDeliveryLimitTracksTheRealWireID makes the limit rename-proof. The wire
// ID is now a package constant rather than a literal precisely so a protocol
// rename cannot silently detach the budget from the protocol.
func TestDeliveryLimitTracksTheRealWireID(t *testing.T) {
	t.Parallel()

	var found bool
	for _, pid := range deliveryBurstProtocols() {
		if pid == coreprotocol.ID(modulert.ModuleDeliveryWireID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("module-delivery wire id %q is not covered by deliveryBurstProtocols()", modulert.ModuleDeliveryWireID)
	}

	source, err := os.ReadFile(filepath.Join("..", "modulert", "manifest.go"))
	if err != nil {
		t.Fatalf("read manifest.go: %v", err)
	}
	if strings.Count(string(source), `"/space-data-network/module-delivery/1.0.0"`) != 1 {
		t.Fatalf("the module-delivery wire id must exist exactly once, as the ModuleDeliveryWireID constant, " +
			"so the resource-manager limit and the registered handler can never drift apart")
	}
}

// TestProtocolPeerDenialIsReported closes the observability hole that made this
// defect cost three investigations: BlockProtocolPeer — the callback for the
// denial that actually fired — was an empty method, so the node reset streams
// and said nothing.
func TestProtocolPeerDenialIsReported(t *testing.T) {
	t.Parallel()

	reporter := newInboundAdmissionReporter()
	before := reporter.Snapshot()
	if before.BlockedProtocolPeers != 0 || before.StreamRefusingSinceSeconds != 0 {
		t.Fatalf("fresh reporter must be clean, got %+v", before)
	}

	reporter.BlockProtocolPeer(coreprotocol.ID(modulert.ModuleDeliveryWireID), peer.ID("test-peer"))

	after := reporter.Snapshot()
	if after.BlockedProtocolPeers != 1 {
		t.Fatalf("BlockedProtocolPeers = %d, want 1 — a protocol-peer denial must be counted, "+
			"it is the one that surfaces to clients as a bare \"stream reset\"", after.BlockedProtocolPeers)
	}
	if after.StreamRefusingSinceSeconds <= 0 {
		t.Fatalf("StreamRefusingSinceSeconds = %v, want > 0 so the ONSET of refusal is recoverable",
			after.StreamRefusingSinceSeconds)
	}
}

// TestBlockedStreamIsCounted covers the peer/system-scope denial path.
func TestBlockedStreamIsCounted(t *testing.T) {
	t.Parallel()

	reporter := newInboundAdmissionReporter()
	reporter.BlockStream(peer.ID("test-peer"), network.DirInbound)
	reporter.BlockProtocol(coreprotocol.ID(modulert.ModuleDeliveryWireID))

	got := reporter.Snapshot()
	if got.BlockedStreams != 1 {
		t.Fatalf("BlockedStreams = %d, want 1", got.BlockedStreams)
	}
	if got.BlockedProtocols != 1 {
		t.Fatalf("BlockedProtocols = %d, want 1", got.BlockedProtocols)
	}
}

// --- the donated public workloads that starved the delivery lane ---

// TestPublicRelayServiceIsOptIn pins the config knob that was declared and
// never read. host-01's sidecar has carried `enable_relay: false` since it was
// written and the node ran the HOP service anyway: a live identify returned
// /libp2p/circuit/relay/0.2.0/hop while the box sat at 98.5% CPU of 2 vCPUs.
func TestPublicRelayServiceIsOptIn(t *testing.T) {
	t.Parallel()

	if config.Default().Network.EnableRelay {
		t.Fatalf("network.enable_relay must default to false: running a public circuit-relay HOP service " +
			"is donated CPU and bandwidth, and must be a deliberate choice")
	}

	source, err := os.ReadFile(filepath.Join(".", "node.go"))
	if err != nil {
		t.Fatalf("read node.go: %v", err)
	}
	text := string(source)
	if !strings.Contains(text, "if n.config.Network.EnableRelay {") {
		t.Fatalf("libp2p.EnableRelayService() must be gated on n.config.Network.EnableRelay; " +
			"an operator-set false that the process ignores is worse than no knob at all")
	}
	// EnableRelay() (client side: dial THROUGH a relay) must survive the gate.
	if !strings.Contains(text, "libp2p.EnableRelay(),") {
		t.Fatalf("libp2p.EnableRelay() (the CLIENT side) must remain unconditional")
	}
}

// TestDHTDefaultsToClientMode pins the second donated workload. ModeAutoServer
// was hardcoded, making every node a server for the public Amino DHT: 780
// inbound connections from ~700 distinct internet IPs and 2105 kad-dht handler
// warnings in 70 minutes on a box whose only job is module delivery.
func TestDHTDefaultsToClientMode(t *testing.T) {
	t.Parallel()

	if config.Default().Network.DHTServer {
		t.Fatalf("network.dht_server must default to false: serving the public IPFS DHT is an unbounded " +
			"public workload unrelated to anything this node is for")
	}

	// The observable difference between the two modes is whether the node
	// REGISTERS the public DHT handler — i.e. whether it answers strangers'
	// routing queries. That is the workload being shed, so assert on it
	// directly rather than on the shape of an option slice.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registersKad := func(mode dhtParticipation) bool {
		h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
		if err != nil {
			t.Fatalf("libp2p.New failed: %v", err)
		}
		defer h.Close()
		d, err := dht.New(ctx, h, publicDHTOptions(mode)...)
		if err != nil {
			t.Fatalf("dht.New failed: %v", err)
		}
		defer d.Close()
		for _, p := range h.Mux().Protocols() {
			if p == coreprotocol.ID("/ipfs/kad/1.0.0") {
				return true
			}
		}
		return false
	}

	if registersKad(dhtParticipationClient) {
		t.Fatalf("publicDHTOptions(dhtParticipationClient) must NOT serve /ipfs/kad/1.0.0: client mode still queries and " +
			"still provides its own records, it just stops answering the whole internet's lookups")
	}
	if !registersKad(dhtParticipationServer) {
		t.Fatalf("publicDHTOptions(dhtParticipationServer) must serve /ipfs/kad/1.0.0 when a node is deliberately deployed as DHT infrastructure")
	}
}

// TestOneClientIdentityMayHoldManyInboundConnections is the direct regression
// test for the reported failure.
//
// go-libp2p caps inbound connections PER PEER IDENTITY at 8 by default and —
// because PeerLimitIncrease declares no Conns fields — AutoScale can never
// raise it on any host size. One browser tab opening the RF gallery derives a
// single identity and then opens ten connections (Promise.all over ten module
// fetches, one delivery client each). Measured live 2026-08-08: ten such
// clients lost 9-10 of 10 delivery streams to 0x1002 resets; four were clean.
func TestOneClientIdentityMayHoldManyInboundConnections(t *testing.T) {
	t.Parallel()

	upstream := rcmgr.DefaultLimits.PeerBaseLimit.ConnsInbound
	if inboundAdmissionPeerConns <= upstream {
		t.Fatalf("inboundAdmissionPeerConns=%d must exceed upstream PeerBaseLimit.ConnsInbound=%d",
			inboundAdmissionPeerConns, upstream)
	}

	// AutoScale genuinely cannot fix this — assert the property, so nobody
	// "simplifies" the raise away believing a bigger box would compensate.
	if rcmgr.DefaultLimits.PeerLimitIncrease.ConnsInbound != 0 ||
		rcmgr.DefaultLimits.PeerLimitIncrease.Conns != 0 {
		t.Fatalf("upstream now scales per-peer conns; re-derive inboundAdmissionPeerConns instead of hardcoding a floor")
	}

	// The concrete, post-scale ceiling is what a client actually meets. Assert
	// it through the real manager rather than the config, by opening a browser
	// tab's worth of inbound connections from ONE peer identity.
	manager, err := newFlatSQLSyncResourceManager()
	if err != nil {
		t.Fatalf("newFlatSQLSyncResourceManager failed: %v", err)
	}
	defer manager.Close()

	remotePeer, err := peer.Decode(deliveryBurstTestPeer)
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}

	scopes := make([]network.ConnManagementScope, 0, browserTabConnectionBurst)
	defer func() {
		for _, scope := range scopes {
			scope.Done()
		}
	}()
	for i := 0; i < browserTabConnectionBurst; i++ {
		scope, err := manager.OpenConnection(network.DirInbound, true, testInboundConnAddr)
		if err != nil {
			t.Fatalf("OpenConnection #%d failed: %v", i+1, err)
		}
		if err := scope.SetPeer(remotePeer); err != nil {
			scope.Done()
			t.Fatalf("SetPeer #%d failed: %v — ONE browser tab opens %d connections from ONE identity, "+
				"and upstream's per-peer ceiling of %d cannot be raised by AutoScale",
				i+1, err, browserTabConnectionBurst, rcmgr.DefaultLimits.PeerBaseLimit.ConnsInbound)
		}
		scopes = append(scopes, scope)
	}
}

// browserTabConnectionBurst is the measured connection count a single gallery
// tab produces: ten RF modules fetched concurrently, one connection each.
const browserTabConnectionBurst = 10

// testInboundConnAddr is any routable remote address; rcmgr only needs it to
// account per-subnet limits.
var testInboundConnAddr = multiaddr.StringCast("/ip4/203.0.113.7/tcp/443")

// TestManyClientsMayShareOneEgressIP pins the third ceiling of 8: rcmgr's
// per-source-IP connection limit.
//
// This is the one that governs the direct /ip4/…/tcp/4004/ws lane. Left at
// upstream's default, one gallery tab exceeds it alone, and every user behind a
// shared NAT competes for the same eight slots — the ninth visitor from one
// company is refused because the first eight were not.
func TestManyClientsMayShareOneEgressIP(t *testing.T) {
	t.Parallel()

	manager, err := newFlatSQLSyncResourceManager()
	if err != nil {
		t.Fatalf("newFlatSQLSyncResourceManager failed: %v", err)
	}
	defer manager.Close()

	scopes := make([]network.ConnManagementScope, 0, browserTabConnectionBurst*4)
	defer func() {
		for _, scope := range scopes {
			scope.Done()
		}
	}()

	// Four tabs' worth of connections from ONE egress IP.
	for i := 0; i < browserTabConnectionBurst*4; i++ {
		scope, err := manager.OpenConnection(network.DirInbound, true, testInboundConnAddr)
		if err != nil {
			t.Fatalf("OpenConnection #%d from a single egress IP failed: %v — upstream's default is %d per /32, "+
				"which one browser tab exceeds on its own", i+1, err, 8)
		}
		scopes = append(scopes, scope)
	}
}

// TestLoopbackStaysExemptForTheTLSProxyLane: Cloudflare-fronted /p2p/ upgrades
// reach the libp2p listener from 127.0.0.1, because this node's own :443 HTTPS
// server reverse-proxies them to the loopback listener. If the loopback
// exemption were ever lost, the ENTIRE browser population would share one
// per-IP budget and the node would fail under trivial load.
func TestLoopbackStaysExemptForTheTLSProxyLane(t *testing.T) {
	t.Parallel()

	manager, err := newFlatSQLSyncResourceManager()
	if err != nil {
		t.Fatalf("newFlatSQLSyncResourceManager failed: %v", err)
	}
	defer manager.Close()

	loopback := multiaddr.StringCast("/ip4/127.0.0.1/tcp/4004")
	scopes := make([]network.ConnManagementScope, 0, browserSubnetConnLimit+64)
	defer func() {
		for _, scope := range scopes {
			scope.Done()
		}
	}()

	for i := 0; i < browserSubnetConnLimit+64; i++ {
		scope, err := manager.OpenConnection(network.DirInbound, true, loopback)
		if err != nil {
			t.Fatalf("loopback OpenConnection #%d failed: %v — the TLS-proxy lane arrives from 127.0.0.1 "+
				"and must not be rate-limited as if it were one remote host", i+1, err)
		}
		scopes = append(scopes, scope)
	}
}

// TestOwnerSetEnableDHTIsHonoured pins the THIRD declared-never-read knob found
// on this path.
//
// host-01's delivery sidecar has carried `peers.enable_dht: false` and ran a
// full public Amino DHT SERVER regardless, because `EnableDHT` had zero read
// sites — exactly the defect `network.enable_relay` had. The operator's setting
// wins outright: a node told not to use the DHT does not get to serve it, no
// matter what `network.dht_server` says.
func TestOwnerSetEnableDHTIsHonoured(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		enableDHT bool
		dhtServer bool
		want      dhtParticipation
	}{
		{"operator disabled the DHT", false, false, dhtParticipationOff},
		{"operator disabled the DHT, server flag must not override", false, true, dhtParticipationOff},
		{"DHT on, default is client", true, false, dhtParticipationClient},
		{"DHT on, explicit server", true, true, dhtParticipationServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &Node{config: &config.Config{}}
			n.config.Peers.EnableDHT = tc.enableDHT
			n.config.Network.DHTServer = tc.dhtServer
			if got := n.dhtParticipation(); got != tc.want {
				t.Fatalf("dhtParticipation() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDHTIsNeverNil guards the reason participation is expressed as a mode
// rather than by skipping construction: Start dereferences n.dht with no nil
// guard, as do the advertisement and discovery routines. Turning the DHT "off"
// by not building it would trade a config defect for a panic.
func TestDHTIsNeverNil(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile(filepath.Join(".", "node.go"))
	if err != nil {
		t.Fatalf("read node.go: %v", err)
	}
	if !strings.Contains(string(source), "n.dht.Bootstrap(ctx)") {
		t.Skip("Bootstrap call site moved; re-derive this guard")
	}
	if !strings.Contains(string(source), "dhtParticipationOff:") {
		t.Fatalf("Start must branch on dhtParticipationOff and skip Bootstrap rather than leave n.dht nil")
	}
}
