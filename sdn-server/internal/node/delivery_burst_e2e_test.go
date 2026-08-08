package node

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	coreprotocol "github.com/libp2p/go-libp2p/core/protocol"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// TestDeliveryBurstOverRealSockets is the end-to-end reproduction, run against
// a real libp2p host over real sockets rather than against limit structs.
//
// It models the measured browser exactly: ONE client identity, ten simultaneous
// connections, two module-delivery round trips each — the shape produced by a
// gallery tab issuing Promise.all over ten module fetches, each with its own
// delivery client. Against live prod on 2026-08-08 this shape lost 9-10 of 10
// streams to `code: 0x1002 ... error code: 4098`, byte-identical to the error
// the owner reported.
//
// Run it with `-limits=upstream` to watch it fail on stock go-libp2p defaults.
func TestDeliveryBurstOverRealSockets(t *testing.T) {
	const clients = 10

	for _, tc := range []struct {
		name       string
		manager    func(t *testing.T) network.ResourceManager
		wantAllOK  bool
		wantReason string
	}{
		{
			name: "stock go-libp2p defaults (the defect)",
			manager: func(t *testing.T) network.ResourceManager {
				limits := rcmgr.DefaultLimits
				libp2p.SetDefaultServiceLimits(&limits)
				rm, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(limits.AutoScale()))
				if err != nil {
					t.Fatalf("rcmgr.NewResourceManager: %v", err)
				}
				return rm
			},
			wantAllOK:  false,
			wantReason: "upstream caps inbound connections at 8 per peer identity and cannot scale it",
		},
		{
			name: "this node's resource manager (the fix)",
			manager: func(t *testing.T) network.ResourceManager {
				rm, err := newFlatSQLSyncResourceManager()
				if err != nil {
					t.Fatalf("newFlatSQLSyncResourceManager: %v", err)
				}
				return rm
			},
			wantAllOK: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, admitted, failures := runDeliveryBurst(t, tc.manager(t), clients)
			t.Logf("%s: provider admitted %d/%d simultaneous connections from ONE identity; %d/%d round trips OK; failures=%v",
				tc.name, admitted, clients, ok, clients*2, failures)

			if !tc.wantAllOK {
				// The DEFECT case. Stock go-libp2p refuses everything past
				// PeerBaseLimit.ConnsInbound. It is asserted rather than
				// skipped, because this number is the whole root cause.
				if admitted >= clients {
					t.Fatalf("expected stock defaults to refuse part of the burst (ceiling %d), but all %d were admitted",
						rcmgr.DefaultLimits.PeerBaseLimit.ConnsInbound, admitted)
				}
				if admitted != rcmgr.DefaultLimits.PeerBaseLimit.ConnsInbound {
					t.Logf("NOTE: admitted %d, upstream ceiling is %d (%s)",
						admitted, rcmgr.DefaultLimits.PeerBaseLimit.ConnsInbound, tc.wantReason)
				}
				return
			}

			// The FIX. The owner's bar is first-attempt success, so every
			// connection in the burst must be admitted and every round trip
			// must complete.
			if admitted != clients {
				t.Fatalf("OWNER BAR VIOLATED: provider admitted only %d/%d simultaneous connections from one "+
					"client identity; the refused ones reach the user as \"stream reset\"", admitted, clients)
			}
			if ok != clients*2 {
				t.Fatalf("OWNER BAR VIOLATED: %d/%d delivery round trips succeeded on the FIRST attempt; failures=%v",
					ok, clients*2, failures)
			}
		})
	}
}

// runDeliveryBurst stands up a provider that answers the real module-delivery
// wire ID, then hits it with `clients` simultaneous connections from ONE
// identity, two round trips each. Returns the number of successful round trips
// and a histogram of failures, plus the number of connections the provider
// actually ADMITTED — the number that is the root cause.
func runDeliveryBurst(t *testing.T, rm network.ResourceManager, clients int) (int, int, map[string]int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	provider, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.ResourceManager(rm),
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("provider libp2p.New: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	// A stand-in for the licensing module's handler with the same wire shape:
	// read the bounded request, answer with a frame.
	provider.SetStreamHandler(coreprotocol.ID(modulert.ModuleDeliveryWireID), func(s network.Stream) {
		defer s.Close()
		_, _ = io.ReadAll(io.LimitReader(s, 16385))
		_, _ = s.Write([]byte("grant"))
	})

	// ONE identity shared by every client, exactly as a browser page derives it
	// once and reuses it for all ten module fetches.
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("GenerateEd25519Key: %v", err)
	}
	providerInfo := peer.AddrInfo{ID: provider.ID(), Addrs: provider.Addrs()}

	var (
		mu       sync.Mutex
		ok       int
		failures = map[string]int{}
		wg       sync.WaitGroup
		gate     = make(chan struct{})
		// connected is a BARRIER: every client holds its connection open until
		// all of them have one. Without it the burst can serialise on a fast
		// loopback and never actually be concurrent, which is the difference
		// between reproducing the defect and merely not seeing it.
		connected sync.WaitGroup
	)
	connected.Add(clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			clientRM, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(rcmgr.InfiniteLimits))
			if err != nil {
				return
			}
			h, err := libp2p.New(
				libp2p.Identity(priv),
				libp2p.NoListenAddrs,
				libp2p.ResourceManager(clientRM),
				libp2p.DisableRelay(),
			)
			if err != nil {
				mu.Lock()
				failures["client host: "+err.Error()]++
				mu.Unlock()
				return
			}
			defer h.Close()

			<-gate
			connectErr := h.Connect(ctx, providerInfo)
			connected.Done()
			if connectErr != nil {
				mu.Lock()
				failures["connect: "+connectErr.Error()]++
				mu.Unlock()
				return
			}
			connected.Wait()
			for rt := 0; rt < 2; rt++ {
				if err := deliveryRoundTrip(ctx, h, provider.ID()); err != nil {
					mu.Lock()
					failures[fmt.Sprintf("round-trip %d: %s", rt, err.Error())]++
					mu.Unlock()
					return
				}
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	close(gate)
	// Sample what the provider actually saw at the barrier, so a green result
	// can never be a burst that quietly serialised.
	admittedCh := make(chan int, 1)
	go func() {
		connected.Wait()
		admittedCh <- len(provider.Network().ConnsToPeer(peerIDFromKey(t, priv)))
	}()
	wg.Wait()
	return ok, <-admittedCh, failures
}

func peerIDFromKey(t *testing.T, priv crypto.PrivKey) peer.ID {
	t.Helper()
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("peer.IDFromPrivateKey: %v", err)
	}
	return id
}

func deliveryRoundTrip(ctx context.Context, h interface {
	NewStream(context.Context, peer.ID, ...coreprotocol.ID) (network.Stream, error)
}, provider peer.ID) error {
	s, err := h.NewStream(ctx, provider, coreprotocol.ID(modulert.ModuleDeliveryWireID))
	if err != nil {
		return err
	}
	defer s.Close()
	if _, err := s.Write(make([]byte, 512)); err != nil {
		_ = s.Reset()
		return err
	}
	if err := s.CloseWrite(); err != nil {
		_ = s.Reset()
		return err
	}
	if _, err := io.ReadAll(s); err != nil {
		_ = s.Reset()
		return err
	}
	return nil
}
