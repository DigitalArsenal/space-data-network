package kubo_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	blocks "github.com/ipfs/go-block-format"
	"github.com/libp2p/go-libp2p-kad-dht/dual"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
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

// TestDHTOffFetchesFromAPeeredProducer: a node with its DHT off has no
// content routing, and Bitswap's broadcast control asks only peers that
// served it before, LAN peers and peered peers. So it never asks a producer
// it is merely connected to (host-01 and host-02, 2026-11-21: host-02 dialed
// host-01, both ledgers empty, every fetch of host-02's blocks through
// host-01's gateway timed out). Peered, the producer is asked and serves.
func TestDHTOffFetchesFromAPeeredProducer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	keyF, _ := newKey(t)
	keyP, idP := newKey(t)
	follower := startConfig(t, kubo.Config{RepoPath: filepath.Join(t.TempDir(), "kubo"), Key: keyF, DHT: kubo.DHTOff})
	producer := startConfig(t, kubo.Config{RepoPath: filepath.Join(t.TempDir(), "kubo"), Key: keyP, DHT: kubo.DHTOff})
	want := blocks.NewBlock([]byte("a producer's published shard, 2026-11-21"))
	if err := producer.IPFS().Blocks.AddBlock(ctx, want); err != nil {
		t.Fatal(err)
	}
	// The producer dials the follower, as host-02 dials host-01.
	if err := producer.Host().Connect(ctx, peer.AddrInfo{ID: follower.Host().ID(), Addrs: follower.Host().Addrs()}); err != nil {
		t.Fatalf("the producer connects to the follower: %v", err)
	}
	// Loopback is "LAN" to broadcast control; the follower knows the
	// producer by a public address, as host-01 knows host-02. Identify
	// finishes first, or it adds the loopback addresses back.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if protos, _ := follower.Host().Peerstore().GetProtocols(idP); len(protos) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("identify of the producer did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	public := multiaddr.StringCast("/ip4/203.0.113.7/tcp/4001")
	follower.Host().Peerstore().ClearAddrs(idP)
	follower.Host().Peerstore().AddAddr(idP, public, time.Hour)
	if addrs := follower.Host().Peerstore().Addrs(idP); len(addrs) != 1 || !addrs[0].Equal(public) {
		t.Fatalf("the follower knows the producer at %v, want only %s", addrs, public)
	}

	fetch, stop := context.WithTimeout(ctx, 5*time.Second)
	_, err := follower.IPFS().Blocks.GetBlock(fetch, want.Cid())
	stop()
	if err == nil {
		t.Fatal("a DHT-off follower fetched from a merely connected producer: the broadcast control this test reproduces is not in force")
	}
	if follower.Host().Network().Connectedness(idP) != network.Connected {
		t.Fatal("the producer's connection dropped; the failed fetch proves nothing")
	}

	if !follower.Peer(peer.AddrInfo{ID: idP}) {
		t.Fatal("Peer reported the producer as peered already")
	}
	if follower.Peer(peer.AddrInfo{ID: idP}) {
		t.Fatal("a second Peer added the producer again")
	}
	fetch, stop = context.WithTimeout(ctx, 10*time.Second)
	got, err := follower.IPFS().Blocks.GetBlock(fetch, want.Cid())
	stop()
	if err != nil {
		t.Fatalf("peered, the producer was not asked: %v", err)
	}
	if !bytes.Equal(got.RawData(), want.RawData()) {
		t.Fatal("received a different block")
	}
	follower.Unpeer(idP)
	for _, p := range follower.IPFS().Peering.ListPeers() {
		if p.ID == idP {
			t.Fatal("Unpeer left the producer peered")
		}
	}
}
