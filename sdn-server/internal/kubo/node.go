// Package kubo runs upstream Kubo inside this process, on the node's own
// libp2p host and identity.
//
// Owner, 2026-10-05: "a single peer ID tied into the crypto subsystem of SDN,
// and then updates for kubo / helia are rolled into SDN". Before this a box
// ran two libp2p peers: sdn-server's host and a separate `ipfs daemon` (a
// supervised child in bundles, ipfs.service on the fleet) with its own random
// identity. Now there is one process, one host and one peer ID, and Kubo's
// version moves only with an SDN release.
//
// Kubo is linked as a library through its public seams only: the plugin
// loader, fsrepo, core.NewNode with BuildCfg.Repo/Host/Routing, and corehttp
// for the RPC API and the gateway. No Kubo source is edited, and the module
// graph equals Kubo's own (go.mod carries Kubo's replace and exclude blocks
// verbatim, because those apply only in the main module).
//
// The node keeps the contract the supervised child had: the RPC API and the
// gateway answer on loopback only, so every CID, pin, publication and archive
// path that speaks the Kubo RPC keeps working unchanged.
package kubo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"

	ipfs "github.com/ipfs/kubo"
	"github.com/ipfs/kubo/commands"
	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core"
	"github.com/ipfs/kubo/core/corehttp"
	kubolibp2p "github.com/ipfs/kubo/core/node/libp2p"
	"github.com/ipfs/kubo/plugin/loader"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	manet "github.com/multiformats/go-multiaddr/net"
)

const (
	// DefaultAPIAddr is where the RPC API answers: deliberately not Kubo's
	// own 5001, which the node's admin listener uses.
	DefaultAPIAddr = "127.0.0.1:5002"
	// DefaultGatewayAddr is where the HTTP gateway answers.
	DefaultGatewayAddr = "127.0.0.1:8080"
)

// DHTMode is how much of the public DHT the node takes part in. It maps the
// node's tri-state (network.dht_server) and peers.enable_dht onto Kubo's own
// routing options.
type DHTMode int

const (
	// DHTAuto serves while AutoNAT reports the node reachable and is a client
	// otherwise. The default.
	DHTAuto DHTMode = iota
	// DHTServer serves regardless of reachability.
	DHTServer
	// DHTClient queries and publishes its own records, never serves.
	DHTClient
	// DHTOff is a client with no bootstrap peers: it joins nothing.
	DHTOff
)

func (m DHTMode) routing() kubolibp2p.RoutingOption {
	switch m {
	case DHTServer:
		return kubolibp2p.DHTServerOption
	case DHTClient, DHTOff:
		return kubolibp2p.DHTClientOption
	default:
		return kubolibp2p.DHTOption
	}
}

func (m DHTMode) routingType() string {
	switch m {
	case DHTServer:
		return "dhtserver"
	case DHTClient, DHTOff:
		return "dhtclient"
	default:
		return "dht"
	}
}

// Config describes the in-process Kubo node.
type Config struct {
	// RepoPath is the Kubo repository. An existing repository (from the
	// supervised child or an operator's ipfs.service) is opened in place;
	// a missing one is created.
	RepoPath string
	// Key is the node's identity key from the HD identity bundle. Kubo gets
	// it in memory; the repository on disk keeps the PeerID only.
	Key crypto.PrivKey
	// HostOptions are the node's own libp2p options: transports, listen and
	// announce addresses, limits, relay, NAT and the rest. The host is built
	// from these, with Kubo's peerstore. They must not carry Identity,
	// Routing or ConnectionGater: the identity comes from Key, routing from
	// DHT, and Gate is composed with Kubo's own gate.
	HostOptions []libp2p.Option
	// Gate is the node's connection gate. A connection must pass both it and
	// the gate Kubo always installs.
	Gate connmgr.ConnectionGater
	// DHT is the node's DHT participation.
	DHT DHTMode
	// Bootstrap is the multiaddr list Kubo bootstraps its DHT from. Ignored
	// for DHTOff.
	Bootstrap []string
	// APIAddr and GatewayAddr are loopback host:port pairs. Port 0 picks a
	// free port; APIURL and GatewayURL report what was bound. Empty serves
	// nothing there.
	APIAddr     string
	GatewayAddr string
	// FetchFromNetwork lets the gateway fetch content this node does not
	// hold. Off by default: the gateway serves what the node has.
	FetchFromNetwork bool
	// Logf receives node events; nil discards them.
	Logf func(format string, args ...any)
}

// Node is a running in-process Kubo node.
type Node struct {
	ipfs       *core.IpfsNode
	apiURL     string
	gatewayURL string
	served     sync.WaitGroup
	stopOnce   sync.Once
	stopErr    error
}

// AgentVersion is the identify agent string for a node whose own agent is
// sdnAgent: Kubo's, with the node's as its suffix, for example
// "kubo/0.43.1/spacedatanetwork/1.1.0". SDN membership checks match on the
// "spacedatanetwork" it contains.
func AgentVersion(sdnAgent string) string {
	ipfs.SetUserAgentSuffix(sdnAgent)
	return ipfs.GetUserAgentVersion()
}

// Version is the linked Kubo release.
func Version() string { return ipfs.CurrentVersionNumber }

// RepoVersion is the repository version the linked Kubo reads and writes.
func RepoVersion() int { return ipfs.RepoVersion }

var plugins struct {
	once   sync.Once
	loader *loader.PluginLoader
	err    error
}

// loadPlugins registers Kubo's built-in plugins (the datastores among them)
// once per process; a second Inject would register them twice.
func loadPlugins() (*loader.PluginLoader, error) {
	plugins.once.Do(func() {
		l, err := loader.NewPluginLoader("")
		if err == nil {
			err = l.Initialize()
		}
		if err == nil {
			err = l.Inject()
		}
		plugins.loader, plugins.err = l, err
	})
	return plugins.loader, plugins.err
}

// Start opens (or creates) the repository and runs the node: Kubo's host is
// built from the node's options and key, the DHT from DHT, and the RPC API
// and gateway listen on loopback.
func Start(ctx context.Context, cfg Config) (*Node, error) {
	if strings.TrimSpace(cfg.RepoPath) == "" {
		return nil, errors.New("kubo: repository path is required")
	}
	if cfg.Key == nil {
		return nil, errors.New("kubo: the node identity key is required")
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	var apiLis, gatewayLis net.Listener
	closeListeners := func() {
		if apiLis != nil {
			apiLis.Close()
		}
		if gatewayLis != nil {
			gatewayLis.Close()
		}
	}
	var err error
	if strings.TrimSpace(cfg.APIAddr) != "" {
		if apiLis, err = listenLoopback(cfg.APIAddr); err != nil {
			return nil, fmt.Errorf("kubo: RPC API: %w", err)
		}
	}
	if strings.TrimSpace(cfg.GatewayAddr) != "" {
		if gatewayLis, err = listenLoopback(cfg.GatewayAddr); err != nil {
			closeListeners()
			return nil, fmt.Errorf("kubo: gateway: %w", err)
		}
	}

	pl, err := loadPlugins()
	if err != nil {
		closeListeners()
		return nil, fmt.Errorf("kubo: plugins: %w", err)
	}
	id, err := peer.IDFromPrivateKey(cfg.Key)
	if err != nil {
		closeListeners()
		return nil, fmt.Errorf("kubo: node identity: %w", err)
	}
	r, tookOver, err := openRepo(cfg.RepoPath, id, cfg.Logf)
	if err != nil {
		closeListeners()
		return nil, err
	}
	settings := cfg.settings
	if tookOver {
		// The repository's content was last announced under the separate
		// Kubo's peer ID, and its provider would resume that schedule: each
		// keyspace region recently reprovided waits for its slot, up to
		// Provide.DHT.Interval (22 h). Kubo's own switch for a fresh start
		// clears that history, so every region is reprovided under the node's
		// peer ID as soon as the provider is online. Later starts resume.
		settings = func(k *config.Config) {
			cfg.settings(k)
			k.Provide.DHT.ResumeEnabled = config.False
		}
	}
	repo := &identityRepo{Repo: r, key: cfg.Key, id: id, settings: settings}

	node, err := core.NewNode(ctx, &core.BuildCfg{
		Online:    true,
		Permanent: true,
		Repo:      repo,
		Host:      hostOption(cfg.HostOptions, cfg.Gate),
		Routing:   cfg.DHT.routing(),
	})
	if err != nil {
		r.Close()
		closeListeners()
		return nil, fmt.Errorf("kubo: start node: %w", err)
	}
	if node.Identity != id {
		node.Close()
		closeListeners()
		return nil, fmt.Errorf("kubo: node came up as %s, want the node identity %s", node.Identity, id)
	}

	n := &Node{ipfs: node}
	cctx := commands.Context{
		ConfigRoot: cfg.RepoPath,
		ReqLog:     &commands.ReqLog{},
		Plugins:    pl,
		ConstructNode: func() (*core.IpfsNode, error) {
			return node, nil
		},
	}
	// The RPC commands are Kubo's own, handed this node. The gateway serves
	// content only, never the commands.
	if apiLis != nil {
		if n.apiURL, err = n.serve(apiLis, corehttp.CommandsOption(cctx), corehttp.CheckVersionOption(), corehttp.VersionOption()); err != nil {
			if gatewayLis != nil {
				gatewayLis.Close()
			}
			n.Stop()
			return nil, fmt.Errorf("kubo: RPC API: %w", err)
		}
		if ma, err := manet.FromNetAddr(apiLis.Addr()); err == nil {
			// The `ipfs` CLI finds a running node through this file.
			_ = r.SetAPIAddr(ma)
		}
	}
	if gatewayLis != nil {
		if n.gatewayURL, err = n.serve(gatewayLis, corehttp.HostnameOption(), corehttp.GatewayOption("/ipfs", "/ipns"), corehttp.VersionOption()); err != nil {
			n.Stop()
			return nil, fmt.Errorf("kubo: gateway: %w", err)
		}
	}
	cfg.Logf("Kubo %s in process as %s (repo %s, repo version %d): RPC %s, gateway %s",
		Version(), id, cfg.RepoPath, RepoVersion(), valueOr(n.apiURL, "off"), valueOr(n.gatewayURL, "off"))
	return n, nil
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// listenLoopback binds addr, which must be a loopback host:port.
func listenLoopback(addr string) (net.Listener, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, errors.New("no listen address")
	}
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("bad address %q: %w", addr, err)
	}
	if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("%q is not a loopback address; the RPC API and gateway listen on loopback only", addr)
	}
	return net.Listen("tcp", addr)
}

// LoopbackAddr returns the host:port of a loopback http URL, or an error
// naming why the URL cannot be where this node serves.
func LoopbackAddr(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" || u.Host == "" {
		return "", fmt.Errorf("%q is not an http://host:port URL", rawURL)
	}
	h, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		return "", fmt.Errorf("%q has no port: %w", rawURL, err)
	}
	if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("%q is not on loopback", rawURL)
	}
	return u.Host, nil
}

// serve runs Kubo's HTTP handler on lis until the node closes, and returns
// the listener's base URL once it is serving.
func (n *Node) serve(lis net.Listener, opts ...corehttp.ServeOption) (string, error) {
	ready := make(chan struct{})
	failed := make(chan error, 1)
	n.served.Add(1)
	go func() {
		defer n.served.Done()
		if err := corehttp.ServeWithReady(n.ipfs, lis, ready, opts...); err != nil {
			failed <- err
		}
	}()
	select {
	case <-ready:
		return "http://" + lis.Addr().String(), nil
	case err := <-failed:
		return "", err
	}
}

// IPFS is the running Kubo node.
func (n *Node) IPFS() *core.IpfsNode { return n.ipfs }

// Host is the node's one libp2p host.
func (n *Node) Host() host.Host { return n.ipfs.PeerHost }

// WANDHT is the public (Amino) DHT, /ipfs/kad/1.0.0.
func (n *Node) WANDHT() *dht.IpfsDHT {
	if n.ipfs.DHT == nil {
		return nil
	}
	return n.ipfs.DHT.WAN
}

// APIURL is the RPC API base URL.
func (n *Node) APIURL() string { return n.apiURL }

// GatewayURL is the gateway base URL, empty when no gateway is served.
func (n *Node) GatewayURL() string { return n.gatewayURL }

// Stop closes the node (host, DHT, repository) and waits for the HTTP
// servers, which end with it.
func (n *Node) Stop() error {
	n.stopOnce.Do(func() {
		n.stopErr = n.ipfs.Close()
		n.served.Wait()
	})
	return n.stopErr
}

// settings are the node-owned Kubo settings, applied in memory on every
// config read so the node's own configuration stays the one source of truth.
// What the host does (listening, transports, limits, relay, NAT) is the
// host options' business, so Kubo's own versions of those are switched off
// here rather than left to build a second, unused copy.
func (c Config) settings(k *config.Config) {
	k.Addresses.Swarm = nil
	k.Addresses.Announce = nil
	k.Addresses.AppendAnnounce = nil
	k.Addresses.NoAnnounce = nil
	k.Addresses.API = nil
	k.Addresses.Gateway = nil
	if c.DHT == DHTOff {
		k.Bootstrap = []string{}
	} else {
		k.Bootstrap = append([]string{}, c.Bootstrap...)
	}
	// AutoConf rewrites bootstrap peers and routers from a remote file: this
	// node's routing is libp2p only, and its bootstrap list is its own.
	k.AutoConf.Enabled = config.False
	k.Routing.Type = config.NewOptionalString(c.DHT.routingType())
	k.Routing.DelegatedRouters = nil
	k.Ipns.DelegatedPublishers = nil
	k.DNS.Resolvers = map[string]string{}
	k.AutoTLS.Enabled = config.False
	k.Swarm.ResourceMgr.Enabled = config.False
	k.Swarm.ConnMgr.Type = config.NewOptionalString("none")
	k.Swarm.RelayClient.Enabled = config.False
	k.Swarm.RelayService.Enabled = config.False
	k.Swarm.DisableBandwidthMetrics = true
	k.Swarm.AddrFilters = nil
	k.Discovery.MDNS.Enabled = false
	k.Pubsub.Enabled = config.False
	k.Gateway.NoFetch = !c.FetchFromNetwork
}
