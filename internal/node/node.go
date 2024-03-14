// node/node.go
package node

import (
	"context"
	"fmt"
	"time"

	config "github.com/DigitalArsenal/space-data-network/configs"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	noise "github.com/libp2p/go-libp2p/p2p/security/noise"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
)

type NodeOptions struct {
	Mnemonic      string
	EncryptedJSON string
	Password      string
	RawKey        []byte
}

type Node struct {
	Host     host.Host
	DHT      *dht.IpfsDHT
	KeyStore *KeyStore
	wallet   *hdwallet.Wallet
}

func (n *Node) GetHost() host.Host {
	return n.Host
}

func (n *Node) GetWallet() *hdwallet.Wallet {
	return n.wallet
}

func NewNode(ctx context.Context, options ...NodeOptions) (*Node, error) {
	config.Init()

	var nodeOptions NodeOptions
	if len(options) > 0 {
		nodeOptions = options[0]
	}

	if config.Conf.Key.EntropyLength > 0 {
		validEntropySizes := map[int]bool{
			16: true,
			20: true,
			24: true,
			28: true,
			32: true,
		}

		if !validEntropySizes[config.Conf.Key.EntropyLength] {
			return nil, fmt.Errorf("invalid entropy length provided in config")
		}
	}

	node := &Node{}

	var err error

	pass := config.Conf.Datastore.Password
	if pass == "" {
		pass = generatePassword()
	}

	node.KeyStore, err = NewKeyStore(pass)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize key store: %w", err)
	}

	privKey, err := node.KeyStore.GetOrGeneratePrivateKey(nodeOptions)
	if err != nil {
		return node, fmt.Errorf("failed to get private key: %w", err)
	}

	node.Host, err = libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.EnableNATService(),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		libp2p.EnableAutoRelayWithPeerSource(
			autoRelayPeerSource,
			autorelay.WithMinInterval(0)),
		libp2p.Security(noise.ID, noise.New),
	)

	if err != nil {
		return node, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	fmt.Printf("Node PeerID: %s\n", node.Host.ID())

	// Set up PNM exchange protocol listener
	SetupPNMExchange(node)

	node.SetHDWallet()

	return node, nil
}

// autoRelayPeerSource returns a function that provides peers for auto-relay.
func autoRelayPeerSource(ctx context.Context, numPeers int) <-chan peer.AddrInfo {

	peerChan := make(chan peer.AddrInfo)

	r := make(chan peer.AddrInfo)

	go func() {
		defer close(r)
		for ; numPeers != 0; numPeers-- {
			select {
			case v, ok := <-peerChan:
				if !ok {
					return
				}
				select {
				case r <- v:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return r

}

func (n *Node) Start(ctx context.Context) error {
	var err error

	n.DHT, err = initDHT(ctx, n.Host)
	if err != nil {
		return fmt.Errorf("failed to initialize DHT: %w", err)
	}

	go discoverPeers(ctx, n, "space-data-network", 30*time.Second)

	/*ps, err := pubsub.NewGossipSub(ctx, n.Host)
	if err != nil {
		return fmt.Errorf("failed to initialize PubSub: %w", err)
	}

		topic, err := ps.Join("space-data-network")
		if err != nil {
			return fmt.Errorf("failed to join topic 'space-data-network': %w", err)
		}

		go streamConsoleTo(ctx, topic)

		sub, err := topic.Subscribe()
		if err != nil {
			return fmt.Errorf("failed to subscribe to topic: %w", err)
		}

		go printMessagesFrom(ctx, sub)
	*/
	return nil
}

func (n *Node) Stop() {
	fmt.Println("Shutting down node...")

	if n.KeyStore != nil {
		if err := n.KeyStore.Close(); err != nil {
			fmt.Println("Failed to close Keystore:", err)
		}
	}
	if n.Host != nil {
		if err := n.Host.Close(); err != nil {
			fmt.Println("Failed to close libp2p host:", err)
		}
	}
	if n.DHT != nil {
		if err := n.DHT.Close(); err != nil {
			fmt.Println("Failed to close DHT:", err)
		}
	}
	fmt.Println("Node stopped successfully.")
}
