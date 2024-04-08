package node

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"runtime/debug"
	"time"

	content "github.com/DigitalArsenal/space-data-network/internal/node/content"
	serverconfig "github.com/DigitalArsenal/space-data-network/serverconfig"
	"github.com/cenkalti/backoff"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
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
	"github.com/multiformats/go-multibase"
	mh "github.com/multiformats/go-multihash"
	"golang.org/x/crypto/argon2"
)

type Node struct {
	Host              host.Host
	DHT               *dht.IpfsDHT
	Wallet            *hdwallet.Wallet
	signingAccount    accounts.Account
	encryptionAccount accounts.Account
	IPFS              *core.IpfsNode
	peerChan          chan peer.AddrInfo
	FileWatcher       Watcher
	publishTimer      *time.Timer
	timerActive       bool
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

func NewSDNNode(ctx context.Context, mnemonic string) (*Node, error) {
	serverconfig.Init()

	node := &Node{
		publishTimer: time.NewTimer(1 * time.Minute),
		timerActive:  false,
	}

	var err error

	repo, wallet, signingAccount, encryptionAccount, privKey, encPrivKey, err := GenerateWalletAndIPFSRepo(ctx, mnemonic)

	if err != nil {
		return nil, fmt.Errorf("failed to load or create IPFS repo: %w", err)
	}

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
			autorelay.WithMinInterval(0)),
	)

	if err != nil {
		return node, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	peerID := node.Host.ID()

	// Update the repository configuration with the peer ID
	cfg, err := repo.Config()
	if err != nil {
		return nil, fmt.Errorf("failed to read IPFS config: %w", err)
	}

	cfg.Ipns.UsePubsub = config.True

	if cfg.Identity.PeerID != peerID.String() {
		cfg.Identity.PeerID = peerID.String()
		cfg.Identity.PrivKey = encPrivKey
		if err := repo.SetConfig(cfg); err != nil {
			return nil, fmt.Errorf("failed to update IPFS repo config: %w", err)
		}
	}

	cPK, _ := crypto.MarshalPrivateKey(privKey)
	cfg.Identity.PrivKey = base64.StdEncoding.EncodeToString(cPK)

	customHostOption := func(id peer.ID, ps peerstore.Peerstore, options ...libp2p.Option) (host.Host, error) {
		return node.Host, nil
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

	node.Wallet = wallet
	node.signingAccount = signingAccount
	node.encryptionAccount = encryptionAccount

	// Extract the public key from the private key
	pubKey := privKey.GetPublic()
	// Marshal the public key to bytes
	pubKeyBytes, err := crypto.MarshalPublicKey(pubKey)
	if err != nil {
		fmt.Printf("failed to marshal public key to bytes: %s\n", err)
	}

	hexPublicKey := hex.EncodeToString(pubKeyBytes)

	fmt.Println("")
	fmt.Printf("Node Public Key: 0x%s \n", hexPublicKey)
	fmt.Println("Node PeerID: ", peerID)

	fmt.Println("")
	fmt.Println("Node Signing Ethereum Address: ", node.signingAccount.Address)
	fmt.Println("Node Encryption Ethereum Address: ", node.encryptionAccount.Address)
	fmt.Println("")

	pid, err := peer.IDFromPublicKey(pubKey)
	if err != nil {
		fmt.Printf("Error getting peer ID from public key: %s\n", err)
	}

	peerMh, err := mh.FromB58String(pid.String())
	if err != nil {
		fmt.Printf("Error converting Peer ID to multihash: %s\n", err)
	}

	// Create a CIDv1 with the 'libp2p-key' codec from the Peer ID's multihash
	peerCid := cid.NewCidV1(cid.Libp2pKey, peerMh)

	// Encode the CIDv1 in base32
	base32Encoded, err := multibase.Encode(multibase.Base32, peerCid.Bytes())
	if err != nil {
		fmt.Printf("Error encoding CID to base32: %s\n", err)
	}

	// Encode the CIDv1 in base36
	base36Encoded, err := multibase.Encode(multibase.Base36, peerCid.Bytes())
	if err != nil {
		fmt.Printf("Error encoding CID to base36: %s\n", err)

	}

	fmt.Println("Base32 Encoded CIDv1:", base32Encoded)
	fmt.Println("Base36 Encoded CIDv1:", base36Encoded)

	content.WriteNodeInfoToTemplate(
		hexPublicKey,
		peerID.String(),
		node.signingAccount.Address.Hex(),
		node.encryptionAccount.Address.Hex(),
		base32Encoded,
		base36Encoded,
		serverconfig.Conf.Folders.RootFolder)

	CreateDefaultServerEPM(node)

	return node, nil
}

func (n *Node) onFileProcessed(filePath string, err error) {
	if err != nil {
		log.Printf("Error processing file onFileProcessed '%s': %v", filePath, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	_, addErr := n.AddFile(ctx, filePath)
	if addErr != nil {
		log.Printf("Failed to add file '%s' to IPFS: %v", filePath, addErr)
		return
	}
}

func (n *Node) publishIPNS() {
	//n.unpublishIPNSRecord()

	if n.publishTimer != nil {
		n.publishTimer.Stop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// IPNS publish logic...
	_, err := n.AddFolderToIPNS(ctx, serverconfig.Conf.Folders.RootFolder)
	if err != nil {
		log.Println("Failed to publish to IPNS:", err)
		return
	}

	//log.Printf("Published to IPNS: %s \n", CID)

}

func (n *Node) Start(ctx context.Context) error {
	var err error

	n.FileWatcher = *NewWatcher(n.onFileProcessed)
	n.FileWatcher.Watch(serverconfig.Conf.Folders.OutgoingFolder)
	n.peerChan = make(chan peer.AddrInfo, 100) // Buffer to avoid blocking

	n.DHT, err = initDHT(ctx, n.Host)
	if err != nil {
		return fmt.Errorf("failed to initialize DHT: %w", err)
	}

	//Start auto relay
	autoRelayFeeder(ctx, n.Host, n.DHT, n.peerChan)
	//TODO REMOVE
	//go discoverPeers(ctx, n, "space-data-network", 30*time.Second)
	//Find others with the same version
	versionHex := []byte(serverconfig.Conf.Info.Version)
	discoveryHex := hex.EncodeToString(argon2.IDKey(versionHex, versionHex, 1, 64*1024, 4, 32))
	go discoverPeers(ctx, n, discoveryHex, 30*time.Second)

	//SetupPNMExchange(n)
	// Initial IPNS publish
	n.publishIPNS()

	// Setup the debounced IPNS publish
	n.FileWatcher = *NewWatcher(func(filePath string, err error) {
		if err != nil {
			log.Printf("Error processing file '%s': %v", filePath, err)
			return
		}

		if !n.timerActive {
			// If the timer is not already active, start it with a delay
			n.publishTimer = time.AfterFunc(30*time.Second, func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()

				// IPNS publish logic...
				CID, err := n.AddFolderToIPNS(ctx, serverconfig.Conf.Folders.RootFolder)
				if err != nil {
					log.Println("Failed to publish to IPNS:", err)
					return
				}

				log.Printf("Published to IPNS: %s \n", CID)

				// Reset timerActive flag after execution
				n.timerActive = false
			})
			n.timerActive = true
		} else {
			// If the timer is already active, reset it to delay the execution
			n.publishTimer.Reset(30 * time.Second)
		}
	})

	n.FileWatcher.Watch(serverconfig.Conf.Folders.OutgoingFolder)

	// Setup periodic IPNS publish every 30 seconds
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n.publishIPNS()
			}
		}
	}()

	return nil
}

func (n *Node) Stop() {
	fmt.Println("Shutting down node...")
	if n.publishTimer != nil {
		n.publishTimer.Stop()
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

	n.FileWatcher.Unwatch()

	fmt.Println("Node stopped successfully.")
}
