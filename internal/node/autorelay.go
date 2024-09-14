package node

import (
	"context"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v4"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

func AutoRelayFeeder(ctx context.Context, h host.Host, dht *dht.IpfsDHT, peerChan chan<- peer.AddrInfo) {
	go func() {
		defer fmt.Println("AutoRelayFeeder stopped")

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

			closestPeers, err := dht.GetClosestPeers(ctx, string(h.ID()))
			if err != nil {
				fmt.Println("Error getting closest peers:", err)
				continue
			}

			for _, pID := range closestPeers {
				addrs := h.Peerstore().Addrs(pID)
				if len(addrs) == 0 {
					continue
				}
				dhtPeer := peer.AddrInfo{ID: pID, Addrs: addrs}
				select {
				case peerChan <- dhtPeer:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}
