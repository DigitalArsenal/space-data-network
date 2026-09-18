package protocol

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/time/rate"
)

// A dataset-catalog announcement from an UNTRUSTED peer is dispatched to the
// node's PNM handler without being stored (see HandlePubSubMessageStored).
// That dispatch is cheap for the sender and not free for us: the announcer
// chooses the manifest CID, so each announcement of a CID this node has not
// already cached can cost one outbound IPFS block/stat plus one cat of up to
// channels.MaxDatasetCatalogManifestBytes, and one slot in the bounded pending
// lane.
//
// WHAT THIS BUDGET IS FOR. The absolute volume is already bounded elsewhere
// and always was: Node.datasetCatalogMu holds the whole node to ONE metadata
// fetch in flight, under a 15s timeout, and an announcement repeating a CID
// already in the catalog is dropped before any fetch. What no bound covered is
// FAIRNESS — one peer announcing novel CIDs in a loop can hold that single
// slot against every other peer's catalog, so discovery from everyone else
// stops. This budget is that fairness bound, per peer.
//
// It is a DoS control, not a security gate. The security gates are the store
// write staying behind mayPush, materializeDatasetPublicationPNM's own
// IsTrusted check, and the signature verification inside
// cacheDatasetPublicationMetadata. It is still far tighter than
// RateLimitConfig's general per-peer message budget (100/s), which is sized
// for record traffic.
const (
	// untrustedAnnouncementsPerSecond sustains one announcement every two
	// seconds — about thirty a minute against a fetch slot that can serve
	// perhaps sixty, so no single identity can take more than roughly half of
	// it. A real publisher announces a catalog when it publishes one.
	untrustedAnnouncementsPerSecond = 0.5
	// untrustedAnnouncementBurst lets a peer that just joined the topic land
	// the per-schema catalogs it actually has without any of them being
	// dropped. GossipSub does not republish an unchanged catalog, so an
	// announcement refused here is that peer's catalog lost until it publishes
	// again: the burst must comfortably exceed a real node's dataset count.
	untrustedAnnouncementBurst = 32
	// maxUntrustedAnnouncementPeers caps the tracking map. libp2p identities
	// are free to mint, so this is a memory bound, not an abuse bound.
	maxUntrustedAnnouncementPeers = 8192
	// untrustedAnnouncementIdle is how long a silent peer keeps its bucket.
	untrustedAnnouncementIdle = 10 * time.Minute
)

type untrustedAnnouncementBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// untrustedAnnouncementLimiter is a per-peer token bucket with lazy eviction.
// It runs no background goroutine on purpose: a handler is constructed per
// node and per test, and a cleanup goroutine per handler outlives the handler.
type untrustedAnnouncementLimiter struct {
	mu    sync.Mutex
	peers map[peer.ID]*untrustedAnnouncementBucket
}

func newUntrustedAnnouncementLimiter() *untrustedAnnouncementLimiter {
	return &untrustedAnnouncementLimiter{peers: make(map[peer.ID]*untrustedAnnouncementBucket)}
}

// Allow reports whether peerID may have one more untrusted announcement
// dispatched. A nil limiter allows: the limiter is a budget, and a handler
// built without one (a bare &SDSExchangeHandler{} in a test) must not silently
// lose the feature the budget protects. Production always has one —
// NewSDSExchangeHandlerWithOptions installs it unconditionally, not from
// config, because the budget is a property of the lane and not an operator
// preference.
func (l *untrustedAnnouncementLimiter) Allow(peerID peer.ID) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	bucket, exists := l.peers[peerID]
	if !exists {
		if len(l.peers) >= maxUntrustedAnnouncementPeers {
			l.evictIdleLocked(now)
		}
		if len(l.peers) >= maxUntrustedAnnouncementPeers {
			return false
		}
		bucket = &untrustedAnnouncementBucket{
			limiter: rate.NewLimiter(rate.Limit(untrustedAnnouncementsPerSecond), untrustedAnnouncementBurst),
		}
		l.peers[peerID] = bucket
	}
	bucket.lastSeen = now
	return bucket.limiter.Allow()
}

func (l *untrustedAnnouncementLimiter) evictIdleLocked(now time.Time) {
	for id, bucket := range l.peers {
		if now.Sub(bucket.lastSeen) > untrustedAnnouncementIdle {
			delete(l.peers, id)
		}
	}
}
