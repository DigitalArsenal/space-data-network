package peers

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// A connection gater is an ADMISSION DECISION. It must be non-blocking and
// allocation-cheap, because libp2p calls it on the critical path of every
// inbound handshake and every outbound dial, and it holds no timeout of its own.
//
// Until 2026-07-30 this package broke that rule in the worst possible way:
// InterceptUpgraded -> RecordConnection -> UpdateStats took the Registry WRITE
// lock and then performed a synchronous FlatSQL round-trip — a WASM call —
// while still holding it. On host-01 one engine call stalled inside that
// round-trip and the consequences were total:
//
//	g1966  41 min in InterceptUpgraded, HOLDING the registry write lock
//	84x    40+ min in Registry.IsStrictMode (53 InterceptSecured inbound,
//	                                        25 InterceptPeerDial outbound)
//
// Because Go's RWMutex blocks new readers once a writer is queued, one stuck
// query became "no connection can be established in either direction": inbound
// fds were accepted and never read (ss showed CLOSE-WAIT with unread Recv-Q),
// the /p2p/ ws-upgrade proxy 502'd, DHT dials parked, and spaceaware.io/beta
// served an empty catalogue. Already-established connections kept working, which
// is precisely why the node looked healthy while being unreachable.
//
// Two changes fix it, and both are structural rather than a tuning knob:
//
//  1. Registry.IsStrictMode reads an atomic and takes NO lock at all (see the
//     strictMode field). That alone unblocks 84 of the 85 measured goroutines.
//  2. The statistics PERSISTENCE moves off the caller's goroutine, to the
//     background writer below. The in-memory counter update stays synchronous —
//     it is a map lookup and a few field writes, it holds the lock for
//     nanoseconds, and keeping it synchronous preserves the read-your-writes
//     behaviour every existing caller and test expects. What is removed is the
//     part that could ever block: the store round-trip.
//
// Net effect: no gater callback can wait on the store, so a stalled store
// degrades statistics durability and nothing else. Peering survives.

// statsUpdate is the signal that in-memory statistics changed and should be
// persisted. It carries no payload: the authoritative state is already in the
// registry maps, and the writer snapshots them when it wakes. A payload-free
// signal is what makes coalescing free — a thousand connection events collapse
// into one write.
type statsUpdate struct{}

// statsPersistDebounce is how long the writer waits after the first dirty
// signal before snapshotting, so a burst of connection events costs one store
// write instead of one per connection. Statistics are advisory counters; a
// sub-second delay in their durability has no observable consequence, whereas a
// store write per inbound connection is exactly the cost that made this path
// dangerous in the first place.
const statsPersistDebounce = 250 * time.Millisecond

// startStatsWriter launches the background statistics persister. One goroutine
// per Registry, and registries are process-scoped singletons.
//
// The channel has capacity 1 and every send is non-blocking: it is a "dirty"
// flag, not a queue. A dropped signal cannot lose data, because the writer
// always persists a FRESH snapshot of the live maps rather than a recorded
// delta — if a signal is dropped it is because a write is already pending, and
// that pending write will observe the newer state anyway.
func (r *Registry) startStatsWriter() {
	r.statsCh = make(chan statsUpdate, 1)
	r.statsStop = make(chan struct{})
	go r.statsWriterLoop()
}

func (r *Registry) statsWriterLoop() {
	for {
		select {
		case <-r.statsStop:
			return
		case <-r.statsCh:
		}

		// Coalesce the burst.
		select {
		case <-time.After(statsPersistDebounce):
		case <-r.statsStop:
			// Still flush what we have before exiting — a clean shutdown
			// should not throw away the last window of statistics.
		}

		r.persistSnapshot()
	}
}

// markStatsDirty signals the background writer without ever blocking the
// caller. Called from the gater's connection path, so it must be
// allocation-free and wait-free.
func (r *Registry) markStatsDirty() {
	if r.persistence == nil || r.statsCh == nil {
		return
	}
	select {
	case r.statsCh <- statsUpdate{}:
	default:
		// A write is already pending and will see this update too.
	}
}

// persistSnapshot copies the registry under the lock and writes the COPY
// outside it.
//
// The copy is the whole point: PersistenceProvider.Save serializes the maps it
// is handed, so handing it the live maps means either holding the write lock
// across a WASM/store call (the defect this file exists to remove) or racing
// the serializer against concurrent mutation. Copying costs a few dozen small
// structs at registry scale and buys the guarantee that NOTHING holds the
// Registry lock while the store is being written.
func (r *Registry) persistSnapshot() {
	if r.persistence == nil {
		return
	}

	r.mu.RLock()
	peersCopy := make(map[peer.ID]*TrustedPeer, len(r.peers))
	for id, tp := range r.peers {
		peersCopy[id] = tp.clone()
	}
	groupsCopy := make(map[string]*PeerGroup, len(r.groups))
	for name, g := range r.groups {
		groupsCopy[name] = g.clone()
	}
	r.mu.RUnlock()

	if err := r.persistence.Save(peersCopy, groupsCopy); err != nil {
		log.Warnf("Failed to persist peer statistics: %v", err)
	}
}

// StopStatsWriter shuts the background persister down. Exposed for tests and
// for an orderly daemon shutdown; not required for correctness (the goroutine
// is process-lifetime otherwise).
func (r *Registry) StopStatsWriter() {
	if r.statsStop == nil {
		return
	}
	select {
	case <-r.statsStop:
		// already closed
	default:
		close(r.statsStop)
	}
}

// FlushStats persists the current statistics synchronously. For tests and
// shutdown paths that need durability now rather than after the debounce.
func (r *Registry) FlushStats() { r.persistSnapshot() }

// clone returns a copy safe to hand to a serializer running outside the
// Registry lock. The scalar fields are copied by value; the three reference
// fields are cloned so a concurrent mutation cannot be observed mid-write.
func (tp *TrustedPeer) clone() *TrustedPeer {
	if tp == nil {
		return nil
	}
	cp := *tp
	if tp.Addrs != nil {
		cp.Addrs = make([]multiaddr.Multiaddr, len(tp.Addrs))
		copy(cp.Addrs, tp.Addrs)
	}
	if tp.AddrsStrings != nil {
		cp.AddrsStrings = make([]string, len(tp.AddrsStrings))
		copy(cp.AddrsStrings, tp.AddrsStrings)
	}
	if tp.Groups != nil {
		cp.Groups = make([]string, len(tp.Groups))
		copy(cp.Groups, tp.Groups)
	}
	return &cp
}

// clone returns a copy safe to serialize outside the Registry lock.
func (g *PeerGroup) clone() *PeerGroup {
	if g == nil {
		return nil
	}
	cp := *g
	if g.Members != nil {
		cp.Members = make([]peer.ID, len(g.Members))
		copy(cp.Members, g.Members)
	}
	if g.MembersStrings != nil {
		cp.MembersStrings = make([]string, len(g.MembersStrings))
		copy(cp.MembersStrings, g.MembersStrings)
	}
	if g.Metadata != nil {
		cp.Metadata = make(map[string]string, len(g.Metadata))
		for k, v := range g.Metadata {
			cp.Metadata[k] = v
		}
	}
	return &cp
}
