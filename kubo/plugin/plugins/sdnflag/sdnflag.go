// Package sdnflag is the Space Data Network membership plugin: the first of
// the two SDN additions to upstream kubo (the other being the WasmEdge module
// runtime). It advertises this node as an SDN node on the public Amino DHT and
// discovers other SDN nodes, using a rendezvous namespace rather than a private
// swarm — so the node remains a full participant in the public IPFS DHT while
// still being able to distinguish SDN peers from arbitrary IPFS peers.
//
// A peer counts as an SDN node only when it advertises under, or is found via,
// the rendezvous namespace "space-data-network/discovery/advertisement-flag/<flag>"
// — never merely by appearing in the DHT routing table or an unrelated provider
// lookup. This mirrors the mechanism proven in the pre-rebase implementation
// (internal/node/advertisement_discovery.go), reduced to the membership core.
package sdnflag

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	core "github.com/ipfs/kubo/core"
	plugin "github.com/ipfs/kubo/plugin"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
)

var log = logging.Logger("plugin/sdnflag")

const (
	// discoveryNamespaceBase is the canonical SDN membership rendezvous key on
	// the public IPFS/Amino DHT. Kept byte-identical to the pre-rebase node so a
	// kubo-based SDN node and a legacy node discover each other.
	discoveryNamespaceBase = "space-data-network/discovery/advertisement-flag"
	// defaultFlag versions the namespace ("<base>/<flag>"). Must match across
	// SDN nodes to rendezvous.
	defaultFlag = "spacedatanetwork/1.0.0"

	advertiseInterval = 30 * time.Second
	discoverInterval  = 60 * time.Second
	discoverInitial   = 10 * time.Second
	findTimeout       = 30 * time.Second
)

type sdnFlagPlugin struct {
	enabled   bool
	namespace string

	mu    sync.RWMutex
	peers map[peer.ID]time.Time
}

var _ plugin.PluginDaemonInternal = (*sdnFlagPlugin)(nil)

// Plugins is the exported list of plugins that will be loaded.
var Plugins = []plugin.Plugin{
	&sdnFlagPlugin{},
}

func (*sdnFlagPlugin) Name() string    { return "sdnflag" }
func (*sdnFlagPlugin) Version() string { return "0.1.0" }

// Init reads optional config. Unlike most kubo plugins this is enabled by
// default: SDN membership is the reason this fork exists. Set
// Plugins.sdnflag.Config.Enabled=false to opt out, or .Flag to override the
// namespace version.
func (p *sdnFlagPlugin) Init(env *plugin.Environment) error {
	p.enabled = true
	flag := defaultFlag
	if env != nil {
		if cfg, ok := env.Config.(map[string]interface{}); ok {
			if v, ok := cfg["Enabled"].(bool); ok {
				p.enabled = v
			}
			if v, ok := cfg["Flag"].(string); ok && strings.TrimSpace(v) != "" {
				flag = strings.TrimSpace(v)
			}
		}
	}
	p.namespace = discoveryNamespaceBase + "/" + flag
	p.peers = make(map[peer.ID]time.Time)
	return nil
}

func (p *sdnFlagPlugin) Start(node *core.IpfsNode) error {
	if !p.enabled {
		return nil
	}
	if err := logging.SetLogLevel("plugin/sdnflag", "info"); err != nil {
		return fmt.Errorf("failed to set log level: %w", err)
	}
	if node.DHT == nil {
		log.Warn("SDN flag: DHT unavailable (offline?); SDN advertisement disabled")
		return nil
	}

	rd := drouting.NewRoutingDiscovery(node.DHT)
	log.Infof("SDN flag active: namespace=%q peer=%s", p.namespace, node.Identity)
	go p.advertiseLoop(node, rd)
	go p.discoverLoop(node, rd)
	return nil
}

func (p *sdnFlagPlugin) advertiseLoop(node *core.IpfsNode, rd *drouting.RoutingDiscovery) {
	ctx := node.Context()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, err := rd.Advertise(ctx, p.namespace); err != nil {
			log.Debugf("SDN flag advertise failed: %v", err)
		}
		timer.Reset(advertiseInterval)
	}
}

func (p *sdnFlagPlugin) discoverLoop(node *core.IpfsNode, rd *drouting.RoutingDiscovery) {
	ctx := node.Context()
	timer := time.NewTimer(discoverInitial)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		p.discoverOnce(ctx, node, rd)
		timer.Reset(discoverInterval)
	}
}

func (p *sdnFlagPlugin) discoverOnce(ctx context.Context, node *core.IpfsNode, rd *drouting.RoutingDiscovery) {
	findCtx, cancel := context.WithTimeout(ctx, findTimeout)
	defer cancel()

	peerChan, err := rd.FindPeers(findCtx, p.namespace)
	if err != nil {
		log.Debugf("SDN flag find peers failed: %v", err)
		return
	}
	self := node.Identity
	for info := range peerChan {
		if info.ID == "" || info.ID == self {
			continue
		}
		p.mu.Lock()
		_, known := p.peers[info.ID]
		p.peers[info.ID] = time.Now()
		p.mu.Unlock()
		if !known {
			log.Infof("SDN peer discovered: %s", info.ID)
		}
		if node.PeerHost.Network().Connectedness(info.ID) != network.Connected {
			if err := node.PeerHost.Connect(findCtx, info); err != nil {
				log.Debugf("SDN flag connect to %s failed: %v", info.ID, err)
			}
		}
	}
}

// SDNPeers returns the peer IDs discovered via the SDN flag namespace. Exposed
// for the node-status/API surface that later phases will wire up.
func (p *sdnFlagPlugin) SDNPeers() []peer.ID {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]peer.ID, 0, len(p.peers))
	for id := range p.peers {
		out = append(out, id)
	}
	return out
}

func (*sdnFlagPlugin) Close() error { return nil }
