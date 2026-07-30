// Package caps (nodeactivity.go): node_activity_read — a read-only,
// policy-mediated view of the HOST's own bounded in-memory activity/event
// ring (peer connects/disconnects, PNM publications, schema record stores,
// channel-grant issuance) for the SpaceAware NODE dashboard's ACTIVITY LOG
// widget (M2 wiring analysis).
//
// Operation naming note (mirrors caps/nodestatus.go's package doc):
// "node_activity_read" has no capPrefixFromName override (module.go is out
// of scope for this change), so — like the other unmapped capabilities
// ("ipfs", "http", "pubsub", "node_status_read") — its hostcall prefix is
// the capability name itself. The one operation is therefore:
//
//   - node_activity_read.activity — the N most recent events, NEWEST
//     FIRST. Input {"limit": int?} (default 50, clamped to
//     1..ActivityRingCapacity). There is no anonymous/operator shaping
//     here, same rationale as node_status_read.status: the ring already
//     carries only host-own, non-secret, peer-ID-level data (see the PII
//     note below), so the host always answers with full detail.
//
// Result shape (snake_case JSON keys are the wire contract):
//
//	{
//	  "count":  int,      // len(events)
//	  "events": [
//	    {
//	      "ts":      string,   // RFC3339 UTC
//	      "kind":    string,   // e.g. "peer_connected", "peer_disconnected",
//	                           // "pnm_publication", "record_stored",
//	                           // "grant_issued"
//	      "peer_id": string,   // OMITTED entirely when empty — never a
//	                           // fabricated "" for events with no peer
//	      "detail":  string    // short, PII-free; always present (may be "")
//	    }
//	  ]
//	}
//
// PII note: events carry only already-public libp2p peer IDs and short
// schema/channel names — never multiaddrs, IP addresses, or key material.
// Every Append call site (internal/node/epm_exchange_notifee.go,
// internal/node/node.go, internal/api/channels.go) is responsible for
// keeping its `detail` argument within that bar; this package does not
// scrub input.
package caps

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// ActivityRingCapacity is the fixed capacity of the production activity
// ring (node.go constructs exactly one, shared between the taps that
// Append and this capability's Snapshot reads) and the hostcall's `limit`
// clamp ceiling.
const ActivityRingCapacity = 256

// ActivityEvent is one entry in the activity ring.
type ActivityEvent struct {
	At   time.Time
	Kind string
	// PeerID is optional; empty omits "peer_id" from the JSON result.
	PeerID string
	// Detail is kept SHORT and PII-free by every call site: peer ids
	// only, no addresses/keys. Always present in the JSON result (may be
	// "").
	Detail string
}

// ActivityRing is a fixed-capacity, race-safe ring buffer of
// ActivityEvent. node.go constructs ONE shared instance: taps
// (epm_exchange_notifee.go's Connected/Disconnected, node.go's PNM/
// schema-record ingest paths, channels.go's issueGrant) call Append; the
// node_activity_read capability reads it back via Snapshot.
//
// Append is deliberately cheap (a mutex lock + slice append, no I/O, no
// blocking channel sends) and nil/panic-safe, so every call site can be an
// unconditional 1-3 line tap with no defensive nil-check required at the
// call site (mirrors caps/nodestatus.go's BandwidthHistoryRing.Add).
type ActivityRing struct {
	mu       sync.Mutex
	capacity int
	now      func() time.Time
	events   []ActivityEvent // append order: oldest first
}

// NewActivityRing creates a ring holding at most capacity events, clocked
// by time.Now. capacity <= 0 is treated as 1, mirroring
// NewBandwidthHistoryRing.
func NewActivityRing(capacity int) *ActivityRing {
	return NewActivityRingWithClock(capacity, time.Now)
}

// NewActivityRingWithClock is NewActivityRing with an injectable clock, so
// tests can assert exact "ts" values deterministically (see
// nodeactivity_test.go) without depending on wall-clock time. A nil now
// defaults to time.Now.
func NewActivityRingWithClock(capacity int, now func() time.Time) *ActivityRing {
	if capacity <= 0 {
		capacity = 1
	}
	if now == nil {
		now = time.Now
	}
	return &ActivityRing{capacity: capacity, now: now, events: make([]ActivityEvent, 0, capacity)}
}

// Append records one event (stamped with the ring's clock), evicting the
// oldest entry once capacity is exceeded. Safe to call on a nil ring (a
// no-op) and never panics — taps call this unconditionally on hot paths
// (peer connect/disconnect, pubsub record ingest, PNM publication, grant
// issuance) and must never be able to block or error that path.
func (r *ActivityRing) Append(kind, peerID, detail string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ActivityEvent{At: r.now().UTC(), Kind: kind, PeerID: peerID, Detail: detail})
	if over := len(r.events) - r.capacity; over > 0 {
		// Drop the oldest `over` entries, keep the rest oldest-first.
		r.events = append(r.events[:0], r.events[over:]...)
	}
}

// Snapshot returns up to limit of the most recent events, NEWEST FIRST.
// limit <= 0, or a limit >= the ring's current length, returns everything
// the ring currently holds. Safe to call on a nil ring (returns nil).
func (r *ActivityRing) Snapshot(limit int) []ActivityEvent {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.events)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]ActivityEvent, limit)
	for i := 0; i < limit; i++ {
		out[i] = r.events[n-1-i]
	}
	return out
}

// ActivityRingReader is the read surface the node_activity_read capability
// depends on — satisfied by *ActivityRing in production and by a fake in
// nodeactivity_test.go, mirroring caps/p2p.go's materials-only DI
// convention (the host never makes response-shaping decisions beyond what
// the ring itself already ordered/trimmed).
type ActivityRingReader interface {
	Snapshot(limit int) []ActivityEvent
}

// NodeActivityMaterials wires the node's shared ActivityRing into the
// node_activity_read capability as a value — the same
// dependency-injection style as caps/nodestatus.go's NodeStatusMaterials —
// so nodeactivity_test.go can drive the handler with a fake, with no live
// Node required.
type NodeActivityMaterials struct {
	// Ring is the shared activity ring. A nil Ring (including a nil
	// *ActivityRing boxed in the interface, which Snapshot handles
	// safely) yields an empty snapshot ({"count":0,"events":[]}) rather
	// than an error — mirrors nodestatus.go's "never fabricate, degrade
	// gracefully" convention.
	Ring ActivityRingReader
}

// NewNodeActivityCapFactory builds the node_activity_read capability
// handler factory. No HostBridge is needed (registered via
// CapabilityRegistry.Register, not RegisterBridgeAware): the result is
// pure JSON, no binary stream segments.
func NewNodeActivityCapFactory(materials NodeActivityMaterials) modulert.CapFactory {
	adapter := &nodeActivityCapAdapter{materials: materials}
	return func(_ *modulert.Module) modulert.CapHandler {
		return adapter.handle
	}
}

type nodeActivityCapAdapter struct {
	materials NodeActivityMaterials
}

type nodeActivityRequest struct {
	Limit int `json:"limit"`
}

func (a *nodeActivityCapAdapter) handle(operation string, payload []byte) ([]byte, error) {
	switch operation {
	case "node_activity_read.activity":
		return a.activity(payload), nil
	default:
		return errCapJSON("unknown node_activity_read operation: " + operation), nil
	}
}

func (a *nodeActivityCapAdapter) activity(payload []byte) []byte {
	var req nodeActivityRequest
	if len(payload) > 0 {
		// A malformed payload is treated the same as an absent one (fall
		// back to the default limit) rather than failing the hostcall —
		// this is a read-only convenience op, not worth a hard error.
		_ = json.Unmarshal(payload, &req)
	}
	return modulert.PreEncodedEnvelope(map[string]interface{}{
		"ok":     true,
		"result": NodeActivitySnapshot(a.materials, req.Limit),
	}, nil)
}

// NodeActivitySnapshot assembles the node_activity_read.activity RESULT object
// from materials — the whole payload documented at the top of this file, with no
// envelope around it.
//
// It is exported for the same reason caps/nodestatus.go's NodeStatusSnapshot is:
// the SAME snapshot is the body of the node's admin-gated HTTP read surface
// (GET /api/node/activity, which the dashboard's ACTIVITY LOG widget reads —
// graph task sdn-dashboard-wave2-edit-layout, IRIS §2/§3). Two assemblers would
// be two contracts, and the dashboard would render a shape no module ever sees.
// There is exactly one assembler and both callers use it.
//
// `limit` follows the hostcall's own clamp: <= 0 means the default 50, and
// anything above ActivityRingCapacity is capped there. No decision about WHO may
// read the ring is made here — the capability registry gates the hostcall and
// the auth wall gates the HTTP path.
func NodeActivitySnapshot(materials NodeActivityMaterials, limit int) map[string]interface{} {
	if limit <= 0 {
		limit = 50
	}
	if limit > ActivityRingCapacity {
		limit = ActivityRingCapacity
	}

	var events []ActivityEvent
	if materials.Ring != nil {
		events = materials.Ring.Snapshot(limit)
	}

	out := make([]map[string]interface{}, 0, len(events))
	for _, ev := range events {
		entry := map[string]interface{}{
			"ts":     ev.At.UTC().Format(time.RFC3339),
			"kind":   ev.Kind,
			"detail": ev.Detail,
		}
		if ev.PeerID != "" {
			entry["peer_id"] = ev.PeerID
		}
		out = append(out, entry)
	}

	return map[string]interface{}{
		"count":  len(out),
		"events": out,
	}
}
