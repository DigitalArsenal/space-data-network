package kubo_test

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	bsmsg "github.com/ipfs/boxo/bitswap/message"
	pb "github.com/ipfs/boxo/bitswap/message/pb"
	"github.com/ipfs/boxo/bitswap/network/bsnet"
	"github.com/ipfs/boxo/routing/offline"
	blocks "github.com/ipfs/go-block-format"
	kubolibp2p "github.com/ipfs/kubo/core/node/libp2p"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/routing"

	"github.com/spacedatanetwork/sdn-server/internal/kubo"
)

type sendBarrier struct {
	peer    peer.ID
	release chan struct{}
	done    chan error
}

type pausedSendHost struct {
	host.Host
	mu        sync.Mutex
	departing map[peer.ID]bool
	entered   chan *sendBarrier
	cleanup   <-chan struct{}
}

func (h *pausedSendHost) NewStream(ctx context.Context, p peer.ID, ids ...protocol.ID) (network.Stream, error) {
	h.mu.Lock()
	pause := h.departing[p]
	h.mu.Unlock()
	if !pause {
		return h.Host.NewStream(ctx, p, ids...)
	}
	bitswap := false
	for _, id := range ids {
		if id == bsnet.ProtocolBitswap {
			bitswap = true
		}
	}
	if !bitswap {
		return h.Host.NewStream(ctx, p, ids...)
	}
	b := &sendBarrier{peer: p, release: make(chan struct{}), done: make(chan error, 1)}
	select {
	case h.entered <- b:
	case <-h.cleanup:
		return nil, context.Canceled
	}
	select {
	case <-b.release:
	case <-h.cleanup:
		return nil, context.Canceled
	}
	s, err := h.Host.NewStream(ctx, p, ids...)
	b.done <- err
	return s, err
}

type departedRouter struct {
	routing.Routing
	entered chan peer.ID
	release <-chan struct{}
}

func (r *departedRouter) FindPeer(_ context.Context, id peer.ID) (peer.AddrInfo, error) {
	r.entered <- id
	// Intentionally ignore cancellation, as the production lookup did.
	// Cleanup, not a deadline, releases this injected fault.
	<-r.release
	return peer.AddrInfo{}, routing.ErrNotFound
}

func TestBitswapServeAfterRequesterDisconnects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish() // release every fault before node cleanup joins workers
	lookups := make(chan peer.ID, 32)
	var barrier *pausedSendHost
	key, _ := newKey(t)
	a := kubo.StartTransportFixture(t, kubo.Config{
		RepoPath: filepath.Join(t.TempDir(), "kubo"), Key: key, DHT: kubo.DHTOff,
		HostOptions: []libp2p.Option{libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0")},
	}, func(args kubolibp2p.RoutingOptionArgs) (routing.Routing, error) {
		return &departedRouter{Routing: offline.NewOfflineRouter(args.Datastore, args.Validator), entered: lookups, release: release}, nil
	}, func(h host.Host) host.Host {
		barrier = &pausedSendHost{Host: h, departing: make(map[peer.ID]bool), entered: make(chan *sendBarrier, 32), cleanup: release}
		return barrier
	})
	info := peer.AddrInfo{ID: a.Host().ID(), Addrs: a.Host().Addrs()}
	put := func(label string) blocks.Block {
		t.Helper()
		b := blocks.NewBlock([]byte(label))
		if err := a.IPFS().Blockstore.Put(ctx, b); err != nil {
			t.Fatal(err)
		}
		if has, err := a.IPFS().Blockstore.Has(ctx, b.Cid()); err != nil || !has {
			t.Fatalf("holder lacks block: %v", err)
		}
		return b
	}
	var reconnect *kubo.Node
	var retry blocks.Block
	for i := 0; i < 16; i++ {
		key, _ := newKey(t)
		b := start(t, filepath.Join(t.TempDir(), "kubo"), key, nil, "")
		want := put(fmt.Sprintf("departing requester %d", i))
		reconnect, retry = b, want
		barrier.mu.Lock()
		barrier.departing[b.Host().ID()] = true
		barrier.mu.Unlock()
		if err := b.Host().Connect(ctx, info); err != nil {
			t.Fatal(err)
		}
		s, err := b.Host().NewStream(ctx, info.ID, bsnet.ProtocolBitswap)
		if err != nil {
			t.Fatal(err)
		}
		m := bsmsg.New(false)
		m.AddEntry(want.Cid(), 1, pb.Message_Wantlist_Block, true)
		if err := m.ToNetV1(s); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		var send *sendBarrier
		select {
		case send = <-barrier.entered:
			if send.peer != b.Host().ID() {
				t.Fatalf("unexpected outbound peer %s", send.peer)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("requester %d of 16: no send worker reached the outbound barrier", i+1)
		}
		removed := make(chan struct{}, 1)
		notify := &network.NotifyBundle{DisconnectedF: func(n network.Network, c network.Conn) {
			if c.RemotePeer() == b.Host().ID() && len(n.ConnsToPeer(b.Host().ID())) == 0 {
				select {
				case removed <- struct{}{}:
				default:
				}
			}
		}}
		a.Host().Network().Notify(notify)
		if err := a.Host().Network().ClosePeer(b.Host().ID()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-removed:
		case <-time.After(5 * time.Second):
			t.Fatal("connection removal was not observed")
		}
		a.Host().Network().StopNotify(notify)
		a.Host().Peerstore().ClearAddrs(b.Host().ID())
		close(send.release)
		select {
		case err := <-send.done:
			if err == nil {
				t.Fatal("reply to departed peer unexpectedly opened a stream")
			}
		case id := <-lookups:
			t.Logf("requester %d entered the nonreturning routing lookup (%s)", i+1, id)
		case <-time.After(5 * time.Second):
			t.Fatal("outbound send neither failed nor entered the controlled lookup")
		}
	}
	key, _ = newKey(t)
	c := start(t, filepath.Join(t.TempDir(), "kubo"), key, nil, "")
	fetch := func(n *kubo.Node, want blocks.Block) {
		t.Helper()
		before, err := a.IPFS().Bitswap.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if err := n.Host().Connect(ctx, info); err != nil {
			t.Fatal(err)
		}
		fetchCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		got, err := n.IPFS().Blocks.GetBlock(fetchCtx, want.Cid())
		if err != nil {
			t.Fatalf("healthy requester was not served: %v", err)
		}
		if !got.Cid().Equals(want.Cid()) || !bytes.Equal(got.RawData(), want.RawData()) {
			t.Fatal("received a different block")
		}
		for {
			after, err := a.IPFS().Bitswap.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if after.BlocksSent > before.BlocksSent {
				break
			}
			select {
			case <-fetchCtx.Done():
				t.Fatal("successful block send was not counted")
			case <-time.After(time.Millisecond):
			}
		}
	}
	fetch(c, put("fresh block after sixteen departed requesters"))
	barrier.mu.Lock()
	delete(barrier.departing, reconnect.Host().ID())
	barrier.mu.Unlock()
	fetch(reconnect, retry)
	select {
	case id := <-lookups:
		t.Fatalf("Bitswap entered routing for %s", id)
	default:
	}

	// An ordinary protocol retains routed-host lookup behavior.
	_, unknown := newKey(t)
	ordinaryDone := make(chan error, 1)
	go func() { _, err := a.Host().NewStream(ctx, unknown, "/sdn/test/ordinary"); ordinaryDone <- err }()
	select {
	case <-lookups:
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary protocol did not use routing")
	}
	// A connected peer that never negotiates must not hold a Bitswap opener.
	key, _ = newKey(t)
	silent := start(t, filepath.Join(t.TempDir(), "kubo"), key, nil, "")
	silent.Host().Network().SetStreamHandler(func(s network.Stream) { <-release; _ = s.Reset() })
	if err := a.Host().Connect(ctx, peer.AddrInfo{ID: silent.Host().ID(), Addrs: silent.Host().Addrs()}); err != nil {
		t.Fatal(err)
	}
	opened := make(chan error, 1)
	go func() {
		s, err := a.Host().NewStream(context.Background(), silent.Host().ID(), bsnet.ProtocolBitswap)
		if s != nil {
			_ = s.Reset()
		}
		opened <- err
	}()
	select {
	case err := <-opened:
		if err == nil {
			t.Fatal("silent peer negotiated unexpectedly")
		}
	case <-time.After(40 * time.Second):
		t.Fatal("silent peer held Bitswap stream opening without a bound")
	}
	t.Log("sixteen departures, fresh-peer exact bytes and BlocksSent progress, reconnect retrieval, ordinary routing, and bounded silent negotiation verified")
	finish()
	select {
	case <-ordinaryDone:
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary routing lookup did not finish during cleanup")
	}
}
