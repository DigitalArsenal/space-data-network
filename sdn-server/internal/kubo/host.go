package kubo

import (
	"fmt"

	kubolibp2p "github.com/ipfs/kubo/core/node/libp2p"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	ma "github.com/multiformats/go-multiaddr"
)

// hostOption is Kubo's BuildCfg.Host seam. Kubo hands it the peer ID, its
// peerstore (holding the node key) and the options it derived from its own
// config. The host is built from the node's own options instead, because
// those carry what the node is tuned for (transports, browser-friendly
// limits, relay bounds, the NAT watchdog). Kubo's versions of those are
// switched off in its config (see Config.settings). One thing of Kubo's is
// always kept: the connection gate it installs, which is ANDed with the
// node's.
func hostOption(options []libp2p.Option, gate connmgr.ConnectionGater) kubolibp2p.HostOption {
	return func(id peer.ID, ps peerstore.Peerstore, kuboOptions ...libp2p.Option) (host.Host, error) {
		key := ps.PrivKey(id)
		if key == nil {
			return nil, fmt.Errorf("kubo: missing private key for node ID %s", id)
		}
		var kubo libp2p.Config
		if err := kubo.Apply(kuboOptions...); err != nil {
			return nil, fmt.Errorf("kubo: read Kubo's host options: %w", err)
		}
		var cfg libp2p.Config
		own := append([]libp2p.Option{libp2p.Identity(key), libp2p.Peerstore(ps)}, options...)
		if err := cfg.Apply(append(own, libp2p.FallbackDefaults)...); err != nil {
			return nil, err
		}
		if cfg.ConnectionGater != nil {
			return nil, fmt.Errorf("kubo: the node's host options carry a connection gate; pass it as Config.Gate")
		}
		cfg.ConnectionGater = composeGates(kubo.ConnectionGater, gate)
		return cfg.NewNode()
	}
}

func composeGates(kubo, node connmgr.ConnectionGater) connmgr.ConnectionGater {
	switch {
	case kubo == nil:
		return node
	case node == nil:
		return kubo
	default:
		return bothGates{kubo: kubo, node: node}
	}
}

// bothGates admits a connection only when both gates do.
type bothGates struct{ kubo, node connmgr.ConnectionGater }

func (g bothGates) InterceptPeerDial(p peer.ID) bool {
	return g.kubo.InterceptPeerDial(p) && g.node.InterceptPeerDial(p)
}

func (g bothGates) InterceptAddrDial(p peer.ID, m ma.Multiaddr) bool {
	return g.kubo.InterceptAddrDial(p, m) && g.node.InterceptAddrDial(p, m)
}

func (g bothGates) InterceptAccept(c network.ConnMultiaddrs) bool {
	return g.kubo.InterceptAccept(c) && g.node.InterceptAccept(c)
}

func (g bothGates) InterceptSecured(d network.Direction, p peer.ID, c network.ConnMultiaddrs) bool {
	return g.kubo.InterceptSecured(d, p, c) && g.node.InterceptSecured(d, p, c)
}

func (g bothGates) InterceptUpgraded(c network.Conn) (bool, control.DisconnectReason) {
	if ok, why := g.kubo.InterceptUpgraded(c); !ok {
		return false, why
	}
	return g.node.InterceptUpgraded(c)
}
