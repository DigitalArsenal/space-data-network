package node

// THE FLEET VERSION VIEW (owner, 2026-11-18: "can we see what version all the
// nodes are on?").
//
// Each node publishes its own update status (update.Status) on the update
// topic of its channel: when its update lane starts and the topic has a peer,
// when a peer joins the topic, when its update state changes, and every
// statusAnnounceInterval. Every node keeps the newest status per peer, and
// joins it with what it already knows of its SDN peers (the /api/peers/sdn
// projection: the name a peer publishes in the directory, when it was last
// seen, its identify agent version). The view is this node's: a peer whose
// status never reached it is "unknown", never guessed.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

const (
	statusAnnounceInterval = 10 * time.Minute
	statusJoinDebounce     = 10 * time.Second
	statusJoinSettle       = 2 * time.Second
	statusFutureLimit      = 10 * time.Minute
	lastSeenRefresh        = time.Minute
)

// updateFleet holds the statuses other nodes published and announces this
// node's own. All methods are nil-safe.
type updateFleet struct {
	mu       sync.Mutex
	statuses map[string]fleetStatus
	local    func() *update.Status
	changed  chan struct{}
	seen     func(peer.ID)
}

type fleetStatus struct {
	status   *update.Status
	received time.Time
}

func newUpdateFleet(seen func(peer.ID)) *updateFleet {
	return &updateFleet{statuses: map[string]fleetStatus{}, changed: make(chan struct{}, 1), seen: seen}
}

// record keeps status when it is newer than the one held for its peer. The
// caller has checked that its peer_id is the message's author.
func (f *updateFleet) record(status *update.Status) {
	if f == nil || status == nil {
		return
	}
	now := time.Now()
	at := status.AtTime()
	if at.After(now.Add(statusFutureLimit)) {
		return
	}
	id, err := peer.Decode(status.PeerID)
	if err != nil {
		return
	}
	f.mu.Lock()
	held, ok := f.statuses[status.PeerID]
	if !ok || !at.Before(held.status.AtTime()) {
		f.statuses[status.PeerID] = fleetStatus{status: status, received: now}
	}
	f.mu.Unlock()
	if f.seen != nil {
		f.seen(id)
	}
}

// localStatus is this node's own status, or nil before its update lane runs.
func (f *updateFleet) localStatus() *update.Status {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	local := f.local
	f.mu.Unlock()
	if local == nil {
		return nil
	}
	return local()
}

// localChanged asks the announcer to publish this node's status now.
func (f *updateFleet) localChanged() {
	if f == nil {
		return
	}
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

func (f *updateFleet) snapshot() map[string]fleetStatus {
	out := map[string]fleetStatus{}
	if f == nil {
		return out
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range f.statuses {
		out[k] = v
	}
	return out
}

// announce publishes this node's status on topic until ctx ends.
func (f *updateFleet) announce(ctx context.Context, topic *pubsub.Topic) {
	if f == nil || topic == nil {
		return
	}
	events, err := topic.EventHandler()
	if err != nil {
		log.Warnf("Update status: cannot follow peers on %s: %v", topic.String(), err)
		return
	}
	defer events.Cancel()
	joined := make(chan struct{}, 1)
	go func() {
		for {
			event, err := events.NextPeerEvent(ctx)
			if err != nil {
				return
			}
			if event.Type == pubsub.PeerJoin {
				select {
				case joined <- struct{}{}:
				default:
				}
			}
		}
	}()
	publish := func(why string) {
		status := f.localStatus()
		if status == nil {
			return
		}
		raw, err := json.Marshal(status)
		if err != nil {
			return
		}
		if err := topic.Publish(ctx, raw); err != nil {
			if ctx.Err() == nil {
				log.Warnf("Update status not published (%s): %v", why, err)
			}
			return
		}
		if why == "interval" {
			log.Debugf("Update status published (%s): %s %s", why, status.State, status.Version)
		} else {
			log.Infof("Update status published (%s): %s, version %s, to %d peers on %s", why, status.State, status.Version, len(topic.ListPeers()), topic.String())
		}
	}
	// A message sent the moment a peer's subscription arrives can reach it
	// before its side of the pub/sub stream is ready, and is then lost: wait
	// statusJoinSettle after a join before announcing.
	settle := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(statusJoinSettle):
			return true
		}
	}
	// First announcement once the topic has a peer to hear it.
	for len(topic.ListPeers()) == 0 {
		select {
		case <-ctx.Done():
			return
		case <-joined:
		case <-f.changed:
		case <-time.After(2 * time.Second):
		}
	}
	if !settle() {
		return
	}
	publish("start")
	ticker := time.NewTicker(statusAnnounceInterval)
	defer ticker.Stop()
	var lastJoin time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publish("interval")
		case <-f.changed:
			publish("change")
		case <-joined:
			if wait := max(statusJoinSettle, statusJoinDebounce-time.Since(lastJoin)); wait > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
			lastJoin = time.Now()
			publish("peer joined")
		}
	}
}

// FleetRow is one node on the fleet view.
type FleetRow struct {
	PeerID string `json:"peer_id"`
	// Self marks this node's own row.
	Self bool `json:"self,omitempty"`
	// Name is the name the node publishes in the peer directory, if any.
	Name string `json:"name,omitempty"`
	// Version is the bundle version from the node's own status, else the
	// version in its identify agent string.
	Version      string `json:"version,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
	Online       bool   `json:"online"`
	// LastSeen is now for a connected peer, else the newest of the peer
	// registry's last-seen time and this node's receipt of its status.
	LastSeen string `json:"last_seen,omitempty"`
	// State is current, pending, failed (from the node's own status) or
	// unknown (no status from it).
	State   string                      `json:"state"`
	Pending *update.StatusPendingUpdate `json:"pending,omitempty"`
	Failed  *update.StatusFailure       `json:"failed,omitempty"`
	// Sequence is the update sequence the node says it runs.
	Sequence int64 `json:"sequence,omitempty"`
	// Acknowledged is the newest signal the node says it accepted;
	// AcknowledgedLatest is true when that signal, or the sequence the node
	// runs, is at or past FleetReport.LatestSignal.
	Acknowledged       *update.StatusAck `json:"acknowledged,omitempty"`
	AcknowledgedLatest *bool             `json:"acknowledged_latest,omitempty"`
	StatusAt           string            `json:"status_at,omitempty"`
}

// FleetReport is GET /api/peers/versions and `update fleet`.
type FleetReport struct {
	GeneratedAt string `json:"generated_at"`
	// LatestSignal is the newest update signal any row acknowledged.
	LatestSignal *update.StatusAck `json:"latest_signal,omitempty"`
	Rows         []FleetRow        `json:"rows"`
}

// UpdateFleet reports the version and update state of this node and every
// SDN peer it knows.
func (n *Node) UpdateFleet() *FleetReport {
	now := time.Now().UTC()
	report := &FleetReport{GeneratedAt: now.Format(time.RFC3339), Rows: []FleetRow{}}
	if n == nil || n.host == nil {
		return report
	}
	statuses := n.updateFleet.snapshot()
	self := n.host.ID().String()
	selfRow := FleetRow{PeerID: self, Self: true, Online: true, LastSeen: report.GeneratedAt, AgentVersion: versioninfo.AgentVersion, State: "unknown"}
	if status := n.updateFleet.localStatus(); status != nil {
		applyFleetStatus(&selfRow, status)
	}
	if selfRow.Version == "" {
		selfRow.Version = versioninfo.Version()
	}
	report.Rows = append(report.Rows, selfRow)

	snapshot := epm.BuildGraphSnapshot(n.host, n.PeerRegistry())
	observed := epm.BuildObservedSDNPeers(snapshot, n.registryPeers(), n.SDNAdvertisementFlagsByPeer(), n.SDNAdvertisementAddrsByPeer())
	online := map[string]bool{}
	for _, node := range snapshot.Nodes {
		if node.IsOnline {
			online[node.PeerID] = true
		}
	}
	listed := map[string]bool{self: true}
	for _, entry := range observed {
		if entry == nil {
			continue
		}
		id := entry.ID.String()
		if listed[id] {
			continue
		}
		listed[id] = true
		row := FleetRow{PeerID: id, Name: strings.TrimSpace(entry.Name), Online: online[id], State: "unknown",
			AgentVersion: strings.TrimSpace(entry.Metadata["agent_version"])}
		seen := entry.LastSeen
		if held, ok := statuses[id]; ok {
			applyFleetStatus(&row, held.status)
			if held.received.After(seen) {
				seen = held.received
			}
		}
		if row.Online {
			seen = now
		}
		if !seen.IsZero() {
			row.LastSeen = seen.UTC().Format(time.RFC3339)
		}
		if row.Version == "" {
			row.Version = agentVersionNumber(row.AgentVersion)
		}
		report.Rows = append(report.Rows, row)
	}
	// A node whose status reached this one through the topic but that is not
	// one of its own SDN peers (relayed by another node).
	for id, held := range statuses {
		if listed[id] {
			continue
		}
		listed[id] = true
		row := FleetRow{PeerID: id, State: "unknown", LastSeen: held.received.UTC().Format(time.RFC3339)}
		if pid, err := peer.Decode(id); err == nil && online[pid.String()] {
			row.Online = true
		}
		applyFleetStatus(&row, held.status)
		report.Rows = append(report.Rows, row)
	}
	for _, row := range report.Rows {
		if row.Acknowledged != nil && (report.LatestSignal == nil || row.Acknowledged.Sequence > report.LatestSignal.Sequence) {
			ack := *row.Acknowledged
			report.LatestSignal = &ack
		}
	}
	if report.LatestSignal != nil {
		for i := range report.Rows {
			if report.Rows[i].State == "unknown" {
				continue
			}
			row := report.Rows[i]
			acked := row.Sequence >= report.LatestSignal.Sequence || row.Acknowledged != nil && row.Acknowledged.Sequence >= report.LatestSignal.Sequence
			report.Rows[i].AcknowledgedLatest = &acked
		}
	}
	sort.SliceStable(report.Rows[1:], func(i, j int) bool {
		a, b := report.Rows[1+i], report.Rows[1+j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.PeerID < b.PeerID
	})
	return report
}

func applyFleetStatus(row *FleetRow, status *update.Status) {
	row.Version = status.Version
	row.State = status.State
	row.Pending = status.Pending
	row.Failed = status.Failed
	row.Acknowledged = status.Acknowledged
	row.StatusAt = status.At
	row.Sequence = status.Sequence
}

// agentVersionNumber is the version in an identify agent string
// ("spacedatanetwork/1.1.9" -> "1.1.9").
func agentVersionNumber(agent string) string {
	if _, version, ok := strings.Cut(agent, "/"); ok {
		return strings.TrimSpace(version)
	}
	return strings.TrimSpace(agent)
}

func (n *Node) registryPeers() []*peers.TrustedPeer {
	if registry := n.PeerRegistry(); registry != nil {
		return registry.ListPeers()
	}
	return nil
}

// refreshLastSeen records every connected peer as seen, once a minute: the
// registry records a connection only when it is upgraded, so a peer on one
// long-lived connection kept the time it connected (the stale last_seen of
// /api/peers/sdn).
func (n *Node) refreshLastSeen() {
	defer n.wg.Done()
	ticker := time.NewTicker(lastSeenRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			registry := n.PeerRegistry()
			if registry == nil || n.host == nil {
				continue
			}
			for _, id := range n.host.Network().Peers() {
				registry.RecordSeen(id)
			}
		}
	}
}
