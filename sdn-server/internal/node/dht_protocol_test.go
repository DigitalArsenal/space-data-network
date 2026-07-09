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
func TestPublicDHTOptionsJoinStockIPFSProtocol(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p.New failed: %v", err)
	}
	defer h.Close()

	d, err := dht.New(ctx, h, publicDHTOptions()...)
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

// TestPublicDHTOptionsPreserveAutoServerMode locks in that removing the
// private ProtocolPrefix did not also drop the auto-server routing mode the
// node relies on (dht.ModeAutoServer).
func TestPublicDHTOptionsPreserveAutoServerMode(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p.New failed: %v", err)
	}
	defer h.Close()

	d, err := dht.New(ctx, h, publicDHTOptions()...)
	if err != nil {
		t.Fatalf("dht.New with publicDHTOptions failed: %v", err)
	}
	defer d.Close()

	if mode := d.Mode(); mode != dht.ModeAutoServer {
		t.Fatalf("dht mode = %v, want dht.ModeAutoServer", mode)
	}
}
