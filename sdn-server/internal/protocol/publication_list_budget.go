package protocol

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	publicationListRows  = 1000
	publicationListBytes = 1 << 20
	publicationListReads = 32
)

type publicationReadQuota struct {
	since time.Time
	reads int
}

// Lazy expiry bounds memory without a goroutine outliving the stream handler.
type publicationListBudget struct {
	mu     sync.Mutex
	quotas map[peer.ID]publicationReadQuota
}

func (b *publicationListBudget) allow(id peer.ID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for id, quota := range b.quotas {
		if now.Sub(quota.since) >= time.Minute {
			delete(b.quotas, id)
		}
	}
	q, known := b.quotas[id]
	if (!known && len(b.quotas) >= 1024) || q.reads >= publicationListReads {
		return false
	}
	if !known {
		q.since = now
	}
	q.reads++
	if b.quotas == nil {
		b.quotas = make(map[peer.ID]publicationReadQuota)
	}
	b.quotas[id] = q
	return true
}
