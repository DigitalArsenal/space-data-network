package kubo

import (
	"context"
	"sort"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/kubo/core"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
)

const (
	bitswapSampleInterval = 30 * time.Second
	bitswapSampleTimeout  = 2 * time.Second
	bitswapStallInterval  = 5 * time.Minute
	bitswapScanLimit      = 512
)

type bitswapSendSample struct {
	sent   uint64
	demand bool
	known  bool
}

type bitswapDemandSampler struct {
	node       *core.IpfsNode
	peerCursor int
	wantCursor int
}

// sample only reads local state. The upstream snapshot APIs return complete
// slices; our scan and blockstore work are capped, including duplicate wants
// and empty peers. A partial scan cannot establish absence of demand.
func (s *bitswapDemandSampler) sample(ctx context.Context) bitswapSendSample {
	stats, err := s.node.Bitswap.Stat()
	if err != nil || ctx.Err() != nil {
		return bitswapSendSample{}
	}
	result := bitswapSendSample{sent: stats.BlocksSent}
	peers := stats.Peers
	if len(peers) == 0 {
		result.known = true
		s.peerCursor, s.wantCursor = 0, 0
		return result
	}
	sort.Strings(peers)
	s.peerCursor %= len(peers)
	startedMidPeer := s.wantCursor != 0
	work := 0
	seen := make(map[cid.Cid]bool)
	for visited := 0; visited < len(peers); visited++ {
		if work == bitswapScanLimit || ctx.Err() != nil {
			return result
		}
		work++
		id, err := peer.Decode(peers[s.peerCursor])
		if err != nil {
			return result
		}
		if s.node.PeerHost.Network().Connectedness(id) == network.Connected {
			wants := s.node.Bitswap.WantlistForPeer(id)
			// A stable order lets successive bounded scans reach later CIDs.
			sort.Slice(wants, func(i, j int) bool { return wants[i].KeyString() < wants[j].KeyString() })
			for s.wantCursor < len(wants) {
				if work == bitswapScanLimit || ctx.Err() != nil {
					return result
				}
				work++
				c := wants[s.wantCursor]
				s.wantCursor++
				if seen[c] {
					continue
				}
				seen[c] = true
				has, err := s.node.Blockstore.Has(ctx, c)
				if err != nil || ctx.Err() != nil {
					return result
				}
				if has {
					s.wantCursor--
					result.demand, result.known = true, true
					return result
				}
			}
		}
		s.peerCursor = (s.peerCursor + 1) % len(peers)
		s.wantCursor = 0
	}
	result.known = !startedMidPeer
	return result
}

type bitswapSendMonitor struct {
	sample      func(context.Context) bitswapSendSample
	alerts      ops.Sink
	initialized bool
	sent        uint64
	demandSince time.Time
}

func (m *bitswapSendMonitor) observe(now time.Time, s bitswapSendSample) {
	if !s.known {
		// Unknown breaks continuity but cannot certify recovery.
		m.demandSince = time.Time{}
		return
	}
	changed := !m.initialized || s.sent != m.sent
	m.initialized, m.sent = true, s.sent
	if changed || !s.demand {
		m.alerts.Clear(ops.KindBitswapSendStalled, "")
		m.demandSince = time.Time{}
	}
	if !s.demand {
		return
	}
	if m.demandSince.IsZero() {
		m.demandSince = now
		return
	}
	if now.Sub(m.demandSince) >= bitswapStallInterval {
		// The public wantlist API includes WANT_HAVE as well as WANT_BLOCK.
		// This is a conservative progress warning, not proof of an outage.
		m.alerts.Raise(ops.KindBitswapSendStalled, "", ops.SeverityWarning,
			"Connected peers want locally held blocks, but no Bitswap block has been sent for five minutes of observed demand.")
	}
}

func (m *bitswapSendMonitor) run(ctx context.Context, ticks <-chan time.Time, now func() time.Time) {
	var pending <-chan bitswapSendSample
	var expired <-chan struct{}
	var cancel context.CancelFunc
	var sampleCtx context.Context
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if pending != nil {
				continue
			}
			sampleCtx, cancel = context.WithTimeout(ctx, bitswapSampleTimeout)
			expired = sampleCtx.Done()
			result := make(chan bitswapSendSample, 1)
			pending = result
			go func(ctx context.Context) { result <- m.sample(ctx) }(sampleCtx)
		case <-expired:
			m.observe(now(), bitswapSendSample{})
			expired = nil
			// Keep pending until this sample actually returns. A store that
			// ignores cancellation cannot accumulate a goroutine per tick.
		case result := <-pending:
			if sampleCtx.Err() != nil {
				result.known = false
			}
			m.observe(now(), result)
			cancel()
			cancel, pending, expired = nil, nil, nil
		}
	}
}

// MonitorBitswapSends runs under the node lifetime, independently of health
// requests. Only the existing alert registry is read by the HTTP handler.
func (n *Node) MonitorBitswapSends(ctx context.Context, alerts ops.Sink) {
	sampler := &bitswapDemandSampler{node: n.ipfs}
	monitor := &bitswapSendMonitor{sample: sampler.sample, alerts: alerts}
	ticker := time.NewTicker(bitswapSampleInterval)
	defer ticker.Stop()
	monitor.run(ctx, ticker.C, time.Now)
}
