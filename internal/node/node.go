// node/node.go
package node

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"

	"os"

	config "github.com/DigitalArsenal/space-data-network/configs"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/crypto"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
)

type Node struct {
	Host     host.Host
	DHT      *dht.IpfsDHT
	KeyStore *KeyStore
}

// NewNode initializes a new libp2p node with the given configuration.
func NewNode(ctx context.Context) (*Node, error) {

	keyStore, err := NewKeyStore()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize key store: %w", err)
	}

	// Return a new Node instance
	return &Node{KeyStore: keyStore}, nil
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

// Start begins the node operation, such as connecting to peers and handling connections.
func (n *Node) Start(ctx context.Context) error {
	pass := config.Conf.Datastore.Password
	if pass == "" {
		pass = generatePassword() // From keystore.go
	}

	privKey, err := n.KeyStore.GetPrivateKey(pass)
	if err != nil {
		return fmt.Errorf("failed to get private key: %w", err)
	}

	n.Host, err = libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.EnableNATService(),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		libp2p.EnableAutoRelayWithPeerSource(
			autoRelayPeerSource,
			autorelay.WithMinInterval(0)),
	)
	if err != nil {
		return fmt.Errorf("failed to create libp2p host: %w", err)
	}

	n.DHT, err = initDHT(ctx, n.Host)
	if err != nil {
		return fmt.Errorf("failed to initialize DHT: %w", err)
	}

	go discoverPeers(ctx, n.Host, n.DHT, "space-data-network")

	ps, err := pubsub.NewGossipSub(ctx, n.Host)
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

	return nil
}

// Stop gracefully shuts down the libp2p node.
// Stop gracefully shuts down the libp2p node and other components.
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

func streamConsoleTo(ctx context.Context, topic *pubsub.Topic) {
	reader := bufio.NewReader(os.Stdin)
	for {
		select {
		case <-ctx.Done():
			fmt.Println("Stopping console stream due to context cancellation.")
			return
		default:
			s, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					fmt.Println("EOF received from stdin, stopping console stream.")
					return
				}
				fmt.Printf("Error reading from stdin: %v\n", err)
				continue
			}
			if err := topic.Publish(ctx, []byte(s)); err != nil {
				fmt.Printf("Publish error: %v\n", err)
			}
		}
	}
}

func printMessagesFrom(ctx context.Context, sub *pubsub.Subscription) {
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			fmt.Printf("Failed to get next message: %v\n", err)
			return
		}
		fmt.Printf("Message from %s: %s\n", msg.ReceivedFrom, string(msg.Data))
	}
}

// Add a new method to Node to extract the public key
func (n *Node) PublicKey(marshaled bool) (string, error) {
	// Check if the Host is nil
	if n.Host == nil {
		return "", fmt.Errorf("host is not initialized")
	}

	// Extract public key from the host's ID
	pubKey, err := n.Host.ID().ExtractPublicKey()
	if err != nil {
		return "", fmt.Errorf("failed to extract public key: %w", err)
	}
	if pubKey == nil {
		return "", fmt.Errorf("public key is nil")
	}

	if marshaled {
		// Marshal the public key to bytes
		pubKeyBytes, err := crypto.MarshalPublicKey(pubKey)
		if err != nil {
			return "", fmt.Errorf("failed to marshal public key: %w", err)
		}
		// Return the marshalled public key in hex format
		return hex.EncodeToString(pubKeyBytes), nil
	}

	// Get the raw public key bytes
	rawBytes, err := pubKey.Raw()
	if err != nil {
		return "", fmt.Errorf("failed to extract raw public key: %w", err)
	}
	// Return the raw public key in hex format
	return hex.EncodeToString(rawBytes), nil
}
