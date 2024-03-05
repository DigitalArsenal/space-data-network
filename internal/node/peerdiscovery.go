package node

import (
	"context"
	"fmt"
	"sync"

	"github.com/DigitalArsenal/space-data-network/internal/node/protocols"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
)

func discoverPeers(ctx context.Context, h host.Host, d *dht.IpfsDHT, channelName string) {
	routingDiscovery := drouting.NewRoutingDiscovery(d)
	dutil.Advertise(ctx, routingDiscovery, channelName)

	anyConnected := false
	for !anyConnected {
		fmt.Println("Searching for peers...")
		peerChan, err := routingDiscovery.FindPeers(ctx, channelName)
		if err != nil {
			panic(err)
		}
		for peer := range peerChan {
			if peer.ID == h.ID() {
				continue
			}
			err := h.Connect(ctx, peer)
			if err != nil {
				// fmt.Printf("Failed connecting to %s, error: %s\n", peer.ID, err) // Uncomment if you want to log connection failures
			} else {
				fmt.Printf("Connected to: %s\n", peer.ID)
				/*for _, addr := range peer.Addrs {
					fmt.Printf("\t%s/p2p/%s\n", addr, peer.ID)
				}*/

				// Request PNM from the connected peer
				if err := protocols.RequestPNM(ctx, h, peer.ID); err != nil {
					fmt.Printf("Failed to request PNM from %s: %v\n", peer.ID, err)
				}

				anyConnected = true
			}
		}
	}
	fmt.Println("Peer discovery complete")
}

func initDHT(ctx context.Context, h host.Host) (*dht.IpfsDHT, error) {
	kademliaDHT, err := dht.New(ctx, h)
	if err != nil {
		return nil, err
	}
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		return nil, err
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

	return kademliaDHT, nil
}
