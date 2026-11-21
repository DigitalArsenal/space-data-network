package kubo_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-kad-dht/dual"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/kubo"
)

// TestDHTOffJoinsNoDHTThroughAServingPeer: a node with its DHT off whose one
// peer serves the DHT (a provider of the owner-machine fleet and its customer)
// takes no part in any DHT. A DHT client "with no bootstrap peers" is not off:
// it puts the serving peer in its routing table, finds the peers that one
// knows, and dials them, which took every fleet provider onto the public DHT
// (2026-11-21: 143-160 public peers in each provider's table, outbound
// connections to public IPFS peers).
func TestDHTOffJoinsNoDHTThroughAServingPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	keyA, _ := newKey(t)
	keyB, _ := newKey(t)
	keyC, idC := newKey(t)
	// A serves the DHT and knows C, as the customer knows the public DHT.
	a := startConfig(t, kubo.Config{RepoPath: filepath.Join(t.TempDir(), "kubo"), Key: keyA, DHT: kubo.DHTServer})
	c := startConfig(t, kubo.Config{RepoPath: filepath.Join(t.TempDir(), "kubo"), Key: keyC, DHT: kubo.DHTServer})
	if err := c.Host().Connect(ctx, peer.AddrInfo{ID: a.Host().ID(), Addrs: a.Host().Addrs()}); err != nil {
		t.Fatalf("C connects to A: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for a.IPFS().DHT.WAN.RoutingTable().Find(idC) == "" && a.IPFS().DHT.LAN.RoutingTable().Find(idC) == "" {
		if time.Now().After(deadline) {
			t.Fatal("A never put C in its routing table")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// B has its DHT off and A as its only peer, as a provider has the customer.
	b := startConfig(t, kubo.Config{RepoPath: filepath.Join(t.TempDir(), "kubo"), Key: keyB, DHT: kubo.DHTOff})
	if err := b.Host().Connect(ctx, peer.AddrInfo{ID: a.Host().ID(), Addrs: a.Host().Addrs()}); err != nil {
		t.Fatalf("B connects to A: %v", err)
	}
	// Kubo hands a router without a DHT on as a nil *dual.DHT.
	if client, ok := b.IPFS().DHTClient.(*dual.DHT); b.IPFS().DHT != nil || (b.IPFS().DHTClient != nil && (!ok || client != nil)) {
		t.Fatalf("a node with its DHT off runs a DHT (DHT %v, DHTClient %T)", b.IPFS().DHT != nil, b.IPFS().DHTClient)
	}
	// A DHT client would find C through A here and dial it.
	lookup, stop := context.WithTimeout(ctx, 10*time.Second)
	_, err := b.IPFS().Routing.FindPeer(lookup, idC)
	stop()
	if err == nil {
		t.Fatal("a node with its DHT off found a peer through the DHT")
	}
	time.Sleep(2 * time.Second)
	if b.Host().Network().Connectedness(idC) == network.Connected {
		t.Fatal("a node with its DHT off connected to a peer it learned through its DHT peer")
	}
	if peers := b.Host().Network().Peers(); len(peers) != 1 || peers[0] != a.Host().ID() {
		t.Fatalf("a node with its DHT off is connected to %v, want only A", peers)
	}
}
