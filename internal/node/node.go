package node

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	configs "github.com/DigitalArsenal/space-data-network/configs"
	"github.com/cenkalti/backoff"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core"
	"github.com/ipfs/kubo/plugin/loader"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	tls "github.com/libp2p/go-libp2p/p2p/security/tls"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/websocket"
	webtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"

	dht "github.com/libp2p/go-libp2p-kad-dht"
)

type Node struct {
	Host              host.Host
	DHT               *dht.IpfsDHT
	KeyStore          *KeyStore
	wallet            *hdwallet.Wallet
	signingAccount    accounts.Account
	encryptionAccount accounts.Account
	IPFS              *core.IpfsNode
	peerChan          chan peer.AddrInfo
}

// autoRelayPeerSource returns a function that provides peers for auto-relay.
func autoRelayFeeder(ctx context.Context, h host.Host, dht *dht.IpfsDHT, peerChan chan<- peer.AddrInfo) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Recovering from unexpected error in AutoRelayFeeder:", r)
			debug.PrintStack()
		}
	}()
	go func() {
		// Feed peers more often right after the bootstrap, then backoff
		bo := backoff.NewExponentialBackOff()
		bo.InitialInterval = 15 * time.Second
		bo.Multiplier = 3
		bo.MaxInterval = 1 * time.Hour
		bo.MaxElapsedTime = 0 // never stop
		t := backoff.NewTicker(bo)
		defer t.Stop()
		for {
			select {
			case <-t.C:
			case <-ctx.Done():
				return
			}

			closestPeers, err := dht.GetClosestPeers(ctx, h.ID().String())
			if err != nil {
				// no-op: usually 'failed to find any peer in table' during startup
				continue
			}
			for _, p := range closestPeers {
				addrs := h.Peerstore().Addrs(p)
				if len(addrs) == 0 {
					continue
				}
				dhtPeer := peer.AddrInfo{ID: p, Addrs: addrs}
				select {
				case peerChan <- dhtPeer:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
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
	node.peerChan = make(chan peer.AddrInfo, 100) // Buffer to avoid blocking

	node.Host, err = libp2p.New(
		libp2p.Identity(privKey),
		libp2p.EnableNATService(),
		libp2p.EnableRelay(),
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0", "/ip6/::/tcp/0"),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Security(tls.ID, tls.New),
		libp2p.Transport(tcp.NewTCPTransport, tcp.WithMetrics()),
		libp2p.Transport(websocket.New),
		libp2p.Transport(quic.NewTransport),
		libp2p.Transport(webtransport.New),
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		libp2p.EnableAutoRelayWithPeerSource(
			func(ctx context.Context, _ int) <-chan peer.AddrInfo {
				return node.peerChan
			},
			autorelay.WithMinInterval(1)),
	)
	if err != nil {
		return node, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	fmt.Println("Node PeerID: ", node.Host.ID())

	customHostOption := func(id peer.ID, ps peerstore.Peerstore, options ...libp2p.Option) (host.Host, error) {
		return node.Host, nil
	}

	repoPath := filepath.Join(configs.Conf.Datastore.Directory, "ipfs")
	// Ensure the directory exists
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		fmt.Printf("failed to create directory: %v", err)
	}

	h := node.Host
	privKeyH := h.Peerstore().PrivKey(h.ID())
	if privKeyH == nil {
		panic("private key not found")
	}

	// Marshal the private key to protobuf
	privKeyBytes, err := crypto.MarshalPrivateKey(privKeyH)
	if err != nil {
		panic(err)
	}

	// Encode the marshaled private key to a base64 string
	privKeyBase64 := base64.StdEncoding.EncodeToString(privKeyBytes)

	// Prepare the IPFS config Identity section
	identity := config.Identity{
		PeerID:  h.ID().String(), // Use String() method for peer.ID
		PrivKey: privKeyBase64,   // Use the base64 encoded marshaled private key
	}

	plugins, err := loader.NewPluginLoader(filepath.Join("", "plugins"))
	if err != nil {
		fmt.Printf("error loading plugins: %s", err)
	}

	// Load preloaded and external plugins
	if err := plugins.Initialize(); err != nil {
		fmt.Printf("error initializing plugins: %s", err)
	}

	if err := plugins.Inject(); err != nil {
		fmt.Printf("error initializing plugins: %s", err)
	}

	datastoreSpec := map[string]interface{}{
		"type": "mount",
		"mounts": []interface{}{
			map[string]interface{}{
				"mountpoint": "/blocks",
				"type":       "measure",
				"prefix":     "flatfs.datastore",
				"child": map[string]interface{}{
					"type":      "flatfs",
					"path":      "blocks",
					"sync":      true,
					"shardFunc": "/repo/flatfs/shard/v1/next-to-last/2",
				},
			},
			map[string]interface{}{
				"mountpoint": "/",
				"type":       "measure",
				"prefix":     "leveldb.datastore",
				"child": map[string]interface{}{
					"type":        "levelds",
					"path":        "datastore",
					"compression": "none",
				},
			},
		},
	}

	datastoreConfig := config.Datastore{
		StorageMax:         "10GB",
		StorageGCWatermark: 90,
		GCPeriod:           "1h", // Example, set according to your needs
		Spec:               datastoreSpec,
		HashOnRead:         false, // Default setting
		BloomFilterSize:    0,     // Default setting
	}

	// Use the identity in your IPFS config
	cfg := &config.Config{
		Identity:  identity, // Assuming 'identity' is already defined
		Datastore: datastoreConfig,
		Ipns: config.Ipns{
			UsePubsub: config.True,
		},
	}

	errx := fsrepo.Init(repoPath, cfg)
	fmt.Println("REP", repoPath)
	if errx != nil {
		fmt.Println("Error Creating Repo: ", errx)
	}

	repo, err := fsrepo.Open(repoPath)
	if err != nil {
		return nil, err
	}

	node.IPFS, err = core.NewNode(ctx, &core.BuildCfg{
		Online:    true,
		Permanent: true,
		Host:      customHostOption,
		Repo:      repo,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create IPFS node: %w", err)
	}

	node.wallet = wallet
	node.signingAccount = signingAccount
	node.encryptionAccount = encryptionAccount
	repoConfig, err := node.IPFS.Repo.Config()
	if err != nil {
		fmt.Printf("Failed to get IPFS repo config: %s\n", err)
	} else {
		// The Repo's path is typically not directly exposed in the config,
		// but the Repo itself knows its path which can be accessed this way
		repoPath := repoConfig.Datastore.Path
		fmt.Println("IPFS repo path:", repoPath)
	}
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

	newCID, _ := n.AddFileFromBytes(ctx, vepm)

	fmt.Println("ADDED CID FOR EPM: ")
	fmt.Println(newCID)

	SetupPNMExchange(n)

	n.DHT, err = initDHT(ctx, n.Host)
	if err != nil {
		return fmt.Errorf("failed to initialize DHT: %w", err)
	}

	//Start auto relay
	autoRelayFeeder(ctx, n.Host, n.DHT, n.peerChan)
	go discoverPeers(ctx, n, "space-data-network", 30*time.Second)

	ipnsCID, err := n.PublishIPNSRecord(ctx, newCID.String())
	if err != nil {
		return fmt.Errorf("failed to publish CID to IPNS: %w", err)
	}
	fmt.Println("NEW IPNS CID:", ipnsCID)

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
