package node

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/DigitalArsenal/space-data-network/internal/node/protocols"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/multiformats/go-multiaddr"
)

var (
	contactedPeers = make(map[peer.ID]struct{})
	connectedPeers = make(map[peer.ID]struct{})
	mutex          = sync.Mutex{}
)

func discoverPeers(ctx context.Context, n *Node, channelName string, discoveryInterval time.Duration) {

	h := n.Host
	d := n.DHT

	// Create a NotifyBundle and assign event handlers
	notifiee := &NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			//TODO connect to any peer
		},
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			mutex.Lock()
			defer mutex.Unlock()
			peerID := conn.RemotePeer()
			_, exists := connectedPeers[peerID]
			if exists {
				delete(connectedPeers, peerID)
			}
		},
	}

	// Register notifiee with the host's network
	h.Network().Notify(notifiee)
	defer h.Network().StopNotify(notifiee)

	ticker := time.NewTicker(discoveryInterval)
	defer ticker.Stop()

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

	ticker = time.NewTicker(discoveryInterval)
	defer ticker.Stop()
	printTicker := time.NewTicker(discoveryInterval * 10)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			//fmt.Println("Stopping peer discovery due to context cancellation")
			return
		case <-ticker.C:
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
					continue
				}

				if err := protocols.RequestPNM(ctx, n.Host, n.IPFS, peer.ID); err != nil {
					continue
				}

				processAndMarkPeer(peer, &mutex, ctx, h)
			}
		case <-printTicker.C:
			//fmt.Println("Searching for peers...")
		case peer := <-discoveredPeersChan: // Handle peers discovered via mDNS
			//fmt.Printf("mDNS discovered peer: %s\n", pi.ID)

			if err := h.Connect(ctx, peer); err != nil {
				continue
			}

			//fmt.Printf("Connected to mDNS peer: %s\n", pi.ID)

			if err := protocols.RequestPNM(ctx, n.Host, n.IPFS, peer.ID); err != nil {
				//fmt.Printf("Failed to request PNM from %s: %v\n", peer.ID, err)
				continue
			}

			processAndMarkPeer(peer, &mutex, ctx, h)
		}
	}
}

func processAndMarkPeer(peer peer.AddrInfo, mutex *sync.Mutex, ctx context.Context, h host.Host) {
	mutex.Lock()
	defer mutex.Unlock()

	peerID := peer.ID
	// If the peer has already been processed, skip it
	if _, processed := contactedPeers[peerID]; processed {
		return
	}

	contactedPeers[peerID] = struct{}{}

	if err := h.Connect(ctx, peer); err != nil {
		log.Printf("Failed to connect to peer %s: %v", peer.ID, err)
		return
	}
	// Attempt to make a reservation
	relayInfo, err := makeRelayReservation(ctx, peer, h)
	if err != nil {
		log.Printf("Failed to make relay reservation with peer %s: %v", peer.ID, err)
		return
	}

	log.Printf("Successfully made relay reservation with peer %s at relay %s", peer.ID, relayInfo)
}

func makeRelayReservation(ctx context.Context, peerInfo peer.AddrInfo, h host.Host) (string, error) {

	_, err := client.Reserve(ctx, h, peerInfo)
	if err != nil {
		return "", fmt.Errorf("failed to reserve relay slot with peer %s: %w", peerInfo.ID, err)
	}

	// Create a new relayed address for the peer
	relayAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/p2p/%s/p2p-circuit/p2p/%s", peerInfo.ID, h.ID()))
	if err != nil {
		return "", fmt.Errorf("failed to create relay address: %w", err)
	}
	log.Println(relayAddr.String())
	return relayAddr.String(), nil
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
