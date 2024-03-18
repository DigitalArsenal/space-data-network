package node

import (
	"context"
	"fmt"
	"time"

	configs "github.com/DigitalArsenal/space-data-network/configs"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ipfs/kubo/core"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	noise "github.com/libp2p/go-libp2p/p2p/security/noise"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
)

type Node struct {
	Host              host.Host
	DHT               *dht.IpfsDHT
	KeyStore          *KeyStore
	wallet            *hdwallet.Wallet
	signingAccount    accounts.Account
	encryptionAccount accounts.Account
	IPFS              *core.IpfsNode
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

func NewNode(ctx context.Context) (*Node, error) {
	configs.Init()

	node := &Node{}

	var err error

	node.KeyStore, err = NewKeyStore(configs.Conf.Datastore.Password)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize key store: %w", err)
	}

	wallet, signingAccount, encryptionAccount, privKey, err := node.KeyStore.GetOrGeneratePrivateKey()
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

	customHostOption := func(id peer.ID, ps peerstore.Peerstore, options ...libp2p.Option) (host.Host, error) {
		return node.Host, nil
	}

	node.IPFS, err = core.NewNode(ctx, &core.BuildCfg{
		Online:    true,
		Permanent: true,
		Host:      customHostOption,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create IPFS node: %w", err)
	}

	node.wallet = wallet
	node.signingAccount = signingAccount
	node.encryptionAccount = encryptionAccount

	fmt.Println("")
	fmt.Println("Node PeerID: ", node.Host.ID())
	fmt.Println("Node Signing Ethereum Address: ", node.signingAccount.Address)
	fmt.Println("Node Encryption Ethereum Address: ", node.encryptionAccount.Address)
	fmt.Println("")

	CreateDefaultServerEPM(node)

	return node, nil
}

func (n *Node) Start(ctx context.Context) error {
	var err error

	vepm, _ := n.KeyStore.LoadEPM()
	/*if len(vepm) > 0 {
		epm := EPM.GetSizePrefixedRootAsEPM(vepm, 0)

		keysLen := epm.KEYSLength() // Get the number of keys

		for i := 0; i < keysLen; i++ {
			key := new(EPM.CryptoKey)
			if epm.KEYS(key, i) {
				keyType := key.KEY_TYPE()
				keyHex := key.PUBLIC_KEY()
				if keyHex != nil {
					fmt.Println(keyType, keyHex)
				}
			}
		}
	}*/

	newCID, _ := n.AddFileFromBytes(ctx, vepm)

	fmt.Println("ADDED CID FOR EPM: ")
	fmt.Println(newCID)

	SetupPNMExchange(n)

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
