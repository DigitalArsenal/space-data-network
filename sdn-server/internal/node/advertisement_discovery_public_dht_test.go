package node

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
)

// testAminoDHTOptions mirrors publicDHTOptions (node.go): it deliberately
// omits dht.ProtocolPrefix so the DHT speaks the stock public IPFS/Amino
// protocol ("/ipfs/kad/1.0.0"), the same wire protocol production nodes join
// since A1 (SDN_ALIGNMENT_FIX_LOOP). It uses dht.ModeServer instead of
// production's dht.ModeAutoServer so routing-table membership is
// deterministic in a loopback-only test (AutoServer's NAT-reachability
// autodetection is meaningless on 127.0.0.1).
func testAminoDHTOptions() []dht.Option {
	return []dht.Option{dht.Mode(dht.ModeServer)}
}

// newTestAminoDHTHost builds one loopback-only libp2p host plus a DHT
// instance speaking the same stock public protocol as publicDHTOptions. The
// host never dials out beyond what the test explicitly connects.
func newTestAminoDHTHost(t *testing.T, ctx context.Context) (host.Host, *dht.IpfsDHT) {
	t.Helper()

	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p.New failed: %v", err)
	}

	d, err := dht.New(ctx, h, testAminoDHTOptions()...)
	if err != nil {
		_ = h.Close()
		t.Fatalf("dht.New failed: %v", err)
	}

	t.Cleanup(func() {
		_ = d.Close()
		_ = h.Close()
	})
	return h, d
}

// connectOnly dials b from a's host directly (the ONLY explicit connection
// the test wires up between the two — everything else peers learn about
// each other through, comes from the DHT protocol itself).
func connectOnly(t *testing.T, ctx context.Context, a host.Host, b host.Host) {
	t.Helper()
	info := peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}
	if err := a.Connect(ctx, info); err != nil {
		t.Fatalf("connect %s -> %s failed: %v", a.ID(), b.ID(), err)
	}
}

// waitForRoutingTableNonEmpty polls until d's routing table has picked up at
// least one peer. dht.New's peer-added hook fires asynchronously (after
// protocol negotiation confirms the connected peer speaks the DHT protocol),
// so it can lag a host.Connect call by a beat; racing that gap with an
// immediate Provide/FindPeers call is the main source of flakiness in a
// from-scratch loopback DHT.
func waitForRoutingTableNonEmpty(t *testing.T, ctx context.Context, d *dht.IpfsDHT, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.RoutingTable().Size() > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context done while waiting for routing table to populate: %v", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("routing table did not pick up any peer within %s", timeout)
}

// TestSDNAdvertisementFlagIsCanonicalOnPublicAminoDHT is the A2 acceptance
// test (SDN_ALIGNMENT_FIX_LOOP): it builds a local, loopback-only 4-host
// Amino-style DHT swarm (one bootstrap + three peers, all speaking the stock
// public "/ipfs/kad/1.0.0" protocol via testAminoDHTOptions/publicDHTOptions)
// and proves that the SDN advertisement-flag rendezvous namespace — NOT mere
// DHT membership — is what identifies an SDN peer:
//
//   - host A advertises under the REAL production SDN membership flag
//     namespace (sdnAdvertisementDiscoveryNamespace, via the production
//     announceSDNAdvertisement function).
//   - host B is only ever explicitly connected to the shared bootstrap host
//     (never dialed directly to A) and discovers A exclusively through the
//     REAL production rendezvous-find path (DiscoverSDNAdvertisementPeers).
//   - host C advertises under a namespace that has nothing to do with SDN,
//     simulating an arbitrary public-IPFS DHT participant. Even though C is
//     part of the same small DHT mesh as A and B (and may well end up in
//     B's routing table / connected peers through ordinary Kademlia
//     convergence, exactly as unrelated public nodes would on the real
//     Amino DHT), it must NOT be returned when B queries the SDN flag
//     namespace — proving peer selection is scoped to the flag/namespace
//     result set, not to "any DHT peer."
func TestSDNAdvertisementFlagIsCanonicalOnPublicAminoDHT(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()

	bootstrapHost, _ := newTestAminoDHTHost(t, ctx)
	hostA, dhtA := newTestAminoDHTHost(t, ctx)
	hostB, dhtB := newTestAminoDHTHost(t, ctx)
	hostC, dhtC := newTestAminoDHTHost(t, ctx)

	// Star topology: A, B, and C each connect ONLY to the common bootstrap
	// host. B and A are never directly dialed to one another.
	connectOnly(t, ctx, hostA, bootstrapHost)
	connectOnly(t, ctx, hostB, bootstrapHost)
	connectOnly(t, ctx, hostC, bootstrapHost)

	// dht.New hooks a libp2p network notifee that adds a directly-connected
	// peer to the routing table once it confirms (via protocol
	// negotiation/identify) that the peer speaks the DHT protocol; that
	// confirmation can land a beat after host.Connect returns. Wait for each
	// routing table to pick up the bootstrap peer before driving any
	// provide/find traffic through it, or an early Provide/FindPeers call
	// can race an empty routing table and silently find nobody.
	for _, d := range []*dht.IpfsDHT{dhtA, dhtB, dhtC} {
		waitForRoutingTableNonEmpty(t, ctx, d, 10*time.Second)
	}

	// Nudge routing-table convergence through the shared bootstrap peer so
	// the subsequent provide/find round trips have someone to route
	// through. RefreshRoutingTable blocks until the walk completes.
	for _, d := range []*dht.IpfsDHT{dhtA, dhtB, dhtC} {
		select {
		case <-d.RefreshRoutingTable():
		case <-ctx.Done():
			t.Fatalf("timed out refreshing routing table: %v", ctx.Err())
		}
	}

	currentTarget, discoverTargets, err := sdnAdvertisementDiscoveryTargets("sdn-alignment-test/1.0.0", nil)
	if err != nil {
		t.Fatalf("sdnAdvertisementDiscoveryTargets failed: %v", err)
	}
	if currentTarget.Namespace != sdnAdvertisementDiscoveryNamespace+"/sdn-alignment-test/1.0.0" {
		t.Fatalf("unexpected advertisement namespace: %q", currentTarget.Namespace)
	}

	nodeA := &Node{ctx: ctx, host: hostA, dht: dhtA, sdnAdvertisementTarget: currentTarget}
	nodeB := &Node{
		ctx:                    ctx,
		host:                   hostB,
		dht:                    dhtB,
		sdnAdvertisementTarget: currentTarget,
		sdnDiscoveryTargets:    discoverTargets,
	}

	// Host A advertises under the REAL production SDN membership flag
	// namespace using the REAL production advertise function.
	nodeA.announceSDNAdvertisement(currentTarget)

	// Host C advertises under a namespace that has nothing to do with SDN —
	// a stand-in for an arbitrary public-IPFS DHT participant using
	// rendezvous discovery for something else entirely. Uses the same
	// drouting/dutil machinery the production code uses, just with a
	// non-SDN namespace, so this is not a strawman: it proves the SDN flag
	// query does not simply return "whatever is advertising nearby."
	unrelatedDiscovery := drouting.NewRoutingDiscovery(dhtC)
	dutil.Advertise(ctx, unrelatedDiscovery, "some-other-app/unrelated-rendezvous")

	// Host B discovers peers exclusively through the REAL production
	// rendezvous-find path (DiscoverSDNAdvertisementPeers), retried a few
	// times to absorb DHT provide/find propagation latency.
	var discoveredA bool
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 8*time.Second)
		if _, discErr := nodeB.DiscoverSDNAdvertisementPeers(attemptCtx); discErr != nil {
			t.Logf("DiscoverSDNAdvertisementPeers attempt failed (will retry): %v", discErr)
		}
		attemptCancel()

		if nodeB.hasSDNAdvertisementPeer(hostA.ID()) {
			discoveredA = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !discoveredA {
		t.Fatalf("host B never discovered host A via the SDN advertisement flag rendezvous namespace %q", currentTarget.Namespace)
	}

	flagsByPeer := nodeB.SDNAdvertisementFlagsByPeer()

	gotAFlags := flagsByPeer[hostA.ID().String()]
	if len(gotAFlags) != 1 || gotAFlags[0] != currentTarget.Flag {
		t.Fatalf("recorded flags for host A = %v, want [%q]", gotAFlags, currentTarget.Flag)
	}

	// Negative control: host C, discovered (if at all) only via an unrelated
	// rendezvous namespace, must never appear in the SDN flag result set —
	// mere DHT presence/connectivity is not evidence of SDN membership.
	if nodeB.hasSDNAdvertisementPeer(hostC.ID()) {
		t.Fatalf("host C (unrelated namespace) must NOT be recorded as an SDN advertisement peer")
	}
	if _, ok := flagsByPeer[hostC.ID().String()]; ok {
		t.Fatalf("host C (unrelated namespace) must NOT appear in the SDN advertisement flag result set: %v", flagsByPeer)
	}

	// hostB itself, and the bootstrap host (which never advertised at all),
	// must likewise be absent from the flag-verified result set.
	if nodeB.hasSDNAdvertisementPeer(hostB.ID()) {
		t.Fatalf("host B must not record itself as an SDN advertisement peer")
	}
	if nodeB.hasSDNAdvertisementPeer(bootstrapHost.ID()) {
		t.Fatalf("bootstrap host (never advertised) must NOT be recorded as an SDN advertisement peer")
	}

	// sdnAdvertisementDiscoveredPeerIDs (the helper buildP2PCapOptions now
	// uses instead of the raw DHT routing table) must expose exactly the
	// flag-verified set: A, and neither B, C, nor the bootstrap host.
	discoveredIDs := nodeB.sdnAdvertisementDiscoveredPeerIDs()
	foundA, foundOther := false, false
	for _, pid := range discoveredIDs {
		switch pid {
		case hostA.ID():
			foundA = true
		case hostC.ID(), bootstrapHost.ID(), hostB.ID():
			foundOther = true
		}
	}
	if !foundA {
		t.Fatalf("sdnAdvertisementDiscoveredPeerIDs() = %v, want to include host A (%s)", discoveredIDs, hostA.ID())
	}
	if foundOther {
		t.Fatalf("sdnAdvertisementDiscoveredPeerIDs() = %v, must not include non-flag-verified peers", discoveredIDs)
	}
}
