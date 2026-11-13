package kubo

import (
	"context"
	"time"

	"github.com/ipfs/boxo/bitswap/network/bsnet"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const bitswapStreamOpenTimeout = 30 * time.Second

// bitswapHost must wrap the routed host, so this policy runs before its
// NewStream can enter Connect/FindPeer. Replies use an existing connection;
// explicit Connect calls (including Bitswap client discovery) still dial.
type bitswapHost struct{ host.Host }

var _ host.Host = (*bitswapHost)(nil)

func (h *bitswapHost) NewStream(ctx context.Context, p peer.ID, protocols ...protocol.ID) (network.Stream, error) {
	for _, id := range protocols {
		switch id {
		case bsnet.ProtocolBitswap, bsnet.ProtocolBitswapOneOne,
			bsnet.ProtocolBitswapOneZero, bsnet.ProtocolBitswapNoVers:
			ctx, cancel := context.WithTimeout(ctx, bitswapStreamOpenTimeout)
			defer cancel()
			return h.Host.NewStream(network.WithNoDial(ctx, "bitswap requires an existing connection"), p, protocols...)
		}
	}
	return h.Host.NewStream(ctx, p, protocols...)
}
