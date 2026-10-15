package protocol

import (
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// WIRE-SPEED PROBE BUDGET (security re-audit NET-06, 2026-10-15).
//
// wire_speed_probe streams up to maxWireSpeedProbeBytes of filler to the
// caller. Open to anyone, with up to 512 concurrent sync streams per peer, it
// let one peer turn the node's uplink into an amplifier. It now needs
// private-read authority (FlatSQLSyncHandler.authorize), and even an
// authorized peer is held to:
//
//   - one probe at a time per peer;
//   - a per-peer byte bucket: wireSpeedProbePeerBurstBytes, refilled at
//     wireSpeedProbePeerRefillBytesPerSecond, charged up front;
//   - maxConcurrentWireSpeedProbes node-wide, whoever asks.
const (
	maxConcurrentWireSpeedProbes           = 2
	wireSpeedProbePeerBurstBytes           = 1 << 30 // 1 GiB
	wireSpeedProbePeerRefillBytesPerSecond = 2 << 20 // 2 MiB/s
	maxWireSpeedProbePeers                 = 1024
)

type wireSpeedProbeBudget struct {
	mu     sync.Mutex
	active int
	peers  map[peer.ID]*wireSpeedProbePeer
	now    func() time.Time
}

type wireSpeedProbePeer struct {
	active bool
	tokens float64
	last   time.Time
}

func newWireSpeedProbeBudget() *wireSpeedProbeBudget {
	return &wireSpeedProbeBudget{peers: map[peer.ID]*wireSpeedProbePeer{}, now: time.Now}
}

// acquire reserves probeBytes for id, or refuses. The returned release must
// be called when the probe ends.
func (b *wireSpeedProbeBudget) acquire(id peer.ID, probeBytes int64) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	st, ok := b.peers[id]
	if !ok {
		if len(b.peers) >= maxWireSpeedProbePeers {
			b.sweepLocked(now)
		}
		if len(b.peers) >= maxWireSpeedProbePeers {
			return nil, fmt.Errorf("wire_speed_probe: too many probing peers; retry later")
		}
		st = &wireSpeedProbePeer{tokens: wireSpeedProbePeerBurstBytes, last: now}
		b.peers[id] = st
	}
	st.refill(now)
	if st.active {
		return nil, fmt.Errorf("wire_speed_probe: a probe from this peer is already running")
	}
	if b.active >= maxConcurrentWireSpeedProbes {
		return nil, fmt.Errorf("wire_speed_probe: %d probes already running; retry later", b.active)
	}
	if st.tokens < float64(probeBytes) {
		wait := time.Duration((float64(probeBytes) - st.tokens) / wireSpeedProbePeerRefillBytesPerSecond * float64(time.Second))
		return nil, fmt.Errorf("wire_speed_probe: byte budget exhausted; retry in %s", wait.Round(time.Second))
	}
	st.tokens -= float64(probeBytes)
	st.active = true
	b.active++
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		st.active = false
		b.active--
	}, nil
}

func (p *wireSpeedProbePeer) refill(now time.Time) {
	if elapsed := now.Sub(p.last); elapsed > 0 {
		p.tokens += elapsed.Seconds() * wireSpeedProbePeerRefillBytesPerSecond
		if p.tokens > wireSpeedProbePeerBurstBytes {
			p.tokens = wireSpeedProbePeerBurstBytes
		}
	}
	p.last = now
}

// sweepLocked forgets idle peers whose bucket has refilled: they are
// indistinguishable from a peer never seen.
func (b *wireSpeedProbeBudget) sweepLocked(now time.Time) {
	for id, st := range b.peers {
		st.refill(now)
		if !st.active && st.tokens >= wireSpeedProbePeerBurstBytes {
			delete(b.peers, id)
		}
	}
}
