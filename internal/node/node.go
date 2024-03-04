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
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
	"github.com/tyler-smith/go-bip39"
)

type Node struct {
	Host         host.Host
	DHT          *dht.IpfsDHT
	KeyStore     *KeyStore
	wallet       *hdwallet.Wallet
	EntropyBytes int
}

func NewNode(ctx context.Context, entropyBytes ...int) (*Node, error) {

	defaultEntropySize := 16

	size := defaultEntropySize
	if len(entropyBytes) > 0 {

		if len(entropyBytes) > 1 {
			return nil, fmt.Errorf("only one entropyBytes value should be provided")
		}

		validEntropySizes := map[int]bool{
			16: true,
			20: true,
			24: true,
			28: true,
			32: true,
		}

		if validEntropySizes[entropyBytes[0]] {
			size = entropyBytes[0]
		}
	}

	keyStore, err := NewKeyStore()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize key store: %w", err)
	}

	// Return a new Node instance
	return &Node{KeyStore: keyStore, EntropyBytes: size}, nil
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
	pass := config.Conf.Datastore.Password
	if pass == "" {
		pass = generatePassword()
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

	n.EntropyBytes = 16

	n.SetHDWallet()

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

func (n *Node) PublicKey(marshaled ...bool) (string, error) {

	useMarshaled := false
	if len(marshaled) > 0 {
		useMarshaled = marshaled[0]
	}

	if n.Host == nil {
		return "", fmt.Errorf("host is not initialized")
	}

	pubKey, err := n.Host.ID().ExtractPublicKey()
	if err != nil {
		return "", fmt.Errorf("failed to extract public key: %w", err)
	}
	if pubKey == nil {
		return "", fmt.Errorf("public key is nil")
	}

	if useMarshaled {

		pubKeyBytes, err := crypto.MarshalPublicKey(pubKey)
		if err != nil {
			return "", fmt.Errorf("failed to marshal public key: %w", err)
		}

		return hex.EncodeToString(pubKeyBytes), nil
	}

	rawBytes, err := pubKey.Raw()
	if err != nil {
		return "", fmt.Errorf("failed to extract raw public key: %w", err)
	}

	return hex.EncodeToString(rawBytes), nil
}

func (n *Node) SetHDWallet(rawKey ...[]byte) error {
	var rawPrivateKeyBytes []byte
	var err error

	if len(rawKey) > 0 {
		rawPrivateKeyBytes = rawKey[0]
	} else {
		privKey, err := n.PrivateKey()
		if err != nil {
			return err
		}

		rawPrivateKeyBytes, err = privKey.Raw()
		if err != nil {
			return fmt.Errorf("failed to get raw private key from node: %v", err)
		}
	}

	if len(rawPrivateKeyBytes) < n.EntropyBytes {
		return fmt.Errorf("not enough bytes in private key for the specified entropy length")
	}

	mnemonic, err := bip39.NewMnemonic(rawPrivateKeyBytes[:n.EntropyBytes])
	if err != nil {
		return fmt.Errorf("failed to generate mnemonic from raw key: %v", err)
	}

	wallet, err := hdwallet.NewFromMnemonic(mnemonic)
	if err != nil {
		return fmt.Errorf("failed to create HD wallet from mnemonic: %v", err)
	}

	n.wallet = wallet

	path := hdwallet.MustParseDerivationPath("m/44'/60'/0'/0/0")

	// Derive the first account using the path
	account, err := n.wallet.Derive(path, false)
	if err != nil {
		return fmt.Errorf("failed to derive the first account: %v", err)
	}

	// Get the address of the derived account
	address := account.Address

	// Print the Ethereum address
	fmt.Printf("First Ethereum Address: %s\n", address.Hex())

	return nil
}

func (n *Node) PrivateKey() (*crypto.Secp256k1PrivateKey, error) {
	if n.Host == nil || n.Host.Peerstore() == nil {
		return nil, fmt.Errorf("host or peerstore not initialized")
	}

	privKey := n.Host.Peerstore().PrivKey(n.Host.ID())
	if privKey == nil {
		return nil, fmt.Errorf("private key not found in peerstore")
	}

	secp256k1PrivKey, ok := privKey.(*crypto.Secp256k1PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not of type *crypto.Secp256k1PrivateKey")
	}

	rawPrivateKeyBytes, err := secp256k1PrivKey.Raw()
	if err != nil {
		fmt.Errorf("failed to get raw private key bytes: %v", err)
	}

	// Ensure the rawPrivateKeyBytes length is suitable for mnemonic generation
	// The NewMnemonic function expects entropy of 128-256 bits (16-32 bytes).
	// We'll use the first 16 bytes for this example.
	if len(rawPrivateKeyBytes) < 16 {
		fmt.Errorf("private key bytes insufficient for mnemonic generation")
	}
	entropy := rawPrivateKeyBytes[:16]

	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		fmt.Errorf("failed to generate mnemonic: %v", err)
	}

	// Print the generated mnemonic
	fmt.Printf("Generated Mnemonic: %s\n", mnemonic)
	return secp256k1PrivKey, nil
}
