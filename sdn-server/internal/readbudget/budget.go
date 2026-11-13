// Package readbudget bounds public reads without locking out new peers.
package readbudget

import (
	"container/list"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

const MaxPeers = 1024

type quota struct {
	id     peer.ID
	at     time.Time
	credit time.Duration
}

// Budget is a per-peer token bucket with bounded, least-recently-seen tracking.
// Evicted peers start with a fresh burst; peer churn never blocks a new reader.
type Budget struct {
	mu       sync.Mutex
	interval time.Duration
	capacity time.Duration
	peers    map[peer.ID]*list.Element
	recent   list.List
}

func New(interval time.Duration, burst int) *Budget {
	if interval <= 0 || burst <= 0 {
		panic("read budget requires a positive interval and burst")
	}
	return &Budget{interval: interval, capacity: interval * time.Duration(burst), peers: make(map[peer.ID]*list.Element)}
}

// Allow consumes one token, or returns the delay until one token is available.
// Rejected reads update recency but do not consume future tokens.
func (b *Budget) Allow(id peer.ID) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	e := b.peers[id]
	if e == nil {
		if len(b.peers) == MaxPeers {
			oldest := b.recent.Back()
			delete(b.peers, oldest.Value.(*quota).id)
			b.recent.Remove(oldest)
		}
		e = b.recent.PushFront(&quota{id: id, at: now, credit: b.capacity})
		b.peers[id] = e
	}
	b.recent.MoveToFront(e)
	q := e.Value.(*quota)
	// Clamp elapsed time before addition so even a long-idle bucket cannot overflow.
	q.credit += min(now.Sub(q.at), b.capacity-q.credit)
	q.at = now
	if q.credit < b.interval {
		// The wire advertises whole milliseconds; round up, never down to zero.
		wait := b.interval - q.credit
		return ((wait + time.Millisecond - 1) / time.Millisecond) * time.Millisecond
	}
	q.credit -= b.interval
	return 0
}
