package node

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/kubo"
)

// startTestKubo runs the node's in-process Kubo the way node.init does for a
// given DHT participation, on loopback, with no RPC or gateway listener.
func startTestKubo(t *testing.T, mode dhtParticipation) *kubo.Node {
	t.Helper()
	key, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	kn, err := kubo.Start(context.Background(), kubo.Config{
		RepoPath:    filepath.Join(t.TempDir(), "kubo"),
		Key:         key,
		HostOptions: []libp2p.Option{libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0")},
		DHT:         kuboDHTMode(mode),
	})
	if err != nil {
		t.Fatalf("kubo.Start: %v", err)
	}
	t.Cleanup(func() { _ = kn.Stop() })
	return kn
}

func servesProtocol(kn *kubo.Node, want protocol.ID) bool {
	for _, p := range kn.Host().Mux().Protocols() {
		if p == want {
			return true
		}
	}
	return false
}

// TestNodeDHTJoinsStockIPFSProtocol: the node's DHT is Kubo's, on the stock
// public IPFS/Amino protocol "/ipfs/kad/1.0.0", and the legacy private
// "/spacedatanetwork/kad/1.0.0" is never registered. Asserted in server mode,
// where the handler is registered and observable on the mux.
func TestNodeDHTJoinsStockIPFSProtocol(t *testing.T) {
	t.Parallel()

	kn := startTestKubo(t, dhtParticipationServer)
	if servesProtocol(kn, "/spacedatanetwork/kad/1.0.0") {
		t.Fatalf("private DHT protocol registered; got protocols=%v", kn.Host().Mux().Protocols())
	}
	if !servesProtocol(kn, "/ipfs/kad/1.0.0") {
		t.Fatalf("stock IPFS DHT protocol not registered; got protocols=%v", kn.Host().Mux().Protocols())
	}
}

// TestNodeDHTModes: an explicit client never serves strangers' lookups (the
// setting host-01 made after 2026-08-08, when serving pinned it at 98.5% CPU),
// and dht_server: always serves regardless of reachability.
func TestNodeDHTModes(t *testing.T) {
	t.Parallel()

	if mode := startTestKubo(t, dhtParticipationClient).WANDHT().Mode(); mode != dht.ModeClient {
		t.Fatalf("client participation runs the public DHT in mode %v, want client", mode)
	}
	if mode := startTestKubo(t, dhtParticipationServer).WANDHT().Mode(); mode != dht.ModeServer {
		t.Fatalf("server participation runs the public DHT in mode %v, want server", mode)
	}
	if mode := startTestKubo(t, dhtParticipationOff).WANDHT().Mode(); mode != dht.ModeClient {
		t.Fatalf("peers.enable_dht: false runs the public DHT in mode %v, want client", mode)
	}
}
