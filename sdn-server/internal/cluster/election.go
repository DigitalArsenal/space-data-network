package cluster

import (
	"sort"
	"sync"
	"time"
)

// ElectionState represents the current leader election state.
type ElectionState struct {
	mu            sync.RWMutex
	localPeerID   string
	currentLeader string
	tracker       *HealthTracker
	missedBeats   map[string]int // consecutive missed heartbeats per peer
}

// MissedBeatsThreshold is how many consecutive missed heartbeats before
// a leader is considered failed (3 × 10s heartbeat = 30s).
const MissedBeatsThreshold = 3

// NewElectionState creates a new election state for the given local node.
func NewElectionState(localPeerID string, tracker *HealthTracker) *ElectionState {
	return &ElectionState{
		localPeerID: localPeerID,
		tracker:     tracker,
		missedBeats: make(map[string]int),
	}
}

// CurrentLeader returns the current leader PeerID, or "" if none elected.
func (es *ElectionState) CurrentLeader() string {
	es.mu.RLock()
	defer es.mu.RUnlock()
	return es.currentLeader
}

// IsLeader returns true if the local node is the current leader.
func (es *ElectionState) IsLeader() bool {
	es.mu.RLock()
	defer es.mu.RUnlock()
	return es.currentLeader == es.localPeerID
}

// Elect runs a leader election among healthy peers. The node with the
// lexicographically lowest PeerID wins (deterministic tiebreaker).
// Returns the elected leader PeerID.
func (es *ElectionState) Elect(now time.Time) string {
	es.mu.Lock()
	defer es.mu.Unlock()

	live := es.tracker.LivePeers(now)
	if len(live) == 0 {
		es.currentLeader = ""
		return ""
	}

	candidates := make([]string, 0, len(live))
	for _, peer := range live {
		candidates = append(candidates, peer.PeerID)
	}
	sort.Strings(candidates)

	es.currentLeader = candidates[0]
	return es.currentLeader
}

// RecordHeartbeat records that a peer sent a heartbeat, resetting its
// missed-beats counter.
func (es *ElectionState) RecordHeartbeat(peerID string) {
	es.mu.Lock()
	defer es.mu.Unlock()
	es.missedBeats[peerID] = 0
}

// RecordMiss increments the missed-beats counter for a peer. Returns true
// if the peer has exceeded MissedBeatsThreshold.
func (es *ElectionState) RecordMiss(peerID string) bool {
	es.mu.Lock()
	defer es.mu.Unlock()
	es.missedBeats[peerID]++
	return es.missedBeats[peerID] >= MissedBeatsThreshold
}

// ShouldReelect returns true if the current leader has exceeded the
// missed heartbeats threshold.
func (es *ElectionState) ShouldReelect() bool {
	es.mu.RLock()
	defer es.mu.RUnlock()
	if es.currentLeader == "" {
		return true
	}
	return es.missedBeats[es.currentLeader] >= MissedBeatsThreshold
}

// ResolveSplitBrain resolves a split-brain scenario where multiple nodes
// claim leadership. The node with the lexicographically higher PeerID yields.
// Returns the PeerID that should remain leader.
func ResolveSplitBrain(claimants []string) string {
	if len(claimants) == 0 {
		return ""
	}
	sort.Strings(claimants)
	return claimants[0] // lowest PeerID wins
}
