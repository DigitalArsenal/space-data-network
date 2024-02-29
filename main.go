package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"sync"

	// Import the go-log package
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
)

var (
	topicNameFlag = flag.String("topicName", "applesauce", "name of topic to join")
)

func main() {

	//log.SetLogLevel("*", "info") // Adjust the log level as needed ("debug", "info", "warn", "error")

	flag.Parse()
	ctx := context.Background()
	peerChan := make(chan peer.AddrInfo)

	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.EnableNATService(),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		libp2p.EnableAutoRelayWithPeerSource(
			func(ctx context.Context, numPeers int) <-chan peer.AddrInfo {
				// TODO(9257): make this code smarter (have a state and actually try to grow the search outward) instead of a long running task just polling our K cluster.
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
			},
			autorelay.WithMinInterval(0),
		),
	)
	if err != nil {
		panic(err)
	}

	// Output the local peer ID
	fmt.Println("Local peer ID:", h.ID())

	go discoverPeers(ctx, h)

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		panic(err)
	}

	topic, err := ps.Join(*topicNameFlag)
	if err != nil {
		panic(err)
	}

	go streamConsoleTo(ctx, topic)

	sub, err := topic.Subscribe()
	if err != nil {
		panic(err)
	}

	printMessagesFrom(ctx, sub)
}

func initDHT(ctx context.Context, h host.Host) *dht.IpfsDHT {
	kademliaDHT, err := dht.New(ctx, h)
	if err != nil {
		panic(err)
	}
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		panic(err)
	}
	var wg sync.WaitGroup
	for _, peerAddr := range dht.DefaultBootstrapPeers {
		peerinfo, _ := peer.AddrInfoFromP2pAddr(peerAddr)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Connect(ctx, *peerinfo); err != nil {
				fmt.Println("Bootstrap warning:", err)
			}
		}()
	}
	wg.Wait()

	return kademliaDHT
}

func discoverPeers(ctx context.Context, h host.Host) {
	kademliaDHT := initDHT(ctx, h)
	routingDiscovery := drouting.NewRoutingDiscovery(kademliaDHT)
	dutil.Advertise(ctx, routingDiscovery, *topicNameFlag)

	anyConnected := false
	for !anyConnected {
		fmt.Println("Searching for peers...")
		peerChan, err := routingDiscovery.FindPeers(ctx, *topicNameFlag)
		if err != nil {
			panic(err)
		}
		for peer := range peerChan {
			if peer.ID == h.ID() {
				continue
			}
			err := h.Connect(ctx, peer)
			if err != nil {
				// Commented out the error printing for connection failure
				// fmt.Printf("Failed connecting to %s, error: %s\n", peer.ID, err)
			} else {
				fmt.Printf("Connected to: %s\n", peer.ID)
				for _, addr := range peer.Addrs {
					fmt.Printf("\t%s/p2p/%s\n", addr, peer.ID)
				}
				anyConnected = true
			}
		}
	}
	fmt.Println("Peer discovery complete")
}

func streamConsoleTo(ctx context.Context, topic *pubsub.Topic) {
	reader := bufio.NewReader(os.Stdin)
	for {
		s, err := reader.ReadString('\n')
		if err != nil {
			panic(err)
		}
		if err := topic.Publish(ctx, []byte(s)); err != nil {
			fmt.Println("### Publish error:", err)
		}
	}
}

func printMessagesFrom(ctx context.Context, sub *pubsub.Subscription) {
	for {
		m, err := sub.Next(ctx)
		if err != nil {
			panic(err)
		}
		fmt.Println(m.ReceivedFrom, ": ", string(m.Message.Data))
	}
}
