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
	lastSeenRefresh        = time.Minute
	// statusExpiry: a status not renewed for this long is dropped (a node
	// renews its status every statusAnnounceInterval).
	statusExpiry = 30 * time.Minute
	// statusCapacity bounds the statuses held from authors this node knows
	// only as SDN nodes by their identify agent string. Once it is full, a
	// status from a new such author is not kept; the statuses held are kept
	// until they expire. Authors this node trusts by configuration or its
	// registry are not counted and always kept.
	statusCapacity = 512
	// signalReannounceFor bounds how long the node that published a signal
	// sends it again to peers that join its update topic.
	signalReannounceFor = time.Hour
)

// updateFleet holds the statuses other nodes published and announces this
// node's own. All methods are nil-safe.
type updateFleet struct {
	mu       sync.Mutex
	statuses map[string]fleetStatus
	local    func() *update.Status
	// latest is the newest update signal this node verified itself.
	latest  func() *update.StatusAck
	changed chan struct{}
	seen    func(peer.ID)
	// reserved reports an author this node trusts by configuration or its
	// registry (Node.configuredStatusAuthor). Nil trusts none.
	reserved func(peer.ID) bool
	// publish is update.publish_status: false publishes no status.
	publish bool
	// announced is the newest signal this node published, sent again to
	// peers that join the topic until announcedUntil.
	announced      []byte
	announcedUntil time.Time
	// announcedTo is the fleet when the signal went out: the peers on the
	// topic and the peers whose status this node held. Only they get it
	// again; a node new to this one learns of releases by its feed check.
	announcedTo map[peer.ID]bool
	// topicPeers lists the peers on the update topic (set by announce).
	topicPeers func() []peer.ID
	// topic is the update topic this node follows.
	topic string
}

// updateFleetTopic is the update topic this node follows, or "".
func (n *Node) updateFleetTopic() string {
	if n == nil || n.updateFleet == nil {
		return ""
	}
	n.updateFleet.mu.Lock()
	defer n.updateFleet.mu.Unlock()
	return n.updateFleet.topic
}

type fleetStatus struct {
	status   *update.Status
	received time.Time
	// reserved: its author is one this node trusts by configuration or its
	// registry; not counted against statusCapacity.
	reserved bool
}

func newUpdateFleet(seen func(peer.ID), publish bool) *updateFleet {
	return &updateFleet{statuses: map[string]fleetStatus{}, changed: make(chan struct{}, 1), seen: seen, publish: publish}
}

// record keeps status when it is newer than the one held for its peer and its
// own time is within statusClockSkew of this node's clock; only then is the
// peer recorded as seen. A replayed older status changes nothing. The caller
// has checked that its peer_id is the message's author. A status from a new
// author this node knows only by its agent string is not kept while
// statusCapacity such statuses are held: those held stay until they expire,
// so a flood of new identities cannot push out the nodes already shown.
func (f *updateFleet) record(status *update.Status) {
	if f == nil || status == nil {
		return
	}
	now := time.Now()
	at := status.AtTime()
	if at.Before(now.Add(-statusClockSkew)) || at.After(now.Add(statusClockSkew)) {
		return
	}
	id, err := peer.Decode(status.PeerID)
	if err != nil {
		return
	}
	f.mu.Lock()
	reservedFn := f.reserved
	f.mu.Unlock()
	reserved := reservedFn != nil && reservedFn(id)
	f.mu.Lock()
	held, ok := f.statuses[status.PeerID]
	newer := !ok || at.After(held.status.AtTime())
	if newer && !ok && !reserved {
		f.pruneLocked(now)
		if f.ordinaryLocked() >= statusCapacity {
			newer = false
		}
	}
	if newer {
		f.statuses[status.PeerID] = fleetStatus{status: status, received: now, reserved: reserved || (ok && held.reserved)}
		f.pruneLocked(now)
	}
	f.mu.Unlock()
	if newer && f.seen != nil {
		f.seen(id)
	}
}

// holds reports whether a status of id is held.
func (f *updateFleet) holds(id peer.ID) bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	held, ok := f.statuses[id.String()]
	return ok && time.Since(held.received) <= statusExpiry
}

// ordinaryLocked counts the statuses whose author is not reserved. The
// caller holds f.mu.
func (f *updateFleet) ordinaryLocked() int {
	count := 0
	for _, held := range f.statuses {
		if !held.reserved {
			count++
		}
	}
	return count
}

// pruneLocked drops expired statuses, then (a bound that record keeps) the
// least recently renewed ordinary ones beyond statusCapacity. The caller
// holds f.mu.
func (f *updateFleet) pruneLocked(now time.Time) {
	for id, held := range f.statuses {
		if now.Sub(held.received) > statusExpiry {
			delete(f.statuses, id)
		}
	}
	for f.ordinaryLocked() > statusCapacity {
		oldest, oldestAt := "", time.Time{}
		for id, held := range f.statuses {
			if held.reserved {
				continue
			}
			if oldest == "" || held.received.Before(oldestAt) {
				oldest, oldestAt = id, held.received
			}
		}
		delete(f.statuses, oldest)
	}
}

// noteAnnounced records a signal this node published on its update topic, to
// send again to peers that join it: a peer that was not on the topic when the
// signal went out (restarting, or briefly disconnected) gets it when it
// joins. The feed check remains the catch-up for a node that joins later or
// only through other nodes.
func (f *updateFleet) noteAnnounced(raw []byte, now time.Time) {
	if f == nil {
		return
	}
	signal, err := update.ParseSignal(raw)
	if err != nil {
		return
	}
	until := now.Add(signalReannounceFor)
	if expires, err := time.Parse(time.RFC3339, signal.ExpiresAt); err == nil && expires.Before(until) {
		until = expires
	}
	f.mu.Lock()
	fleet := map[peer.ID]bool{}
	for id := range f.statuses {
		if pid, err := peer.Decode(id); err == nil {
			fleet[pid] = true
		}
	}
	if f.topicPeers != nil {
		for _, id := range f.topicPeers() {
			fleet[id] = true
		}
	}
	f.announced, f.announcedUntil, f.announcedTo = append([]byte(nil), raw...), until, fleet
	f.mu.Unlock()
}

func (f *updateFleet) reannouncement(now time.Time) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.announced == nil || now.After(f.announcedUntil) {
		f.announced = nil
		return nil
	}
	return f.announced
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
	f.pruneLocked(time.Now())
	for k, v := range f.statuses {
		out[k] = v
	}
	return out
}

func (f *updateFleet) latestSignal() *update.StatusAck {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	latest := f.latest
	f.mu.Unlock()
	if latest == nil {
		return nil
	}
	return latest()
}

// reannounceStatusWait is how long after a peer joins the topic this node
// waits for that peer's own status before it decides whether the peer needs
// the signal sent again (a joining node publishes its status about two
// seconds after it sees a peer on the topic).
const reannounceStatusWait = 6 * time.Second

// reannounce sends the newest signal this node published again when a peer
// that was part of the fleet when it went out (on the topic, or known by its
// status) joins the topic, and the peer's own status, if it sends one, does
// not show it already has that signal or a later sequence: a node that was
// restarting or disconnected when the signal went out gets it when it comes
// back. A node new to this one learns of releases by its feed check. At most
// once per statusJoinDebounce, and only while the signal is within
// signalReannounceFor of its publication and not expired.
func (f *updateFleet) reannounce(ctx context.Context, topic *pubsub.Topic, joins <-chan peer.ID) {
	var last time.Time
	for {
		var id peer.ID
		select {
		case <-ctx.Done():
			return
		case id = <-joins:
		}
		joinedAt := time.Now()
		if f.reannouncement(joinedAt) == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(reannounceStatusWait):
		}
		raw := f.reannouncement(time.Now())
		if raw == nil || !f.wasInFleet(id) || f.peerHasSignal(id, raw, joinedAt) {
			continue
		}
		if wait := statusJoinDebounce - time.Since(last); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		last = time.Now()
		if err := topic.Publish(ctx, raw); err == nil {
			log.Infof("Update signal sent again on %s: peer %s joined the topic and has not reported it", topic.String(), id)
		}
	}
}

// wasInFleet reports whether id was on the topic, or had sent this node its
// status, when the signal went out.
func (f *updateFleet) wasInFleet(id peer.ID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.announcedTo[id]
}

// peerHasSignal reports whether id's own status, received since joinedAt,
// shows the signal in raw or a later sequence (it runs it or accepted it).
func (f *updateFleet) peerHasSignal(id peer.ID, raw []byte, joinedAt time.Time) bool {
	signal, err := update.ParseSignal(raw)
	if err != nil {
		return true
	}
	f.mu.Lock()
	held, ok := f.statuses[id.String()]
	f.mu.Unlock()
	if !ok || held.received.Before(joinedAt) {
		return false
	}
	status := held.status
	return status.Sequence >= signal.Sequence || status.Acknowledged != nil && status.Acknowledged.Sequence >= signal.Sequence ||
		status.Pending != nil && status.Pending.Sequence >= signal.Sequence
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
	joins := make(chan peer.ID, 64)
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
				select {
				case joins <- event.Peer:
				default:
				}
			}
		}
	}()
	f.mu.Lock()
	f.topicPeers = topic.ListPeers
	f.mu.Unlock()
	go f.reannounce(ctx, topic, joins)
	publish := func(why string) {
		if !f.publish {
			return
		}
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
	// Source says where Version and State come from: "local" (this node's
	// own update root), "status" (the node's own signed status message:
	// self-reported, never checked by this node), or "identify" (no status
	// from the node; Version is its identify agent version).
	Source string `json:"source"`
	// Version is the bundle version from the node's own status, else the
	// version in its identify agent string.
	Version      string `json:"version,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
	Online       bool   `json:"online"`
	// LastSeen is now for a connected peer, else the newest of the peer
	// registry's last-seen time and this node's receipt of a fresh status
	// from it.
	LastSeen string `json:"last_seen,omitempty"`
	// State is current, pending or failed (from the node's own status), or
	// "not reporting" (no status from the node reached this one: a build
	// before 1.2.0, a node with update.publish_status off, a node reached
	// only through nodes that do not relay statuses, or a status that
	// expired).
	State   string                      `json:"state"`
	Pending *update.StatusPendingUpdate `json:"pending,omitempty"`
	Failed  *update.StatusFailure       `json:"failed,omitempty"`
	// Sequence is the update sequence the node says it runs.
	Sequence int64 `json:"sequence,omitempty"`
	// Acknowledged is the newest signal the node says it accepted;
	// AcknowledgedLatest is true when that signal, or the sequence the node
	// says it runs, is at or past FleetReport.LatestSignal.
	Acknowledged       *update.StatusAck `json:"acknowledged,omitempty"`
	AcknowledgedLatest *bool             `json:"acknowledged_latest,omitempty"`
	StatusAt           string            `json:"status_at,omitempty"`
}

// FleetStateNotReporting is the state of a row with no status.
const FleetStateNotReporting = "not reporting"

// FleetReport is GET /api/peers/versions and `update fleet`.
type FleetReport struct {
	GeneratedAt string `json:"generated_at"`
	// LatestSignal is the newest update signal this node verified itself
	// (signed by a trusted root, for its channel and kind). What other nodes
	// report never moves it.
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
	selfRow := FleetRow{PeerID: self, Self: true, Source: "local", Online: true, LastSeen: report.GeneratedAt, AgentVersion: versioninfo.AgentVersion, State: FleetStateNotReporting}
	if status := n.updateFleet.localStatus(); status != nil {
		applyFleetStatus(&selfRow, status)
		selfRow.Source = "local"
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
		row := FleetRow{PeerID: id, Name: strings.TrimSpace(entry.Name), Online: online[id], Source: "identify", State: FleetStateNotReporting,
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
	// A node whose status this node accepted (it identified the node as an
	// SDN node itself) and that the peer directory does not list.
	for id, held := range statuses {
		if listed[id] {
			continue
		}
		listed[id] = true
		row := FleetRow{PeerID: id, State: FleetStateNotReporting, LastSeen: held.received.UTC().Format(time.RFC3339)}
		if pid, err := peer.Decode(id); err == nil && online[pid.String()] {
			row.Online = true
		}
		applyFleetStatus(&row, held.status)
		report.Rows = append(report.Rows, row)
	}
	report.LatestSignal = n.updateFleet.latestSignal()
	if report.LatestSignal != nil {
		for i := range report.Rows {
			if report.Rows[i].State == FleetStateNotReporting {
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
	row.Source = "status"
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
	// A node's agent is Kubo's with the SDN agent as its suffix
	// ("kubo/0.43.1/spacedatanetwork/1.1.9"): the version after the last
	// "spacedatanetwork/".
	if i := strings.LastIndex(agent, "spacedatanetwork/"); i >= 0 {
		version, _, _ := strings.Cut(agent[i+len("spacedatanetwork/"):], "/")
		return strings.TrimSpace(version)
	}
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
