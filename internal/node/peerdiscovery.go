package node

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
)

var (
	contactedPeers = make(map[peer.ID]struct{})
	connectedPeers = make(map[peer.ID]struct{})
	mutex          = sync.Mutex{}
)

//TODO
/*
// isPublicIP checks if the given IP address is a public one.
func isPublicIP(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()
}

// hasPublicIP checks if any of the multiaddresses contain a public IP address.
func hasPublicIP(addrs []multiaddr.Multiaddr) bool {
	for _, addr := range addrs {
		ip, err := addr.ValueForProtocol(multiaddr.P_IP4)
		if err == nil {
			if isPublicIP(net.ParseIP(ip)) {
				return true
			}
		}

		ip, err = addr.ValueForProtocol(multiaddr.P_IP6)
		if err == nil {
			if isPublicIP(net.ParseIP(ip)) {
				return true
			}
		}
	}
	return false
}
*/

func discoverPeers(ctx context.Context, n *Node, channelName string, discoveryInterval time.Duration) {

	h := n.Host
	d := n.DHT
	p := n.peerChan

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

	printTicker := time.NewTicker(discoveryInterval * 10)
	defer printTicker.Stop()

	routingDiscovery := drouting.NewRoutingDiscovery(d)
	dutil.Advertise(ctx, routingDiscovery, channelName)

	// Initialize mDNS service
	notifee := &discoveryNotifee{h: h, contactedPeers: make(map[peer.ID]struct{}), mutex: &sync.Mutex{}, discoveredPeersChan: p}
	mdnsService := mdns.NewMdnsService(h, channelName, notifee)
	go func() {
		if err := mdnsService.Start(); err != nil {
			fmt.Println("Failed to start mDNS service:", err)
		}
	}()

	defer mdnsService.Close()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("Stopping peer discovery due to context cancellation")
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

				if alreadyContacted(peer.ID, &mutex) {
					continue
				}

				err := h.Connect(ctx, peer)
				if err != nil {
					continue
				}

				select {
				case p <- peer:
				case <-ctx.Done():
					return
				}

				processAndMarkPeer(peer, &mutex)
			}
		case <-printTicker.C:
		case pi := <-p: // Handle peers discovered via mDNS

			if alreadyContacted(pi.ID, &mutex) {
				continue
			}

			if err := h.Connect(ctx, pi); err != nil {
				continue
			}
			processAndMarkPeer(pi, &mutex)
		}
	}
}

func alreadyContacted(peerID peer.ID, mutex *sync.Mutex) bool {
	mutex.Lock()
	defer mutex.Unlock()
	_, contacted := contactedPeers[peerID]
	return contacted
}

func processAndMarkPeer(peer peer.AddrInfo, mutex *sync.Mutex) {
	mutex.Lock()
	defer mutex.Unlock()

	peerID := peer.ID
	// If the peer has already been processed, skip it
	if _, processed := contactedPeers[peerID]; processed {
		return
	}
	pubKey, err := peerID.ExtractPublicKey()
	if err != nil {
		fmt.Printf("Peer Found: %s (public key not retrievable)\n", peerID)
	} else {
		pubKeyBytes, err := pubKey.Raw()
		if err != nil {
			fmt.Printf("Peer Found: %s (public key not decodable)\n", peerID)
		} else {
			pubKeyB64 := base64.StdEncoding.EncodeToString(pubKeyBytes)
			fmt.Printf("Peer Found: %s, Public Key: %s\n", peerID, pubKeyB64)
		}
	}
	contactedPeers[peerID] = struct{}{}
}

func initDHT(ctx context.Context, h host.Host) (*dht.IpfsDHT, error) {
	dhtOpts := []dht.Option{
		dht.Mode(dht.ModeServer), // Enable server mode for full DHT functionality
		dht.Concurrency(30),      // Increase query concurrency
		//dht.BucketSize(20),       // Increase the bucket size in the routing table

	}
	kademliaDHT, err := dht.New(ctx, h, dhtOpts...)
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
