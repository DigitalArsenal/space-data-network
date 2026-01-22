// Package main provides the entry point for the Space Data Network edge relay.
// The edge relay is a minimal node that only provides relay services without storage.
//
//go:build edge
// +build edge

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/libp2p/go-libp2p/p2p/transport/websocket"
	"github.com/multiformats/go-multiaddr"
	"github.com/spf13/cobra"
)

var log = logging.Logger("sdn-edge")

// EdgeConfig contains edge relay configuration.
type EdgeConfig struct {
	ListenAddrs    []string
	BootstrapPeers []string
	MaxConnections int
	HealthPort     int
}

var rootCmd = &cobra.Command{
	Use:   "spacedatanetwork-edge",
	Short: "Space Data Network Edge Relay",
	Long: `spacedatanetwork-edge runs a minimal edge relay node that provides
relay services for browser clients and peers behind firewalls.

It does not store data or process queries - it only relays connections
and forwards PubSub messages.`,
	RunE: runEdge,
}

var (
	listenAddrs    []string
	bootstrapPeers []string
	maxConns       int
	healthPort     int
	debug          bool
)

func init() {
	rootCmd.Flags().StringArrayVarP(&listenAddrs, "listen", "l", []string{"/ip4/0.0.0.0/tcp/8080/ws"}, "listen addresses")
	rootCmd.Flags().StringArrayVarP(&bootstrapPeers, "bootstrap", "b", []string{}, "bootstrap peer addresses")
	rootCmd.Flags().IntVarP(&maxConns, "max-conns", "m", 500, "maximum connections")
	rootCmd.Flags().IntVarP(&healthPort, "health-port", "p", 0, "health check port (0 to disable)")
	rootCmd.Flags().BoolVarP(&debug, "debug", "d", false, "enable debug logging")
}

func main() {
	if debug {
		logging.SetAllLoggers(logging.LevelDebug)
	} else {
		logging.SetAllLoggers(logging.LevelInfo)
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runEdge(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := EdgeConfig{
		ListenAddrs:    listenAddrs,
		BootstrapPeers: bootstrapPeers,
		MaxConnections: maxConns,
		HealthPort:     healthPort,
	}

	edge, err := NewEdgeNode(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to create edge node: %w", err)
	}

	log.Info("Starting Space Data Network Edge Relay...")
	log.Infof("Peer ID: %s", edge.PeerID())
	for _, addr := range edge.ListenAddrs() {
		log.Infof("Listening on: %s/p2p/%s", addr, edge.PeerID())
	}

	// Start health check server if enabled
	if healthPort > 0 {
		go startHealthServer(healthPort, edge)
	}

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Info("Shutting down...")
	return edge.Close()
}

// EdgeNode represents a minimal edge relay node.
type EdgeNode struct {
	host   host.Host
	dht    *dht.IpfsDHT
	pubsub *pubsub.PubSub
	ctx    context.Context
	cancel context.CancelFunc
}

// NewEdgeNode creates a new edge relay node.
func NewEdgeNode(ctx context.Context, cfg EdgeConfig) (*EdgeNode, error) {
	nodeCtx, cancel := context.WithCancel(ctx)

	// Generate identity key
	privKey, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}

	// Parse listen addresses
	listenMAs := make([]multiaddr.Multiaddr, 0, len(cfg.ListenAddrs))
	for _, addr := range cfg.ListenAddrs {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			log.Warnf("Invalid listen address %s: %v", addr, err)
			continue
		}
		listenMAs = append(listenMAs, ma)
	}

	// Create connection manager with low limits for edge
	connMgr, err := connmgr.NewConnManager(
		10,                  // low water
		cfg.MaxConnections,  // high water
		connmgr.WithGracePeriod(time.Minute),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create connection manager: %w", err)
	}

	// Create minimal libp2p host for relay
	var dhtRouting *dht.IpfsDHT
	h, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrs(listenMAs...),

		// WebSocket for edge relays
		libp2p.Transport(websocket.New),

		// Security
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.Security(noise.ID, noise.New),

		// Connection management
		libp2p.ConnectionManager(connMgr),

		// Enable relay services (BE a relay for others)
		libp2p.EnableRelay(),
		libp2p.EnableRelayService(),
		libp2p.EnableHolePunching(),

		// DHT for peer discovery only
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			var err error
			dhtRouting, err = dht.New(nodeCtx, h,
				dht.Mode(dht.ModeServer),
				dht.ProtocolPrefix("/spacedatanetwork"),
			)
			return dhtRouting, err
		}),

		libp2p.NATPortMap(),
		libp2p.EnableNATService(),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	// Create GossipSub for message relay
	ps, err := pubsub.NewGossipSub(nodeCtx, h)
	if err != nil {
		h.Close()
		cancel()
		return nil, fmt.Errorf("failed to create pubsub: %w", err)
	}

	edge := &EdgeNode{
		host:   h,
		dht:    dhtRouting,
		pubsub: ps,
		ctx:    nodeCtx,
		cancel: cancel,
	}

	// Bootstrap DHT
	if err := dhtRouting.Bootstrap(nodeCtx); err != nil {
		log.Warnf("DHT bootstrap warning: %v", err)
	}

	// Connect to bootstrap peers
	for _, addr := range cfg.BootstrapPeers {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			log.Warnf("Invalid bootstrap address %s: %v", addr, err)
			continue
		}
		peerInfo, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			log.Warnf("Failed to parse peer info from %s: %v", addr, err)
			continue
		}
		go func(pi peer.AddrInfo) {
			if err := h.Connect(nodeCtx, pi); err != nil {
				log.Warnf("Failed to connect to bootstrap peer %s: %v", pi.ID, err)
			} else {
				log.Infof("Connected to bootstrap peer %s", pi.ID)
			}
		}(*peerInfo)
	}

	// Join edge relay announcement topic
	_, err = ps.Join("/spacedatanetwork/edge-relays")
	if err != nil {
		log.Warnf("Failed to join edge relay topic: %v", err)
	}

	return edge, nil
}

// PeerID returns the node's peer ID.
func (e *EdgeNode) PeerID() peer.ID {
	return e.host.ID()
}

// ListenAddrs returns the node's listen addresses.
func (e *EdgeNode) ListenAddrs() []multiaddr.Multiaddr {
	return e.host.Addrs()
}

// ConnectedPeers returns the number of connected peers.
func (e *EdgeNode) ConnectedPeers() int {
	return len(e.host.Network().Peers())
}

// Close shuts down the edge node.
func (e *EdgeNode) Close() error {
	e.cancel()
	return e.host.Close()
}

// Health check server
func startHealthServer(port int, edge *EdgeNode) {
	// Simple HTTP health check
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","peers":%d,"peer_id":"%s"}`,
			edge.ConnectedPeers(), edge.PeerID())
	})

	addr := fmt.Sprintf(":%d", port)
	log.Infof("Health check server listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Warnf("Health server error: %v", err)
	}
}
