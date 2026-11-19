package node

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	ps "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

const (
	// statusClockSkew bounds how far a status's own time may be from this
	// node's clock: an older status is a replay, a later one a wrong clock.
	statusClockSkew = 5 * time.Minute
	// A node publishes its status at its start, on a state change, after a
	// peer joins its topic (at most once per statusJoinDebounce) and every
	// statusAnnounceInterval. statusRateBurst statuses per author pass at
	// once, then one per statusRateRefill.
	statusRateBurst  = 6
	statusRateRefill = 10 * time.Second
	// statusRateAuthors bounds the rate table; only known SDN nodes enter it.
	statusRateAuthors = 1024
)

func (n *Node) registerUpdateTopicValidator(topic string) error {
	const prefix = "/sdn/updates/v1/"
	if !strings.HasPrefix(topic, prefix) {
		return nil
	}
	roots, err := update.LoadTrustRoots(update.PathsFor(bundle.ResolveCurrent().Root))
	if err != nil {
		return err
	}
	limiter := newStatusRateLimiter()
	return n.pubsub.RegisterTopicValidator(topic, func(_ context.Context, from peer.ID, message *ps.Message) ps.ValidationResult {
		if n.host != nil && from == n.host.ID() {
			return ps.ValidationAccept
		}
		if message == nil {
			return ps.ValidationReject
		}
		// A node's update status (update_fleet.go). Accepted, and so relayed,
		// only when its peer_id is the message author (whose signature the
		// pub/sub router checked), the author is a node this node has itself
		// identified as an SDN node, its own time is within statusClockSkew of
		// this node's clock, and the author is within its rate.
		if update.IsStatusMessage(message.Data) {
			return n.validateStatus(message, limiter, time.Now())
		}
		signal, err := update.ParseSignal(message.Data)
		if err != nil {
			return ps.ValidationReject
		}
		if err := signal.Verify(update.SignalVerifyOptions{TrustedRoots: roots, Channel: strings.TrimPrefix(topic, prefix)}); err != nil {
			var clockError *update.SignalTimeError
			if errors.As(err, &clockError) {
				return ps.ValidationIgnore
			}
			return ps.ValidationReject
		}
		return ps.ValidationAccept
	})
}

func (n *Node) validateStatus(message *ps.Message, limiter *statusRateLimiter, now time.Time) ps.ValidationResult {
	status, err := update.ParseStatus(message.Data)
	if err != nil {
		return ps.ValidationReject
	}
	author := message.GetFrom()
	if author == "" || status.PeerID != author.String() {
		return ps.ValidationReject
	}
	if !n.knownSDNNode(author) {
		return ps.ValidationIgnore
	}
	if at := status.AtTime(); at.Before(now.Add(-statusClockSkew)) || at.After(now.Add(statusClockSkew)) {
		return ps.ValidationIgnore
	}
	if !limiter.allow(author, now, n.reservedStatusAuthor(author)) {
		return ps.ValidationIgnore
	}
	return ps.ValidationAccept
}

// knownSDNNode reports whether this node has itself identified id as an SDN
// node: the agent version id presented to this node's identify exchange, the
// same evidence the peer directory admits a node by (a pinned peer counts
// too). A claim in a DHT advertisement is not this node's observation and does
// not count.
func (n *Node) knownSDNNode(id peer.ID) bool {
	if n == nil || n.host == nil {
		return false
	}
	if agent, err := n.host.Peerstore().Get(id, "AgentVersion"); err == nil {
		if text, ok := agent.(string); ok && epm.IsSDNAgentVersion(text) {
			return true
		}
	}
	if registry := n.PeerRegistry(); registry != nil {
		if _, pinned := registry.PinOf(id); pinned {
			return true
		}
	}
	return false
}

// reservedStatusAuthor reports whether this node has reason to trust id
// beyond the agent string it presented to identify (which any peer chooses):
// a pin (a config trusted peer is one), a configured bootstrap peer, a peer
// the registry trusts, or a peer whose status this node already holds. Such a
// peer has its own room in the status rate table and the status store, so
// peers known only by their agent string cannot crowd it out.
func (n *Node) reservedStatusAuthor(id peer.ID) bool {
	if n == nil {
		return false
	}
	if n.configuredStatusAuthor(id) {
		return true
	}
	return n.updateFleet.holds(id)
}

// configuredStatusAuthor is reservedStatusAuthor without the held statuses.
func (n *Node) configuredStatusAuthor(id peer.ID) bool {
	if n == nil || id == "" {
		return false
	}
	if registry := n.PeerRegistry(); registry != nil {
		if _, pinned := registry.PinOf(id); pinned {
			return true
		}
		if registry.IsTrusted(id) {
			return true
		}
	}
	return n.bootstrapPeerIDs()[id]
}

// bootstrapPeerIDs are the peer ids of network.bootstrap.
func (n *Node) bootstrapPeerIDs() map[peer.ID]bool {
	n.bootstrapIDsOnce.Do(func() {
		n.bootstrapIDs = map[peer.ID]bool{}
		if n.config == nil {
			return
		}
		for _, entry := range n.config.Network.Bootstrap {
			if info, err := peer.AddrInfoFromString(strings.TrimSpace(entry)); err == nil {
				n.bootstrapIDs[info.ID] = true
			}
		}
	})
	return n.bootstrapIDs
}

// statusRateLimiter is a token bucket per status author. Authors this node
// has reason to trust (reservedStatusAuthor) have their own table, so the
// bound on authors (statusRateAuthors) applies only to the others.
type statusRateLimiter struct {
	mu       sync.Mutex
	buckets  map[peer.ID]*statusBucket
	reserved map[peer.ID]*statusBucket
}

type statusBucket struct {
	tokens float64
	at     time.Time
}

func newStatusRateLimiter() *statusRateLimiter {
	return &statusRateLimiter{buckets: map[peer.ID]*statusBucket{}, reserved: map[peer.ID]*statusBucket{}}
}

func (l *statusRateLimiter) allow(id peer.ID, now time.Time, reserved bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	table := l.buckets
	if reserved {
		table = l.reserved
		if b, ok := l.buckets[id]; ok {
			// Known before it was reserved: its bucket moves with it.
			delete(l.buckets, id)
			table[id] = b
		}
	}
	b, ok := table[id]
	if !ok {
		if len(table) >= statusRateAuthors {
			for k, v := range table {
				if now.Sub(v.at) > statusRateBurst*statusRateRefill {
					delete(table, k)
				}
			}
			if !reserved && len(table) >= statusRateAuthors {
				return false
			}
		}
		b = &statusBucket{tokens: statusRateBurst, at: now}
		table[id] = b
	}
	b.tokens = min(float64(statusRateBurst), b.tokens+now.Sub(b.at).Seconds()/statusRateRefill.Seconds())
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
