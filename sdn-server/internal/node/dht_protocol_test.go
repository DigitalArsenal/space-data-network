package node

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// TestPublicDHTOptionsJoinStockIPFSProtocol asserts that publicDHTOptions
// (the exact option set the node passes to dht.New) results in the node
// speaking the stock public IPFS/Amino DHT protocol "/ipfs/kad/1.0.0", and
// that the legacy private "/spacedatanetwork/kad/1.0.0" protocol is never
// registered. This is a fast, network-free unit test: the libp2p host only
// binds a loopback listener and never dials out.
//
// This is about the PROTOCOL PREFIX (which swarm the node joins), not about
// routing mode, so it is asserted in server mode where the handler is
// registered and the protocol names are observable on the mux. Which mode the
// node runs in by default is a separate question, tested in
// TestPublicDHTOptionsDefaultToClientMode.
func TestPublicDHTOptionsJoinStockIPFSProtocol(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p.New failed: %v", err)
	}
	defer h.Close()

	d, err := dht.New(ctx, h, publicDHTOptions(dhtParticipationServer)...)
	if err != nil {
		t.Fatalf("dht.New with publicDHTOptions failed: %v", err)
	}
	defer d.Close()

	const stockProtocol = protocol.ID("/ipfs/kad/1.0.0")
	const privateProtocol = protocol.ID("/spacedatanetwork/kad/1.0.0")

	registered := h.Mux().Protocols()
	var foundStock bool
	for _, p := range registered {
		if p == privateProtocol {
			t.Fatalf("private DHT protocol %q must not be registered; got protocols=%v", privateProtocol, registered)
		}
		if p == stockProtocol {
			foundStock = true
		}
	}
	if !foundStock {
		t.Fatalf("stock IPFS DHT protocol %q not registered on host; got protocols=%v", stockProtocol, registered)
	}
}

// TestPublicDHTOptionsDefaultToClientMode replaces the former
// TestPublicDHTOptionsPreserveAutoServerMode, which locked in dht.ModeAutoServer
// as "the auto-server routing mode the node relies on".
//
// IT RELIED ON NOTHING. Nothing in this node needs to SERVE the public Amino
// DHT; every capability it uses — querying, and PROVIDING its own records for
// module-delivery provider discovery — works identically in client mode. What
// server mode actually bought was an unbounded public workload. Measured on
// host-01 (2 vCPU, sole job: serving encrypted modules to browsers), 2026-08-08:
//
//   - 780 established inbound connections on :4004 from ~700 distinct internet
//     IPs, against a legitimate peer set of one sibling node plus browsers;
//   - 2105 go-libp2p-kad-dht handler warnings in 70 minutes, i.e. the node
//     answering strangers' lookups roughly every two seconds;
//   - the daemon pinned at 98.5% CPU with a load average of 3.5 on two cores.
//
// In that state concurrent module-delivery streams lost the race for scheduler
// time and resource-manager headroom and were reset with StreamErrorCode
// 0x1002, which reached the owner as "stream reset" on a first module fetch.
//
// Server mode remains available and is a deliberate, config-gated choice
// (`network.dht_server: true`) for a node actually deployed as public DHT
// infrastructure and sized for it.
func TestPublicDHTOptionsDefaultToClientMode(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	newDHT := func(mode dhtParticipation) *dht.IpfsDHT {
		h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
		if err != nil {
			t.Fatalf("libp2p.New failed: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		d, err := dht.New(ctx, h, publicDHTOptions(mode)...)
		if err != nil {
			t.Fatalf("dht.New with publicDHTOptions(%v) failed: %v", mode, err)
		}
		t.Cleanup(func() { _ = d.Close() })
		return d
	}

	if mode := newDHT(dhtParticipationClient).Mode(); mode != dht.ModeClient {
		t.Fatalf("default dht mode = %v, want dht.ModeClient — the node must not serve the public DHT unless asked", mode)
	}
	if mode := newDHT(dhtParticipationServer).Mode(); mode != dht.ModeAutoServer {
		t.Fatalf("dht_server:true mode = %v, want dht.ModeAutoServer", mode)
	}
}
