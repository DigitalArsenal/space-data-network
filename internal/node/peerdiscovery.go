package node

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/DigitalArsenal/space-data-network/internal/node/protocols"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
)

type discoveryNotifee struct {
	h                   host.Host
	contactedPeers      map[peer.ID]struct{}
	mutex               *sync.Mutex
	discoveredPeersChan chan peer.AddrInfo // Channel for sending discovered peers

}

func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.h.ID() || alreadyContacted(pi.ID, n.contactedPeers, n.mutex) {
		return
	}

	n.discoveredPeersChan <- pi // Send the discovered peer to the main loop

}

func discoverPeers(ctx context.Context, h host.Host, d *dht.IpfsDHT, channelName string, discoveryInterval time.Duration) {
	routingDiscovery := drouting.NewRoutingDiscovery(d)
	dutil.Advertise(ctx, routingDiscovery, channelName)
	discoveredPeersChan := make(chan peer.AddrInfo)

	// Initialize mDNS service
	notifee := &discoveryNotifee{h: h, contactedPeers: make(map[peer.ID]struct{}), mutex: &sync.Mutex{}, discoveredPeersChan: discoveredPeersChan}
	mdnsService := mdns.NewMdnsService(h, "space-data-network-mdns", notifee)
	go func() {
		if err := mdnsService.Start(); err != nil {
			fmt.Println("Failed to start mDNS service:", err)
		}
	}()
	defer mdnsService.Close()

	ticker := time.NewTicker(discoveryInterval)
	defer ticker.Stop()

	contactedPeers := make(map[peer.ID]struct{})
	var mutex sync.Mutex

	for {
		select {
		case <-ctx.Done():
			fmt.Println("Stopping peer discovery due to context cancellation")
			return
		case <-ticker.C:
			fmt.Println("Searching for peers...")
			peerChan, err := routingDiscovery.FindPeers(ctx, channelName)
			if err != nil {
				panic(err)
			}
			for peer := range peerChan {
				if peer.ID == h.ID() {
					continue
				}

				if alreadyContacted(peer.ID, contactedPeers, &mutex) {
					continue
				}
				markAsContacted(peer.ID, contactedPeers, &mutex)

				err := h.Connect(ctx, peer)
				if err != nil {
					continue
				}

				fmt.Printf("Connected to: %s\n", peer.ID)
				for _, addr := range peer.Addrs {
					fmt.Printf("\t%s/p2p/%s\n", addr, peer.ID)
				}

				// Request PNM from the connected DHT peer
				if err := protocols.RequestPNM(ctx, h, peer.ID); err != nil {
					fmt.Printf("Failed to request PNM from %s: %v\n", peer.ID, err)
				}
			}
		case pi := <-discoveredPeersChan: // Handle peers discovered via mDNS
			fmt.Printf("mDNS discovered peer: %s\n", pi.ID)

			if alreadyContacted(pi.ID, contactedPeers, &mutex) {
				continue
			}
			markAsContacted(pi.ID, contactedPeers, &mutex)

			if err := h.Connect(ctx, pi); err != nil {
				continue
			}
			fmt.Printf("Connected to mDNS peer: %s\n", pi.ID)

			// Request PNM from the connected mDNS peer
			if err := protocols.RequestPNM(ctx, h, pi.ID); err != nil {
				fmt.Printf("Failed to request PNM from %s: %v\n", pi.ID, err)
			}
		}
	}
}

func alreadyContacted(peerID peer.ID, contactedPeers map[peer.ID]struct{}, mutex *sync.Mutex) bool {
	mutex.Lock()
	defer mutex.Unlock()
	_, contacted := contactedPeers[peerID]
	return contacted
}

func markAsContacted(peerID peer.ID, contactedPeers map[peer.ID]struct{}, mutex *sync.Mutex) {
	mutex.Lock()
	defer mutex.Unlock()
	contactedPeers[peerID] = struct{}{}
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
